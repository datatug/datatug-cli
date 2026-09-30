package commands

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/dtstate"
)

// stubUI replaces the state file and the terminal program and returns what the
// app was started with.
func stubUI(t *testing.T, state *dtstate.DatatugState, stateErr error) (modules *[]datatugui.Module, opts *datatugui.Options) {
	t.Helper()
	modules, opts = new([]datatugui.Module), new(datatugui.Options)
	origState, origRun := getDatatugState, runApp
	getDatatugState = func() (*dtstate.DatatugState, error) { return state, stateErr }
	runApp = func(m []datatugui.Module, o datatugui.Options, _ ...tea.ProgramOption) error {
		*modules, *opts = m, o
		return nil
	}
	t.Cleanup(func() { getDatatugState, runApp = origState, origRun })
	return modules, opts
}

func TestUIModulesInMenuOrder(t *testing.T) {
	var ids []string
	for _, m := range uiModules() {
		ids = append(ids, m.ID)
	}
	want := []string{datatugui.ScreenProjects, datatugui.ScreenViewers, datatugui.ScreenSettings, datatugui.ScreenAPIMonitor}
	if !slices.Equal(ids, want) {
		t.Errorf("modules = %v, want %v", ids, want)
	}
}

func TestRunUIStartsOnTheSavedScreen(t *testing.T) {
	_, opts := stubUI(t, &dtstate.DatatugState{CurrentScreenPath: "settings/yaml"}, nil)
	if err := runUI(""); err != nil {
		t.Fatal(err)
	}
	if opts.Start != datatugui.ScreenSettings || opts.Initial != nil {
		t.Errorf("options = %+v", *opts)
	}
}

func TestRunUIFallsBackToProjectsWhenStateIsUnreadable(t *testing.T) {
	_, opts := stubUI(t, nil, errors.New("corrupt state"))
	if err := runUI(""); err != nil {
		t.Fatal(err)
	}
	if opts.Start != datatugui.ScreenProjects {
		t.Errorf("start = %q", opts.Start)
	}
}

func TestRunUIRejectsAFileThatIsNotSQLite(t *testing.T) {
	stubUI(t, &dtstate.DatatugState{}, nil)
	plain := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(plain, []byte("just text, not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runUI(plain); err == nil || err.Error() != "not a SQLite file" {
		t.Errorf("err = %v", err)
	}
}

func TestRunUIOpensASQLiteFileOnTopOfTheStartScreen(t *testing.T) {
	_, opts := stubUI(t, &dtstate.DatatugState{}, nil)
	db := filepath.Join(t.TempDir(), "sales.db")
	if err := os.WriteFile(db, append([]byte("SQLite format 3\x00"), make([]byte, 100)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runUI(db); err != nil {
		t.Fatal(err)
	}
	if opts.Initial == nil || opts.Initial.Content == nil {
		t.Fatalf("the database page was not passed: %+v", *opts)
	}
}

func TestUICommandPassesTheFileFlag(t *testing.T) {
	_, opts := stubUI(t, &dtstate.DatatugState{}, nil)
	cmd := uiCommandArgs()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--file", filepath.Join(t.TempDir(), "missing.db")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a missing file is not a SQLite file")
	}
	if opts.Start != "" {
		t.Errorf("the UI must not start when the file is rejected: %+v", *opts)
	}
}
