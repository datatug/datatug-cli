package commands

// specscore: feature/cli/daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/daemon"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/spf13/cobra"
)

const (
	// ClientFlag and ClientEnv name the global caller flag and its environment fallback.
	ClientFlag = "client"
	ClientEnv  = "DATATUG_CLIENT"

	daemonFormatJSON = "json"
	daemonFormatText = "text"
	// callerCLI is recorded as startedBy when `daemon start` is run without a caller.
	callerCLI = "cli"
)

// newDaemonManager is the seam tests replace; it defaults to the real OS.
var newDaemonManager = daemon.NewManager

// daemonCommand wires `datatug daemon status|start|stop|url`.
func daemonCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Own the local read-only API server that AI-agent mods link their cards to",
		Long: "datatug daemon owns one `datatug serve` process bound to 127.0.0.1, started read-only\n" +
			"(no --allow-writes, --allow-live-connections or --allow-opaque-sql) and exiting after " + daemon.IdleTimeout.String() + " with no request.\n" +
			"State (pidfile, record, log) lives in ~/datatug/daemon; `datatug daemon stop` stops it.\n" +
			"Any command run with --client claude-mod (or DATATUG_CLIENT) ensures it is running afterwards.",
	}
	cmd.AddCommand(daemonStatusCommand(), daemonStartCommand(), daemonStopCommand(), daemonURLCommand())
	return cmd
}

func addFormatFlag(cmd *cobra.Command) {
	cmd.Flags().String("format", daemonFormatJSON, "Output format: json or text")
}

func daemonFormat(cmd *cobra.Command) (string, error) {
	format, _ := cmd.Flags().GetString("format")
	if format != daemonFormatJSON && format != daemonFormatText {
		return "", Exit(fmt.Sprintf("unknown --format %q: use json or text", format), exitCodeUsage)
	}
	return format, nil
}

func daemonStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon runs, its URL, pid, owner and log (never starts it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := daemonFormat(cmd)
			if err != nil {
				return err
			}
			return printDaemonStatus(cmd.OutOrStdout(), format, newDaemonManager().Status())
		},
	}
	addFormatFlag(cmd)
	return cmd
}

func daemonStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Ensure the daemon runs: reuse it or start it (exit 4 when it cannot be started)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := daemonFormat(cmd)
			if err != nil {
				return err
			}
			caller := callerOf(cmd)
			if caller == "" {
				caller = callerCLI
			}
			st, startErr := newDaemonManager().Start(caller)
			if err = printDaemonStatus(cmd.OutOrStdout(), format, st); err != nil {
				return err
			}
			if startErr != nil {
				if st.StartError == daemon.StartPolicyPrincipalRequired {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), policyPrincipalWarning)
				}
				return Exit(startErr.Error(), exitCodeDatabase)
			}
			return nil
		},
	}
	addFormatFlag(cmd)
	return cmd
}

func daemonStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon this CLI started (never a server it did not start)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := daemonFormat(cmd)
			if err != nil {
				return err
			}
			m := newDaemonManager()
			if err = m.Stop(); errors.Is(err, daemon.ErrNotOwned) {
				return Exit(err.Error(), exitCodeNotFound)
			} else if err != nil {
				return Exit(err.Error(), exitCodeDatabase)
			}
			return printDaemonStatus(cmd.OutOrStdout(), format, m.Status())
		},
	}
	addFormatFlag(cmd)
	return cmd
}

func daemonURLCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the web UI link of a table, its top records or one record (never starts the daemon)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := daemonFormat(cmd)
			if err != nil {
				return err
			}
			f := cmd.Flags()
			if f.Changed("result") {
				return writeResultLink(cmd, format)
			}
			if f.Changed("row") {
				return Exit("--row is the position of a row of a result: it goes with --result", exitCodeUsage)
			}
			view, err := tableViewFlag(cmd)
			if err != nil {
				return err
			}
			var req daemon.LinkRequest
			req.Project, _ = f.GetString("project")
			req.Env, _ = f.GetString("env")
			req.DB, _ = f.GetString("db")
			req.Table, _ = f.GetString("table")
			req.Schema, _ = f.GetString("schema")
			req.View = view
			req.Records, _ = f.GetBool("records")
			req.Limit, _ = f.GetInt("limit")
			req.Record, _ = f.GetString("record")
			if req, err = resolveLinkDB(cmd.Context(), req); err != nil {
				return err
			}
			st := newDaemonManager().Status()
			req.Code = linkCode(st)
			req.Metadata = func() *daemon.Metadata { return linkMetadata(req) }
			link, err := daemon.BuildLink(st, req)
			var notFound daemon.NotFoundError
			if errors.As(err, &notFound) {
				return Exit(err.Error(), exitCodeNotFound)
			} else if err != nil {
				return Exit(err.Error(), exitCodeUsage)
			}
			if format == daemonFormatText {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), link.URL)
				return err
			}
			return writeJSON(cmd.OutOrStdout(), link)
		},
	}
	f := cmd.Flags()
	f.String("project", "", "Project id")
	f.String("env", "", "Environment id")
	f.String("db", "", "Database (catalog) id as the project names it, or a datatug://<project>/<db>[?env=<ENV>] reference (instead of --project and --env); never a connection URL")
	f.String("table", "", "Table or view name")
	f.String("schema", "", "Schema of the table (PostgreSQL)")
	f.String("view", "", "The object is a view (bare --view); with --result, the view of the result page: --view=table|record|columns|sources|related")
	f.Lookup("view").NoOptDefVal = "true"
	f.String("result", "", "Link to a stored query result (the id of `query run --result-id`) instead of a table")
	f.Int("row", 0, "With --result: the position of the row to show (1-based)")
	f.Bool("records", false, "Link to the top records of the table")
	f.Int("limit", 0, "With --records: how many rows (default 50, at most 1000)")
	f.String("record", "", "Link to one record: its primary key as a JSON object")
	addFormatFlag(cmd)
	return cmd
}

// tableViewFlag is --view of a table link: bare (or =true) for a view.
func tableViewFlag(cmd *cobra.Command) (bool, error) {
	switch view, _ := cmd.Flags().GetString("view"); view {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, Exit("--view takes no value for a table link; a view name such as --view=sources goes with --result", exitCodeUsage)
	}
}

// resultLinkExclusive are the flags of a table link, refused with --result.
var resultLinkExclusive = []string{"project", "env", "db", "table", "schema", "records", "limit", "record"}

// writeResultLink is `daemon url --result <id> [--view=<view>] [--row <n>]`.
// Like every `daemon url`, it never starts the daemon and sends no request;
// whether the result is there is read from the store on disk.
func writeResultLink(cmd *cobra.Command, format string) error {
	f := cmd.Flags()
	for _, name := range resultLinkExclusive {
		if f.Changed(name) {
			return Exit("--result names a whole result: --"+name+" is not taken with it", exitCodeUsage)
		}
	}
	var req daemon.ResultLinkRequest
	req.ID, _ = f.GetString("result")
	req.View, _ = f.GetString("view")
	req.Row, _ = f.GetInt("row")
	if f.Changed("row") && req.Row == 0 {
		return Exit(fmt.Sprintf("--row must be between 1 and %d", daemon.MaxResultRow), exitCodeUsage)
	}
	if queryresults.ValidID(req.ID) {
		if store, err := openResultsStore(); err == nil {
			_, err = store.Locate(req.ID)
			req.Exists = err == nil
		}
	}
	st := newDaemonManager().Status()
	if req.Exists {
		req.Code = linkCode(st)
	}
	link, err := daemon.BuildResultLink(st, req, queryresults.ValidID)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if format == daemonFormatText {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), link.URL)
		return err
	}
	return writeJSON(cmd.OutOrStdout(), link)
}

