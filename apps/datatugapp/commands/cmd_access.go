package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/spf13/cobra"
)

// Perimeter access commands (D23, D25, D27). An agent files requests for
// itself only, through its user's product login; its user sees them first.
// Registering an agent, forwarding a request and every decision happen in the
// web app: a product (CLI) credential only reads and files self-requests.

type accessClientOptions struct {
	baseURL string
	auth    device.ProductOptions
}

type accessCommandDependencies struct {
	credential func(context.Context, device.ProductOptions) (device.ProductReadCredential, error)
	client     *http.Client
	// sessions resolves agent sessions (D29); nil means none.
	sessions *agentSessions
}

func accessCommand() *cobra.Command {
	return accessCommandWith(accessCommandDependencies{credential: device.ProductCredentialForRead, client: newProtectedReadHTTPClient(), sessions: defaultAgentSessions()})
}

func accessCommandWith(dependencies accessCommandDependencies) *cobra.Command {
	command := &cobra.Command{Use: "access", Short: "Request data access for yourself or your agent, and see your agents", Long: "Register agents, forward their requests and decide in the DataTug web app; from the CLI you list and disable your agents, file self-requests and see your agents' requests.", Args: cobra.NoArgs}
	agents := &cobra.Command{Use: "agents", Short: "List and disable your agents on their own account", Args: cobra.NoArgs}
	agents.AddCommand(accessAgentsListCommand(dependencies), accessAgentsDisableCommand(dependencies))
	command.AddCommand(agents, accessRequestCommand(dependencies), accessInboxCommand(dependencies))
	return command
}

// safeDenialCode keeps only a plain server code; anything else is never
// printed, so a response cannot inject terminal control sequences.
func safeDenialCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, c := range code {
		if (c < 'A' || c > 'Z') && c != '_' {
			return false
		}
	}
	return true
}

// accessRefusal is a server refusal; code is its safe error code.
type accessRefusal struct {
	*exitError
	code string
}

func addAccessClientFlags(command *cobra.Command, options *accessClientOptions) {
	flags := command.Flags()
	options.baseURL = defaultAIAPIURL
	flags.StringVar(&options.baseURL, "base-url", defaultAIAPIURL, "API /v0/ base URL (HTTPS, or loopback development)")
	flags.StringVar(&options.auth.Issuer, "auth-host", "", "Versioned HTTPS product authorization issuer")
	flags.StringVar(&options.auth.Realm, "realm", "", "Exact server credential realm")
	flags.BoolVar(&options.auth.InsecureStorage, "insecure-storage", false, "Use the plaintext product credential created with auth product login --insecure-storage")
}

type accessScopeFlags struct {
	space, project, connection string
}

func addAccessScopeFlags(command *cobra.Command, scope *accessScopeFlags, connection bool) {
	flags := command.Flags()
	flags.StringVar(&scope.space, "space", "", "Space ID")
	flags.StringVar(&scope.project, "project", "", "Project ID")
	if connection {
		flags.StringVar(&scope.connection, "connection", "", "Exact project catalog connection ID")
	}
}

func (s accessScopeFlags) valid(connection bool) bool {
	return validProtectedSelector(s.space) && validProtectedSelector(s.project) && (!connection || validProtectedSelector(s.connection))
}

// accessCall sends one access-control API call with the product login and
// prints the server's JSON answer. It never retries or falls back.
func accessCall(command *cobra.Command, dependencies accessCommandDependencies, options accessClientOptions, method, path string, query url.Values, body any, caller agentCaller) error {
	raw, err := accessDo(command.Context(), dependencies, options, method, path, query, body, caller)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") != nil {
		return errors.New("invalid access service response")
	}
	pretty.WriteByte('\n')
	_, err = command.OutOrStdout().Write(pretty.Bytes())
	return err
}

