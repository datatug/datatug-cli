package api

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/httpsource"
)

// ListSources is all or nothing: when the HTTP queries of the project cannot be
// listed, it says so and names what failed, instead of answering with the sources
// it did find. (A project with no queries folder is not this case: it has no HTTP
// queries, and its sources list fine.)
func TestListSources_HTTPQueriesCannotBeListed(t *testing.T) {
	cause := errors.New("queries folder unreadable")
	origLoadHTTP := loadHTTPQueries
	t.Cleanup(func() { loadHTTPQueries = origLoadHTTP })
	loadHTTPQueries = func(string) ([]httpsource.LoadedQuery, error) { return nil, cause }

	sources, err := ListSources(context.Background(), mockProjectStore{}, t.TempDir(), "")

	if !errors.Is(err, cause) {
		t.Fatalf("ListSources error = %v, want it to wrap %v", err, cause)
	}
	if want := "resolver: list HTTP query defs: queries folder unreadable"; err.Error() != want {
		t.Fatalf("ListSources error = %q, want %q", err.Error(), want)
	}
	if sources != nil {
		t.Fatalf("ListSources sources = %v, want none with the error", sources)
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
