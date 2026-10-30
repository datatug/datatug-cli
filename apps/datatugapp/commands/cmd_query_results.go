package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	queryResultsIDFlag          = "id"
	queryResultsPromptStdinFlag = "prompt-stdin"
)

// annotateNow is the time `query results annotate` records; a seam for tests.
var annotateNow = time.Now

// stdinIsTerminal is whether in is an interactive terminal; a seam for tests.
var stdinIsTerminal = func(in io.Reader) bool {
	file, ok := in.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// queryResultsCommand is `query results`: the private result files that
// `query run --result-id` writes. Every subcommand reads or changes the store
// only; none opens a database.
func queryResultsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "results",
		Short: "Locate, check, prune and remove the result files of query run --result-id",
		Long: `The result files of ` + "`query run --result-id <id>`" + ` live in ~/` + queryresults.DefaultDir + `/<id>/
(data.json and meta.json, private to you). A result lives 7 days; the oldest go
first once more than 200 results or 512 MiB are kept.`,
	}
	cmd.AddCommand(queryResultsLocateCommand(), queryResultsCheckCommand(), queryResultsPruneCommand(), queryResultsRmCommand(), queryResultsAnnotateCommand(), queryResultsShowCommand())
	return cmd
}

// Show reopens the validated stored typed recordset without a connection,
// reexecution, fake record keys or label-to-map conversion.
func queryResultsShowCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "show --id <id>", Short: "Reopen a stored typed result without running its query", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := checkResultsFormat(cmd); err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString(queryResultsIDFlag)
		store, err := openResultsStore()
		if err != nil {
			return storeExit(err)
		}
		loaded, err := store.Load(id)
		if err != nil {
			return storeExit(err)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Anonymous bool                  `json:"anonymous,omitempty"`
			Recordset any                   `json:"recordset"`
			Columns   []queryresults.Column `json:"ordinalColumns"`
		}{loaded.Anonymous, loaded.Recordset, loaded.Meta.Columns})
	}}
	cmd.Flags().String(queryResultsIDFlag, "", "Result id")
	_ = cmd.MarkFlagRequired(queryResultsIDFlag)
	addResultsFormatFlag(cmd)
	return cmd
}

// addResultsFormatFlag adds --format, whose only value is json.
func addResultsFormatFlag(cmd *cobra.Command) {
	cmd.Flags().String(queryFormatFlag, "json", "Output format: json")
}

func checkResultsFormat(cmd *cobra.Command) error {
	if format, _ := cmd.Flags().GetString(queryFormatFlag); format != "json" {
		return Exit(fmt.Sprintf("unsupported --format %q: want json", format), exitCodeUsage)
	}
	return nil
}

func queryResultsLocateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "locate --id <id>",
		Short: "Print where a complete result's files are, as one JSON line",
		Long: `Prints {"resultFiles":{"version":1,"id":…,"root":…,"data":…,"meta":…}} for a
complete, unexpired result, else {"resultFiles":null,"reason":"not-found"|"incomplete"|"expired"}.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkResultsFormat(cmd); err != nil {
				return err
			}
			id, _ := cmd.Flags().GetString(queryResultsIDFlag)
			store, err := openResultsStore()
			if err != nil {
				_, _ = cmd.OutOrStdout().Write(queryresults.LocateLine(nil, queryresults.ReasonOf(err)))
				return storeExit(err)
			}
			locator, err := store.Locate(id)
			if err != nil {
				_, _ = cmd.OutOrStdout().Write(queryresults.LocateLine(nil, queryresults.ReasonOf(err)))
				if reason := queryresults.ReasonOf(err); reason == queryresults.ReasonBadID {
					return storeExit(err)
				}
				return nil
			}
			_, _ = cmd.OutOrStdout().Write(queryresults.LocateLine(&locator, ""))
			return nil
		},
	}
	cmd.Flags().String(queryResultsIDFlag, "", "Result id")
	_ = cmd.MarkFlagRequired(queryResultsIDFlag)
	addResultsFormatFlag(cmd)
	return cmd
}

func queryResultsCheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "check",
		Short:        `Answer {"usable":true} when the result store can be written`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkResultsFormat(cmd); err != nil {
				return err
			}
			store, err := openResultsStore()
			if err == nil {
				err = store.Check()
			}
			answer := struct {
				Usable bool   `json:"usable"`
				Reason string `json:"reason,omitempty"`
			}{Usable: err == nil, Reason: queryresults.ReasonOf(err)}
			encoded, _ := json.Marshal(answer)
			_, _ = cmd.OutOrStdout().Write(append(encoded, '\n'))
			return nil
		},
	}
	addResultsFormatFlag(cmd)
	return cmd
}

func queryResultsPruneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "prune [--all]",
		Short:        "Remove expired, incomplete and the oldest results (all of them with --all)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkResultsFormat(cmd); err != nil {
				return err
			}
			all, _ := cmd.Flags().GetBool("all")
			store, err := openResultsStore()
			if err != nil {
				return storeExit(err)
			}
			removed, err := store.Prune(all)
			if err != nil {
				return storeExit(err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "{\"removed\":%d}\n", removed)
			return nil
		},
	}
	cmd.Flags().Bool("all", false, "Remove every result")
	addResultsFormatFlag(cmd)
	return cmd
}

func queryResultsRmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "rm --id <id>",
		Short:        "Remove one result",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkResultsFormat(cmd); err != nil {
				return err
			}
			id, _ := cmd.Flags().GetString(queryResultsIDFlag)
			store, err := openResultsStore()
			if err == nil {
				err = store.Remove(id)
			}
			switch reason := queryresults.ReasonOf(err); reason {
			case "":
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), `{"removed":1}`)
				return nil
			case queryresults.ReasonNotFound:
				return Exit(err.Error(), exitCodeNotFound)
			default:
				return storeExit(err)
			}
		},
	}
	cmd.Flags().String(queryResultsIDFlag, "", "Result id")
	_ = cmd.MarkFlagRequired(queryResultsIDFlag)
	addResultsFormatFlag(cmd)
	return cmd
}

// queryResultsAnnotateCommand is `query results annotate`: it records the
// prompt that produced a result. The text comes from stdin only, so it never
// appears in a process list or shell history, and it is never printed back.
func queryResultsAnnotateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "annotate --id <id> --prompt-stdin",
		Short: "Record the prompt that produced a result, read from stdin",
		Long: `Reads UTF-8 text from stdin and stores it as the "prompt" of the result's
