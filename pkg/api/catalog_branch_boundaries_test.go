package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

type catalogFaultFile struct {
	connectionCatalogFile
	statErr, closeErr error
}

func (f catalogFaultFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.connectionCatalogFile.Stat()
}
func (f catalogFaultFile) Close() error {
	err := f.connectionCatalogFile.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func TestConnectionCatalogReadIOAndCapacityDiscardPartialMetadata(t *testing.T) {
	old := openConnectionCatalogFile
	t.Cleanup(func() { openConnectionCatalogFile = old })
	for _, mode := range []string{"open", "stat", "close", "negative budget", "remaining budget"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			_ = os.MkdirAll(filepath.Join(root, "connections"), 0700)
			_ = os.WriteFile(filepath.Join(root, "connections/catalog.json"), []byte(`{"format":"datatug-demo-connections/v1"}`), 0600)
			reader, err := openQualifiedProjectReader(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			failure := errors.New("catalog IO")
			openConnectionCatalogFile = func(r *qualifiedProjectReader, p string) (connectionCatalogFile, error) {
				if mode == "open" {
					return nil, failure
				}
				f, err := old(r, p)
				if err != nil {
					return nil, err
				}
				out := catalogFaultFile{connectionCatalogFile: f}
				if mode == "stat" {
					out.statErr = failure
				}
				if mode == "close" {
					out.closeErr = failure
				}
				return out, nil
			}
			if mode == "negative budget" {
				reader.bytes = maxQualifiedReadBytes + 1
			}
			if mode == "remaining budget" {
				reader.bytes = maxQualifiedReadBytes - 1
			}
			if out, err := readLocalCatalogDocument(reader, "connections/catalog.json"); err == nil || out != nil {
				t.Fatal("failed catalog read exposed bytes", mode, err)
			}
		})
	}
	openConnectionCatalogFile = old
	for _, mode := range []string{"non-json", "too many", "unsafe name", "unsafe source", "duplicate IDs", "closed root"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "connections")
			_ = os.MkdirAll(dir, 0700)
			put := func(name, body string) {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "non-json":
				put("README.txt", "not a catalog")
			case "too many":
				for i := 0; i < 33; i++ {
					put(fmt.Sprintf("c%d.json", i), `{"format":"datatug-demo-connections/v1"}`)
				}
			case "unsafe name":
				put("bad name.json", `{}`)
			case "unsafe source":
				put("c.json", `{"format":"datatug-demo-connections/v1","connections":[{"id":"c","dataset":"c","storage":"ovdb","readiness":"public-api","source":"https://user:secret@example.com"}]}`)
			case "duplicate IDs":
				for _, name := range []string{"a.json", "b.json"} {
					put(name, `{"format":"datatug-demo-connections/v1","bigQueryPlans":[{"id":"same","readiness":"setup-required"}]}`)
				}
			}
			reader, err := openQualifiedProjectReader(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			if mode == "closed root" {
				reader.close()
			}
			if out, err := readMergedLocalConnectionCatalog(reader); err == nil || out != nil {
				t.Fatal("incomplete aggregate catalog accepted", mode, err)
			}
		})
	}
}

func TestConnectionCatalogHostAndQueryFolderCapabilityFailures(t *testing.T) {
	project, _ := servedWritableProject(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, true)
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}
	ctx := context.Background()
	oldDir, oldReader := servedProjectDirCatalog, openConnectionCatalogReader
	t.Cleanup(func() { servedProjectDirCatalog, openConnectionCatalogReader = oldDir, oldReader })
	openConnectionCatalogReader = func(string) (*qualifiedProjectReader, error) { return nil, io.ErrUnexpectedEOF }
	if out, err := LocalProjectConnectionCatalog(ctx, ref); err == nil || out != nil {
		t.Fatal("reader construction failure accepted")
	}
	openConnectionCatalogReader = oldReader
	servedProjectDirCatalog = func(string) (string, error) { return "", io.ErrUnexpectedEOF }
	if out, err := LocalProjectConnectionCatalogForBranch(ctx, ref, ""); err == nil || out != nil {
		t.Fatal("serving root failure accepted")
	}
	original := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = original })
	for _, unsupported := range []bool{false, true} {
		storage.NewDatatugStore = func(string) (storage.Store, error) {
			if !unsupported {
				return nil, io.ErrUnexpectedEOF
			}
			return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return &mockProjectStore{} }}, nil
		}
		if out, err := DeclaredProjectQueryFolders(ctx, ref); unsupported {
			if err != nil || out == nil || len(out) != 0 {
				t.Fatal("unsupported declarations must be empty", out, err)
			}
		} else if err == nil || out != nil {
			t.Fatal("store failure exposed declarations")
		}
		if out, err := CreateProjectQueryFolder(ctx, QueryFolderCreate{ProjectRef: ref, Path: "~", Name: "reports"}); err == nil || out != nil {
			t.Fatal("unsupported folder provider accepted write")
		}
	}
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	if out, err := DeclaredProjectQueryFolders(ctx, ref); err == nil || out != nil {
		t.Fatal("unbound folder principal accepted")
	}
	if err := (QueryFolderCreate{ProjectRef: ref, Name: "bad/name"}).Validate(); err == nil {
		t.Fatal("invalid folder identity accepted")
	}
}

