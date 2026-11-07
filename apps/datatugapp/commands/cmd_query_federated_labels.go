package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
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
