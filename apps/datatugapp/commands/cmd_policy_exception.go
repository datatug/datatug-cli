package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/policyexceptions"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

// exitCodeHumanTerminal is the exit code of a person-only command run without
// an interactive terminal.
const exitCodeHumanTerminal = 6

// Seams for tests: the terminal check of apply, reject and revoke, the clock,
// the wait between status polls and the id source.
var (
	exceptionStdinIsTerminal = func(in io.Reader) bool { return stdinIsTerminal(in) }
	exceptionNow             = time.Now
	exceptionSleep           = time.Sleep
	newExceptionID           = policyexceptions.NewID
	listCollections          = dbschema.ListCollections
	describeCollection       = dbschema.DescribeCollection
)

// maxStatusWait bounds status --wait.
const maxStatusWait = policyexceptions.PendingTTL

const policyExceptionLong = "A local policy exception is a time-boxed allow rule you add to your own local policy, on this machine: " +
	"one role, one table of one source of a project, all rows of that table, and an exact list of fields. " +
	"An agent proposes one (propose); you apply, reject or revoke it in your own interactive terminal, never in the agent's shell. " +
	"It is overlaid on the policy document it names each time `query run --project <p> --source <db>` loads the project's policies, " +
	"only until it expires and only while that document is unchanged. It is not an approval valid anywhere else.\n\n" +
	"Exceptions and their audit log live in <project>/.datatug/policy-exceptions/. Local Perimeter keeps an honest agent inside the lines: " +
	"an agent that can drive a pseudo-terminal, or that edits files as you, is not held by it. " +
	"A real security boundary needs DB-native permissions or DataTug Cloud approvals."

func policyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Local access policies of a project", Args: cobra.NoArgs}
	exception := &cobra.Command{Use: "exception", Short: "Propose and decide time-boxed local policy exceptions", Long: policyExceptionLong, Args: cobra.NoArgs}
	exception.AddCommand(exceptionProposeCommand(), exceptionStatusCommand(), exceptionListCommand(), exceptionApplyCommand(), exceptionRejectCommand(), exceptionRevokeCommand(), exceptionAuditCommand())
	cmd.AddCommand(exception)
	return cmd
}

// exceptionProject is the project an exception command works on.
type exceptionProject struct {
	dir, id, arg string
	store        policyexceptions.Store
	projStore    datatug.ProjectStore
}

func addExceptionCommonFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("project", "p", "", "Registered project ID or project directory (required)")
	cmd.Flags().String("format", "text", "Output format: text or json")
}

func readExceptionCommon(cmd *cobra.Command) (exceptionProject, string, error) {
	project, _ := cmd.Flags().GetString("project")
	format, _ := cmd.Flags().GetString("format")
	if format != "text" && format != "json" {
		return exceptionProject{}, format, Exit(fmt.Sprintf("unsupported --format %q: want text or json", format), exitCodeUsage)
	}
	if project == "" {
		return exceptionProject{}, format, Exit("--project is required", exitCodeUsage)
	}
	dir, id, projStore, err := resolveQueryProjectRef(project)
	if err != nil {
		return exceptionProject{}, format, Exit(err.Error(), exitCodeUsage)
	}
	return exceptionProject{dir: dir, id: id, arg: project, store: policyexceptions.Store{ProjectDir: dir}, projStore: projStore}, format, nil
}

func (p exceptionProject) command(verb, id string) string {
	return fmt.Sprintf("datatug policy exception %s %s --project %s", verb, id, shellWord(p.arg))
}

// exceptionView is the JSON shape of one exception.
type exceptionView struct {
	ID               string                     `json:"id"`
	Kind             string                     `json:"kind"`
	State            policyexceptions.State     `json:"state"`
	ProjectID        string                     `json:"projectId"`
	Source           string                     `json:"source"`
	Policy           policyexceptions.PolicyRef `json:"policy"`
	Role             string                     `json:"role"`
	Table            string                     `json:"table"`
	Rows             string                     `json:"rows"`
	Fields           []string                   `json:"fields"`
	AppliedFields    []string                   `json:"appliedFields,omitempty"`
	Reason           string                     `json:"reason"`
	DurationSeconds  int64                      `json:"durationSeconds"`
	ProposedAt       time.Time                  `json:"proposedAt"`
	PendingExpiresAt time.Time                  `json:"pendingExpiresAt"`
	DecidedAt        *time.Time                 `json:"decidedAt,omitempty"`
	ExpiresAt        *time.Time                 `json:"expiresAt,omitempty"`
	Next             map[string]string          `json:"next,omitempty"`
	Note             string                     `json:"note"`
}

