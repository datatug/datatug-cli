package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var sessionInterpretDetailed = chat.InterpretDetailed

type sessionCLI struct {
	base, prefix, token string
	query               url.Values
	caps                api.SessionContextCapabilities
	client              *http.Client
}

func sessionCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Use private investigation sessions and context bags locally or in the cloud", SilenceUsage: true}
	cmd.PersistentFlags().String("agent", "http://localhost:8989", "DataTug server base URL")
	cmd.PersistentFlags().Bool("hosted", false, "Use hosted /v0 routes and a Firebase token from DATATUG_TOKEN")
	cmd.PersistentFlags().String("project", "", "Admitted project selector (required)")
	cmd.PersistentFlags().String("storage", "local", "Project storage selector: local, github.com, firestore")
	cmd.PersistentFlags().String("spaceID", "", "Shared Space selector")
	cmd.PersistentFlags().String("branch", "", "Registered GitHub branch")
	cmd.PersistentFlags().String("environment", "", "Admitted environment for a new session or bag")
	cmd.PersistentFlags().String("identity-domain", "", "Explicit identity domain when an environment has multiple domains")
	cmd.PersistentFlags().String("op", "", "Operation ID for this command; use operation to recover an uncertain response")
	cmd.AddCommand(sessionReadCommand("capabilities"), sessionReadCommand("list"), sessionReadCommand("show"), sessionReadCommand("operation"), sessionReadCommand("runs"), sessionReadCommand("messages"), sessionReadCommand("input"), sessionReadCommand("run"), sessionReadCommand("revisions"), sessionPinCommand(false), sessionPinCommand(true), sessionCreateCommand(), sessionBagCommand(), sessionPutCommand(), sessionApplyCommand(), sessionClearCommand(), sessionRawCommand())
	cmd.AddCommand(sessionQueryCommand(), sessionDashboardCommand(), sessionResultCommand(), sessionImportCommand(), sessionRenameCommand(false), sessionRenameCommand(true))
	for _, transition := range []string{"start", "stop", "archive", "restore", "delete"} {
		cmd.AddCommand(sessionTransitionCommand(transition))
	}
	return cmd
}

func newSessionCLI(cmd *cobra.Command) (sessionCLI, error) {
	base, _ := cmd.Flags().GetString("agent")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return sessionCLI{}, errors.New("invalid --agent URL")
	}
	hosted, _ := cmd.Flags().GetBool("hosted")
	cli := sessionCLI{base: strings.TrimRight(base, "/"), prefix: "/datatug/session-context", query: url.Values{}, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	if hosted {
		cli.prefix = "/v0/datatug/session-context"
		cli.token = os.Getenv("DATATUG_TOKEN")
		if cli.token == "" {
			return cli, errors.New("DATATUG_TOKEN is required for hosted sessions")
		}
		if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
			return cli, errors.New("hosted token requires HTTPS or loopback")
		}
	}
	for _, key := range []string{"project", "storage", "spaceID", "branch"} {
		value, _ := cmd.Flags().GetString(key)
		if value != "" {
			cli.query.Set(key, value)
		}
	}
	if cli.query.Get("project") == "" {
		return cli, errors.New("--project is required")
	}
	if _, err = cli.request(cmd.Context(), http.MethodGet, "/capabilities", nil, &cli.caps); err != nil {
		return cli, err
	}
	if cli.caps.SchemaVersion != 1 || cli.caps.DatabaseID == "" || cli.caps.ProjectID == "" {
		return cli, errors.New("server returned invalid session capabilities")
	}
	cli.query.Set("databaseId", cli.caps.DatabaseID)
	cli.query.Set("projectId", cli.caps.ProjectID)
	return cli, nil
}

func (c sessionCLI) request(ctx context.Context, method, suffix string, body any, target any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+c.prefix+suffix+"?"+c.query.Encode(), reader)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	agentaccess.Apply(req) // a local agent needs its session token; a hosted token set above is kept
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("session server unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, errors.New("invalid session response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		return nil, &sessionRequestError{StatusCode: resp.StatusCode, Code: envelope.Error.Code}
	}
	if target != nil && json.Unmarshal(data, target) != nil {
		return nil, errors.New("invalid session response")
	}
	return data, nil
}

