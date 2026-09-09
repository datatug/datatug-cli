package commands

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/go-git/go-git/v5"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite" // pure-Go sqlite driver: verifying an on-disk demo db file needs no cgo
)

// The real demo lives in github.com/datatug/datatug-demo-projects (plural) -
// a full DataTug project (demo-project-1: Customer/Invoice/Country entities,
// DTQL/SQL/HTTP queries, an admin/support access policy) checked into git,
// not synthesized. This replaces the old `datatug demo`, which cloned the
// LEGACY, singular github.com/datatug/datatug-demo-project and then
// programmatically built a minimal project around a downloaded Chinook
// SQLite file, only to panic on save (storage.NewDatatugStore was never
// wired - see datatug/datatug spec/research/2026-09-09-current-state-audit.md).
// Cloning the real, already-complete project makes both failure modes
// impossible: there is no legacy clone left to reach, and no project-save
// call that could hit the unwired store.
const (
	// demoOrgRepo matches the "clone GitHub projects under here" convention
	// documented on pkg/dtroot (~/datatug/github.com/...) and already used by
	// the TUI's own demo-project opener
	// (apps/datatugapp/datatugui/dtproject/datatug_demo_project.go) - cloning
	// to the same path means the CLI `demo` command and the TUI share one
	// clone instead of each keeping a separate copy.
	demoOrgRepo           = "github.com/datatug/datatug-demo-projects"
	demoReposGitURL       = "https://" + demoOrgRepo + ".git"
	demoProjectFolder     = "demo-project-1"
	demoProjectRegistryID = "demo-project-1"

	// Every sqlite3 catalog in demo-project-1 is the same Chinook fixture
	// (see servers/db/sqlite3/localhost/catalogs/*/*.db.json in the demo
	// repo); downloaded once and written to every missing path.
	chinookSQLiteSourceURL = "https://github.com/datatug/chinook-database/blob/master/ChinookDatabase/DataSources/Chinook_Sqlite.sqlite?raw=true"
)

func demoCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Installs & serves the demo project",
		Long:  "Clones/refreshes the datatug-demo-projects repo, verifies its SQLite fixture(s), registers demo-project-1, then serves it (equivalent to `serve --project <dir> --as admin --role admin`)",
		RunE:  demoCommandAction,
	}
	cmd.Flags().Bool("reset-db", false, "Re-downloads the demo SQLite fixture file(s) from the internet")
	cmd.Flags().Bool("reset-project", false, "Re-clones the demo project from scratch")
	return cmd
}

type demoCommand struct {
	ResetDB      bool
	ResetProject bool
}

// cloneOrUpdateRepo clones url into dir if dir does not exist yet, or fetches
// and fast-forwards it (via Pull) if it does. A package var so tests can
// substitute a fake that never touches the network (REQ: no network in unit
// tests).
var cloneOrUpdateRepo = defaultCloneOrUpdateRepo