func TestCatalogLegacyReadAndSelectedBranchRootFailures(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "connections/demo-db.json"), 0700)
	if out, err := readLocalConnectionCatalog(root); err == nil || out != nil {
		t.Fatal("nonregular legacy catalog accepted")
	}
	_ = os.RemoveAll(filepath.Join(root, "connections/demo-db.json"))
	_ = os.WriteFile(filepath.Join(root, "connections/demo-db.json"), []byte(`{"format":"datatug-demo-connections/v1"}`), 0600)
	oldRead := ioReadAllCatalog
	t.Cleanup(func() { ioReadAllCatalog = oldRead })
	ioReadAllCatalog = func(io.Reader) ([]byte, error) { return nil, io.ErrUnexpectedEOF }
	if out, err := readLocalConnectionCatalog(root); err == nil || out != nil {
		t.Fatal("failed legacy catalog read accepted")
	}
	ioReadAllCatalog = oldRead
	dir, git := setupTestGitProject(t, "catalog-scope")
	configureBranchReadTest(t, "catalog-scope", dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	oldDir := servedProjectDirCatalog
	t.Cleanup(func() { servedProjectDirCatalog = oldDir })
	servedProjectDirCatalog = func(string) (string, error) { return "", io.ErrUnexpectedEOF }
	if out, err := LocalProjectConnectionCatalogForBranch(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "catalog-scope"}, branch); err == nil || out != nil {
		t.Fatal("selected branch root failure accepted")
	}
}

func TestSampleContextDoesNotAdvertiseChangedPublicFixturePins(t *testing.T) {
	body, err := os.ReadFile("testdata/demo-project-connections.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	_ = json.Unmarshal(body, &catalog)
	connections := catalog["connections"].([]any)
	found := false
	for _, v := range connections {
		c := v.(map[string]any)
		if c["id"] == "chinook-sqlite" {
			found = true
			c["source"] = "https://changed.example/v1/databases/chinook"
		}
	}
	if !found {
		t.Fatal("fixture pin missing")
	}
	body, _ = json.Marshal(catalog)
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "connections"), 0700)
	_ = os.WriteFile(filepath.Join(root, "connections/demo-db.json"), body, 0600)
	if out := localSessionSampleSubjects(root, []SessionContextEnvironment{{EnvironmentID: "prod", IdentityDomain: "tenant"}}); out != nil {
		t.Fatal("changed public fixture pin advertised native context subject")
	}
}

func TestQueryFolderCurrentBranchProviderFailuresDoNotCreateFiles(t *testing.T) {
	dir, git := setupTestGitProject(t, "folder-ports")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"folder-ports": dir}, Capabilities{AllowWrites: true})
	original := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = original })
	for _, missingCapability := range []bool{false, true} {
		storage.NewDatatugStore = func(string) (storage.Store, error) {
			if !missingCapability {
				return nil, io.ErrUnexpectedEOF
			}
			return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return &mockProjectStore{} }}, nil
		}
		if out, err := CreateProjectQueryFolder(context.Background(), QueryFolderCreate{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "folder-ports"}, Branch: branch, Path: "~", Name: "reports"}); err == nil || out != nil {
			t.Fatal("unavailable current-branch folder provider accepted")
		}
		if _, err := os.Stat(filepath.Join(dir, "queries/reports")); !os.IsNotExist(err) {
			t.Fatal("failed folder provider wrote a directory")
		}
	}
}