func sessionReadCommand(kind string) *cobra.Command {
	use, args := kind, cobra.NoArgs
	paged := kind == "list" || kind == "runs" || kind == "messages" || kind == "revisions"
	if kind != "list" && kind != "capabilities" {
		use += " <id>"
		args = cobra.ExactArgs(1)
	}
	if kind == "revisions" {
		use = "revisions <session-id> <entry-id>"
		args = cobra.ExactArgs(2)
	}
	cmd := &cobra.Command{Use: use, Short: "Read retained session context without running a source query", Args: args, SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		suffix := "/sessions"
		if kind == "capabilities" {
			suffix = "/capabilities"
		}
		if kind == "revisions" {
			suffix += "/" + url.PathEscape(args[0]) + "/entries/" + url.PathEscape(args[1]) + "/revisions"
		}
		if kind == "runs" || kind == "messages" {
			suffix += "/" + url.PathEscape(args[0]) + "/" + kind
		}
		if kind == "input" || kind == "run" {
			suffix = "/" + kind + "s/" + url.PathEscape(args[0])
		}
		if kind == "show" {
			suffix += "/" + url.PathEscape(args[0])
		}
		if kind == "operation" {
			suffix = "/operations/" + url.PathEscape(args[0])
		}
		if paged {
			cursor, _ := cmd.Flags().GetString("cursor")
			cli.query.Set("cursor", cursor)
			limit, _ := cmd.Flags().GetString("limit")
			cli.query.Set("limit", limit)
		}
		data, err := cli.request(cmd.Context(), http.MethodGet, suffix, nil, nil)
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		}
		return err
	}}
	if paged {
		cmd.Flags().String("cursor", "", "Next cursor from previous page")
		cmd.Flags().String("limit", "100", "Page size, at most 100")
	}
	return cmd
}

func (c sessionCLI) scope(cmd *cobra.Command) (*sessioncontext.Scope, error) {
	selected, _ := cmd.Flags().GetString("environment")
	if selected == "" && len(c.caps.Environments) == 1 {
		selected = c.caps.Environments[0].EnvironmentID
	}
	domain, _ := cmd.Flags().GetString("identity-domain")
	var chosen *sessioncontext.Scope
	for _, env := range c.caps.Environments {
		if env.EnvironmentID == selected && (domain == "" || env.IdentityDomain == domain) {
			if chosen != nil {
				return nil, errors.New("environment has multiple identity domains; select --identity-domain")
			}
			chosen = &sessioncontext.Scope{EnvironmentID: selected, IdentityDomain: env.IdentityDomain}
		}
	}
	if chosen != nil {
		return chosen, nil
	}
	return nil, errors.New("--environment must select an admitted environment")
}

func sessionOp(cmd *cobra.Command) string {
	id, _ := cmd.Flags().GetString("op")
	if id == "" {
		id = uuid.NewString()
	}
	return id
}
func (c sessionCLI) submit(cmd *cobra.Command, request sessioncontext.Command) error {
	if request.OpID == "" {
		request.OpID = sessionOp(cmd)
	}
	request.DatabaseID, request.ProjectID = c.caps.DatabaseID, c.caps.ProjectID
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "operation %s\n", request.OpID)
	var result sessioncontext.CommandResult
	_, err := c.request(cmd.Context(), http.MethodPost, "/commands", request, &result)
	if err != nil {
		return fmt.Errorf("%w; recover operation %s before retrying", err, request.OpID)
	}
	// Receipts never contain values; a successful command refreshes the snapshot.
	if request.Action == "importLegacy" {
		request.SessionID = result.Receipt.ResourceID
	}
	var snapshot sessioncontext.Snapshot
	if _, err = c.request(cmd.Context(), http.MethodGet, "/sessions/"+url.PathEscape(request.SessionID), nil, &snapshot); err != nil {
		return fmt.Errorf("operation %s accepted; snapshot refresh unavailable", result.Receipt.OpID)
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
		Receipt  sessioncontext.Receipt  `json:"receipt"`
		Snapshot sessioncontext.Snapshot `json:"snapshot"`
	}{result.Receipt, snapshot})
}
func (c sessionCLI) snapshot(ctx context.Context, id string) (sessioncontext.Snapshot, error) {
	var s sessioncontext.Snapshot
	_, err := c.request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil, &s)
	return s, err
}
func sessionBag(s sessioncontext.Snapshot, id string) (sessioncontext.Bag, error) {
	if id == "" {
		id = s.Session.DefaultBagID
	}
	for _, b := range s.Bags {
		if b.ID == id {
			return b, nil
		}
	}
	return sessioncontext.Bag{}, errors.New("bag is not part of this session")
}

func sessionCreateCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "create", Short: "Create a durable session with a default bag", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		scope, err := cli.scope(cmd)
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			id = uuid.NewString()
		}
		title, _ := cmd.Flags().GetString("title")
		return cli.submit(cmd, sessioncontext.Command{Action: "createSession", SessionID: id, BagID: uuid.NewString(), Title: title, Scope: scope})
	}}
	cmd.Flags().String("id", "", "Stable session ID (generated when omitted)")
	cmd.Flags().String("title", "", "Session title")
	return cmd
}

func sessionBagCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "bag <session-id>", Short: "Create another named, environment-scoped bag", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		s, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		scope, err := cli.scope(cmd)
		if err != nil {
			return err
		}
		title, _ := cmd.Flags().GetString("title")
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			id = uuid.NewString()
		}
		return cli.submit(cmd, sessioncontext.Command{Action: "createBag", SessionID: args[0], ExpectedVersion: s.Session.Version, BagID: id, Title: title, Scope: scope})
	}}
	cmd.Flags().String("title", "", "Bag title")
	cmd.Flags().String("id", "", "Bag ID (generated when omitted)")
	return cmd
}

func readSessionInput(cmd *cobra.Command, target any) error {
	path, _ := cmd.Flags().GetString("input")
	reader := cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		reader = f
	}
	data, err := io.ReadAll(io.LimitReader(reader, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || apicontract.DecodeStrict(data, target) != nil {
		return errors.New("input must be one bounded strict JSON object")
	}
	return nil
}
func sessionPutCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "put <session-id>", Short: "Add/revise a typed entry from JSON {subject,value,alias?}", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var input struct {
			Subject sessioncontext.Subject       `json:"subject"`
			Value   sessioncontext.ValueEnvelope `json:"value"`
			Alias   string                       `json:"alias,omitempty"`
		}
		if err := readSessionInput(cmd, &input); err != nil {
			return err
		}
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		s, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		bid, _ := cmd.Flags().GetString("bag")
		b, err := sessionBag(s, bid)
		if err != nil {
			return err
		}
		subjectID, _ := cmd.Flags().GetString("subject")
		if subjectID != "" {
			if input.Subject.ConceptID != "" {
				return errors.New("choose either --subject or an explicit input subject")
			}
			found := false
			for _, candidate := range cli.caps.Subjects {
				if candidate.ID == subjectID && candidate.Scope == b.Scope {
					if found {
						return errors.New("ambiguous admitted subject")
					}
					input.Subject = candidate.Subject
					found = true
				}
			}
			if !found {
				return errors.New("--subject does not name an admitted subject in this bag scope")
			}
		}
		eid, _ := cmd.Flags().GetString("entry")
		var version int64
		if eid == "" {
			eid = uuid.NewString()
		} else {
			for _, entry := range s.Entries {
				if entry.ID == eid && entry.BagID == b.ID {
					version = entry.Version
				}
			}
		}
		return cli.submit(cmd, sessioncontext.Command{Action: "putEntry", SessionID: args[0], ExpectedVersion: s.Session.Version, BagID: b.ID, ExpectedBagVersion: b.Version, EntryID: eid, ExpectedEntryVersion: version, RevisionID: uuid.NewString(), Subject: &input.Subject, Value: &input.Value, Alias: input.Alias})
	}}
	cmd.Flags().String("input", "-", "JSON input file, or - for stdin")
	cmd.Flags().String("bag", "", "Bag ID (default bag when omitted)")
	cmd.Flags().String("entry", "", "Existing entry to revise")
	cmd.Flags().String("subject", "", "Admitted subject ID from session capabilities; omit subject from input JSON")
	return cmd
}