func viewOf(e policyexceptions.Exception, now time.Time) exceptionView {
	return exceptionView{ID: e.ID, Kind: e.Kind, State: e.Status(now), ProjectID: e.ProjectID, Source: e.Source, Policy: e.Policy, Role: e.Role, Table: e.Table,
		Rows: "all rows of " + e.Table, Fields: e.Fields, AppliedFields: e.AppliedFields, Reason: e.Reason, DurationSeconds: e.DurationSeconds,
		ProposedAt: e.ProposedAt, PendingExpiresAt: e.PendingExpiresAt, DecidedAt: e.DecidedAt, ExpiresAt: e.ExpiresAt, Note: policyexceptions.HonestLimit}
}

func writeExceptionView(w io.Writer, format string, view exceptionView) error {
	if format == "json" {
		return writeJSONLine(w, view)
	}
	_, err := io.WriteString(w, exceptionText(view))
	return err
}

// exceptionText is the text form of one exception.
func exceptionText(view exceptionView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: role %s on %s.%s (all rows), fields %s\n", view.ID, view.State, view.Role, view.Source, view.Table, strings.Join(view.Fields, ", "))
	if len(view.AppliedFields) > 0 {
		fmt.Fprintf(&b, "  applied fields: %s\n", strings.Join(view.AppliedFields, ", "))
	}
	if view.ExpiresAt != nil {
		fmt.Fprintf(&b, "  expires at: %s\n", view.ExpiresAt.UTC().Format(time.RFC3339))
	} else if view.State == policyexceptions.StatePending {
		fmt.Fprintf(&b, "  waits for a decision until: %s\n", view.PendingExpiresAt.UTC().Format(time.RFC3339))
	}
	verbs := make([]string, 0, len(view.Next))
	for verb := range view.Next {
		verbs = append(verbs, verb)
	}
	sort.Strings(verbs)
	for _, verb := range verbs {
		fmt.Fprintf(&b, "  %s: %s\n", verb, view.Next[verb])
	}
	b.WriteString(view.Note + "\n")
	return b.String()
}

// writeJSONLine writes v as one line of JSON, without HTML escaping.
func writeJSONLine(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(v)
}

// projectPolicy is one policy document of a project.
type projectPolicy struct {
	name, rel string
	data      []byte
	codec     access.Codec
	loaded    accesspolicies.Loaded
}

// findProjectPolicy picks the policy document an exception binds to: the one
// whose metadata name or file name (without extension) is want, or the only
// one when want is empty.
func findProjectPolicy(projectDir, want string) (projectPolicy, error) {
	dir := filepath.Join(projectDir, projectPoliciesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return projectPolicy{}, Exit("the project has no policies/ folder: an exception is added to an existing local policy", exitCodeUsage)
	}
	var found []projectPolicy
	for _, entry := range entries {
		codec := accesspolicies.CodecFor(entry.Name())
		if entry.IsDir() || codec == nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return projectPolicy{}, Exit(fmt.Sprintf("policy %s cannot be read", entry.Name()), exitCodeUsage)
		}
		item, err := accesspolicies.DecodeLoaded(data, codec, path)
		if err != nil {
			return projectPolicy{}, Exit(fmt.Sprintf("policy %s: %v", entry.Name(), err), exitCodeUsage)
		}
		base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if want != "" && want != item.Policy.Name() && want != base {
			continue
		}
		found = append(found, projectPolicy{name: item.Policy.Name(), rel: projectPoliciesDir + "/" + entry.Name(), data: data, codec: codec, loaded: item})
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case want == "" && len(found) > 1:
		return projectPolicy{}, Exit("the project has several policies: pass --policy <name>", exitCodeUsage)
	}
	return projectPolicy{}, Exit(fmt.Sprintf("no single policy of the project is named %q", want), exitCodeUsage)
}