func defaultCloneOrUpdateRepo(dir, url string) error {
	if _, err := os.Stat(dir); err == nil {
		repo, err := git.PlainOpen(dir)
		if err != nil {
			return fmt.Errorf("failed to open existing clone at %s: %w", dir, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return fmt.Errorf("failed to get worktree for %s: %w", dir, err)
		}
		if err := wt.Pull(&git.PullOptions{RemoteName: "origin"}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("failed to update clone at %s: %w", dir, err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0777); err != nil {
		return fmt.Errorf("failed to create parent directory for %s: %w", dir, err)
	}
	if _, err := git.PlainClone(dir, false, &git.CloneOptions{URL: url, Tags: git.NoTags}); err != nil {
		return fmt.Errorf("failed to git clone %s: %w", url, err)
	}
	return nil
}

// downloadSQLiteSource fetches the Chinook SQLite fixture once and writes it
// to every destination path. A package var so tests can substitute a fake
// that never touches the network.
var downloadSQLiteSource = defaultDownloadSQLiteSource

func defaultDownloadSQLiteSource(dests ...string) error {
	writers := make([]io.Writer, len(dests))
	files := make([]*os.File, len(dests))
	for i, dest := range dests {
		f, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", dest, err)
		}
		files[i] = f
		writers[i] = f
	}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	log.Println("Downloading SQLite version of Chinook database...")
	resp, err := http.Get(chinookSQLiteSourceURL)
	if err != nil {
		return fmt.Errorf("get request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if _, err := io.Copy(io.MultiWriter(writers...), resp.Body); err != nil {
		return fmt.Errorf("failed to write response body into file: %w", err)
	}
	return nil
}

// dbCatalogFile is the on-disk shape of a *.db.json DbCatalog file (only the
// two fields this command needs).
type dbCatalogFile struct {
	Driver string `json:"driver"`
	Path   string `json:"path"`
}

// findSQLiteCatalogPaths walks demoProjectDir for every "*.db.json" catalog
// file and returns the absolute, ~-expanded paths of every sqlite3 catalog
// with a Path set - the demo project's own declared list of SQLite files it
// needs, read from its own config rather than hardcoded here.
func findSQLiteCatalogPaths(demoProjectDir string) ([]string, error) {
	var paths []string
	seen := make(map[string]bool)
	err := filepath.WalkDir(demoProjectDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".db.json") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", p, err)
		}
		var catalog dbCatalogFile
		if err := json.Unmarshal(data, &catalog); err != nil {
			return fmt.Errorf("failed to parse %s: %w", p, err)
		}
		if catalog.Driver != "sqlite3" || catalog.Path == "" {
			return nil
		}
		expanded, err := homedir.Expand(catalog.Path)
		if err != nil {
			return fmt.Errorf("failed to expand path %q from %s: %w", catalog.Path, p, err)
		}
		if !seen[expanded] {
			seen[expanded] = true
			paths = append(paths, expanded)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// ensureSQLiteFiles downloads whichever of paths do not already exist (or
// every one of them, with ResetDB), then verifies every path opens and its
// Customer table has rows.
func (c demoCommand) ensureSQLiteFiles(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	var missing []string
	for _, p := range paths {
		if c.ResetDB {
			missing = append(missing, p)
			continue
		}
		if _, err := os.Stat(p); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("failed to check for existing SQLite file %s: %w", p, err)
			}
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		for _, p := range missing {
			if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", p, err)
			}
		}
		if err := downloadSQLiteSource(missing...); err != nil {
			return fmt.Errorf("failed to download demo SQLite file(s): %w", err)
		}
	}
	return verifySQLiteFiles(paths)
}

func verifySQLiteFiles(paths []string) error {
	for _, p := range paths {
		if err := verifySQLiteFile(p); err != nil {
			return fmt.Errorf("failed to verify demo SQLite file %s: %w", p, err)
		}
	}
	return nil
}

func verifySQLiteFile(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var count int
	//goland:noinspection SqlDialectInspection,SqlNoDataSourceInspection
	if err := db.QueryRow(`SELECT COUNT(*) FROM Customer`).Scan(&count); err != nil {
		return fmt.Errorf("failed to count Customer rows: %w", err)
	}
	if count <= 0 {
		return errors.New("table Customer is empty")
	}
	return nil
}

// registerDemoProject adds (or refreshes the path of) the demo project entry
// in ~/.datatug.yaml. Idempotent: re-running `datatug demo` after it is
// already registered updates the path if it changed and otherwise no-ops,
// rather than erroring like dtconfig.AddProjectToSettings does on a
// duplicate ID.
func registerDemoProject(id, path string) error {
	settings, err := dtconfig.GetSettings()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", dtconfig.ConfigFileName, err)
	}
	for _, p := range settings.Projects {
		if p.ID == id {
			if p.Path == path {
				return nil
			}
			p.Path = path
			return dtconfig.SaveSettings(settings)
		}
	}
	settings.Projects = append(settings.Projects, &dtconfig.ProjectRef{ID: id, Path: path})
	return dtconfig.SaveSettings(settings)
}

// serveDemoProjectFunc hands off to the same code path `serve --project <dir>
// --as admin --role admin` uses - the demo ships a policies/ set, so a
// principal is required (resolveServeSession, cmd_serve.go). A package var so
// tests can substitute a spy instead of actually starting (and blocking on) a
// real HTTP server.
var serveDemoProjectFunc = defaultServeDemoProject

func defaultServeDemoProject(demoProjectDir string) error {
	cmd := serveCommandArgs()
	if err := cmd.Flags().Set(serveProjectFlag, demoProjectDir); err != nil {
		return err
	}
	if err := cmd.Flags().Set(serveAsFlag, "admin"); err != nil {
		return err
	}
	if err := cmd.Flags().Set(serveRoleFlag, "admin"); err != nil {
		return err
	}
	return serveCommandAction(cmd, nil)
}

func (c demoCommand) Execute() error {
	reposDir := filepath.Join(dtroot.Path(), demoOrgRepo)

	if c.ResetProject {
		if err := os.RemoveAll(reposDir); err != nil {
			return fmt.Errorf("failed to remove existing demo project clone: %w", err)
		}
	}
	log.Println("Cloning/updating demo project repo from", demoReposGitURL, "...")
	if err := cloneOrUpdateRepo(reposDir, demoReposGitURL); err != nil {
		return fmt.Errorf("failed to clone/update %s: %w", demoReposGitURL, err)
	}

	demoProjectPath := filepath.Join(reposDir, demoProjectFolder)
	if info, err := os.Stat(demoProjectPath); err != nil || !info.IsDir() {
		return fmt.Errorf("demo project folder %s not found after cloning %s", demoProjectPath, demoReposGitURL)
	}

	sqlitePaths, err := findSQLiteCatalogPaths(demoProjectPath)
	if err != nil {
		return fmt.Errorf("failed to read demo project's SQLite catalog config: %w", err)
	}
	if err := c.ensureSQLiteFiles(sqlitePaths); err != nil {
		return err
	}

	if err := registerDemoProject(demoProjectRegistryID, demoProjectPath); err != nil {
		return fmt.Errorf("failed to register demo project in %s: %w", dtconfig.ConfigFileName, err)
	}

	log.Println("DataTug demo project is ready at", demoProjectPath)
	return serveDemoProjectFunc(demoProjectPath)
}

func demoCommandAction(cmd *cobra.Command, _ []string) error {
	resetDB, _ := cmd.Flags().GetBool("reset-db")
	resetProject, _ := cmd.Flags().GetBool("reset-project")
	c := demoCommand{
		ResetDB:      resetDB,
		ResetProject: resetProject,
	}
	return c.Execute()
}
