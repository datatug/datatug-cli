package meaninglabelsfile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/gofrs/flock"
)

func change(t *testing.T, doc meaninglabels.Document) meaninglabels.Document {
	t.Helper()
	out, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet,
		Target: meaninglabels.Target{Source: "nw", Table: "customers", Column: "card_number"}, Class: meaninglabels.ClassFinancial, Reason: "PAN"},
		meaninglabels.Actor{ID: "user:me"}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoundTripAndConflict(t *testing.T) {
	ctx := context.Background()
	s := Store{Dir: t.TempDir()}
	empty, err := s.Load(ctx)
	if err != nil || empty.Revision != "" {
		t.Fatalf("%+v %v", empty, err)
	}
	doc := change(t, empty)
	if err := s.Save(ctx, doc, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil || got.Revision != doc.Revision {
		t.Fatalf("%+v %v", got, err)
	}
	if meaninglabels.ErrorCode(s.Save(ctx, doc, "")) != meaninglabels.CodeRevisionConflict {
		t.Fatal("want conflict")
	}
	if meaninglabels.ErrorCode(s.Save(ctx, meaninglabels.Document{Format: "x"}, "")) != meaninglabels.CodeInvalidDocument {
		t.Fatal("want invalid")
	}
	if err := os.WriteFile(filepath.Join(s.Dir, FileName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx); meaninglabels.ErrorCode(err) != meaninglabels.CodeInvalidDocument {
		t.Fatalf("corrupt: %v", err)
	}
	if err := s.Save(ctx, doc, doc.Revision); meaninglabels.ErrorCode(err) != meaninglabels.CodeInvalidDocument {
		t.Fatalf("save over corrupt: %v", err)
	}
}

func TestFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	s := Store{Dir: t.TempDir()}
	doc := change(t, meaninglabels.Document{Format: meaninglabels.Format})
	restore := func() {
		readFile, writeFile, rename, mkdirAll, openFile, marshal = os.ReadFile, os.WriteFile, os.Rename, os.MkdirAll, os.OpenFile, json.Marshal
		tryLock = func(l *flock.Flock, ctx context.Context) (bool, error) {
			return l.TryLockContext(ctx, 20*time.Millisecond)
		}
	}
	defer restore()

	readFile = func(string) ([]byte, error) { return nil, boom }
	if _, err := s.Load(ctx); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := s.Save(ctx, doc, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	restore()
	tryLock = func(*flock.Flock, context.Context) (bool, error) { return false, nil }
	if err := s.Save(ctx, doc, ""); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatal(err)
	}
	restore()
	writeFile = func(string, []byte, os.FileMode) error { return boom }
	if err := s.Save(ctx, doc, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	restore()
	rename = func(string, string) error { return boom }
	if err := s.Save(ctx, doc, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	restore()

	if err := s.Append(AuditRecord{Event: meaninglabels.Event{Type: "meaning.field.revealed_locally"}, Columns: []string{"email"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(AuditRecord{Event: meaninglabels.Event{Type: "meaning.label.set"}}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(s.Dir, AuditPath))
	if lines := strings.Split(strings.TrimSpace(string(body)), "\n"); len(lines) != 2 || !strings.Contains(lines[0], `"columns":["email"]`) {
		t.Fatalf("audit: %s", body)
	}
	mkdirAll = func(string, os.FileMode) error { return boom }
	if err := s.Append(AuditRecord{}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	restore()
	marshal = func(any) ([]byte, error) { return nil, boom }
	if err := s.Append(AuditRecord{}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	restore()
	openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, boom }
	if err := s.Append(AuditRecord{}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}
