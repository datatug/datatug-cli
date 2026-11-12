package dtproject

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"testing"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
)

func TestSettingsOrNone(t *testing.T) {
	registered := dtconfig.Settings{Projects: []*dtconfig.ProjectRef{{ID: "alpha"}}}
	broken := errors.New("yaml: line 3: mapping values are not allowed")
	for name, tc := range map[string]struct {
		settings dtconfig.Settings
		err      error
		want     int
		wantErr  error
	}{
		"registered projects":     {settings: registered, want: 1},
		"no settings file":        {err: fmt.Errorf("open ~/.datatug.yaml: %w", fs.ErrNotExist)},
		"empty settings file":     {err: io.EOF},
		"unreadable settings":     {err: broken, wantErr: broken},
		"settings beside failure": {settings: registered, err: broken, want: 1, wantErr: broken},
	} {
		t.Run(name, func(t *testing.T) {
			stub(t, &getSettings, func() (dtconfig.Settings, error) { return tc.settings, tc.err })
			settings, err := settingsOrNone()
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(settings.Projects) != tc.want {
				t.Fatalf("projects = %d, want %d", len(settings.Projects), tc.want)
			}
		})
	}
}

// A first run has no settings file. The Projects page must still offer the
// actions that create or add a project instead of reporting a load error.
func TestProjectsFirstRunOffersCreateAndAdd(t *testing.T) {
	for name, err := range map[string]error{
		"no settings file":    fmt.Errorf("open ~/.datatug.yaml: %w", fs.ErrNotExist),
		"empty settings file": io.EOF,
	} {
		t.Run(name, func(t *testing.T) {
			stub(t, &getSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, err })
			stub(t, &readSettings, settingsOrNone)
			h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
			h.RequireContains("Create new local project").RequireContains("Add existing").RequireContains("Demo Project 1")
			h.RequireNotContains("load projects")
		})
	}
}
