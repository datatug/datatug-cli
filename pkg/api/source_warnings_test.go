package api

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// registerWarnTestEnvironment writes a real environment + DB catalog record
// (mirroring pkg/server/endpoints/semantic_fixture_test.go's
// registerChinookEnvironment) pointing at dbPath, and wires
// storage.NewDatatugStore/filestore.SetProjectPath the way `datatug serve`
// does at startup, so ProjectStoreFor(projectID) and WarnMissingSourceFiles
// can resolve it.
func registerWarnTestEnvironment(t *testing.T, projectID, dir, dbPath string) {
	t.Helper()
	// httpQuerySources (ListSources' HTTP branch) walks <projectDir>/queries
	// and errors if it does not exist (unlike recordsetSources, which
	// tolerates a missing directory) — every real project has this folder,
	// so create it here rather than changing that pre-existing behavior.
	if err := os.MkdirAll(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatalf("mkdir queries dir: %v", err)
	}
	filestore.SetProjectPath(projectID, dir)
	pathsByID := map[string]string{projectID: dir}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}

	projStore := filestore.NewProjectStore(projectID, dir)
	ctx := context.Background()
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
	env.ID = "local"
	if err := projStore.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: dbPath, DbModel: "chinook"}}
	catalog.ID = "chinook-local"
	if err := projStore.SaveEnvDbCatalog(ctx, env.ID, serverID, catalog.ID, catalog); err != nil {
		t.Fatalf("SaveEnvDbCatalog: %v", err)
	}
}

func captureLog(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

// TestWarnMissingSourceFiles_LogsOneWarningPerMissingFile covers S80 Fix 2's
// startup-warning requirement: when `datatug serve --project` starts, one
// warning line names each environment source whose file path does not
// exist, so the operator sees it before the browser does (S77's finding:
// this used to surface only as an undocumented 500 on the first request
// that touched it).
func TestWarnMissingSourceFiles_LogsOneWarningPerMissingFile(t *testing.T) {
	dir := t.TempDir()
	projectID := "warn-test-missing"
	missingPath := filepath.Join(dir, "chinook-local.sqlite")
	registerWarnTestEnvironment(t, projectID, dir, missingPath)

	buf := captureLog(t)
	WarnMissingSourceFiles(context.Background(), map[string]string{projectID: dir})

	out := buf.String()
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("expected a WARNING log line, got %q", out)
	}
	if !strings.Contains(out, missingPath) {
		t.Fatalf("expected the log line to name the missing path %q, got %q", missingPath, out)
	}
	if !strings.Contains(out, "datatug demo") {
		t.Fatalf("expected the `datatug demo` recovery hint, got %q", out)
	}
}

// TestWarnMissingSourceFiles_NoWarningWhenFileExists proves the warning is
// specific to actually-missing files, not printed unconditionally.
func TestWarnMissingSourceFiles_NoWarningWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	projectID := "warn-test-present"
	existingPath := filepath.Join(dir, "chinook-local.sqlite")
	if err := os.WriteFile(existingPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	registerWarnTestEnvironment(t, projectID, dir, existingPath)

	buf := captureLog(t)
	WarnMissingSourceFiles(context.Background(), map[string]string{projectID: dir})

	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("expected no WARNING log line for an existing file, got %q", buf.String())
	}
}
