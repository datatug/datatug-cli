package detective

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

func TestNativeSourceNeverStartsBeforeJournalDirectoryDurability(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.SaveCheck(ctx, Check{ID: "durable-check", Title: "Durable", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	old := writeItemSyncDir
	defer func() { writeItemSyncDir = old }()
	failed := errors.New("directory sync failed")
	writeItemSyncDir = func(string) error { return failed }
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		return apicontract.Result{}, nil
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "durable-check", Environment: "qa"}
	if _, err := s.ExecuteAction(ctx, "directory-failure", "alice", req, runner, nil, nil, nil); !errors.Is(err, failed) {
		t.Fatal("unsynced acceptance acknowledged", err)
	}
	if calls != 0 {
		t.Fatal("source ran before journal directory was durable")
	}
	writeItemSyncDir = old
	// A journal whose rename happened before the failed directory sync is an
	// uncertain acceptance. Retry must not repeat source execution.
	if _, err := s.ExecuteAction(ctx, "directory-failure", "alice", req, runner, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatal("uncertain acceptance redispatched", err)
	}
	if calls != 0 {
		t.Fatal("retry executed source after failed durable acceptance")
	}
}

func TestNativeStoreRejectsPhysicalCaseAliasesWithoutMovingJournals(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "CanonicalProject")
	private := filepath.Join(root, "CanonicalPrivate")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	stubQuery(t, project, "q")
	s, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCheck(context.Background(), Check{ID: "check", Title: "Preserved", Steps: []Step{{ID: "q", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	projectAlias := filepath.Join(root, "canonicalproject")
	info, err := os.Stat(projectAlias)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem is case-sensitive")
	}
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(project)
	if err != nil || !os.SameFile(info, original) {
		t.Fatal("invalid alias fixture", err)
	}
	if _, err = NewLocalStore("p", projectAlias, private); err == nil {
		t.Fatal("case alias split native journal identity")
	}
	if _, err = NewLocalStore("p", project, filepath.Join(root, "canonicalprivate")); err == nil {
		t.Fatal("private case alias split native identity")
	}
	canonical, err := NewLocalStore("p", project, private)
	if err != nil || canonical.privateDir != s.privateDir {
		t.Fatal("canonical journal moved", err)
	}
	check, err := canonical.GetCheck(context.Background(), "check")
	if err != nil || check.Title != "Preserved" {
		t.Fatal("alias rejection erased canonical state", err)
	}
}
