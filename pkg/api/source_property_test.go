package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// A source a client sent where an ID belongs is named in the error only when it
// is a plain name (dbcopy.SourceIDDisplay): never as a whole source string.
func TestProperty_ResolveSourceNeverEchoesASourceString(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range sourcecases.All() {
		_, err := ResolveSource(context.Background(), mockProjectStore{}, projectDir, "", c.Source)
		if err == nil {
			t.Fatalf("%s: a source that no registry holds must not resolve", c.Name)
		}
		if leaked := sourcecases.Leaks(c, err.Error()); len(leaked) > 0 {
			t.Errorf("%s\n  leaked %q in %q", c.Name, leaked, err.Error())
		}
		if !strings.Contains(err.Error(), "unknown source") {
			t.Errorf("%s: error %q should still say the source is unknown", c.Name, err.Error())
		}
	}
	// A plain name is still named, so the message stays useful.
	_, err := ResolveSource(context.Background(), mockProjectStore{}, projectDir, "", "chinook")
	if err == nil || !strings.Contains(err.Error(), `unknown source "chinook"`) {
		t.Fatalf("a plain source ID should be named: %v", err)
	}
}

// A database or an environment a client sent to a legacy route (exec/select,
// exec/execute_commands) is named in the error only when it is a plain name,
// and the error of the lookup that failed, which quotes the path the value was
// turned into, is carried only then. The text is read before any sink, so a
// redactor cannot be what keeps a secret out.
func TestProperty_ResolveSourceURLNeverEchoesASourceString(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	// The loaders of the file store say which path they could not read, and the
	// path holds the ID they were given.
	store := mockProjectStore{
		loadEnvironmentFunc: func(_ context.Context, id string, _ ...datatug.StoreOption) (*datatug.Environment, error) {
			if id != "local" {
				return nil, fmt.Errorf("open %s: no such file or directory", filepath.Join(projectDir, "environments", id))
			}
			return &datatug.Environment{DbServers: []*datatug.EnvDbServer{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}, nil
		},
		loadEnvDbCatalogFunc: func(_ context.Context, env, _, id string, _ ...datatug.StoreOption) (datatug.DbCatalog, error) {
			return datatug.DbCatalog{}, fmt.Errorf("open %s: no such file or directory", filepath.Join(projectDir, "environments", env, "catalogs", id))
		},
		loadEnvDbCatalogsFunc: func(_ context.Context, env string, _ ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return nil, fmt.Errorf("read %s: no such directory", filepath.Join(projectDir, "environments", env, "catalogs"))
		},
	}
	failed := 0
	for _, c := range sourcecases.All() {
		var texts []string
		for _, call := range []struct {
			name              string
			environment, base string
			run               func(environment, database string) error
			wantPhrase        string
		}{
			{"database", "local", c.Source, func(e, d string) error { _, _, err := resolveSourceURL(ctx, store, e, d, projectDir); return err }, "not found in environment"},
			{"environment", c.Source, "chinook", func(e, d string) error { _, _, err := resolveSourceURL(ctx, store, e, d, projectDir); return err }, "load environment"},
			{"resolver environment", c.Source, "chinook", func(e, d string) error { _, err := ResolveSource(ctx, store, projectDir, e, d); return err }, "list catalogs for environment"},
		} {
			err := call.run(call.environment, call.base)
			if err == nil {
				t.Fatalf("%s: %s: a lookup that finds nothing must fail", c.Name, call.name)
			}
			if !strings.Contains(err.Error(), call.wantPhrase) {
				t.Fatalf("%s: the %s message %q does not say %q: the property does not reach the message it is meant to read", c.Name, call.name, err.Error(), call.wantPhrase)
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
		t.Errorf("%d generated sources leaked through a lookup error", failed)
	}

	// A plain name is still named, with the lookup's own error, so the message stays useful.
	_, _, err := resolveSourceURL(ctx, store, "local", "chinook", projectDir)
	if err == nil || !strings.Contains(err.Error(), `database "chinook" not found in environment "local": open `) {
		t.Fatalf("a plain database ID should be named with the lookup error: %v", err)
	}
	// A server-less environment names the IDs the same way.
	empty := mockProjectStore{loadEnvironmentFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
		return &datatug.Environment{}, nil
	}}
	if _, _, err = resolveSourceURL(ctx, empty, "local", "chinook", projectDir); err == nil || !strings.Contains(err.Error(), `environment "local" has no DB servers configured; cannot resolve database "chinook"`) {
		t.Fatalf("a plain ID of a server-less environment should be named: %v", err)
	}
	if _, _, err = resolveSourceURL(ctx, empty, "local", "http://u:s3cretpw@host/x", projectDir); err == nil || strings.Contains(err.Error(), "s3cretpw") {
		t.Fatalf("a source string given as the database of a server-less environment: %v", err)
	}
}
