package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cov100fSetVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func cov100fHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func cov100fRun(t *testing.T, add func(*cobra.Command), argv ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	add(root)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(argv)
	err = root.ExecuteContext(context.Background())
	return
}

// cov100fStore is a storage.Store that always returns the same project store.
type cov100fStore struct {
	proj datatug.ProjectStore
}

func (s cov100fStore) GetProjectStore(string) datatug.ProjectStore { return s.proj }
func (s cov100fStore) CreateProject(context.Context, dto.CreateProjectRequest) (*datatug.ProjectSummary, error) {
	return nil, errors.New("not implemented")
}
func (s cov100fStore) DeleteProject(context.Context, string) error {
	return errors.New("not implemented")
}
func (s cov100fStore) GetProjects(context.Context) ([]datatug.ProjectBrief, error) {
	return nil, errors.New("not implemented")
}

// cov100fRS wraps a ProjectStore and overrides recordset methods.
type cov100fRS struct {
	datatug.ProjectStore
	defs    []*datatug.RecordsetDefinition
	defsErr error
	def     *datatug.RecordsetDefinition
	defErr  error
	data    datatug.Recordset
	dataErr error
}

func (s cov100fRS) LoadRecordsetDefinitions(context.Context, ...datatug.StoreOption) ([]*datatug.RecordsetDefinition, error) {
	return s.defs, s.defsErr
}
func (s cov100fRS) LoadRecordsetDefinition(context.Context, string, ...datatug.StoreOption) (*datatug.RecordsetDefinition, error) {
	return s.def, s.defErr
}
func (s cov100fRS) LoadRecordsetData(context.Context, string) (datatug.Recordset, error) {
	return s.data, s.dataErr
}

func TestCov100fConfigCommand(t *testing.T) {
	home := cov100fHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, dtconfig.ConfigFileName), []byte("projects: []\n"), 0o600))
	t.Run("ok", func(t *testing.T) {
		_, _, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(configCommand()) }, "config")
		require.NoError(t, err)
	})
	t.Run("print fails", func(t *testing.T) {
		cov100fSetVar(t, &configPrintSettings, func(dtconfig.Settings, dtconfig.Format, io.Writer) error {
			return errors.New("print boom")
		})
		_, _, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(configCommand()) }, "config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "print boom")
	})
}

func TestCov100fDatasetCommandAction(t *testing.T) {
	t.Run("init fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetInitProject, func(*datasetCommand) error {
			return errors.New("no project")
		})
		err := datasetCommandAction(nil, nil)
		require.Error(t, err)
	})
	t.Run("ok", func(t *testing.T) {
		cov100fSetVar(t, &datasetInitProject, func(*datasetCommand) error { return nil })
		require.NoError(t, datasetCommandAction(nil, nil))
	})
}

func TestCov100fDatasetsCommandAction(t *testing.T) {
	t.Run("init fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetsInitProject, func(*datasetsCommand) error {
			return errors.New("no project")
		})
		require.Error(t, datasetsCommandAction(nil, nil))
	})
	t.Run("load fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetsInitProject, func(v *datasetsCommand) error {
			v.store = cov100fStore{proj: cov100fRS{defsErr: errors.New("load boom")}}
			return nil
		})
		err := datasetsCommandAction(nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load boom")
	})
	t.Run("lists", func(t *testing.T) {
		cov100fSetVar(t, &datasetsInitProject, func(v *datasetsCommand) error {
			v.store = cov100fStore{proj: cov100fRS{defs: []*datatug.RecordsetDefinition{
				{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "rs-a"}}},
				{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "rs-b"}}},
			}}}
			return nil
		})
		require.NoError(t, datasetsCommandAction(nil, nil))
	})
}

