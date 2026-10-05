package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

// A project ID or a saved-query ID a client sent can be a whole source string.
// Every lookup that reports one that is not there names it only when it is a plain
// name (a query ID: when each of its folders and its name is one), and says nothing
// of it otherwise.
func TestProperty_QueryLookupsNeverEchoAProjectOrQueryIDThatIsASourceString(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, storage.QueriesFolder))
	// Two queries that share a bare name: a bare ID that names both is ambiguous.
	for _, folder := range []string{"a", "b"} {
		mustMkdir(t, filepath.Join(dir, storage.QueriesFolder, folder))
		mustWrite(t, filepath.Join(dir, storage.QueriesFolder, folder, "shared."+storage.QueryFileSuffix+".json"), `{"id":"shared"}`)
	}
	ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"demo": dir}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })

	ctx := context.Background()
	getQuery := func(project, id string) error {
		_, err := GetQuery(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: "s", ProjectID: project}, ID: id})
		return err
	}
	failed := 0
	for _, c := range sourcecases.All() {
		var texts []string
		for _, call := range []struct {
			name string
			run  func() error
			want error
			// phrase is text of the message that the property must reach.
			phrase string
		}{
			{"GetQuery of a project", func() error { return getQuery(c.Source, "q") }, ErrQueryNotFound, "unknown project"},
			{"GetQuery of a query", func() error { return getQuery("demo", c.Source) }, ErrQueryNotFound, "query not found"},
			{"ResolveQueryID with a folder", func() error { _, err := ResolveQueryID(dir, "a/"+c.Source); return err }, ErrQueryNotFound, "query not found"},
			{"ResolveQueryID", func() error { _, err := ResolveQueryID(dir, c.Source); return err }, ErrQueryNotFound, "query not found"},
			{"loadQueryDocument", func() error { _, err := loadQueryDocument(c.Source, "q", datatug.QueryTypeSQL); return err }, nil, "no project directory configured for project"},
		} {
			err := call.run()
			if err == nil || (call.want != nil && !errors.Is(err, call.want)) || !strings.Contains(err.Error(), call.phrase) {
				t.Fatalf("%s: %s returned %v, want an error that says %q: the property does not reach the message it is meant to read", c.Name, call.name, err, call.phrase)
			}
			texts = append(texts, err.Error())
		}
		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d generated sources leaked through a project or query ID", failed)
	}

	// An ambiguous bare ID names the queries it matches, which are the project's own.
	_, err := ResolveQueryID(dir, "shared")
	if !errors.Is(err, ErrAmbiguousQueryID) || !strings.Contains(err.Error(), `"shared" matches a/shared, b/shared`) {
		t.Fatalf("an ambiguous plain query ID should be named with its matches: %v", err)
	}

	// A plain name is still named, so the message stays useful.
	for _, tc := range []struct {
		run  func() error
		want string
	}{
		{func() error { return getQuery("no-such-project", "q") }, `unknown project "no-such-project"`},
		{func() error { return getQuery("demo", "no-such-query") }, `query not found: "no-such-query"`},
		{func() error { _, err := ResolveQueryID(dir, "folder/no-such-query"); return err }, `query not found: "folder/no-such-query"`},
		{func() error { _, err := loadQueryDocument("no-such-project", "q", datatug.QueryTypeSQL); return err }, `no project directory configured for project "no-such-project"`},
	} {
		if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("a plain ID should be named: got %v, want %q", err, tc.want)
		}
	}
}
