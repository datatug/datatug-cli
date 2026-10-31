package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
)

func TestVisibilityInventoryKeepsCanonicalDefinitionsAndRejectsUnreadableSources(t *testing.T) {
	for _, kind := range []string{"checks", "validators", "query-validators", "scenarios", "cases", "all"} {
		t.Run(kind, func(t *testing.T) {
			s, e := NewLocalStore("p", t.TempDir(), t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			if kind != "all" {
				dir := s.definitionDir(kind)
				if kind == "cases" {
					dir = s.privateItemDir(kind)
				}
				if e = os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = s.VisibilityItems(context.Background()); e == nil {
					t.Fatal("unreadable current definition silently omitted")
				}
				return
			}
			for name, item := range map[string]any{"checks": Check{ID: "check", Version: 1}, "validators": PublishedValidator{ID: "validator", Title: "Validator", Version: 2}, "query-validators": QueryValidators{QueryID: "canonical-query", Version: 3}, "scenarios": Scenario{ID: "scenario", Title: "Scenario", Version: 4}} {
				if e = writeItem(s.definitionDir(name), "native", item); e != nil {
					t.Fatal(e)
				}
			}
			items, e := s.VisibilityItems(context.Background())
			if e != nil || len(items) != 4 {
				t.Fatalf("%+v %v", items, e)
			}
			for _, item := range items {
				if item.Title == "" || item.Ref.ID == "native" {
					t.Fatalf("filename escaped as canonical identity: %+v", item)
				}
			}
		})
	}
}
func archivedVisibilityCase(t *testing.T) (*LocalStore, Case) {
	t.Helper()
	s, e := NewLocalStore("p", t.TempDir(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	old := Case{ID: "legacy", Title: "Archived", Status: "archived", Version: 2}
	if e = writeItem(s.privateItemDir("cases"), old.ID, old); e != nil {
		t.Fatal(e)
	}
	return s, old
}
func TestVisibilityCaseRestoreRejectsInvalidProofsAndSourceChanges(t *testing.T) {
	for _, kind := range []string{"invalid-request", "invalid-project", "corrupt-receipt", "missing-source", "unreadable-source", "corrupt-source", "version", "work-alias", "active-source", "owning-error", "marshal", "receipt", "journal", "unreadable-journal", "case-write", "case-conflict", "case-sync", "audit", "complete"} {
		t.Run(kind, func(t *testing.T) {
			s, old := archivedVisibilityCase(t)
			actor, operation, version := "owner", "restore", old.Version
			oldRead, oldRestore, oldMarshal, oldReceipt, oldSync, oldRename, oldAudit := actionSafeReadFile, visibilityRestoreCase, stateMarshal, actionNewOperationReceipt, writeItemSyncDir, storeRename, auditOpenFile
			t.Cleanup(func() {
				actionSafeReadFile = oldRead
				visibilityRestoreCase = oldRestore
				stateMarshal = oldMarshal
				actionNewOperationReceipt = oldReceipt
				writeItemSyncDir = oldSync
				storeRename = oldRename
				auditOpenFile = oldAudit
			})
			failed := errors.New("native durability boundary failed")
			switch kind {
			case "invalid-request":
				actor = ""
			case "invalid-project":
				s.projectID = ""
			case "corrupt-receipt":
				if e := writeItem(s.privateItemDir("action-operations"), operation, map[string]bool{"done": true}); e != nil {
					t.Fatal(e)
				}
			case "missing-source":
				if e := os.Remove(filepath.Join(s.privateItemDir("cases"), old.ID+".json")); e != nil {
					t.Fatal(e)
				}
			case "unreadable-source":
				actionSafeReadFile = func(dir, id string) ([]byte, error) {
					if dir == s.privateItemDir("cases") {
						return nil, failed
					}
					return oldRead(dir, id)
				}
			case "corrupt-source":
				if e := os.WriteFile(filepath.Join(s.privateItemDir("cases"), old.ID+".json"), []byte("{"), 0600); e != nil {
					t.Fatal(e)
				}
			case "version":
				version++
			case "work-alias":
				old.WorkItemID = "work"
				if e := writeItem(s.privateItemDir("cases"), old.ID, old); e != nil {
					t.Fatal(e)
				}
			case "active-source":
				old.Status = "active"
				if e := writeItem(s.privateItemDir("cases"), old.ID, old); e != nil {
					t.Fatal(e)
				}
			case "owning-error":
				visibilityRestoreCase = func(Case, string, time.Time) (Case, AuditEvent, error) { return Case{}, AuditEvent{}, failed }
			case "marshal":
				stateMarshal = func(any) ([]byte, error) { return nil, failed }
			case "receipt":
				actionNewOperationReceipt = func(string, core.OperationBinding, int64, time.Time) (core.OperationReceipt, error) {
					return core.OperationReceipt{}, failed
				}
			case "journal":
				writeItemSyncDir = func(dir string) error {
					if dir == s.privateItemDir("action-operations") {
						return failed
					}
					return oldSync(dir)
				}
			case "unreadable-journal":
				actionSafeReadFile = func(dir, id string) ([]byte, error) {
					if dir == s.privateItemDir("action-operations") {
						return nil, failed
					}
					return oldRead(dir, id)
				}
			case "case-write":
				storeRename = func(a, b string) error {
					if filepath.Dir(b) == s.privateItemDir("cases") {
						return failed
					}
					return oldRename(a, b)
				}
			case "case-conflict":
				actionSafeReadFile = func(dir, id string) ([]byte, error) {
					body, e := oldRead(dir, id)
					if dir == s.privateItemDir("cases") {
						old.Status = "active"
						old.Version = 3
						if e = writeItem(dir, id, old); e != nil {
							return nil, e
						}
					}
					return body, e
				}
			case "case-sync":
				writeItemSyncDir = func(dir string) error {
					if dir == s.privateItemDir("cases") {
						return failed
					}
					return oldSync(dir)
				}
			case "audit":
				auditOpenFile = func(string, int, os.FileMode) (syncCloserWriter, error) { return nil, failed }
			case "complete":
				calls := 0
				writeItemSyncDir = func(dir string) error {
					if dir == s.privateItemDir("action-operations") {
						calls++
						if calls == 2 {
							return failed
						}
					}
					return oldSync(dir)
				}
			}
			result, e := s.RestoreVisibilityCase(context.Background(), old.ID, actor, operation, version)
			if e == nil || result.ID != "" {
				t.Fatalf("unsafe restore acknowledged %+v %v", result, e)
			}
		})
	}
}
func TestVisibilityCaseRestoreRecoversAcceptedReceipt(t *testing.T) {
	s, old := archivedVisibilityCase(t)
	body, e := safeReadFile(s.privateItemDir("cases"), old.ID)
	if e != nil {
		t.Fatal(e)
	}
	next, audit, e := core.RestoreCase(old, "owner", time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	after, e := json.Marshal(next)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := core.BindOperation("p", "owner", "restore-case", old.ID, struct{ Version int64 }{2})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := core.NewOperationReceipt("restore", binding, next.Version, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	audit.ID = "restore"
	audit.Project = "p"
	op := actionOperation{Receipt: receipt, Kind: "cases", ID: old.ID, Before: body, After: after, Audit: audit, Produced: true, Response: ActionResponse{Case: &next}}
	if e = writeDurableItem(s.privateItemDir("action-operations"), "restore", op); e != nil {
		t.Fatal(e)
	}
	result, e := s.RestoreVisibilityCase(context.Background(), old.ID, "owner", "restore", 2)
	if e != nil || result.Version != 3 || result.Status != "active" {
		t.Fatalf("accepted native restore not recovered %+v %v", result, e)
	}
}