func accessDo(ctx context.Context, dependencies accessCommandDependencies, options accessClientOptions, method, path string, query url.Values, body any, caller agentCaller) ([]byte, error) {
	if validateAIAPIURL(options.baseURL) != nil {
		return nil, Exit("access requires an HTTPS API /v0/ base URL, or loopback development", exitCodeUsage)
	}
	credential, err := dependencies.credential(ctx, options.auth)
	if err != nil {
		return nil, err
	}
	if _, err = sneatauth.ProductDelegationDigest(credential.Bearer); err != nil || credential.Subject == "" || !time.Now().Before(credential.ExpiresAt) {
		return nil, errors.New("access requires an active product-only login (datatug auth product login)")
	}
	target := options.baseURL + "datatug/access/" + path
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body) // Fixed DTOs of strings, integers and times.
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Bearer)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setProtectedAgentHeader(req, caller.id, caller.session)
	response, err := dependencies.client.Do(req)
	if err != nil {
		return nil, errors.New("access service unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, errors.New("invalid access service response")
	}
	if response.StatusCode != http.StatusOK {
		var denial struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &denial)
		code := denial.Error.Code
		if !safeDenialCode(code) {
			code = http.StatusText(response.StatusCode)
		}
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusBadRequest {
			return nil, &accessRefusal{exitError: &exitError{msg: "access refused: " + code, code: exitCodeAccessDenied}, code: code}
		}
		return nil, &accessRefusal{exitError: &exitError{msg: "access unavailable: " + code, code: exitCodeDatabase}, code: code}
	}
	return raw, nil
}

func accessAgentsListCommand(dependencies accessCommandDependencies) *cobra.Command {
	var options accessClientOptions
	var scope accessScopeFlags
	command := &cobra.Command{Use: "list", Short: "List the agents you registered in a project", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		if !scope.valid(false) {
			return Exit("agents list requires valid --space and --project", exitCodeUsage)
		}
		return accessCall(command, dependencies, options, http.MethodGet, "agents", url.Values{"spaceID": {scope.space}, "projectId": {scope.project}}, nil, agentCaller{})
	}}
	addAccessScopeFlags(command, &scope, false)
	addAccessClientFlags(command, &options)
	return command
}

func accessAgentsDisableCommand(dependencies accessCommandDependencies) *cobra.Command {
	var options accessClientOptions
	var scope accessScopeFlags
	var revision int64
	command := &cobra.Command{Use: "disable <agent-id>", Short: "Disable one of your agents at once", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(command *cobra.Command, args []string) error {
		if !scope.valid(false) || !validProtectedSelector(args[0]) || revision < 1 {
			return Exit("agents disable requires valid --space, --project, agent ID and --revision", exitCodeUsage)
		}
		return accessCall(command, dependencies, options, http.MethodPost, "agents/"+args[0]+"/disable", nil, map[string]any{"spaceID": scope.space, "projectId": scope.project, "expectedRevision": revision}, agentCaller{})
	}}
	addAccessScopeFlags(command, &scope, false)
	command.Flags().Int64Var(&revision, "revision", 0, "The agent's current revision (from agents list)")
	addAccessClientFlags(command, &options)
	return command
}

type accessRequestFlags struct {
	scope    accessScopeFlags
	agent    bool
	agentID  string
	harness  string
	lifetime string
	tables   []string
	reason   string
	seconds  int
}

