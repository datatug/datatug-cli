package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestResolveCreatedAt(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 2, 3, 4, time.UTC)
	if got := resolveCreatedAt("c", "s", true, now); got != "s" {
		t.Fatalf("update keeps stored, got %q", got)
	}
	if got := resolveCreatedAt("c", "", true, now); got != "" {
		t.Fatalf("update of undated stays undated, got %q", got)
	}
	if got := resolveCreatedAt("c", "", false, now); got != "c" {
		t.Fatalf("create takes caller's, got %q", got)
	}
	if got := resolveCreatedAt("", "", false, now); got != datatug.NewCreatedAt(now) {
		t.Fatalf("create stamps now, got %q", got)
	}
}

func TestCheckCreatedAtLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	c := Check{ID: "dated", Title: "Dated", Steps: []Step{{ID: "a", Kind: "query", QueryID: "q"}}}
	created, err := s.SaveCheck(ctx, c, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := datatug.ValidateOptionalCreatedAt(created.CreatedAt); err != nil || created.CreatedAt == "" {
		t.Fatalf("create must stamp a valid date, got %q (%v)", created.CreatedAt, err)
	}
	original := created.CreatedAt
	created.CreatedAt = "2000-01-01T00:00:00Z" // backdate attempt
	updated, err := s.SaveCheck(ctx, created, "alice")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetCheck(ctx, "dated")
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != original || stored.CreatedAt != original {
		t.Fatalf("update must keep stored date %q, got %q / %q", original, updated.CreatedAt, stored.CreatedAt)
	}
	bad := Check{ID: "bad-date", Title: "Bad", CreatedAt: "yesterday", Steps: c.Steps}
	if _, err := s.SaveCheck(ctx, bad, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid date must be rejected, got %v", err)
	}
}

func TestScenarioCreatedAtLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	sc := Scenario{ID: "dated-scen", Title: "Dated", Steps: []ScenarioStep{{ID: "s1", Kind: "query", Ref: "q"}}}
	created, err := s.SaveScenario(ctx, sc, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := datatug.ValidateOptionalCreatedAt(created.CreatedAt); err != nil || created.CreatedAt == "" {
		t.Fatalf("create must stamp a valid date, got %q (%v)", created.CreatedAt, err)
	}
	first := created.CreatedAt
	created.CreatedAt = "2000-01-01T00:00:00Z"
	updated, err := s.SaveScenario(ctx, created, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != first {
		t.Fatalf("update must keep stored date %q, got %q", first, updated.CreatedAt)
	}
	sc.ID, sc.CreatedAt = "bad-scen", "yesterday"
	if _, err := s.SaveScenario(ctx, sc, "alice"); err == nil {
		t.Fatal("invalid date must be rejected")
	}
}

func TestLegacyFilesWithoutCreatedAtLoadAndStayKeyless(t *testing.T) {
	s := testStore(t)
	dir := filepath.Join(s.projectDir, "checks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"legacy","title":"Legacy","version":1,"steps":[{"id":"a","kind":"query","queryId":"q"}]}`
	path := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCheck(context.Background(), "legacy")
	if err != nil || c.CreatedAt != "" {
		t.Fatalf("legacy check must load undated, got %q (%v)", c.CreatedAt, err)
	}
	if got, _ := os.ReadFile(path); string(got) != raw {
		t.Fatal("loading must not rewrite the file")
	}
	data, _ := marshalForTest(c)
	if strings.Contains(data, "createdAt") {
		t.Fatalf("empty createdAt must be omitted, got %s", data)
	}
}

func marshalForTest(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func TestLegacyUndatedUpdateStaysUndated(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	dir := filepath.Join(s.projectDir, "checks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"legacy","title":"Legacy","version":1,"steps":[{"id":"a","kind":"query","queryId":"q"}]}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCheck(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	c.CreatedAt = "2000-01-01T00:00:00Z"
	if _, err := s.SaveCheck(ctx, c, "alice"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "legacy.json"))
	if strings.Contains(string(got), "createdAt") {
		t.Fatalf("update must not backfill a legacy file: %s", got)
	}
}

func TestActionAndAICatalogPathsStampCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	checkReq := ActionRequest{Type: "save-check", Project: "p", Check: &Check{ID: "act-check", Title: "One", Steps: []Step{{ID: "a", Kind: "query", QueryID: "q"}}}}
	first, err := s.ExecuteAction(ctx, "act-check-1", "alice", checkReq, nil, nil, nil, nil)
	if err != nil || first.Check == nil || first.Check.CreatedAt == "" {
		t.Fatalf("action create must stamp: %+v, %v", first, err)
	}
	replay, err := s.ExecuteAction(ctx, "act-check-1", "alice", checkReq, nil, nil, nil, nil)
	if err != nil || replay.Check.CreatedAt != first.Check.CreatedAt {
		t.Fatalf("replay must be idempotent: %+v, %v", replay, err)
	}
	stored, err := s.GetCheck(ctx, "act-check")
	if err != nil || stored.CreatedAt != first.Check.CreatedAt {
		t.Fatalf("stored date mismatch: %+v, %v", stored, err)
	}
	upd := *first.Check
	upd.CreatedAt = "2000-01-01T00:00:00Z"
	second, err := s.ExecuteAction(ctx, "act-check-2", "alice", ActionRequest{Type: "save-check", Project: "p", Check: &upd}, nil, nil, nil, nil)
	if err != nil || second.Check.CreatedAt != first.Check.CreatedAt {
		t.Fatalf("action update must keep stored date: %+v, %v", second, err)
	}

	scReq := ActionRequest{Type: "save-scenario", Project: "p", Scenario: &Scenario{ID: "act-scen", Title: "S", Steps: []ScenarioStep{{ID: "s1", Kind: "query", Ref: "q"}}}}
	sc1, err := s.ExecuteAction(ctx, "act-scen-1", "alice", scReq, nil, nil, nil, nil)
	if err != nil || sc1.Scenario == nil || sc1.Scenario.CreatedAt == "" {
		t.Fatalf("scenario action create must stamp: %+v, %v", sc1, err)
	}
	if r, err := s.ExecuteAction(ctx, "act-scen-1", "alice", scReq, nil, nil, nil, nil); err != nil || r.Scenario.CreatedAt != sc1.Scenario.CreatedAt {
		t.Fatalf("scenario replay: %+v, %v", r, err)
	}
	su := *sc1.Scenario
	su.CreatedAt = "2000-01-01T00:00:00Z"
	sc2, err := s.ExecuteAction(ctx, "act-scen-2", "alice", ActionRequest{Type: "save-scenario", Project: "p", Scenario: &su}, nil, nil, nil, nil)
	if err != nil || sc2.Scenario.CreatedAt != sc1.Scenario.CreatedAt {
		t.Fatalf("scenario action update must keep date: %+v, %v", sc2, err)
	}
}
