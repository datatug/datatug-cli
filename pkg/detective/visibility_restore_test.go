package detective

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVisibilityCaseRestoreUsesNativeCASAndDurableReplay(t *testing.T) {
	store, err := NewLocalStore("p", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := Case{ID: "old-case", Title: "Archived Case", Version: 4, Status: "archived", IncidentRefs: []string{"store/incident"}, ScenarioRefs: []string{"scenario"}, RunRefs: []string{"retained-run"}}
	if err = writeItem(store.privateItemDir("cases"), before.ID, before); err != nil {
		t.Fatal(err)
	}
	restored, err := store.RestoreVisibilityCase(context.Background(), before.ID, "owner", "restore-case", 4)
	if err != nil || restored.Status != "active" || restored.Version != 5 || len(restored.RunRefs) != 1 || len(restored.IncidentRefs) != 1 {
		t.Fatalf("restore changed retained content: %+v %v", restored, err)
	}
	reloaded, err := NewLocalStore("p", store.projectDir, filepath.Dir(filepath.Dir(store.privateDir)))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reloaded.RestoreVisibilityCase(context.Background(), before.ID, "owner", "restore-case", 4)
	if err != nil || replay.Version != restored.Version {
		t.Fatalf("native receipt replay differs: %+v %v", replay, err)
	}
	if _, err = reloaded.RestoreVisibilityCase(context.Background(), before.ID, "other-owner", "restore-case", 4); err == nil {
		t.Fatal("native restore receipt rebound to another actor")
	}
	if _, err = os.Stat(filepath.Join(store.privateItemDir("action-operations"), "restore-case.json")); err != nil {
		t.Fatal("native restore receipt missing", err)
	}
}
