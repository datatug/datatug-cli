package detective

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVisibilityInventoryUsesCurrentDefinitionsAndCasesWithoutRunHistory(t *testing.T) {
	store, err := NewLocalStore("p", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = writeItem(store.definitionDir("checks"), "check", Check{ID: "check", Title: "Current check", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err = writeItem(store.privateItemDir("cases"), "legacy", Case{ID: "legacy", Title: "Current case", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err = writeItem(store.privateItemDir("cases"), "work", Case{ID: "work", WorkItemID: "work-1", Title: "Work projection", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(store.privateItemDir("runs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(store.privateItemDir("runs"), "unreadable.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := store.VisibilityItems(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("current inventory consulted history or included Work alias: %+v %v", items, err)
	}
	for _, item := range items {
		if item.Ref.ID == "work" || item.Ref.Kind == "run" {
			t.Fatalf("unexpected target: %+v", item)
		}
	}
}
