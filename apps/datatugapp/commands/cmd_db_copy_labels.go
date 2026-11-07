package commands

import (
	"context"
	"fmt"
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
// engine masks, so a selected table with a column whose effective label is
// protected (an owner label, a pending proposal, or a read-time suggestion an
// owner has not dismissed) refuses the whole copy, for every principal: copy
// has no policies to switch off. A source that is not a project source has no
// labels and is not checked.
//
// It checks the same way as the TugQL guard of a project read
// (federatedLabelGuard): a label document that cannot be read, or a source
// that cannot be opened, refuses; a source with a protected label stored for
// it is checked fully (an object that cannot be shown to be a reviewed table
// is masked whole, and a table whose columns cannot be listed refuses); a
// source with none is checked by read-time suggestions only. A source whose
// tables cannot be listed refuses whether or not a label is stored for it: a
// copy writes every table, so a check that cannot see them cannot pass it.
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
	db, err := openCopySource(ctx, src, schema)
	if err != nil {
		return fmt.Sprintf("source %s cannot be opened to check its sensitivity labels, so the copy is refused", project.catalog)
	}
	defer dbcopy.CloseSource(db)
	labelled := sourceHasProtectedLabel(doc, project.catalog)
	refs, err := listCollections(ctx, db, nil)
	if err != nil {
		return fmt.Sprintf("the tables of %s cannot be listed to check their sensitivity labels, so the copy is refused", project.catalog)
	}
	// A filter the copy itself refuses checks every table: never fewer than it copies.
	if directives, err := buildDirectivesFromFlags(cmd); err == nil {
		if selected, err := filter.ApplyTableFilter(refs, directives); err == nil {
			refs = selected
		}
	}
	for _, ref := range refs {
		if reason := copyTableRefusal(ctx, db, doc, src, project.catalog, ref.Name(), labelled); reason != "" {
			return reason
		}
	}
	return ""
}

// copyTableRefusal is copyLabelRefusal for one selected table.
func copyTableRefusal(ctx context.Context, db dal.DB, doc meaninglabels.Document, src dbcopy.BackendRef, catalog, table string, labelled bool) string {
	for _, l := range doc.Labels {
		if l.Class.Protected() && strings.EqualFold(l.Target.Source, catalog) && meaninglabels.TableMatches(l.Target.Table, table) {
			return copyProtected(catalog, table, meaninglabels.ColumnLabel{Column: l.Target.Column, Class: l.Class})
		}
	}
	columns, ok := collectionColumns(ctx, db, table)
	if !ok && labelled {
		return fmt.Sprintf("the columns of %s.%s cannot be listed to check their sensitivity labels, so the copy is refused; leave the table out with --exclude", catalog, table)
	}
	object := &projectRead{table: table, backend: src}
	if labelled {
		object.describe(ctx, db)
	}
	for _, l := range meaninglabels.EffectiveObject(doc, catalog, table, object.view, labelColumnsOf(columns)) {
		if l.Protected() {
			return copyProtected(catalog, table, l)
		}
	}
	return ""
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
