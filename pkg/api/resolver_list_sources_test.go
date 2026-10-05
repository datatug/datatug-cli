package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/httpsource"
)

// ListSources is all or nothing: when the HTTP queries of the project cannot be
// listed, it says so in one sentence built from nothing the cause said (which quotes a
// path of the project folder), instead of answering with the sources it did find. (A
// project with no queries folder is not this case: it has no HTTP queries, and its
// sources list fine.)
func TestListSources_HTTPQueriesCannotBeListed(t *testing.T) {
	projectDir := t.TempDir()
	cause := errors.New("read " + projectDir + "/queries/a.query.json: permission denied")
	origLoadHTTP := loadHTTPQueries
	t.Cleanup(func() { loadHTTPQueries = origLoadHTTP })
	loadHTTPQueries = func(string) ([]httpsource.LoadedQuery, error) { return nil, cause }
	logged := captureLog(t)

	sources, err := ListSources(context.Background(), mockProjectStore{}, projectDir, "")

	if err == nil {
		t.Fatal("ListSources: want an error")
	}
	if want := "the HTTP query definitions of the project could not be listed"; err.Error() != want {
		t.Fatalf("ListSources error = %q, want exactly %q", err.Error(), want)
	}
	if errors.Is(err, cause) {
		t.Fatalf("ListSources error wraps the cause, which quotes a path of the project folder")
	}
	if sources != nil {
		t.Fatalf("ListSources sources = %v, want none with the error", sources)
	}
	if !strings.Contains(logged.String(), "permission denied") {
		t.Errorf("the cause is not in the log: %q", logged.String())
	}
}

// The sources of a project that has no queries folder (what a scan or init writes,
// and git does not keep an empty folder) list without an error.
func TestListSources_ProjectWithoutQueriesFolder(t *testing.T) {
	sources, err := ListSources(context.Background(), mockProjectStore{}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("ListSources sources = %v, want none", sources)
	}
}
