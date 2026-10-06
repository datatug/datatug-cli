package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

var openExportSource = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
	return ref.Open(ctx)
}

var runNativeExport = dbcopy.ExportInGitDB

// dbExportCommand writes a complete native inGitDB project from any source
// adapter available through DataTug's DALgo URL dispatcher.
func dbExportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "export", Short: "Export a DALgo database into a native inGitDB project",
		Long: "Exports all source collections into one native inGitDB records file per collection. The destination must not exist; it is published only after every collection succeeds. Supported source URLs: sqlite://, ingitdb://, postgres:// (preview), http://, https://, openvaultdb://, or env:NAME.",
		RunE: dbExportAction,
	}
	cmd.Flags().String("from", "", "Source DALgo URL")
	cmd.Flags().String("to", "", "Destination ingitdb:// local directory (must not exist)")
	cmd.Flags().String("records-format", "json", "Records file format for every table: json, jsonl, ingr, csv, or yaml")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func dbExportAction(cmd *cobra.Command, _ []string) error {
	from, _ := cmd.Flags().GetString("from")
	to, _ := cmd.Flags().GetString("to")
	recordsFormat, _ := cmd.Flags().GetString("records-format")
	srcRef, err := dbcopy.Parse(from)
	if err != nil {
		return Exit(fmt.Sprintf("--from: %v", dbcopy.RedactErrorWithSecrets(err, from)), 2)
	}
	tgtRef, err := dbcopy.Parse(to)
	if err != nil {
		return Exit(fmt.Sprintf("--to: %v", dbcopy.RedactErrorWithSecrets(err, to)), 2)
	}
	if tgtRef.Scheme != "ingitdb" {
		return Exit("--to must be an ingitdb:// local directory", 2)
	}
	srcRef = srcRef.WithFlag("--from")
	src, err := openExportSource(cmd.Context(), srcRef)
	if err != nil {
		return Exit(fmt.Sprintf("open --from: %v", srcRef.OpenFailure(err)), 4)
	}
	counts, err := runNativeExport(cmd.Context(), src, tgtRef.Path, dbcopy.ExportOptions{RecordsFormat: recordsFormat})
	if errors.Is(err, dbcopy.ErrSourceHasNoTables) {
		return Exit("source has no collections to export", 1)
	}
	if err != nil {
		return Exit(redactExportFailure(err, from, srcRef).Error(), 1)
	}
	var total int64
	for _, count := range counts {
		total += count
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "db export: exported %d collections and %d rows to %s\n", len(counts), total, tgtRef.Display())
	return nil
}

// An env:NAME source keeps only its variable name in the CLI argument. The
// parsed ref retains the resolved locator, so scrub secrets from both forms
// before returning an arbitrary provider introspection or row error.
func redactExportFailure(err error, from string, ref dbcopy.BackendRef) error {
	return dbcopy.RedactErrorWithSecrets(dbcopy.RedactErrorWithSecrets(err, ref.Path), from)
}
