package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func discoveredBoundProject(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonical
	child := filepath.Join(root, "declared")
	if err = os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(child, "datatug-project.json"), []byte(`{"id":"declared","title":"Declared project"}`), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(child, "owned.sqlite")
	if err = os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	store := filestore.NewProjectStore("declared", child)
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
	env.ID = "local"
	if err = store.SaveEnvironment(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: target, DbModel: "selected"}}
	catalog.ID = "selected"
	server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	if err = store.SaveEnvDbCatalog(context.Background(), "local", server, catalog.ID, catalog); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(child, "policies"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(child, "policies", "read.yaml"), []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: readonly}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: read, effect: allow, operations: [query]}]}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(child, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": "declared", "environment": "local", "catalog": "selected", "sqliteFile": target, "requiredPolicies": []string{"policies/read.yaml"}, "principal": map[string]any{"id": "operator"}})
	if err = os.WriteFile(filepath.Join(child, ".datatug", "security.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, declared, err := secureread.LoadBoundSession(context.Background(), child, secureread.BoundSessionOptions{Required: true}); err != nil || !declared {
		t.Fatalf("valid declaration fixture: %v %v", declared, err)
	}
	return root, child
}

func TestServeHTTPDiscoveredSecurityDeclarationRefusesBeforeFactoriesAndListener(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "declared", true: "invalid"}[malformed], func(t *testing.T) {
			root, child := discoveredBoundProject(t)
			if malformed {
				if err := os.WriteFile(filepath.Join(child, ".datatug", "security.json"), []byte(`{`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			oldListen, oldFactory, oldStore, oldMonitor := listenAndServeFn, filestoreNewStore, storage.NewDatatugStore, apimonitor.Default
			t.Cleanup(func() {
				listenAndServeFn, filestoreNewStore, storage.NewDatatugStore, apimonitor.Default = oldListen, oldFactory, oldStore, oldMonitor
				api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
			})
			apimonitor.Default = &apimonitor.Registry{}
			listened, factories := 0, 0
			listenAndServeFn = func(*http.Server) error { listened++; return http.ErrServerClosed }
			filestoreNewStore = func(id string, paths map[string]string) (storage.Store, error) {
				factories++
				return oldFactory(id, paths)
			}
			s := NewHttpServer()
			err := s.ServeHTTP(map[string]string{}, "127.0.0.1", 8989, unrestrictedTestSession(t), api.Capabilities{ProjectsRoot: root, EvidencePrivateDir: t.TempDir()})
			if !errors.Is(err, secureread.ErrSourceBinding) || listened != 0 || factories != 0 {
				t.Fatalf("discovered profile reached unbound factories/listener: err=%v factories=%d listened=%d", err, factories, listened)
			}
			want := `project "declared" declares security`
			if malformed {
				want = `cannot inspect security configuration for project "declared"`
			}
			if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), root) {
				t.Fatalf("startup error lacks safe project-specific reason: %v", err)
			}
		})
	}
}
