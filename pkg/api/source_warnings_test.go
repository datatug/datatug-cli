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
	registerWarnTestEnvironmentWithDriver(t, "sqlite3", projectID, dir, dbPath)
}

// registerWarnTestEnvironmentWithDriver is registerWarnTestEnvironment for a
// catalog of the given driver (sqlite3, or openvaultdb, whose path is the
// connection descriptor). The environment's server stays a sqlite3 one: an
// environment's server driver is validated against a fixed list, a catalog's is
// not.
func registerWarnTestEnvironmentWithDriver(t *testing.T, driver, projectID, dir, dbPath string) {
	t.Helper()
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
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: driver, Path: dbPath, DbModel: "chinook"}}
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
	serveProjectDirs(t, map[string]string{projectID: dir}) // serve announces what it serves
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
	serveProjectDirs(t, map[string]string{projectID: dir}) // serve announces what it serves
	WarnMissingSourceFiles(context.Background(), map[string]string{projectID: dir})

	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("expected no WARNING log line for an existing file, got %q", buf.String())
	}
}

// A missing source file's warning names the file path as the display form of
// the path: the query string of a catalog path never reaches the log line.
func TestWarnMissingSourceFiles_NeverLogsTheQueryOfThePath(t *testing.T) {
	dir := t.TempDir()
	projectID := "warn-test-redacted"
	registerWarnTestEnvironment(t, projectID, dir, filepath.Join(dir, "x.sqlite")+"?password=s3cr3t-DT01")

	buf := captureLog(t)
	serveProjectDirs(t, map[string]string{projectID: dir}) // serve announces what it serves
	WarnMissingSourceFiles(context.Background(), map[string]string{projectID: dir})

	out := buf.String()
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("expected a WARNING log line, got %q", out)
	}
	if strings.Contains(out, "s3cr3t-DT01") || !strings.Contains(out, "x.sqlite") {
		t.Fatalf("warning must name the file and not the secret: %q", out)
	}
}

// An OpenVaultDB source is backed by a connection descriptor file, so `serve`
// warns at startup when the descriptor is missing, as it does for a SQLite file
// and an inGitDB directory; one that exists gets no warning.
func TestWarnMissingSourceFiles_WarnsAboutAMissingOpenVaultDBDescriptor(t *testing.T) {
	dir := t.TempDir()
	projectID := "warn-test-ovdb-missing"
	missing := filepath.Join(dir, "orders-ovdb.json")
	registerWarnTestEnvironmentWithDriver(t, "openvaultdb", projectID, dir, missing)

	buf := captureLog(t)
	serveProjectDirs(t, map[string]string{projectID: dir}) // serve announces what it serves
	WarnMissingSourceFiles(context.Background(), map[string]string{projectID: dir})

	out := buf.String()
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, missing) {
		t.Fatalf("expected a WARNING that names the missing descriptor %q, got %q", missing, out)
	}

	present := filepath.Join(dir, "present-ovdb.json")
	if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	presentProject := "warn-test-ovdb-present"
	presentDir := t.TempDir()
	registerWarnTestEnvironmentWithDriver(t, "openvaultdb", presentProject, presentDir, present)
	buf = captureLog(t)
	WarnMissingSourceFiles(context.Background(), map[string]string{presentProject: presentDir})
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("expected no WARNING for an existing descriptor, got %q", buf.String())
	}
}