meta.json: {"text","truncated","at"}. Control characters other than newline and
tab are dropped, the text is trimmed and cut to 8 KiB on a character boundary
("truncated":true). Running it again replaces the prompt; nothing else in
meta.json changes. The text is never echoed: the answer is
{"id":…,"truncated":…,"bytes":…}. An unknown id exits 3 like rm (checked before stdin is read); a store that
cannot be used, a terminal on stdin and empty text exit 2.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkResultsFormat(cmd); err != nil {
				return err
			}
			if read, _ := cmd.Flags().GetBool(queryResultsPromptStdinFlag); !read {
				return Exit("--prompt-stdin is required: the prompt is read from stdin only", exitCodeUsage)
			}
			id, _ := cmd.Flags().GetString(queryResultsIDFlag)
			if !queryresults.ValidID(id) {
				return storeExit(&queryresults.Error{Reason: queryresults.ReasonBadID})
			}
			store, err := openResultsStore()
			if err == nil {
				// An unknown or unusable result is answered before stdin is read.
				_, err = store.Locate(id)
			}
			if err != nil {
				return annotateExit(err)
			}
			if stdinIsTerminal(cmd.InOrStdin()) {
				return Exit("stdin is a terminal: pipe the text on stdin", exitCodeUsage)
			}
			raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), queryresults.MaxPromptReadBytes+1))
			if err != nil {
				return Exit("read stdin failed", exitCodeUsage)
			}
			text, truncated := queryresults.NormalizePrompt(raw)
			if text == "" {
				return Exit("the prompt on stdin is empty", exitCodeUsage)
			}
			if err = store.Annotate(id, text, truncated, annotateNow()); err != nil {
				return annotateExit(err)
			}
			encoded, _ := json.Marshal(struct {
				ID        string `json:"id"`
				Truncated bool   `json:"truncated"`
				Bytes     int    `json:"bytes"`
			}{id, truncated, len(text)})
			_, _ = cmd.OutOrStdout().Write(append(encoded, '\n'))
			return nil
		},
	}
	cmd.Flags().String(queryResultsIDFlag, "", "Result id")
	cmd.Flags().Bool(queryResultsPromptStdinFlag, false, "Read the prompt from stdin (required)")
	_ = cmd.MarkFlagRequired(queryResultsIDFlag)
	addResultsFormatFlag(cmd)
	return cmd
}

// annotateExit is the exit of a failed annotate: 3 for an unknown id, as rm
// has it, else the result-store exit.
func annotateExit(err error) error {
	if queryresults.ReasonOf(err) == queryresults.ReasonNotFound {
		return Exit(err.Error(), exitCodeNotFound)
	}
	return storeExit(err)
}