// checkRoleHasNoRule refuses an exception on a table the role already has a
// rule for: an exception only opens a table the role cannot read at all, so
// it never widens a row condition or a field list.
func checkRoleHasNoRule(ctx context.Context, policy projectPolicy, role, table string) error {
	if err := policyexceptions.Applicable(policy.data, policy.codec); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	ctx = access.WithPrincipal(ctx, access.Principal{Roles: []string{role}})
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(table, ""))).SelectColumns()
	for _, line := range accesspolicies.Explain(ctx, []accesspolicies.Loaded{policy.loaded}, query, nil) {
		if line.Allowed || line.Rule != "" {
			return Exit(fmt.Sprintf("role %s already has a rule on %s in policy %s: an exception only opens a table the role cannot read at all; change the policy itself instead", role, table, policy.name), exitCodeUsage)
		}
	}
	return nil
}

// sourceColumns opens the source and returns the table's canonical name and
// its column names, as the source reports them.
func sourceColumns(ctx context.Context, sourceURL, table string) (string, []string, error) {
	backend, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return "", nil, Exit(err.Error(), exitCodeUsage)
	}
	db, err := openBackend(ctx, backend)
	if err != nil {
		return "", nil, Exit(backend.OpenFailure(err).Error(), exitCodeDatabase)
	}
	defer dbcopy.CloseSource(db)
	collections, err := listCollections(ctx, db, nil)
	if err != nil {
		return "", nil, Exit("the tables of the source cannot be listed", exitCodeDatabase)
	}
	canonical := ""
	for _, ref := range collections {
		if strings.EqualFold(ref.Name(), table) {
			if canonical != "" {
				return "", nil, Exit(fmt.Sprintf("table %q is ambiguous in the source", table), exitCodeUsage)
			}
			canonical = ref.Name()
		}
	}
	if canonical == "" {
		return "", nil, Exit(fmt.Sprintf("the source has no table %q", table), exitCodeUsage)
	}
	ref := dal.NewRootCollectionRef(canonical, "")
	def, err := describeCollection(ctx, db, &ref)
	if err != nil || len(def.Fields) == 0 {
		return "", nil, Exit(fmt.Sprintf("the columns of %s cannot be read", canonical), exitCodeDatabase)
	}
	columns := make([]string, len(def.Fields))
	for i, field := range def.Fields {
		columns[i] = string(field.Name)
	}
	return canonical, columns, nil
}

// canonicalFields maps each requested field to the column of the same name
// (ignoring case) and refuses one the table does not have.
func canonicalFields(requested, columns []string, table string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, name := range requested {
		match := ""
		for _, column := range columns {
			if strings.EqualFold(column, name) {
				match = column
			}
		}
		if match == "" {
			return nil, Exit(fmt.Sprintf("%s has no column %q", table, name), exitCodeUsage)
		}
		if !seen[match] {
			seen[match] = true
			out = append(out, match)
		}
	}
	return out, nil
}

func checkExceptionReason(reason string) error {
	if reason == "" || len(reason) > 256 || strings.ContainsAny(reason, "\x00\r\n\x1b") {
		return Exit("--reason is required: one line of at most 256 characters saying why", exitCodeUsage)
	}
	return nil
}

func exceptionProposeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "propose",
		Short: "Propose a local policy exception for one table (the agent side); the person applies it in their own terminal",
		Long:  policyExceptionLong,
		Example: "  datatug policy exception propose --project datatug-demo --source chinook --role agent --table Employee \\\n" +
			"    --fields EmployeeId,FirstName,LastName,Title --for 15m --reason \"build the support escalation list\" --format json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         exceptionProposeAction,
	}
	addExceptionCommonFlags(cmd)
	f := cmd.Flags()
	f.String("source", "", "Source (database) ID of the project (required)")
	f.String("env", "", "Environment of the source (default: the project's only environment)")
	f.String("table", "", "Exact table name; no patterns (required)")
	f.StringSlice("fields", nil, "Exact column names, comma-separated; no patterns (required)")
	f.String("role", "", "Role the exception is for (required)")
	f.String("policy", "", "Policy of the project the exception is added to, by metadata name or file name (default: the project's only policy)")
	f.Duration("for", policyexceptions.DefaultDuration, "How long the exception lasts once applied (at most 8h)")
	f.String("reason", "", "Why the access is needed (required)")
	return cmd
}

