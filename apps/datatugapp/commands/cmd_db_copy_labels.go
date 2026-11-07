package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/spf13/cobra"
)

// copyLabelRefusal is the Meaning Graph sensitivity-label check of `db copy`
// and `db export` whose --from is a project source (a datatug:// reference):
// the reason the copy is refused, or "" when it may run. A copy writes every
// value of every column of the tables it selects (--include/--exclude or a
// filter config; --where and --limit select rows, never columns), and no copy
// engine masks, so it follows the rule of reads that cannot mask (native SQL,
// a TugQL read): a selected column protected by a stored label (an owner label
// or a pending proposal, on the column or its whole table) refuses the whole
// copy for every caller, as copy has no policies to switch off. A column
// protected only by a read-time suggestion refuses an agent (a detected AI
// harness); anyone else copies it, and stderr names the suggested columns
// once. A source that is not a project source has no labels and is not
// checked.
//
// It checks the same way as the TugQL guard of a project read
// (federatedLabelGuard): a label document that cannot be read refuses; a
// source with a protected label stored for it is checked fully (an object that
// cannot be shown to be a reviewed table is masked whole, and a table whose
// columns cannot be listed refuses); a source with none is checked by
// read-time suggestions only. A source that cannot be opened, or whose tables
// cannot be listed, refuses when a label is stored for it or the caller is an
// agent: a check that cannot see what a copy writes cannot pass it.
//
// The source is opened as the copy opens it (openCopySource), with the copy's
// PostgreSQL schema, so the check sees the same tables the copy writes.
//
// Labels are looked up under the catalog id the reference names, as a
// single-source `query run --db datatug://...` does; another catalog id of the
// project that resolves to the same database is not consulted.
func copyLabelRefusal(cmd *cobra.Command, src dbcopy.BackendRef, project *projectSource, schema string) string {
	if project == nil {
		return ""
	}
	ctx := cmd.Context()
	doc, err := loadLabels(ctx, project.dir)
	if err != nil {
		return errLabelsUnreadable.Message
	}
	labelled := sourceHasProtectedLabel(doc, project.catalog)
	strict := callerIsAgent()
	db, err := openCopySource(ctx, src, schema)
	if err != nil {
		if labelled || strict {
			return fmt.Sprintf("source %s cannot be opened to check its sensitivity labels, so the copy is refused", project.catalog)
		}
		return "" // the copy reports the source it cannot open
	}
	defer dbcopy.CloseSource(db)
	refs, err := listCollections(ctx, db, nil)
	if err != nil {
		if labelled || strict {
			return fmt.Sprintf("the tables of %s cannot be listed to check their sensitivity labels, so the copy is refused", project.catalog)
		}
		return ""
	}
	// A filter the copy itself refuses checks every table: never fewer than it copies.
	if directives, err := buildDirectivesFromFlags(cmd); err == nil {
		if selected, err := filter.ApplyTableFilter(refs, directives); err == nil {
			refs = selected
		}
	}
	var suggested []string
	for _, ref := range refs {
		reason, names := copyTableRefusal(ctx, db, doc, src, project.catalog, ref.Name(), labelled, strict)
		if reason != "" {
			return reason
		}
		suggested = append(suggested, names...)
	}
	sort.Strings(suggested)
	warner := &suggestionWarner{out: cmd.ErrOrStderr(), warned: map[string]bool{}}
	warner.warn("this copy", project.catalog, suggested)
	return ""
}

// copyTableRefusal is copyLabelRefusal for one selected table: the reason the
// copy is refused, or the table.column names that only a read-time suggestion
// protects.
func copyTableRefusal(ctx context.Context, db dal.DB, doc meaninglabels.Document, src dbcopy.BackendRef, catalog, table string, labelled, strict bool) (string, []string) {
	for _, l := range doc.Labels {
		if l.Class.Protected() && strings.EqualFold(l.Target.Source, catalog) && meaninglabels.TableMatches(l.Target.Table, table) {
			return copyProtected(catalog, table, meaninglabels.ColumnLabel{Column: l.Target.Column, Class: l.Class}), nil
		}
	}
	columns, ok := collectionColumns(ctx, db, table)
	if !ok && labelled {
		return fmt.Sprintf("the columns of %s.%s cannot be listed to check their sensitivity labels, so the copy is refused; leave the table out with --exclude", catalog, table), nil
	}
	object := &projectRead{table: table, backend: src}
	if labelled {
		object.describe(ctx, db)
	}
	var suggested []string
	for _, l := range meaninglabels.EffectiveObject(doc, catalog, table, object.view, labelColumnsOf(columns)) {
		switch {
		case !l.Protected():
		case strict || l.Origin != meaninglabels.FromSuggested:
			return copyProtected(catalog, table, l), nil
		default:
			suggested = append(suggested, table+"."+l.Column)
		}
	}
	return "", suggested
}

// copyProtected is the reason of a copy refused for a protected column.
func copyProtected(catalog, table string, l meaninglabels.ColumnLabel) string {
	target := catalog + "." + table
	if l.Column != "" {
		target += "." + l.Column
	}
	return fmt.Sprintf("%s is protected (%s), and a copy cannot mask labelled columns, so the copy is refused; leave the table out with --exclude, or copy only unlabelled tables with --include", target, l.Class)
}

// copyRefusedFormat and copyRefusedCode are how every command reports a
// copyLabelRefusal: exit 5 with the FIELD_PROTECTED code, as a project read.
const copyRefusedFormat = "access denied: " + meaninglabels.CodeFieldProtected + ": %s"

const copyRefusedCode = exitCodeAccessDenied