func sessionAppliedCommand(cmd *cobra.Command, id, action string) error {
	cli, err := newSessionCLI(cmd)
	if err != nil {
		return err
	}
	s, err := cli.snapshot(cmd.Context(), id)
	if err != nil {
		return err
	}
	bid, _ := cmd.Flags().GetString("bag")
	b, err := sessionBag(s, bid)
	if err != nil {
		return err
	}
	var slotVersion int64
	for _, slot := range s.ContextSet.Slots {
		if slot.Scope == b.Scope {
			slotVersion = slot.Version
		}
	}
	selection := []sessioncontext.EntryRef{}
	for _, e := range s.Entries {
		if e.BagID == b.ID {
			selection = append(selection, sessioncontext.EntryRef{EntryID: e.ID, RevisionID: e.CurrentRevisionID})
		}
	}
	enabled := true
	if action == "applyContext" {
		disabled, _ := cmd.Flags().GetBool("disable")
		enabled = !disabled
	}
	return cli.submit(cmd, sessioncontext.Command{Action: action, SessionID: id, ExpectedVersion: s.Session.Version, BagID: b.ID, ExpectedBagVersion: b.Version, ExpectedSlotVersion: slotVersion, ExpectedContextSetID: s.ContextSet.ID, Selection: selection, Enabled: &enabled})
}
func sessionApplyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "apply <session-id>", Short: "Apply all current entries in one bag; enable or disable the session context", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return sessionAppliedCommand(cmd, args[0], "applyContext")
	}}
	cmd.Flags().String("bag", "", "Bag ID (default bag when omitted)")
	cmd.Flags().Bool("disable", false, "Disable applied context while retaining the slots")
	return cmd
}
func sessionClearCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "clear <session-id>", Short: "Clear only the selected bag and its applied slot", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		confirmed, _ := cmd.Flags().GetBool("yes")
		if !confirmed {
			return errors.New("confirm clearing this bag with --yes; retained history and other bags remain")
		}
		return sessionAppliedCommand(cmd, args[0], "clearBag")
	}}
	cmd.Flags().String("bag", "", "Bag ID (default bag when omitted)")
	cmd.Flags().Bool("yes", false, "Confirm clearing the selected bag")
	return cmd
}
func sessionTransitionCommand(transition string) *cobra.Command {
	return &cobra.Command{Use: transition + " <session-id>", Short: "Change session lifecycle without changing Cases", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		s, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return cli.submit(cmd, sessioncontext.Command{Action: "transitionSession", SessionID: args[0], ExpectedVersion: s.Session.Version, Transition: transition})
	}}
}
func sessionRawCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "command", Short: "Submit exact command JSON, including its stable operation ID, from stdin/file", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var request sessioncontext.Command
		if err := readSessionInput(cmd, &request); err != nil {
			return err
		}
		if request.OpID == "" {
			return errors.New("command input requires opId for safe replay")
		}
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		return cli.submit(cmd, request)
	}}
	cmd.Flags().String("input", "-", "Command JSON file, or - for stdin")
	return cmd
}