func exceptionProposeAction(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	source, _ := f.GetString("source")
	env, _ := f.GetString("env")
	table, _ := f.GetString("table")
	fields, _ := f.GetStringSlice("fields")
	role, _ := f.GetString("role")
	policyName, _ := f.GetString("policy")
	duration, _ := f.GetDuration("for")
	reason, _ := f.GetString("reason")
	for _, check := range []struct{ what, value string }{{"--source", source}, {"--table", table}, {"--role", role}} {
		if err := policyexceptions.CheckName(check.what, check.value); err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
	}
	if len(fields) == 0 {
		return Exit("--fields is required: the exact columns the exception opens", exitCodeUsage)
	}
	for _, field := range fields {
		if err := policyexceptions.CheckName("field", field); err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
	}
	if err := policyexceptions.CheckDuration(duration); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if err := checkExceptionReason(reason); err != nil {
		return err
	}
	project, format, err := readExceptionCommon(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	store := project.projStore
	envID, err := resolveQueryEnvironment(ctx, store, env)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	resolved, err := resolveQuerySource(ctx, store, project.dir, envID, source)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	canonical, columns, err := sourceColumns(ctx, resolved.URL, table)
	if err != nil {
		return err
	}
	if fields, err = canonicalFields(fields, columns, canonical); err != nil {
		return err
	}
	policy, err := findProjectPolicy(project.dir, policyName)
	if err != nil {
		return err
	}
	if err = checkRoleHasNoRule(ctx, policy, role, canonical); err != nil {
		return err
	}
	id, err := newExceptionID()
	if err != nil {
		return err
	}
	now := exceptionNow().UTC()
	e := policyexceptions.Exception{
		ID: id, Kind: policyexceptions.Kind, ProjectID: project.id, Source: source,
		Policy: policyexceptions.PolicyRef{Name: policy.name, Path: policy.rel, SHA256: policyexceptions.Fingerprint(policy.data)},
		Role:   role, Table: canonical, Fields: fields, Reason: reason, DurationSeconds: int64(duration / time.Second),
		State: policyexceptions.StatePending, ProposedAt: now, PendingExpiresAt: now.Add(policyexceptions.PendingTTL),
	}
	if err = project.store.Propose(e); err != nil {
		return err
	}
	view := viewOf(e, now)
	view.Next = map[string]string{
		"apply":  project.command("apply", id),
		"reject": project.command("reject", id),
		"status": project.command("status", id) + " --wait 10m --format json",
	}
	view.Note = "Ask the person to run apply or reject in their own terminal; an agent cannot. " + policyexceptions.HonestLimit
	return writeExceptionView(cmd.OutOrStdout(), format, view)
}

func exceptionStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "status <id>",
		Short:        "Show one local policy exception: pending, applied, rejected, revoked or expired",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, format, err := readExceptionCommon(cmd)
			if err != nil {
				return err
			}
			wait, _ := cmd.Flags().GetDuration("wait")
			if wait < 0 || wait > maxStatusWait {
				return Exit(fmt.Sprintf("--wait must be between 0 and %s", maxStatusWait), exitCodeUsage)
			}
			deadline := exceptionNow().Add(wait)
			for {
				e, _, err := project.store.Get(args[0])
				if err != nil {
					return exceptionLookupFailure(err)
				}
				now := exceptionNow()
				if e.Status(now) != policyexceptions.StatePending || !now.Before(deadline) {
					return writeExceptionView(cmd.OutOrStdout(), format, viewOf(e, now))
				}
				exceptionSleep(time.Second)
			}
		},
	}
	addExceptionCommonFlags(cmd)
	cmd.Flags().Duration("wait", 0, "Wait up to this long (at most 30m) while the exception is pending, instead of polling")
	return cmd
}

func exceptionLookupFailure(err error) error {
	if errors.Is(err, policyexceptions.ErrNotFound) {
		return Exit("no local policy exception has that id in this project", exitCodeNotFound)
	}
	return err
}

func exceptionListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List the local policy exceptions of a project",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			project, format, err := readExceptionCommon(cmd)
			if err != nil {
				return err
			}
			state, _ := cmd.Flags().GetString("state")
			all, err := project.store.List()
			if err != nil {
				return err
			}
			now := exceptionNow()
			views := []exceptionView{}
			for _, e := range all {
				if state == "" || string(e.Status(now)) == state {
					views = append(views, viewOf(e, now))
				}
			}
			if format == "json" {
				return writeJSONLine(cmd.OutOrStdout(), map[string]any{"kind": policyexceptions.Kind, "exceptions": views})
			}
			var b strings.Builder
			for _, view := range views {
				b.WriteString(exceptionText(view))
			}
			_, err = io.WriteString(cmd.OutOrStdout(), b.String())
			return err
		},
	}
	addExceptionCommonFlags(cmd)
	cmd.Flags().String("state", "", "Only exceptions in this state: pending, applied, rejected, revoked or expired")
	return cmd
}

func exceptionAuditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "audit",
		Short:        "Show the append-only audit log of local policy exceptions (scopes only, never row values)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			project, format, err := readExceptionCommon(cmd)
			if err != nil {
				return err
			}
			events, err := project.store.Audit()
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSONLine(cmd.OutOrStdout(), map[string]any{"kind": policyexceptions.Kind, "events": events})
			}
			var b strings.Builder
			for _, event := range events {
				fmt.Fprintf(&b, "%s %s %s: role %s on %s.%s", event.At.UTC().Format(time.RFC3339), event.ID, event.Event, event.Role, event.Source, event.Table)
				if len(event.Fields) > 0 {
					fmt.Fprintf(&b, " fields %s", strings.Join(event.Fields, ", "))
				}
				b.WriteString("\n")
			}
			_, err = io.WriteString(cmd.OutOrStdout(), b.String())
			return err
		},
	}
	addExceptionCommonFlags(cmd)
	return cmd
}

// humanTerminalRequired refuses a person-only command run without an
// interactive terminal, changing nothing: exit 6.
func humanTerminalRequired(cmd *cobra.Command, verb, id string) error {
	project, _ := cmd.Flags().GetString("project")
	format, _ := cmd.Flags().GetString("format")
	command := fmt.Sprintf("datatug policy exception %s %s --project %s", verb, id, shellWord(project))
	message := "the person who owns the policy must run this in their own interactive terminal: an agent cannot " + verb + " a local policy exception"
	if format == "json" {
		_ = writeJSONLine(cmd.ErrOrStderr(), map[string]any{"error": map[string]string{"code": "HUMAN_TERMINAL_REQUIRED", "message": message, "command": command}})
		return Reported(message, exitCodeHumanTerminal)
	}
	return Exit(message+": "+command, exitCodeHumanTerminal)
}

// personCommand is the shared shape of apply, reject and revoke: the terminal
// check comes before anything is read.
func personCommand(verb, short string, setup func(*cobra.Command), run func(cmd *cobra.Command, project exceptionProject, e policyexceptions.Exception, raw []byte) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:          verb + " <id>",
		Short:        short,
		Long:         policyExceptionLong,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !exceptionStdinIsTerminal(cmd.InOrStdin()) {
				return humanTerminalRequired(cmd, verb, args[0])
			}
			project, _, err := readExceptionCommon(cmd)
			if err != nil {
				return err
			}
			e, raw, err := project.store.Get(args[0])
			if err != nil {
				return exceptionLookupFailure(err)
			}
			return run(cmd, project, e, raw)
		},
	}
	addExceptionCommonFlags(cmd)
	if setup != nil {
		setup(cmd)
	}
	return cmd
}

// errNotPending refuses a decision on an exception that is no longer pending.
func errNotPending(e policyexceptions.Exception, now time.Time) error {
	return Exit(fmt.Sprintf("exception %s is %s: only a pending proposal can be decided; ask for a new one", e.ID, e.Status(now)), exitCodeUsage)
}

