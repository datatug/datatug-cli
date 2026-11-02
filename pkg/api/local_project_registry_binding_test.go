package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalRegistryCreateReplayRefusesDeclaredProjectBeforeRegistration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}

	for _, writable := range []bool{false, true} {
		t.Run(map[bool]string{false: "readonly-owner", true: "write-enabled-owner"}[writable], func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registry, err := localprojects.Open(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			session, err := secureread.NewSession(secureread.SessionOptions{As: "owner", NoPolicies: true})
			if err != nil {
				t.Fatal(err)
			}
			ConfigureSecureSession(session, map[string]string{}, Capabilities{AllowWrites: writable})
			ConfigureLocalProjectRegistry(registry)
			if err = ConfigureExecutionEvidence(map[string]string{}, nil, executionstore.Options{PrivateDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = CloseExecutionEvidence()
				ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
			})
			originalEvidence := registerCreatedProjectEvidence
			evidenceCalls := 0
			registerCreatedProjectEvidence = func(id, path string) error { evidenceCalls++; return originalEvidence(id, path) }
			t.Cleanup(func() { registerCreatedProjectEvidence = originalEvidence })
			request := localprojects.Create{Store: "local", ID: "late-declared", Title: "Late declared project", OperationID: "known-create-operation"}
			child := filepath.Join(root, request.ID)
			if err = os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(name string, b []byte) {
				t.Helper()
				f := filepath.Join(child, name)
				if e := os.MkdirAll(filepath.Dir(f), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(f, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			digest := sha256.Sum256([]byte(request.ID + "\x00" + request.Title))
			document, _ := json.Marshal(map[string]any{"id": request.ID, "title": request.Title, "localCreation": map[string]any{"operationId": request.OperationID, "digest": hex.EncodeToString(digest[:])}})
			write("datatug-project.json", document)
			target := filepath.Join(child, "owned.sqlite")
			write("owned.sqlite", nil)
			store := filestore.NewProjectStore(request.ID, child)
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
			write("policies/read.yaml", []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: readonly}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: read, effect: allow, operations: [query]}]}]\n"))
			config, _ := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": request.ID, "environment": "local", "catalog": "selected", "sqliteFile": target, "requiredPolicies": []string{"policies/read.yaml"}, "principal": map[string]any{"id": "operator"}})
			write(".datatug/security.json", config)
			if bound, declared, e := secureread.LoadBoundSession(context.Background(), child, secureread.BoundSessionOptions{Required: true}); e != nil || !declared || !bound.SourceBound() {
				t.Fatalf("valid declaration control: %v %v %v", e, declared, bound.SourceBound())
			}
			state, err := GetLocalProjectRegistry()
			if err != nil || len(state.Projects) != 1 {
				t.Fatalf("runtime discovery control: %+v %v", state, err)
			}
			if _, ok := SecureConfiguredProjectPaths()[request.ID]; ok {
				t.Fatal("List alone changed served map")
			}
			_, err = CreateLocalProject(context.Background(), request)
			actual, admitted := SecureConfiguredProjectPaths()[request.ID]
			t.Logf("writeEnabled=%v replayError=%v served=%v pathMatches=%v", writable, err, admitted, actual == child)
			if (!errors.Is(err, secureread.ErrAccessDenied) && !errors.Is(err, secureread.ErrSourceBinding)) || admitted || evidenceCalls != 0 {
				t.Fatalf("runtime replay admitted declared child under old policy-free owner: error=%v served=%v evidenceCalls=%d", err, admitted, evidenceCalls)
			}
		})
	}
}
