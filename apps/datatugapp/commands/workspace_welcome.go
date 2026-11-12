package commands

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

// The sample project of the welcome screen: the bundled music-store database
// alone, with its schema scanned and no access policy, so every table opens in
// the workspace at once. It is not the Perimeter demo of `datatug demo
// install`, whose policy admits an agent role to a few rows only.
const (
	sampleProjectID    = "datatug-sample"
	sampleProjectTitle = "Sample music store"
	sampleEnvironment  = "local"
	// sampleFirstTable is opened as soon as the sample is: a first result
	// without a key press. Its address columns show the sensitive-column rule.
	sampleFirstTable = "Invoice"
	sampleNotice     = "This is the sample music store, with its Invoice table open. Arrows move in the table; Shift+← goes to the Explorer, where Enter opens any other table; Ctrl+P lists everything you can do."
)

// Doors of the sample install, replaced in tests.
var (
	installSampleFunc     = installSample
	sampleRegistration    = demoRegistration
	sampleAddToSettings   = dtconfig.AddProjectToSettings
	sampleScan            = scanSample
	sampleFixtureFiles    = func() ([]demofixtures.File, error) { return demoInstallFiles() }
	sampleProjectsRootDir = dtroot.Path
	sampleRename          = os.Rename
)

// installSample makes sure the sample project is there and returns its ID: it
// writes the bundled database, scans it and registers the project. Each step
// that is already done is kept, and one that was damaged is done again: a
// database that is not the bundled one (a write that was cut short), a project
// that was never scanned, a folder that was deleted while still registered. It
// writes nothing to the terminal: it runs while the workspace is on screen.
func installSample() (string, error) {
	registered, err := sampleRegistration(sampleProjectID)
	if err != nil {
		return "", err
	}
	dir := registered
	if dir == "" {
		dir = filepath.Join(sampleProjectsRootDir(), sampleProjectID)
	}
	files, err := sampleFixtureFiles()
	if err != nil {
		return "", err
	}
	database := filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath))
	for _, f := range files {
		if f.Path != demofixtures.ChinookPath {
			continue
		}
		if current, readErr := os.ReadFile(database); readErr == nil && bytes.Equal(current, f.Content) {
			break
		}
		if err := demoInstallMkdirAll(filepath.Dir(database), 0o755); err != nil {
			return "", fmt.Errorf("cannot make the sample folder: %w", err)
		}
		// Written beside its place and renamed: a write that is cut short
		// leaves no half database for the next run to trip on.
		partial := database + ".partial"
		if err := demoInstallWriteFile(partial, f.Content, 0o644); err != nil {
			return "", fmt.Errorf("cannot write the sample database: %w", err)
		}
		if err := sampleRename(partial, database); err != nil {
			return "", fmt.Errorf("cannot write the sample database: %w", err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "dbmodels", demofixtures.ChinookSource)); statErr != nil {
		if err := sampleScan(dir, database); err != nil {
			return "", fmt.Errorf("cannot read the sample database: %w", err)
		}
	}
	if registered == "" {
		if err := sampleAddToSettings(dtconfig.ProjectRef{ID: sampleProjectID, Path: dir, Title: sampleProjectTitle}); err != nil {
			return "", fmt.Errorf("cannot register the sample project: %w", err)
		}
	}
	return sampleProjectID, nil
}

// scanSample runs the scan of `datatug scan` on the sample database with its
// progress lines discarded.
func scanSample(dir, database string) error {
	scan := scanCommandArgs()
	scan.SetOut(io.Discard)
	scan.SetErr(io.Discard)
	for name, value := range map[string]string{
		"directory": dir, "project": sampleProjectID, "driver": "sqlite3",
		"db": demofixtures.ChinookSource, "env": sampleEnvironment, "path": database,
	} {
		_ = scan.Flags().Set(name, value) // the flags are the scan command's own
	}
	previous := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previous)
	return scanCommandAction(scan, nil)
}

// sampleFailure says in one line why the sample is not there.
func sampleFailure(err error) string {
	return "The sample could not be prepared: " + strings.Join(strings.Fields(err.Error()), " ") + "."
}

// sampleReadyMsg ends an install started from the welcome screen.
type sampleReadyMsg struct {
	project string
	err     error
}

// welcome is the first screen of a machine with no project: two choices and
// nothing to learn first.
type welcome struct {
	index  int
	busy   bool
	failed string
	// choose opens a project for chat: the workspace closes and reopens on it.
	choose func(project string) tea.Cmd
}

var welcomeChoices = []struct{ title, detail string }{
	{"Explore the sample music store", "A small sample database: customers, invoices, tracks. It stays on this machine: no signup, no network."},
	{"Use my own database", "Create a project, or add one you already have."},
}

// update handles a message while the welcome screen is shown. done reports
// that the person left it for the Projects page.
func (w *welcome) update(msg tea.Msg) (cmd tea.Cmd, done bool) {
	switch msg := msg.(type) {
	case sampleReadyMsg:
		w.busy = false
		if msg.err != nil {
			w.failed = sampleFailure(msg.err) + " Press Enter to try again, or Esc for your own projects."
			return nil, false
		}
		return w.choose(msg.project), false
	case tea.KeyPressMsg:
		if w.busy {
			return nil, false
		}
		switch msg.String() {
		case "up", "k":
			w.index = max(0, w.index-1)
		case "down", "j", "tab":
			w.index = min(len(welcomeChoices)-1, w.index+1)
		case "esc":
			return nil, true
		case "enter":
			if w.index == 1 {
				return nil, true
			}
			w.busy, w.failed = true, ""
			return func() tea.Msg {
				project, err := installSampleFunc()
				return sampleReadyMsg{project: project, err: err}
			}, false
		}
	}
	return nil, false
}

func (w *welcome) view(width, height int) string {
	width = max(20, width)
	inner := max(16, min(width-4, 76))
	title := lipgloss.NewStyle().Bold(true).Foreground(theme.AccentColor())
	muted := lipgloss.NewStyle().Foreground(theme.MutedColor())
	chosen := lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor())
	lines := []string{"", title.Render("Welcome to DataTug")}
	for _, line := range strings.Split(ansi.Wrap("Browse your databases from the terminal: open tables, follow relationships, and ask questions in plain words once AI is set up.", inner, " "), "\n") {
		lines = append(lines, muted.Render(line))
	}
	lines = append(lines, "")
	for i, choice := range welcomeChoices {
		marker, style := "  ", lipgloss.NewStyle().Bold(true)
		if i == w.index {
			marker, style = "▸ ", chosen
		}
		lines = append(lines, style.Render(marker+choice.title))
		for _, detail := range strings.Split(ansi.Wrap(choice.detail, inner-4, " "), "\n") {
			lines = append(lines, muted.Render("    "+detail))
		}
		lines = append(lines, "")
	}
	switch {
	case w.busy:
		lines = append(lines, chosen.Render("Preparing the sample…"))
	case w.failed != "":
		lines = append(lines, strings.Split(ansi.Wrap(w.failed, inner, " "), "\n")...)
	default:
		lines = append(lines, theme.WorkspaceKeyHint("↑↓")+" choose   "+theme.WorkspaceKeyHint("Enter")+" select   "+theme.WorkspaceKeyHint("Esc")+" all projects")
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate("  "+line, width, "…")
	}
	if len(lines) > height && height > 0 {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