func TestCov100fDatasetDefCommandAction(t *testing.T) {
	t.Run("init fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetDefInitProject, func(*datasetDefCommand) error {
			return errors.New("no project")
		})
		require.Error(t, datasetDefCommandAction(nil, nil))
	})
	t.Run("load fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetDefInitProject, func(v *datasetDefCommand) error {
			v.Dataset = "missing"
			v.store = cov100fStore{proj: cov100fRS{defErr: errors.New("missing def")}}
			return nil
		})
		require.Error(t, datasetDefCommandAction(nil, nil))
	})
	t.Run("yaml ok", func(t *testing.T) {
		cov100fSetVar(t, &datasetDefInitProject, func(v *datasetDefCommand) error {
			v.Dataset = "customers"
			v.store = cov100fStore{proj: cov100fRS{def: &datatug.RecordsetDefinition{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "customers"}},
				Columns:     datatug.RecordsetColumnDefs{{Name: "id", Type: "string"}},
			}}}
			return nil
		})
		require.NoError(t, datasetDefCommandAction(nil, nil))
	})
}

func TestCov100fDatasetDataCommandAction(t *testing.T) {
	sample := datatug.Recordset{
		Columns: []datatug.RecordsetColumn{{Name: "id"}, {Name: "name"}},
		Rows:    [][]any{{1, "a"}, {2, "b"}},
	}
	t.Run("init fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetDataInitProject, func(*datasetDataCommand) error {
			return errors.New("no project")
		})
		require.Error(t, datasetDataCommandAction(nil, nil))
	})
	t.Run("load fails", func(t *testing.T) {
		cov100fSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
			v.File = "x"
			v.store = cov100fStore{proj: cov100fRS{dataErr: errors.New("no data")}}
			return nil
		})
		require.Error(t, datasetDataCommandAction(nil, nil))
	})
	t.Run("yaml default", func(t *testing.T) {
		cov100fSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
			v.File = "x"
			v.store = cov100fStore{proj: cov100fRS{data: sample}}
			return nil
		})
		require.NoError(t, datasetDataCommandAction(nil, nil))
	})
	t.Run("yaml indent digit and tab", func(t *testing.T) {
		for _, indent := range []string{"2", "TAB"} {
			cov100fSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
				v.File, v.Format, v.Indent = "x", "yaml", indent
				v.store = cov100fStore{proj: cov100fRS{data: sample}}
				return nil
			})
			require.NoError(t, datasetDataCommandAction(nil, nil), indent)
		}
	})
	t.Run("json indent variants", func(t *testing.T) {
		for _, indent := range []string{"", "TAB", "2", "xx"} {
			cov100fSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
				v.File, v.Format, v.Indent = "x", "json", indent
				v.store = cov100fStore{proj: cov100fRS{data: sample}}
				return nil
			})
			require.NoError(t, datasetDataCommandAction(nil, nil), indent)
		}
	})
	t.Run("unknown format", func(t *testing.T) {
		cov100fSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
			v.File, v.Format = "x", "xml"
			v.store = cov100fStore{proj: cov100fRS{data: sample}}
			return nil
		})
		err := datasetDataCommandAction(nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown")
	})
}

func TestCov100fWriteRows(t *testing.T) {
	rs := datatug.Recordset{
		Columns: []datatug.RecordsetColumn{{Name: "a"}, {Name: "b"}},
		Rows:    [][]any{{1, "x"}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	require.NoError(t, writeRows(rs, enc))
	assert.Contains(t, buf.String(), `"a"`)
}

func TestCov100fProjectsCommand(t *testing.T) {
	home := cov100fHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, dtconfig.ConfigFileName), []byte(`
projects:
  - id: demo
    title: Demo
    url: github.com/acme/demo
`), 0o600))
	out, _, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(projectsCommandArgs()) }, "projects")
	require.NoError(t, err)
	_ = out // projectsCommandAction writes to os.Stdout, not cmd.Out

	_, _, err = cov100fRun(t, func(r *cobra.Command) { r.AddCommand(projectsCommandArgs()) }, "projects", "-o", "json")
	require.NoError(t, err)
}