func sessionPinCommand(isChat bool) *cobra.Command {
	name, short := "pin", "Pin immutable inputs before an explicit execution"
	if isChat {
		name, short = "send", "Ask the chat runtime with explicitly pinned entry attachments"
	}
	cmd := &cobra.Command{Use: name + " <session-id>", Short: short, Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		op := sessionOp(cmd)
		if isChat {
			if recovered, recoveryErr := cli.recoverClientRun(cmd, op, args[0], "chat"); recovered || recoveryErr != nil {
				return recoveryErr
			}
		}
		var input struct {
			Text        string                    `json:"text"`
			Schema      string                    `json:"schema"`
			Attachments []sessioncontext.EntryRef `json:"attachments"`
		}
		if isChat {
			if err := readSessionInput(cmd, &input); err != nil {
				return err
			}
		}
		var interpretation chat.InterpretRequest
		if isChat {
			profile, _ := cmd.Flags().GetString("ai")
			if profile == "" {
				return errors.New("--ai must select a configured AI profile")
			}
			options := chatOptions{ai: profile}
			options.model, _ = cmd.Flags().GetString("model")
			options.baseURL, _ = cmd.Flags().GetString("base-url")
			options.thinking, _ = cmd.Flags().GetString("thinking")
			if err := resolveChatAIProfile(&options, cmd); err != nil {
				return err
			}
			protocol := "openai-chat"
			if strings.HasPrefix(options.model, "claude") {
				protocol = "anthropic-messages"
			}
			base := options.baseURL
			if base == "" {
				base = "https://api.openai.com"
				if protocol == "anthropic-messages" {
					base = "https://api.anthropic.com"
				}
			}
			interpretation = chat.InterpretRequest{Question: input.Text, Schema: input.Schema, Provider: chat.InterpretProvider{Protocol: protocol, BaseURL: base, Model: options.model, APIKey: options.apiKey}}
			if err := interpretation.Validate(); err != nil {
				return err
			}
		}
		s, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		scope, err := cli.scope(cmd)
		if err != nil {
			return err
		}
		kind, definition, revision := "chat", "composer", "composer-1"
		if !isChat {
			kind, _ = cmd.Flags().GetString("kind")
			definition, _ = cmd.Flags().GetString("definition")
			revision, _ = cmd.Flags().GetString("revision")
			if definition == "" || revision == "" {
				return errors.New("--definition and --revision are required")
			}
		}
		definitionDocument := json.RawMessage(`{"schemaVersion":1,"kind":"chat","queries":[]}`)
		if !isChat {
			if err := readSessionInput(cmd, &definitionDocument); err != nil {
				return err
			}
		}
		pin := sessioncontext.Command{DefinitionDocument: definitionDocument, OpID: op, Action: "pinInput", SessionID: args[0], ExpectedVersion: s.Session.Version, ExpectedContextSetID: s.ContextSet.ID, Scope: scope, InputSnapshotID: uuid.NewString(), RunID: uuid.NewString(), Kind: kind, DefinitionID: definition, DefinitionRevision: revision, Text: input.Text, Attachments: input.Attachments}
		if err := cli.submit(cmd, pin); err != nil {
			return err
		}
		if !isChat {
			return nil
		}
		var pinned struct {
			Input     sessioncontext.InputSnapshot   `json:"input"`
			Revisions []sessioncontext.EntryRevision `json:"revisions"`
		}
		if _, err = cli.request(cmd.Context(), http.MethodGet, "/inputs/"+pin.InputSnapshotID, nil, &pinned); err != nil {
			return err
		}
		attachments, _ := json.Marshal(pinned.Revisions)
		if len(pinned.Revisions) > 0 {
			interpretation.Schema += "\nExplicit attachment snapshot " + pin.InputSnapshotID + ":\n" + string(attachments)
		}
		response, providerErr := sessionInterpretDetailed(cmd.Context(), interpretation)
		report := sessioncontext.Command{OpID: uuid.NewString(), Action: "reportClientRun", Kind: "chat", SessionID: args[0], RunID: pin.RunID, ExpectedRunVersion: 1, RunState: "completed", Text: response.DTQL}
		if providerErr != nil {
			report.RunState = "failed"
			report.ErrorCode = "PROVIDER_UNAVAILABLE"
		}
		if err = cli.submit(cmd, report); err != nil {
			return fmt.Errorf("%w; inspect session operation %s and session run %s; original send operation %s", err, report.OpID, pin.RunID, pin.OpID)
		}
		if providerErr != nil {
			return errors.New("chat provider unavailable; retained message and pinned inputs remain recoverable")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			RunID           string               `json:"runId"`
			InputSnapshotID string               `json:"inputSnapshotId"`
			Interpretation  chat.InterpretResult `json:"interpretation"`
		}{pin.RunID, pin.InputSnapshotID, response})
	}}
	if isChat {
		cmd.Flags().String("input", "-", "JSON {text,schema,attachments:[{entryId,revisionId}]} file, or - for stdin")
		cmd.Flags().String("ai", "", "Configured AI profile name; its key stays in memory")
		cmd.Flags().String("model", "", "Override the configured model")
		cmd.Flags().String("base-url", "", "Override the configured provider base URL")
		cmd.Flags().String("thinking", "", "Provider reasoning preference")
	} else {
		cmd.Flags().String("input", "-", "Immutable definition document JSON file, or - for stdin")
		cmd.Flags().String("kind", "query", "Execution kind: query or dashboard (Check execution requires its server adapter)")
		cmd.Flags().String("definition", "", "Definition ID")
		cmd.Flags().String("revision", "", "Definition revision")
	}
	return cmd
}

func sessionQueryCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "query <session-id>", Short: "Execute a saved local query with immutable session inputs through the policy executor", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		hosted, _ := cmd.Flags().GetBool("hosted")
		if hosted {
			return errors.New("this server has no hosted native-query executor; use the admitted sample dashboard")
		}
		s, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		scope, err := cli.scope(cmd)
		if err != nil {
			return err
		}
		query, _ := cmd.Flags().GetString("query")
		source, _ := cmd.Flags().GetString("source")
		if query == "" {
			return errors.New("--query is required")
		}
		type binding struct {
			EntryID     string `json:"entryId"`
			ParameterID string `json:"parameterId"`
		}
		flags, _ := cmd.Flags().GetStringArray("binding")
		bindings := []binding{}
		for _, flag := range flags {
			parameter, entry, ok := strings.Cut(flag, "=")
			if !ok || parameter == "" || entry == "" {
				return errors.New("--binding uses parameterID=entryID")
			}
			bindings = append(bindings, binding{entry, parameter})
		}
		if len(bindings) == 0 {
			return errors.New("at least one --binding parameterID=entryID is required")
		}
		op := sessionOp(cmd)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "operation %s\n", op)
		request := struct {
			Command  sessioncontext.Command `json:"command"`
			QueryID  string                 `json:"queryId"`
			Source   string                 `json:"source,omitempty"`
			Bindings []binding              `json:"bindings"`
		}{sessioncontext.Command{OpID: op, Action: "pinInput", Kind: "query", SessionID: args[0], ExpectedVersion: s.Session.Version, ExpectedContextSetID: s.ContextSet.ID, Scope: scope, InputSnapshotID: uuid.NewString(), RunID: uuid.NewString()}, query, source, bindings}
		data, err := cli.request(cmd.Context(), http.MethodPost, "/query", request, nil)
		if err != nil {
			return fmt.Errorf("%w; recover operation %s before retrying", err, op)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}}
	cmd.Flags().String("query", "", "Saved query ID")
	cmd.Flags().String("source", "", "Admitted source ID (optional for unambiguous saved queries)")
	cmd.Flags().StringArray("binding", nil, "Explicit parameterID=entryID binding from the applied context (repeatable)")
	return cmd
}

func sessionRenameCommand(bag bool) *cobra.Command {
	name, action := "rename", "renameSession"
	if bag {
		name, action = "rename-bag", "renameBag"
	}
	cmd := &cobra.Command{Use: name + " <session-id>", Short: "Edit the display title while retaining stable IDs", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		title, _ := cmd.Flags().GetString("title")
		if strings.TrimSpace(title) == "" {
			return errors.New("--title is required")
		}
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		snapshot, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		request := sessioncontext.Command{Action: action, SessionID: args[0], ExpectedVersion: snapshot.Session.Version, Title: title}
		if bag {
			id, _ := cmd.Flags().GetString("bag")
			selected, err := sessionBag(snapshot, id)
			if err != nil {
				return err
			}
			request.BagID, request.ExpectedBagVersion = selected.ID, selected.Version
		}
		return cli.submit(cmd, request)
	}}
	cmd.Flags().String("title", "", "New display title")
	if bag {
		cmd.Flags().String("bag", "", "Bag ID (default bag when omitted)")
	}
	return cmd
}
