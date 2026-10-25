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

func TestStampCreatedAt(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 2, 3, 4, time.UTC)
	if got := stampCreatedAt("c", "s", now); got != "s" {
		t.Fatalf("stored must win, got %q", got)
	}
	if got := stampCreatedAt("c", "", now); got != "c" {
		t.Fatalf("caller must apply without stored, got %q", got)
	}
	if got := stampCreatedAt("", "", now); got != datatug.NewCreatedAt(now) {
		t.Fatalf("now must apply, got %q", got)
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
	created.CreatedAt = "2000-01-01T00:00:00Z" // backdate attempt
	updated, err := s.SaveCheck(ctx, created, "alice")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetCheck(ctx, "dated")
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt == "2000-01-01T00:00:00Z" || stored.CreatedAt != updated.CreatedAt {
		t.Fatalf("update must keep stored date, got %q / %q", updated.CreatedAt, stored.CreatedAt)
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
