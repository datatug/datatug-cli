package dtentity

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestMarshalUnmarshalEntity_RoundTripsTables(t *testing.T) {
	entity := &datatug.Entity{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "User", Title: "User"}},
		Tables:      datatug.TableKeys{datatug.NewTableKey("users", "public", "", nil)},
	}

	data, err := MarshalEntity(entity)
	if err != nil {
		t.Fatalf("MarshalEntity: %v", err)
	}

	loaded, err := UnmarshalEntity(data)
	if err != nil {
		t.Fatalf("UnmarshalEntity: %v", err)
	}
	if len(loaded.Tables) != 1 {
		t.Fatalf("expected 1 table key, got %d", len(loaded.Tables))
	}
	if got, want := loaded.Tables[0].Name(), "users"; got != want {
		t.Errorf("Tables[0].Name() = %q, want %q", got, want)
	}
	if got, want := loaded.Tables[0].Schema(), "public"; got != want {
		t.Errorf("Tables[0].Schema() = %q, want %q", got, want)
	}
}

func TestMarshalEntity_NoTables_OmitsTablesKey(t *testing.T) {
	entity := &datatug.Entity{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "User", Title: "User"}},
	}
	data, err := MarshalEntity(entity)
	if err != nil {
		t.Fatalf("MarshalEntity: %v", err)
	}
	loaded, err := UnmarshalEntity(data)
	if err != nil {
		t.Fatalf("UnmarshalEntity: %v", err)
	}
	if len(loaded.Tables) != 0 {
		t.Errorf("expected no table keys, got %d", len(loaded.Tables))
	}
}

func TestUnmarshalEntity_FromAuthoredYAMLShapedJSON(t *testing.T) {
	// The shape entity add/parseEntityDocs feeds in: hand-authored, no "Ref".
	data := []byte(`{"id":"User","tables":[{"name":"users","schema":"public"}]}`)
	entity, err := UnmarshalEntity(data)
	if err != nil {
		t.Fatalf("UnmarshalEntity: %v", err)
	}
	if len(entity.Tables) != 1 || entity.Tables[0].Name() != "users" {
		t.Errorf("Tables = %+v, want one key named users", entity.Tables)
	}
}
