package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// installProjectLabels makes every TugQL and native SQL read of session check
// the project's Meaning Graph sensitivity labels first, for every principal
// whatever its profile (ai-onboarding: labelled columns are masked for every
// principal). catalogs maps each project source URL to the catalog ids that
// resolve to it, the Source of a label target. Used by `query run --project
// --query`, `query run --db datatug://...` and `chat`.
func installProjectLabels(session *secureread.Session, projectDir string, catalogs map[string][]string) {
	session.FederatedColumns = federatedLabelGuard(projectDir)
	session.NativeSQL = nativeSQLLabelGuard(projectDir)
	session.SourceCatalogs = catalogs
}

// errLabelsUnreadable refuses a read whose project labels cannot be read.
var errLabelsUnreadable = &secureread.FieldProtectedError{Message: "the project's sensitivity labels (datatug-labels.json) cannot be read, so the read is refused"}

// sourceHasProtectedLabel reports whether any protected label (owner or
// proposed) is stored for source.
func sourceHasProtectedLabel(doc meaninglabels.Document, source string) bool {
	for _, l := range doc.Labels {
		if l.Class.Protected() && strings.EqualFold(l.Target.Source, source) {
			return true
		}
	}
	return false
}

// federatedLabelGuard is the sensitivity-label check of every TugQL read of a
// project, federated or single-source. A TugQL read cannot mask yet, so a leaf
// that names a column whose effective label is protected, or projects every
// column of a table with one, refuses the whole read, as the single-table
// project read refuses what it cannot mask. A query's database names are the
// project's catalog ids, the Source of a label's target. A label document that
// cannot be read refuses the read.
//
// A source with no protected label stored for it is checked by read-time
// suggestions only: it opens no source unless a wildcard needs its columns,
// treats no object as an unreviewed view, and lets a wildcard whose columns
// cannot be listed pass. A source with a protected label is checked fully, and
// anything that cannot be described refuses.
func federatedLabelGuard(projectDir string) secureread.FederatedColumnGuard {
	return func(ctx context.Context, leaf secureread.FederatedLeaf) error {
		doc, err := loadLabels(ctx, projectDir)
		if err != nil {
			return errLabelsUnreadable
		}
		labelled := sourceHasProtectedLabel(doc, leaf.Database)
		object := &projectRead{table: leaf.Collection, backend: dbcopy.BackendRef{Scheme: leaf.Scheme}}
		var db dal.DB
		openErr := errNotOpened
		if labelled || leaf.AllColumns {
			if db, openErr = leaf.Open(ctx); openErr != nil && labelled {
				return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s cannot be opened to check its sensitivity labels, so the read is refused", leaf.Database)}
			}
		}
		if labelled {
			object.describe(ctx, db)
		}
		names := leaf.Columns
		if leaf.AllColumns {
			for _, l := range doc.Labels {
				if l.Class.Protected() && strings.EqualFold(l.Target.Source, leaf.Database) && meaninglabels.TableMatches(l.Target.Table, leaf.Collection) {
					return federatedProtected(leaf, meaninglabels.ColumnLabel{Column: l.Target.Column, Class: l.Class})
				}
			}
			var columns []string
			ok := false
			if openErr == nil {
				columns, ok = collectionColumns(ctx, db, leaf.Collection)
			}
			if !ok && labelled {
				return &secureread.FieldProtectedError{Message: fmt.Sprintf("the columns of %s.%s cannot be listed to check their sensitivity labels; name the columns the query reads", leaf.Database, leaf.Collection)}
			}
			names = append(names, columns...)
		}
		for _, l := range meaninglabels.EffectiveObject(doc, leaf.Database, leaf.Collection, object.view, labelColumnsOf(names)) {
			if l.Protected() {
				return federatedProtected(leaf, l)
			}
		}
		return nil
	}
}

// errNotOpened marks a leaf whose source the guard did not open.
var errNotOpened = errors.New("not opened")

// collectionColumns lists a collection's column names; ok is false when db
// cannot describe it.
func collectionColumns(ctx context.Context, db dal.DB, collection string) ([]string, bool) {
	ref := dal.NewRootCollectionRef(collection, "")
	def, err := describeCollection(ctx, db, &ref)
	if err != nil || len(def.Fields) == 0 {
		return nil, false
	}
	names := make([]string, len(def.Fields))
	for i, field := range def.Fields {
		names[i] = string(field.Name)
	}
	return names, true
}

// federatedProtected is the FIELD_PROTECTED refusal of a TugQL read.
func federatedProtected(leaf secureread.FederatedLeaf, l meaninglabels.ColumnLabel) error {
	target := leaf.Database + "." + leaf.Collection
	if l.Column != "" {
		target += "." + l.Column
	}
	return &secureread.FieldProtectedError{Message: fmt.Sprintf("%s is protected (%s), and federated reads cannot mask labelled columns yet, so the read is refused; name only unlabelled columns, or read the table alone with --source to get it masked", target, l.Class)}
}

// nativeSQLLabelGuard refuses native SQL against a source that has a
// protected column (an owner label, a proposal or a read-time suggestion):
// SQL text cannot be inspected, so its result cannot be masked. Decision 0003
// makes a raw query database-wide, so the whole source is checked. A source
// that cannot be opened to check refuses; a source whose tables or columns
// cannot all be listed refuses only when a protected label is stored for it.
func nativeSQLLabelGuard(projectDir string) secureread.NativeSQLGuard {
	return func(ctx context.Context, catalog, sourceURL string) error {
		doc, err := loadLabels(ctx, projectDir)
		if err != nil {
			return errLabelsUnreadable
		}
		backend, _ := dbcopy.Parse(sourceURL) // an unparsable URL fails to open below
		db, err := openBackend(ctx, backend)
		if err != nil {
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s cannot be opened to check its sensitivity labels, so the read is refused", catalog)}
		}
		defer dbcopy.CloseSource(db)
		if meaninglabels.AnyProtected(doc, catalog, sourceLabelColumns(ctx, db)) {
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s has columns protected by sensitivity labels, and native SQL cannot be masked, so the read is refused; use a TugQL query, which is checked column by column", catalog)}
		}
		return nil
	}
}

// sourceLabelColumns lists every table of a source with its columns; a table
// whose columns cannot be read is listed with none, and a source whose tables
// cannot be listed gives nil. Either way only the stored labels then speak.
func sourceLabelColumns(ctx context.Context, db dal.DB) map[string][]meaninglabels.Column {
	collections, err := listCollections(ctx, db, nil)
	if err != nil {
		return nil
	}
	tables := map[string][]meaninglabels.Column{}
	for _, ref := range collections {
		columns, _ := collectionColumns(ctx, db, ref.Name())
		tables[ref.Name()] = labelColumnsOf(columns)
	}
	return tables
}