func TestCov100fProjectsAdd(t *testing.T) {
	home := cov100fHome(t)
	dir := t.TempDir()
	_, _, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(projectsCommandArgs()) },
		"projects", "add", "--project", "MyProj", "--directory", dir)
	require.NoError(t, err)
	settings, err := dtconfig.GetSettings()
	require.NoError(t, err)
	require.NotNil(t, settings.GetProjectConfig("myproj"))

	// same path: no-op
	_, _, err = cov100fRun(t, func(r *cobra.Command) { r.AddCommand(projectsCommandArgs()) },
		"projects", "add", "--project", "MyProj", "--directory", dir)
	require.NoError(t, err)

	// different path: error
	_, _, err = cov100fRun(t, func(r *cobra.Command) { r.AddCommand(projectsCommandArgs()) },
		"projects", "add", "--project", "MyProj", "--directory", t.TempDir())
	require.Error(t, err)

	_ = home
}

func TestCov100fQueriesCommandActionPanics(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r)
		assert.Contains(t, r.(string), "not implemented")
	}()
	_ = queriesCommandAction(nil, nil)
}

func TestCov100fOpenDB(t *testing.T) {
	var err error
	out := covDCaptureStdout(t, func() {
		_, _, err = cov100fRun(t, func(r *cobra.Command) { r.AddCommand(dbCommand()) },
			"db", "sqlite:////tmp/demo.db")
	})
	require.NoError(t, err)
	// The display form of the argument as it was typed (dburl's own string drops a slash).
	assert.Equal(t, "Opening database at sqlite:////tmp/demo.db", out)
}

