package commands

// specscore: feature/cli/daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/datatug/datatug-cli/pkg/daemon"
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
			var req daemon.LinkRequest
			req.Project, _ = f.GetString("project")
			req.Env, _ = f.GetString("env")
			req.DB, _ = f.GetString("db")
			req.Table, _ = f.GetString("table")
			req.Schema, _ = f.GetString("schema")
			req.View, _ = f.GetBool("view")
			req.Records, _ = f.GetBool("records")
			req.Limit, _ = f.GetInt("limit")
			req.Record, _ = f.GetString("record")
			link, err := daemon.BuildLink(newDaemonManager().Status(), req)
			if err != nil {
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
	f.String("db", "", "Database (catalog) id as the project names it, never a connection URL")
	f.String("table", "", "Table or view name")
	f.String("schema", "", "Schema of the table (PostgreSQL)")
	f.Bool("view", false, "The object is a view")
	f.Bool("records", false, "Link to the top records of the table")
	f.Int("limit", 0, "With --records: how many rows (default 50, at most 1000)")
	f.String("record", "", "Link to one record: its primary key as a JSON object")
	addFormatFlag(cmd)
	return cmd
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
