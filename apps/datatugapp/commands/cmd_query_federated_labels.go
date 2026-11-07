package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/querybody"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// federatedLabelGuard is the sensitivity-label check of every saved federated
// TugQL read of a project (`query run --project <p> --query <id>`). A
// federated read cannot mask yet, so a leaf that names a column whose
// effective label is protected, or projects every column of a table with one,
// refuses the whole read, as the single-table project read refuses what it
// cannot mask. A query's database names are the project's catalog ids, the
// Source of a label's target, as `--source` is for the project read. A label
// document that cannot be read refuses the read.
func federatedLabelGuard(projectDir string) secureread.FederatedColumnGuard {
	return func(ctx context.Context, leaf secureread.FederatedLeaf) error {
		doc, err := loadLabels(ctx, projectDir)
		if err != nil {
			return &secureread.FieldProtectedError{Message: "the project's sensitivity labels (datatug-labels.json) cannot be read, so the read is refused"}
		}
		object := &projectRead{table: leaf.Collection, backend: dbcopy.BackendRef{Scheme: leaf.Scheme}}
		object.describe(ctx, leaf.DB)
		names := leaf.Columns
		if leaf.AllColumns {
			for _, l := range doc.Labels {
				if l.Class.Protected() && strings.EqualFold(l.Target.Source, leaf.Database) && meaninglabels.TableMatches(l.Target.Table, leaf.Collection) {
					return federatedProtected(leaf, meaninglabels.ColumnLabel{Column: l.Target.Column, Class: l.Class})
				}
			}
			ref := dal.NewRootCollectionRef(leaf.Collection, "")
			def, err := describeCollection(ctx, leaf.DB, &ref)
			if err != nil || len(def.Fields) == 0 {
				return &secureread.FieldProtectedError{Message: fmt.Sprintf("the columns of %s.%s cannot be listed to check their sensitivity labels; name the columns the federated query reads", leaf.Database, leaf.Collection)}
			}
			for _, field := range def.Fields {
				names = append(names, string(field.Name))
			}
		}
		for _, l := range meaninglabels.EffectiveObject(doc, leaf.Database, leaf.Collection, object.view, labelColumnsOf(names)) {
			if l.Protected() {
				return federatedProtected(leaf, l)
			}
		}
		return nil
	}
}

// federatedProtected is the FIELD_PROTECTED refusal of a federated read.
func federatedProtected(leaf secureread.FederatedLeaf, l meaninglabels.ColumnLabel) error {
	target := leaf.Database + "." + leaf.Collection
	if l.Column != "" {
		target += "." + l.Column
	}
	return &secureread.FieldProtectedError{Message: fmt.Sprintf("%s is protected (%s), and federated reads cannot mask labelled columns yet, so the read is refused; name only unlabelled columns, or read the table alone with --source to get it masked", target, l.Class)}
}

// guardSavedTugQL checks a single-source saved TugQL query against the
// project's labels with the federated guard, every collection a leaf of the
// resolved catalog, before the query runs.
func guardSavedTugQL(ctx context.Context, executor *secureread.Executor, source resolvedQuerySource, queryDef *datatug.QueryDef) error {
	document, err := querybody.Structured(queryDef)
	if err != nil {
		return err
	}
	return executor.GuardTugQLColumns(ctx, []byte(document), source.Catalog, source.URL)
}

// refuseLabelledNativeSQL refuses a saved native SQL read of a source that has
// a protected column (an owner label, a proposal or a read-time suggestion),
// or whose columns cannot all be listed: SQL text cannot be inspected, so its
// result cannot be masked. Decision 0003 makes a raw query database-wide, so
// the whole source is checked. A source that cannot be opened is left to the
// read itself, which then fails without reading anything.
func refuseLabelledNativeSQL(ctx context.Context, projectDir string, source resolvedQuerySource) error {
	doc, err := loadLabels(ctx, projectDir)
	if err != nil {
		return &secureread.FieldProtectedError{Message: "the project's sensitivity labels (datatug-labels.json) cannot be read, so the read is refused"}
	}
	backend, _ := dbcopy.Parse(source.URL) // an unparsable URL fails to open below
	db, err := openBackend(ctx, backend)
	if err != nil {
		return nil
	}
	defer dbcopy.CloseSource(db)
	if meaninglabels.RawRefusal(doc, source.Catalog, sourceLabelColumns(ctx, db), true) != nil {
		return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s has columns protected by sensitivity labels, and native SQL cannot be masked, so the read is refused; use a TugQL query, which is checked column by column", source.Catalog)}
	}
	return nil
}

// sourceLabelColumns lists every table of a source with its columns; a table
// whose columns cannot be read is listed with none, and a source whose tables
// cannot be listed gives nil, both of which RawRefusal refuses.
func sourceLabelColumns(ctx context.Context, db dal.DB) map[string][]meaninglabels.Column {
	collections, err := listCollections(ctx, db, nil)
	if err != nil {
		return nil
	}
	tables := map[string][]meaninglabels.Column{}
	for _, ref := range collections {
		var columns []meaninglabels.Column
		if def, err := describeCollection(ctx, db, &ref); err == nil {
			for _, field := range def.Fields {
				columns = append(columns, meaninglabels.Column{Name: string(field.Name)})
			}
		}
		tables[ref.Name()] = columns
	}
	return tables
}