func TestCov100fRenderCommand(t *testing.T) {
	t.Run("init fails", func(t *testing.T) {
		cov100fSetVar(t, &renderInitProject, func(*renderCommand) error {
			return errors.New("no project")
		})
		require.Error(t, renderCommandAction(nil, nil))
	})
	t.Run("dir missing", func(t *testing.T) {
		cov100fSetVar(t, &renderInitProject, func(v *renderCommand) error {
			v.ProjectDir = filepath.Join(t.TempDir(), "missing")
			v.projectID = "p"
			return nil
		})
		err := renderCommandAction(nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
	t.Run("load and save", func(t *testing.T) {
		dir := t.TempDir()
		projectFile := filepath.Join(dir, "datatug-project.json")
		compact := `{"id":"render-p","access":"private","created":{"at":"2026-01-02T03:04:05Z"}}`
		require.NoError(t, os.WriteFile(projectFile, []byte(compact), 0o600))
		cov100fSetVar(t, &renderInitProject, func(v *renderCommand) error {
			v.ProjectDir = dir
			return nil
		})
		require.NoError(t, renderCommandAction(nil, nil))
		saved, err := os.ReadFile(projectFile)
		require.NoError(t, err)
		assert.NotEqual(t, compact, string(saved), "the project must be saved back")
		assert.Contains(t, string(saved), `"id": "render-p"`)
	})
}

func TestCov100fScanCommandAction(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"scan-p"}`), 0o600))
	cmd := scanCommandArgs()
	cmd.SetArgs([]string{"-d", dir, "--db", "chinook", "--env", "local", "-D", "sqlite3", "--path", filepath.Join(dir, "missing.db")})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err) // scan will fail on missing sqlite / schema update — still covers action
}

func TestCov100fDemoCommandActionAndDefaults(t *testing.T) {
	home := cov100fHome(t)
	origClone, origDownload, origServe := cloneOrUpdateRepo, downloadSQLiteSource, serveDemoProjectFunc
	t.Cleanup(func() {
		cloneOrUpdateRepo, downloadSQLiteSource, serveDemoProjectFunc = origClone, origDownload, origServe
	})
	sqlitePath := filepath.Join(home, "datatug", "dbs", "chinook-local.sqlite")
	cloneOrUpdateRepo = func(dir, url string) error {
		catalogFile := filepath.Join(dir, demoProjectFolder, "servers/db/sqlite3/localhost/catalogs/chinook-local/chinook-local.db.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(catalogFile), 0o755))
		return os.WriteFile(catalogFile, []byte(`{"driver":"sqlite3","path":"`+sqlitePath+`"}`), 0o600)
	}
	downloadSQLiteSource = func(dests ...string) error {
		for _, d := range dests {
			writeFixtureSQLiteFile(t, d)
		}
		return nil
	}
	serveDemoProjectFunc = func(string) error { return nil }

	cmd := demoCommandArgs()
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	t.Run("defaultServeDemoProject via seams", func(t *testing.T) {
		var sets []string
		cov100fSetVar(t, &demoSetFlag, func(cmd *cobra.Command, name, value string) error {
			sets = append(sets, name+"="+value)
			return cmd.Flags().Set(name, value)
		})
		cov100fSetVar(t, &demoServeAction, func(*cobra.Command, []string) error { return nil })
		require.NoError(t, defaultServeDemoProject("/tmp/demo"))
		assert.Contains(t, strings.Join(sets, ","), serveProjectFlag)
	})
	t.Run("defaultServeDemoProject setflag fail", func(t *testing.T) {
		cov100fSetVar(t, &demoSetFlag, func(*cobra.Command, string, string) error {
			return errors.New("set boom")
		})
		require.Error(t, defaultServeDemoProject("/tmp/demo"))
	})

	t.Run("defaultDownloadSQLiteSource httptest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("sqlite-bytes"))
		}))
		t.Cleanup(srv.Close)
		cov100fSetVar(t, &demoSQLiteSourceURL, srv.URL)
		dest := filepath.Join(t.TempDir(), "a.sqlite")
		require.NoError(t, defaultDownloadSQLiteSource(dest))
		data, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, "sqlite-bytes", string(data))
	})

	t.Run("defaultCloneOrUpdateRepo local", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "repo")
		// Create a bare-enough local git repo as clone source via PlainInit + worktree isn't needed —
		// defaultCloneOrUpdateRepo needs a real URL. Exercise the "dir does not exist" MkdirAll
		// failure by pointing at an unwritable parent instead when possible; otherwise cover
		// "stat existing non-repo" via planting a file at dir.
		require.NoError(t, os.WriteFile(dir, []byte("not-a-dir-yet"), 0o600))
		// dir exists as a file → PlainOpen fails
		err := defaultCloneOrUpdateRepo(dir, "file:///nope")
		require.Error(t, err)
	})
}

func TestCov100fConsoleCommandArgsAndSetenvFail(t *testing.T) {
	cmd := consoleCommandArgs()
	require.Equal(t, "console", cmd.Name())
	cov100fSetVar(t, &consoleSetenv, func(string, string) error { return errors.New("setenv boom") })
	v := &consoleCommand{}
	require.Error(t, v.Execute(nil))
}

func TestCov100fInitProjectCommandOptions(t *testing.T) {
	v := &projectBaseCommand{}
	require.Error(t, v.initProjectCommand(projectCommandOptions{projNameRequired: true}))
	require.Error(t, v.initProjectCommand(projectCommandOptions{projDirRequired: true}))
	require.Error(t, v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}))

	home := cov100fHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, dtconfig.ConfigFileName), []byte(`
projects:
  - id: known
    path: /tmp/known
`), 0o600))
	v = &projectBaseCommand{ProjectName: "known"}
	require.NoError(t, v.initProjectCommand(projectCommandOptions{}))
	assert.Equal(t, "/tmp/known", v.ProjectDir)

	v = &projectBaseCommand{ProjectName: "missing"}
	require.ErrorIs(t, v.initProjectCommand(projectCommandOptions{}), ErrUnknownProjectName)

	cov100fSetVar(t, &newProjectsStore, func(string, map[string]string) (storage.Store, error) {
		return nil, errors.New("store boom")
	})
	v = &projectBaseCommand{ProjectName: "known"}
	require.Error(t, v.initProjectCommand(projectCommandOptions{}))
}
