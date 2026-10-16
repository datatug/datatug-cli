package commands

import (
	"encoding/json"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/spf13/cobra"
)

const queryResultsIDFlag = "id"

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
	cmd.AddCommand(queryResultsLocateCommand(), queryResultsCheckCommand(), queryResultsPruneCommand(), queryResultsRmCommand())
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
