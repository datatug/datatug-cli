package dbcopy

import (
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"strings"
	"testing"
)

func TestValidateExportRelationshipsFailsMissingTargetAndField(t *testing.T) {
	parent := exportTable{def: dbschema.CollectionDef{Name: "parent", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}, PrimaryKey: []dal.FieldName{"id"}}}
	child := exportTable{def: dbschema.CollectionDef{Name: "child", Fields: []dbschema.FieldDef{{Name: "parent_id", Type: dbschema.Int}}, ForeignKeys: []dbschema.ForeignKeyDef{{Fields: []dal.FieldName{"parent_id"}, ReferencedCollection: "missing", ReferencedFields: []dal.FieldName{"id"}}}}}
	if err := validateExportRelationships([]exportTable{parent, child}); err == nil || !strings.Contains(err.Error(), "missing collection") {
		t.Fatalf("missing target: %v", err)
	}
	child.def.ForeignKeys[0].ReferencedCollection = "parent"
	child.def.ForeignKeys[0].ReferencedFields = []dal.FieldName{"nope"}
	if err := validateExportRelationships([]exportTable{parent, child}); err == nil || !strings.Contains(err.Error(), "missing field") {
		t.Fatalf("missing field: %v", err)
	}
	child.def.ForeignKeys[0].ReferencedFields = nil
	if err := validateExportRelationships([]exportTable{parent, child}); err != nil {
		t.Fatalf("implicit target PK: %v", err)
	}
	parent.def.PrimaryKey = nil
	if err := validateExportRelationships([]exportTable{parent, child}); err == nil || !strings.Contains(err.Error(), "no resolvable target key") {
		t.Fatalf("unresolved implicit target PK must fail closed: %v", err)
	}
}
