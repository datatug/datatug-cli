package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func apiBoundFixture(t *testing.T) (string, datatug.ProjectStore, secureread.Session) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	store := filestore.NewProjectStore("binding", root)
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
	env.ID = "local"
	if err = store.SaveEnvironment(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	for _, id := range []string{"selected", "other"} {
		path := filepath.Join(root, id+".sqlite")
		if err = os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: path, DbModel: id + "-alias"}}
		catalog.ID = id
		if err = store.SaveEnvDbCatalog(context.Background(), "local", server, id, catalog); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(filepath.Join(root, "policies"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: selected}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: read, effect: allow, operations: [query]}]}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": "binding", "environment": "local", "catalog": "selected", "sqliteFile": filepath.Join(root, "selected.sqlite"), "requiredPolicies": []string{"policies/read.yaml"}, "principal": map[string]any{"id": "operator"}})
	if err = os.MkdirAll(filepath.Join(root, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".datatug", "security.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal(err)
	}
	return root, store, session
}
func TestBoundSourceBothResolversAndCacheIdentity(t *testing.T) {
	root, store, s := apiBoundFixture(t)
	ConfigureSecureSession(s, map[string]string{"binding": root}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	originalID := SecurityContextID()
	if !SourceBoundProfile() || !ValidateSecurityContext(originalID) {
		t.Fatal("binding not configured")
	}
	for _, id := range []string{"selected", "other", "selected-alias", "other-alias", ""} {
		allowed := id == "selected"
		resolved, err := ResolveSource(context.Background(), store, root, "local", id)
		if (err == nil) != allowed {
			t.Fatalf("new resolver %q: %v", id, err)
		}
		if allowed {
			if _, bound, err := BoundSourceBackend(context.Background(), resolved.URL); !bound || err != nil {
				t.Fatal("pinned backend", err)
			}
		}
		_, _, err = resolveSourceURL(context.Background(), store, "local", id, root)
		if (err == nil) != allowed {
			t.Fatalf("legacy resolver %q: %v", id, err)
		}
	}
	if _, err := catalogSources(context.Background(), store, root, "other"); err == nil {
		t.Fatal("private target list accepted other environment")
	}
	all, err := ListSources(context.Background(), store, root, "local")
	if err != nil || len(all) != 1 || all[0].ID != "selected" {
		t.Fatalf("discovery: %+v %v", all, err)
	}
	if _, err = ResolveProjectSourceURL(context.Background(), store, root, "local", "selected"); !errors.Is(err, secureread.ErrSourceBinding) {
		t.Fatalf("named CLI silently unbound: %v", err)
	}
	if _, _, err = boundResolvedSource(context.Background(), nil, root, "local", "selected"); err == nil {
		t.Fatal("missing registry")
	}
	if _, err = ResolveSource(context.Background(), store, root, "other", "selected"); err == nil {
		t.Fatal("environment substitution")
	}
	ConfigureSecureSession(s, map[string]string{"binding": root}, Capabilities{})
	if ValidateSecurityContext(originalID) || !ValidateSecurityContext(SecurityContextID()) {
		t.Fatal("session cache identity reused")
	}
	if err = os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if ValidateSecurityContext(SecurityContextID()) || CheckSourceBinding(context.Background()) == nil {
		t.Fatal("policy change retained cache authority")
	}
	if _, err = ListSources(context.Background(), store, root, "local"); err == nil {
		t.Fatal("changed policy still discovers")
	}
}
func TestBoundSourceInvalidServerCannotFallBack(t *testing.T) {
	for _, name := range []string{"writes", "raw", "live", "other-project", "extra-project"} {
		t.Run(name, func(t *testing.T) {
			root, _, s := apiBoundFixture(t)
			caps := Capabilities{}
			paths := map[string]string{"binding": root}
			switch name {
			case "writes":
				caps.AllowWrites = true
			case "raw":
				caps.AllowOpaqueSQL = true
			case "live":
				caps.AllowLiveConnections = true
			case "other-project":
				paths = map[string]string{"other": root}
			case "extra-project":
				paths["extra"] = root
			}
			ConfigureSecureSession(s, paths, caps)
			t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
			if !SourceBoundProfile() || CheckSourceBinding(context.Background()) == nil || ValidateSecurityContext(SecurityContextID()) {
				t.Fatal("invalid startup became legacy")
			}
		})
	}
}

func TestBoundReadScopeAndCatalogProjectionKeepOriginalIdentity(t *testing.T) {
	root, store, session := apiBoundFixture(t)
	ConfigureSecureSession(session, map[string]string{"binding": root}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	project, environment, catalog, bound, err := BoundReadScope(context.Background())
	if err != nil || !bound || project != "binding" || environment != "local" || catalog != "selected" {
		t.Fatalf("original scope: %q %q %q %v %v", project, environment, catalog, bound, err)
	}
	items, err := catalogSources(context.Background(), store, root, environment)
	if err != nil || len(items) != 1 || items[0].ID != catalog || items[0].ID == "other" {
		t.Fatalf("catalog projection escaped selected source: %+v %v", items, err)
	}
	if err := os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, bound, err := BoundReadScope(context.Background()); !bound || !errors.Is(err, secureread.ErrSourceBinding) {
		t.Fatalf("changed policy kept discovery authority: bound=%v err=%v", bound, err)
	}
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	project, environment, catalog, bound, err = BoundReadScope(context.Background())
	if err != nil || bound || project != "" || environment != "" || catalog != "" {
		t.Fatalf("legacy session invented a binding: %q %q %q %v %v", project, environment, catalog, bound, err)
	}
}