func accessRequestCommand(dependencies accessCommandDependencies) *cobra.Command {
	var options accessClientOptions
	var flags accessRequestFlags
	command := &cobra.Command{Use: "request", Short: "Request read access for yourself, or as your agent ask your user", Long: "Files a self-request. With --agent, the agent asks its user first: the person who registered it (--agent-id) or you. The agent never names anyone else, and never decides. An agent request asks for 'This session' (--lifetime session, the default when the harness session has an agent session: --harness-session or " + harnessSessionEnv + ") or for a deadline (--lifetime deadline, --seconds; the default without an agent session).\n\n" + agentSessionHonestLimit, Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		if err := flags.validate(); err != nil {
			return err
		}
		caller := agentCaller{}
		if flags.agent {
			session, err := dependencies.sessions.forAgentCall(command.Context(), dependencies, options, flags.harness, &flags.agentID)
			if err != nil {
				return err
			}
			switch {
			case flags.lifetime == "session" && session == "":
				return Exit("a 'This session' request needs an agent session: start one with 'datatug agent session start' and pass --harness-session (or set "+harnessSessionEnv+"), or pass --lifetime deadline", exitCodeUsage)
			case flags.lifetime == "" && session != "":
				flags.lifetime = "session"
			case flags.lifetime == "":
				flags.lifetime = "deadline"
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "Note: no agent session is active, so the approval will be \"Until a deadline\" (--seconds).")
			}
			caller = agentCaller{id: flags.agentID, session: session}
		}
		// The grant scope names the connection's protection, as the server
		// reports it; the catalog ID alone is not a grant scope.
		raw, err := accessDo(command.Context(), dependencies, options, http.MethodGet, "protection", url.Values{"spaceID": {flags.scope.space}, "projectId": {flags.scope.project}, "connectionId": {flags.scope.connection}}, nil, agentCaller{})
		if err != nil {
			return err
		}
		var view struct {
			Connection dataaccess.ConnectionRef `json:"connection"`
		}
		if json.Unmarshal(raw, &view) != nil || !validProtectedSelector(view.Connection.ID) {
			return errors.New("invalid access protection response")
		}
		body := flags.body(view.Connection.ID, time.Now())
		path := "requests"
		if flags.agent {
			path = "agent/requests"
		}
		return accessCall(command, dependencies, options, http.MethodPost, path, nil, body, caller)
	}}
	addAccessScopeFlags(command, &flags.scope, true)
	f := command.Flags()
	f.BoolVar(&flags.agent, "agent", false, "File the request as your agent")
	f.StringVar(&flags.agentID, "agent-id", "", "With --agent: the registered agent on its own account (default: the agent of the harness session)")
	addHarnessSessionFlag(command, &flags.harness)
	f.StringVar(&flags.lifetime, "lifetime", "", "With --agent: session (default with an agent session; needs one) or deadline (default without)")
	f.StringSliceVar(&flags.tables, "table", nil, "Exact table as namespace.name (repeatable); omit for the whole connection")
	f.StringVar(&flags.reason, "reason", "", "Why access is needed")
	f.IntVar(&flags.seconds, "seconds", 300, "Requested duration in seconds")
	addAccessClientFlags(command, &options)
	return command
}

func (f accessRequestFlags) validate() error {
	if !f.scope.valid(true) || f.reason == "" || len(f.reason) > 256 || f.seconds < 1 || !validProtectedAgent(f.agent, f.agentID) {
		return Exit("access request requires valid --space, --project, --connection, --reason and --seconds; --agent-id needs --agent", exitCodeUsage)
	}
	if f.lifetime != "" && (!f.agent || f.lifetime != "session" && f.lifetime != "deadline") {
		return Exit("--lifetime needs --agent and is session or deadline", exitCodeUsage)
	}
	for _, table := range f.tables {
		if _, _, ok := cutTable(table); !ok {
			return Exit("--table must be namespace.name", exitCodeUsage)
		}
	}
	return nil
}

func (f accessRequestFlags) body(connection string, now time.Time) any {
	scope := dataaccess.GrantScope{Connection: connection, Requires: []string{}}
	for _, table := range f.tables {
		namespace, name, _ := cutTable(table)
		scope.Objects = append(scope.Objects, dataaccess.ObjectRef{Namespace: namespace, Name: name})
		scope.Requires = []string{dataaccess.ObjectsExact}
	}
	request := map[string]any{"permissions": []dataaccess.Permission{dataaccess.DataRead}, "scope": scope, "reason": f.reason, "expiresAt": now.Add(time.Duration(f.seconds) * time.Second).UTC().Format(time.RFC3339Nano)}
	if f.lifetime != "" {
		request["lifetime"] = f.lifetime
	}
	return map[string]any{"spaceID": f.scope.space, "projectId": f.scope.project, "request": request}
}

func cutTable(value string) (string, string, bool) {
	for i := 0; i < len(value); i++ {
		if value[i] == '.' {
			return value[:i], value[i+1:], validProtectedSelector(value[:i]) && validProtectedSelector(value[i+1:])
		}
	}
	return "", "", false
}

func accessInboxCommand(dependencies accessCommandDependencies) *cobra.Command {
	var options accessClientOptions
	var scope accessScopeFlags
	command := &cobra.Command{Use: "inbox", Short: "List your agents' requests on a connection and what you may do about each", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		if !scope.valid(true) {
			return Exit("access inbox requires valid --space, --project and --connection", exitCodeUsage)
		}
		return accessCall(command, dependencies, options, http.MethodGet, "agent-requests", url.Values{"spaceID": {scope.space}, "projectId": {scope.project}, "connectionId": {scope.connection}}, nil, agentCaller{})
	}}
	addAccessScopeFlags(command, &scope, true)
	addAccessClientFlags(command, &options)
	return command
}