// resolveLinkDB turns a datatug://<project>/<db>[?env=<ENV>] --db into the project, environment
// and source id of the link. The reference replaces --project and --env (giving both is exit 2); a
// missing environment is read from the project's registry on disk (the one environment that lists
// the source), with no network and no database connection. Any other --db must be a plain source
// id: a connection URL or a path is exit 2 and is never echoed.
func resolveLinkDB(ctx context.Context, req daemon.LinkRequest) (daemon.LinkRequest, error) {
	if !dbcopy.IsDatatugRef(req.DB) {
		if req.DB != "" && !dbcopy.IsPlainSourceID(req.DB) {
			return req, Exit("--db must be a source id or a datatug://<project>/<db> reference, never a connection URL or a path", exitCodeUsage)
		}
		return req, nil
	}
	if req.Project != "" || req.Env != "" {
		return req, Exit("give --db datatug://... or --project, --env and --db <id>, not both", exitCodeUsage)
	}
	ref, err := dbcopy.ParseDatatugRef(req.DB)
	if err != nil {
		return req, Exit(err.Error(), exitCodeUsage)
	}
	if ref.Project == "" {
		return req, Exit("the reference names no project: write datatug://<project>/<db>", exitCodeUsage)
	}
	req.Project, req.DB, req.Env = ref.Project, ref.DB, ref.Env
	if req.Env != "" {
		return req, nil
	}
	base, err := openProjectForRead(ref.Project, "")
	if err != nil {
		return req, err
	}
	holding, err := api.EnvironmentsListingSource(ctx, base.store.GetProjectStore(base.projectID), ref.DB)
	if err != nil {
		return req, Exit(err.Error(), exitCodeDatabase)
	}
	switch len(holding) {
	case 0:
		return req, Exit(fmt.Sprintf("source %q is not listed in any environment of the project", dbcopy.SourceIDDisplay(ref.DB)), exitCodeNotFound)
	case 1:
		req.Env = holding[0]
		return req, nil
	}
	return req, Exit((&api.AmbiguousSourceError{Environments: holding}).Error(), exitCodeUsage)
}

// linkMetadata reads the scanned relations named like the request's table from the project on disk
// (the files `show` lists, but only the columns file of that name), with no network and no database
// connection. nil when the project holds none for it: an unknown project, source or environment, a
// source never scanned or with no relation at all, or files that cannot be read (or are too large).
// Nothing of the failure is reported (a text of it would quote a path).
func linkMetadata(req daemon.LinkRequest) *daemon.Metadata {
	if api.ValidateCatalogIdentifiers(req.Env, req.DB) != nil {
		return nil
	}
	base, err := openProjectForRead(req.Project, "")
	if err != nil {
		return nil
	}
	found, scanned, err := api.FindCatalogRelations(base.ProjectDir, req.Env, req.DB, req.Table)
	if err != nil || !scanned {
		return nil
	}
	meta := &daemon.Metadata{Relations: make([]daemon.Relation, 0, len(found))}
	for _, r := range found {
		rel := daemon.Relation{Schema: r.Schema, Name: r.Name, View: r.DbType == "VIEW"}
		keyed := make([]api.CatalogColumn, 0, len(r.Columns))
		for _, c := range r.Columns {
			if c.PrimaryKeyPosition > 0 {
				keyed = append(keyed, c)
			}
		}
		sort.Slice(keyed, func(i, j int) bool { return keyed[i].PrimaryKeyPosition < keyed[j].PrimaryKeyPosition })
		for _, c := range keyed {
			rel.PrimaryKey = append(rel.PrimaryKey, c.Name)
		}
		meta.Relations = append(meta.Relations, rel)
	}
	return meta
}

// linkCode mints the one-time code of the link from the access key that the agent on st's port left
// in the state directory: locally, with no HTTP request, and without starting anything. "" when
// the agent is not running or left no key (an agent that predates the token needs none).
func linkCode(st daemon.Status) string {
	if !st.Running {
		return ""
	}
	secret, err := agentaccess.ReadKey(agentaccess.StateDir(), st.Port)
	if err != nil {
		return ""
	}
	code, _ := agentaccess.MintCode(secret, time.Now()) // "" when there is no entropy: no code
	return code
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printDaemonStatus(w io.Writer, format string, st daemon.Status) error {
	if format == daemonFormatJSON {
		return writeJSON(w, st)
	}
	_, err := fmt.Fprintf(w, "running: %t\nurl: %s\nuiBase: %s\npid: %d\nstartedBy: %s\nstartedAt: %s\nversion: %s\nlogFile: %s\nidleTimeout: %s\nerror: %s\n",
		st.Running, st.URL, st.UIBase, st.PID, st.StartedBy, st.StartedAt, st.Version, st.LogFile, st.IdleTimeout, st.Error)
	return err
}
