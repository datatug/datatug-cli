package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/labelguard"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// installProjectLabels makes every TugQL and native SQL read of session check
// the project's Meaning Graph sensitivity labels first, for every principal
// whatever its profile (ai-onboarding: labelled columns are masked for every
// principal). catalogs maps each project source URL to the catalog ids that
// resolve to it, the Source of a label target. Used by `query run --project
// --query`, `query run --db datatug://...` and `chat`.
//
// A stored protected label refuses for every principal. Read-time suggestions
// alone refuse a strict principal (see labelStrictness); anyone else reads
// on, and warn receives one warning naming the suggested columns.
//
// alwaysStrict makes every suggestion refuse whoever the caller is: `chat`
// hands what it reads to an AI model.
func installProjectLabels(session *secureread.Session, projectDir string, catalogs map[string][]string, warn io.Writer, alwaysStrict bool) {
	strict := alwaysStrict || labelStrictness(*session)
	session.FederatedColumns = federatedLabelGuard(projectDir, strict, warn)
	session.NativeSQL = nativeSQLLabelGuard(projectDir, strict, warn)
	session.SourceCatalogs = catalogs
}

// labelStrictness reports whether read-time suggestions alone refuse a read:
// for an agent (a detected AI harness) or a policy-governed session. The
// harness signal is self-reported, so it only ever makes a read stricter.
// The session's profile is read as NormalizeSession settles it; a session it
// refuses is strict.
func labelStrictness(session secureread.Session) bool {
	normalized, _ := secureread.NormalizeSession(session)
	return callerIsAgent() || !normalized.DatabaseProfile.PolicyFreeReads()
}

// errLabelsUnreadable refuses a read whose project labels cannot be read.
var errLabelsUnreadable = &secureread.FieldProtectedError{Message: "the project's sensitivity labels (datatug-labels.json) cannot be read, so the read is refused"}

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
//
// A column protected only by a read-time suggestion refuses a strict
// principal; anyone else reads it, and warn names it once.
func federatedLabelGuard(projectDir string, strict bool, warn io.Writer) secureread.FederatedColumnGuard {
	warner := labelguard.NewWarner(warn)
	return func(ctx context.Context, leaf secureread.FederatedLeaf) error {
		doc, err := loadLabels(ctx, projectDir)
		if err != nil {
			return errLabelsUnreadable
		}
		labelled := labelguard.SourceHasProtectedLabel(doc, leaf.Database)
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
			if l, ok := labelguard.StoredTableLabel(doc, leaf.Database, leaf.Collection); ok {
				return federatedProtected(leaf, l)
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
		refused, suggested := labelguard.CheckColumns(doc, leaf.Database, leaf.Collection, object.view, names, strict)
		if refused != nil {
			return federatedProtected(leaf, *refused)
		}
		warner.Warn("this TugQL read", leaf.Database, suggested)
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

// nativeSQLLabelGuard decides native SQL against a source: SQL text cannot be
// inspected, so its result cannot be masked, and Decision 0003 makes a raw
// query database-wide, so the whole source is judged. A protected label stored
// for the source (an owner label or a proposal) refuses it for every
// principal. Read-time suggestions alone refuse it for a strict principal (an
// agent, or a policy-governed session); for anyone else the SQL runs and warn
// names the suggested columns once. A strict principal is refused when the
// source cannot be opened to check it, or its columns cannot be listed only
// when a label is stored (already refused).
func nativeSQLLabelGuard(projectDir string, strict bool, warn io.Writer) secureread.NativeSQLGuard {
	warner := labelguard.NewWarner(warn)
	return func(ctx context.Context, catalog, sourceURL string) error {
		doc, err := loadLabels(ctx, projectDir)
		if err != nil {
			return errLabelsUnreadable
		}
		if labelguard.SourceHasProtectedLabel(doc, catalog) {
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s has columns protected by sensitivity labels, and native SQL cannot be masked, so the read is refused; use a TugQL query, which is checked column by column", catalog)}
		}
		backend, _ := dbcopy.Parse(sourceURL) // an unparsable URL fails to open below
		db, err := openBackend(ctx, backend)
		if err != nil {
			if strict {
				return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s cannot be opened to check its sensitivity labels, so the read is refused", catalog)}
			}
			return nil
		}
		defer dbcopy.CloseSource(db)
		suggested := labelguard.SuggestedColumns(doc, catalog, sourceLabelColumns(ctx, db))
		switch {
		case len(suggested) == 0:
			return nil
		case strict:
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s has columns that read-time suggestions mark sensitive (%s), and native SQL cannot be masked, so the read is refused; use a TugQL query, which is checked column by column", catalog, strings.Join(suggested, ", "))}
		default:
			warner.Warn("native SQL", catalog, suggested)
			return nil
		}
	}
}

// callerIsAgent reports whether this process runs under an AI agent harness.
func callerIsAgent() bool {
	caller := detectCaller()
	return caller != nil && caller.Harness != ""
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