func exceptionApplyCommand() *cobra.Command {
	return personCommand("apply", "Apply a proposed local policy exception to your own local policy (you, in your own terminal)",
		func(cmd *cobra.Command) {
			cmd.Flags().StringSlice("fields", nil, "Apply only these of the proposed fields (narrowing only)")
		},
		func(cmd *cobra.Command, project exceptionProject, e policyexceptions.Exception, raw []byte) error {
			now := exceptionNow()
			if e.Status(now) != policyexceptions.StatePending {
				return errNotPending(e, now)
			}
			fields := e.Fields
			if narrowed, _ := cmd.Flags().GetStringSlice("fields"); len(narrowed) > 0 {
				if !policyexceptions.Subset(narrowed, e.Fields) {
					return Exit(fmt.Sprintf("--fields may only narrow the proposal: pick from %s", strings.Join(e.Fields, ", ")), exitCodeUsage)
				}
				fields = nil
				for _, field := range e.Fields {
					for _, keep := range narrowed {
						if keep == field {
							fields = append(fields, field)
							break
						}
					}
				}
			}
			policy, err := findProjectPolicy(project.dir, e.Policy.Name)
			if err != nil || policy.rel != e.Policy.Path || policyexceptions.Fingerprint(policy.data) != e.Policy.SHA256 {
				return Exit("the policy changed since the exception was proposed, so it cannot be applied; ask for a new proposal", exitCodeUsage)
			}
			// The policy is byte for byte the one the proposal was checked against
			// (no rule of the role on the table), so that check still holds.
			scope := policyexceptions.Scope(e, fields, now)
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprint(out, scope)
			_, _ = fmt.Fprintf(out, "Type the table name (%s) to apply this exception; anything else cancels: ", e.Table)
			line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if strings.TrimRight(line, "\r\n") != e.Table {
				return Exit("cancelled: nothing was applied", 1)
			}
			hash := policyexceptions.ScopeHash(scope)
			var expires time.Time
			err = project.store.Commit(e.ID, raw, func(current *policyexceptions.Exception) (policyexceptions.Event, error) {
				at := exceptionNow().UTC()
				if current.Status(at) != policyexceptions.StatePending {
					return policyexceptions.Event{}, errNotPending(*current, at)
				}
				expires = at.Add(current.Duration())
				current.State, current.AppliedFields, current.DecidedAt, current.ExpiresAt, current.ScopeHash = policyexceptions.StateApplied, fields, &at, &expires, hash
				kind := "applied"
				if len(fields) < len(current.Fields) {
					kind = "narrowed"
				}
				event := policyexceptions.NewEvent(kind, *current, fields, at)
				event.ScopeHash = hash
				return event, nil
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Applied exception %s until %s. %s\n", e.ID, expires.Format(time.RFC3339), policyexceptions.HonestLimit)
			return err
		})
}

func exceptionRejectCommand() *cobra.Command {
	return personCommand("reject", "Reject a proposed local policy exception (you, in your own terminal)",
		func(cmd *cobra.Command) { cmd.Flags().String("reason", "", "Why it is rejected") },
		func(cmd *cobra.Command, project exceptionProject, e policyexceptions.Exception, raw []byte) error {
			now := exceptionNow()
			if e.Status(now) != policyexceptions.StatePending {
				return errNotPending(e, now)
			}
			reason, _ := cmd.Flags().GetString("reason")
			if reason != "" {
				if err := checkExceptionReason(reason); err != nil {
					return err
				}
			}
			err := project.store.Commit(e.ID, raw, func(current *policyexceptions.Exception) (policyexceptions.Event, error) {
				at := exceptionNow().UTC()
				current.State, current.DecidedAt, current.DecisionReason = policyexceptions.StateRejected, &at, reason
				event := policyexceptions.NewEvent("rejected", *current, nil, at)
				event.Reason = reason
				return event, nil
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Rejected exception %s: nothing was added to the policy.\n", e.ID)
			return err
		})
}

func exceptionRevokeCommand() *cobra.Command {
	return personCommand("revoke", "Revoke an applied local policy exception before it expires (you, in your own terminal)", nil,
		func(cmd *cobra.Command, project exceptionProject, e policyexceptions.Exception, raw []byte) error {
			now := exceptionNow()
			if e.Status(now) != policyexceptions.StateApplied {
				return Exit(fmt.Sprintf("exception %s is %s: only an applied exception can be revoked", e.ID, e.Status(now)), exitCodeUsage)
			}
			err := project.store.Commit(e.ID, raw, func(current *policyexceptions.Exception) (policyexceptions.Event, error) {
				at := exceptionNow().UTC()
				current.State, current.DecidedAt = policyexceptions.StateRevoked, &at
				return policyexceptions.NewEvent("revoked", *current, nil, at), nil
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked exception %s: its rule no longer applies.\n", e.ID)
			return err
		})
}
