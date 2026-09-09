package commands

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/mitchellh/go-homedir"
	_ "modernc.org/sqlite"
)

// go-homedir caches the resolved home directory for the whole process by
// default (var homedirCache in its Dir()); without disabling that cache here,
// whichever test in this package happens to call dtconfig.GetSettings or
// dtroot.Path first "wins" HOME for every later t.Setenv("HOME", ...) in the
// same test binary run. See pkg/dtroot/dtroot_test.go for the equivalent
// concern solved by overriding dtroot's own homedirDir var instead - that
// option isn't available here since dtconfig's matching var is unexported in
// datatug-core.
func init() {
	homedir.DisableCache = true
}

func TestDemoCommandArgs_RegistersFlags(t *testing.T) {
	cmd := demoCommandArgs()
	for _, name := range []string{"reset-db", "reset-project"} {
		if !cmdHasFlag(cmd, name) {
			t.Errorf("demo command must register --%s flag", name)
		}
	}
}

func writeCatalogFile(t *testing.T, path, driver, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	content := `{"driver":"` + driver + `","path":"` + dbPath + `"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFindSQLiteCatalogPaths(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, filepath.Join(dir, "servers/db/sqlite3/localhost/catalogs/chinook-local/chinook-local.db.json"),
		"sqlite3", filepath.Join(dir, "dbs", "chinook-local.sqlite"))
	writeCatalogFile(t, filepath.Join(dir, "servers/db/sqlite3/localhost/catalogs/chinook-prod/chinook-prod.db.json"),
		"sqlite3", filepath.Join(dir, "dbs", "chinook-prod.sqlite"))
	// A duplicate reference to the local catalog (mirrors the demo repo's
	// own layout, where environments/local/catalogs/... repeats the same
	// catalog the servers/ tree already declares) must be deduplicated.
	writeCatalogFile(t, filepath.Join(dir, "environments/local/catalogs/chinook-local/chinook-local.db.json"),
		"sqlite3", filepath.Join(dir, "dbs", "chinook-local.sqlite"))
	// A non-sqlite3 catalog must be ignored.
	writeCatalogFile(t, filepath.Join(dir, "servers/db/mysql/prod/catalogs/other/other.db.json"),
		"mysql", "")

	paths, err := findSQLiteCatalogPaths(dir)
	if err != nil {
		t.Fatalf("findSQLiteCatalogPaths: %v", err)
	}
	sort.Strings(paths)
	want := []string{
		filepath.Join(dir, "dbs", "chinook-local.sqlite"),
		filepath.Join(dir, "dbs", "chinook-prod.sqlite"),
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestFindSQLiteCatalogPaths_ExpandsHome(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	dir := t.TempDir()
	writeCatalogFile(t, filepath.Join(dir, "chinook-local.db.json"), "sqlite3", "~/datatug/dbs/chinook-local.sqlite")

	paths, err := findSQLiteCatalogPaths(dir)
	if err != nil {
		t.Fatalf("findSQLiteCatalogPaths: %v", err)
	}
	want := filepath.Join(tmpHome, "datatug", "dbs", "chinook-local.sqlite")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths = %v, want [%v]", paths, want)
	}
}

// writeFixtureSQLiteFile creates a minimal valid sqlite file with a non-empty
// Customer table, matching what verifySQLiteFile checks for.
func writeFixtureSQLiteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stale %s: %v", path, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO Customer (CustomerId) VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestEnsureSQLiteFiles_DownloadsOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "chinook-prod.sqlite")
	missing := filepath.Join(dir, "chinook-local.sqlite")
	writeFixtureSQLiteFile(t, existing)

	origDownload := downloadSQLiteSource
	defer func() { downloadSQLiteSource = origDownload }()
	var downloadedFor []string
	downloadSQLiteSource = func(dests ...string) error {
		downloadedFor = append(downloadedFor, dests...)
		for _, d := range dests {
			writeFixtureSQLiteFile(t, d)
		}
		return nil
	}

	c := demoCommand{}
	if err := c.ensureSQLiteFiles([]string{existing, missing}); err != nil {
		t.Fatalf("ensureSQLiteFiles: %v", err)
	}
	if len(downloadedFor) != 1 || downloadedFor[0] != missing {
		t.Errorf("downloadedFor = %v, want [%v] (only the missing one)", downloadedFor, missing)
	}
}

func TestEnsureSQLiteFiles_ResetDB_RedownloadsEverything(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "chinook-prod.sqlite")
	p2 := filepath.Join(dir, "chinook-local.sqlite")
	writeFixtureSQLiteFile(t, p1)
	writeFixtureSQLiteFile(t, p2)

	origDownload := downloadSQLiteSource
	defer func() { downloadSQLiteSource = origDownload }()
	var downloadedFor []string
	downloadSQLiteSource = func(dests ...string) error {
		downloadedFor = append(downloadedFor, dests...)
		for _, d := range dests {
			writeFixtureSQLiteFile(t, d)
		}
		return nil
	}

	c := demoCommand{ResetDB: true}
	if err := c.ensureSQLiteFiles([]string{p1, p2}); err != nil {
		t.Fatalf("ensureSQLiteFiles: %v", err)
	}
	sort.Strings(downloadedFor)
	want := []string{p1, p2}
	sort.Strings(want)
	if len(downloadedFor) != 2 || downloadedFor[0] != want[0] || downloadedFor[1] != want[1] {
		t.Errorf("downloadedFor = %v, want %v (ResetDB re-downloads every path)", downloadedFor, want)
	}
}

func TestEnsureSQLiteFiles_EmptyDownloadedFile_FailsVerify(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "chinook-local.sqlite")

	origDownload := downloadSQLiteSource
	defer func() { downloadSQLiteSource = origDownload }()
	downloadSQLiteSource = func(dests ...string) error {
		for _, d := range dests {
			// Simulate a download that produced an empty/invalid db.
			if err := os.WriteFile(d, nil, 0o600); err != nil {
				t.Fatalf("write %s: %v", d, err)
			}
		}
		return nil
	}

	c := demoCommand{}
	if err := c.ensureSQLiteFiles([]string{missing}); err == nil {
		t.Fatal("expected ensureSQLiteFiles to fail verification for an empty db file")
	}
}

func TestRegisterDemoProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := registerDemoProject("demo-project-1", "/path/to/demo-project-1"); err != nil {
		t.Fatalf("registerDemoProject (new): %v", err)
	}
	settings, err := dtconfig.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	ref := settings.GetProjectConfig("demo-project-1")
	if ref == nil || ref.Path != "/path/to/demo-project-1" {
		t.Fatalf("GetProjectConfig(demo-project-1) = %+v, want Path=/path/to/demo-project-1", ref)
	}

	// Idempotent: registering again with the same path must not error and
	// must not duplicate the entry.
	if err := registerDemoProject("demo-project-1", "/path/to/demo-project-1"); err != nil {
		t.Fatalf("registerDemoProject (idempotent re-register): %v", err)
	}
	settings, err = dtconfig.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if len(settings.Projects) != 1 {
		t.Fatalf("Projects = %+v, want exactly 1 entry (no duplicate)", settings.Projects)
	}

	// Re-registering with a changed path updates it in place.
	if err := registerDemoProject("demo-project-1", "/new/path"); err != nil {
		t.Fatalf("registerDemoProject (path change): %v", err)
	}
	settings, err = dtconfig.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	ref = settings.GetProjectConfig("demo-project-1")
	if ref == nil || ref.Path != "/new/path" {
		t.Fatalf("GetProjectConfig(demo-project-1) after path change = %+v, want Path=/new/path", ref)
	}
}

func TestDemoCommand_Execute_ClonesVerifiesRegistersAndServes(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origClone, origDownload, origServe := cloneOrUpdateRepo, downloadSQLiteSource, serveDemoProjectFunc
	defer func() {
		cloneOrUpdateRepo, downloadSQLiteSource, serveDemoProjectFunc = origClone, origDownload, origServe
	}()

	sqlitePath := filepath.Join(tmpHome, "datatug", "dbs", "chinook-local.sqlite")
	var cloneCalls []string
	cloneOrUpdateRepo = func(dir, url string) error {
		cloneCalls = append(cloneCalls, dir)
		catalogFile := filepath.Join(dir, demoProjectFolder, "servers/db/sqlite3/localhost/catalogs/chinook-local/chinook-local.db.json")
		if err := os.MkdirAll(filepath.Dir(catalogFile), 0o755); err != nil {
			return err
		}
		return os.WriteFile(catalogFile, []byte(`{"driver":"sqlite3","path":"`+sqlitePath+`"}`), 0o600)
	}
	var downloadCalls []string
	downloadSQLiteSource = func(dests ...string) error {
		downloadCalls = append(downloadCalls, dests...)
		for _, d := range dests {
			writeFixtureSQLiteFile(t, d)
		}
		return nil
	}
	var servedDir string
	serveDemoProjectFunc = func(demoProjectDir string) error {
		servedDir = demoProjectDir
		return nil
	}

	c := demoCommand{}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(cloneCalls) != 1 {
		t.Fatalf("clone called %d times, want 1", len(cloneCalls))
	}
	if len(downloadCalls) != 1 || downloadCalls[0] != sqlitePath {
		t.Errorf("downloadCalls = %v, want [%v]", downloadCalls, sqlitePath)
	}

	wantProjectDir := filepath.Join(cloneCalls[0], demoProjectFolder)
	if servedDir != wantProjectDir {
		t.Errorf("serveDemoProjectFunc called with %q, want %q", servedDir, wantProjectDir)
	}

	settings, err := dtconfig.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	ref := settings.GetProjectConfig(demoProjectRegistryID)
	if ref == nil || ref.Path != wantProjectDir {
		t.Fatalf("GetProjectConfig(%s) = %+v, want Path=%s", demoProjectRegistryID, ref, wantProjectDir)
	}
}

func TestDemoCommand_Execute_ResetProject_RemovesExistingClone(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origClone, origDownload, origServe := cloneOrUpdateRepo, downloadSQLiteSource, serveDemoProjectFunc
	defer func() {
		cloneOrUpdateRepo, downloadSQLiteSource, serveDemoProjectFunc = origClone, origDownload, origServe
	}()

	sqlitePath := filepath.Join(tmpHome, "datatug", "dbs", "chinook-local.sqlite")
	cloneOrUpdateRepo = func(dir, url string) error {
		catalogFile := filepath.Join(dir, demoProjectFolder, "servers/db/sqlite3/localhost/catalogs/chinook-local/chinook-local.db.json")
		if err := os.MkdirAll(filepath.Dir(catalogFile), 0o755); err != nil {
			return err
		}
		return os.WriteFile(catalogFile, []byte(`{"driver":"sqlite3","path":"`+sqlitePath+`"}`), 0o600)
	}
	downloadSQLiteSource = func(dests ...string) error {
		for _, d := range dests {
			writeFixtureSQLiteFile(t, d)
		}
		return nil
	}
	serveDemoProjectFunc = func(string) error { return nil }

	// First run creates the clone dir; a marker file is planted in it below
	// so the second run can prove ResetProject actually removed it.
	if err := (demoCommand{}).Execute(); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	reposDir := filepath.Join(tmpHome, "datatug", demoOrgRepo)
	marker := filepath.Join(reposDir, "stale-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if err := (demoCommand{ResetProject: true}).Execute(); err != nil {
		t.Fatalf("second Execute (ResetProject): %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected ResetProject to remove the existing clone (and its marker file), stat err = %v", err)
	}
}
