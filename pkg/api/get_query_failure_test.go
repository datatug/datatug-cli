package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

// A query that is not there, and an ID that names more than one, keep their own answers. Any
// other failure to walk the queries tree of the project is answered with the sentence that
// queries/all_queries answers for it, built from the ID of the project, with the cause (which
// quotes the path of the queries folder) in the log.
func TestGetQuery_AFailureToWalkTheQueriesTreeIsTheSentenceOfAllQueries(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, storage.QueriesFolder, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"q1.query.json", filepath.Join("sub", "q1.query.json")} {
		if err := os.WriteFile(filepath.Join(projectDir, storage.QueriesFolder, name), []byte(`{"id":"q1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p1": projectDir}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	ref := func(id string) dto.ProjectItemRef {
		return dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: "store1", ProjectID: "p1"}, ID: id}
	}

	// The two answers that are not a failure.
	if _, err := GetQuery(context.Background(), ref("no-such-query")); !errors.Is(err, ErrQueryNotFound) {
		t.Errorf("a query that is not there: %v, want ErrQueryNotFound", err)
	}
	if _, err := GetQuery(context.Background(), ref("q1")); !errors.Is(err, ErrAmbiguousQueryID) {
		t.Errorf("a bare ID that names two queries: %v, want ErrAmbiguousQueryID", err)
	}

	// A walk that fails: the queries tree of the project cannot be read.
	logged := logOf(t)
	savedRel := filepathRel
	t.Cleanup(func() { filepathRel = savedRel })
	filepathRel = func(string, string) (string, error) { return "", errors.New("MARKER-cause of the walk") }

	_, err := GetQuery(context.Background(), ref("q1"))
	const want = `queries of project "p1" could not be loaded`
	if err == nil || err.Error() != want {
		t.Fatalf("a failure to walk the tree: %v, want exactly %q", err, want)
	}
	if errors.Is(err, ErrQueryNotFound) || errors.Is(err, ErrAmbiguousQueryID) {
		t.Errorf("a failure to walk the tree is neither of the answers above: %v", err)
	}
	for _, leak := range []string{projectDir, "MARKER", "list queries under"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the answer holds %q: %v", leak, err)
		}
	}
	if !strings.Contains(logged.String(), "MARKER-cause of the walk") || !strings.Contains(logged.String(), projectDir) {
		t.Errorf("the cause is not in the log: %q", logged.String())
	}
}
