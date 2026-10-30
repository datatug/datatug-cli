package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

// exportDialogOverlay is ChatUI's chatshell.Overlay port of export_dialog.go's
// exportDialog (checklist item #47): scope/format/directory/filename fields,
// tab to move focus, a directory browser (bubbles/filepicker), Enter on
// "Export" runs ChatUI.exportCommand. Field-collection/validation logic is
// ported verbatim; chatshell centres the overlay itself, so this no longer
// composes its own canvas the way ui.go's exportOverlay(background) did.
type exportDialogOverlay struct {
	ui *ChatUI

	scope  int
	format int
	focus  int // scope, format, directory, filename, browse, export
	dir    textinput.Model
	name   textinput.Model
	picker *filepicker.Model
	err    string
}

// exportDialogGetwd is os.Getwd, as a seam: newExportDialogOverlay's
// getwd-fails fallback ("." as the starting directory) is exercised in
// tests by overriding this var, since os.Getwd itself cannot be made to
// fail portably.
var exportDialogGetwd = os.Getwd

// newExportDialogOverlay mirrors ui.go's openExportDialog.
func newExportDialogOverlay(ui *ChatUI) *exportDialogOverlay {
	directory, err := exportDialogGetwd()
	if err != nil {
		directory = "."
	}
	d := &exportDialogOverlay{ui: ui, dir: textinput.New(), name: textinput.New()}
	d.dir.Prompt, d.name.Prompt = "", ""
	d.dir.SetValue(directory)
	name := "recordset"
	if ui.lastGridRecordSetID != "" {
		if record, ok := ui.snapshot.RecordSets[ui.lastGridRecordSetID]; ok {
			name = record.Title
		}
	} else if len(ui.snapshot.Workspace.ExportBucket) > 0 {
		d.scope = 1
		name = "bucket"
	}
	d.name.SetValue(exportFileStem(name))
	return d
}

func (d *exportDialogOverlay) focusInput() {
	switch d.focus {
	case 2:
		d.dir.Focus()
	case 3:
		d.name.Focus()
	}
}

// Update satisfies chatshell.Overlay.
func (d *exportDialogOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if d.picker != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			switch key.String() {
			case "ctrl+c":
				return d, tea.Quit, false
			case "esc":
				d.picker = nil
				return d, nil, false
			case "space": // select the directory currently being viewed
				d.dir.SetValue(d.picker.CurrentDirectory)
				d.picker = nil
				return d, nil, false
			}
		}
		picker, cmd := d.picker.Update(msg)
		d.picker = &picker
		if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" && picker.Path != "" {
			if info, err := os.Stat(picker.Path); err == nil && info.IsDir() {
				d.dir.SetValue(picker.Path)
				d.picker = nil
				return d, nil, false
			}
		}
		return d, cmd, false
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return d, nil, false
	}
	d.err = ""
	switch key.String() {
	case "ctrl+c":
		return d, tea.Quit, false
	case "esc":
		return d, nil, true
	case "tab", "shift+tab":
		d.dir.Blur()
		d.name.Blur()
		step := 1
		if key.String() == "shift+tab" {
			step = -1
		}
		d.focus = (d.focus + step + 6) % 6
		d.focusInput()
		return d, nil, false
	case "left", "right":
		if d.focus < 2 {
			step := 1
			if key.String() == "left" {
				step = -1
			}
			if d.focus == 0 {
				d.scope = (d.scope + step + 2) % 2
			} else {
				d.format = (d.format + step + len(exportFormats)) % len(exportFormats)
			}
			return d, nil, false
		}
	case "enter":
		switch d.focus {
		case 0, 1, 2, 3:
			d.dir.Blur()
			d.name.Blur()
			d.focus++
			d.focusInput()
			return d, nil, false
		case 4:
			if info, err := os.Stat(d.dir.Value()); err != nil || !info.IsDir() {
				d.err = "Choose an existing directory."
				return d, nil, false
			}
			picker := filepicker.New()
			picker.CurrentDirectory = d.dir.Value()
			picker.DirAllowed = true
			picker.FileAllowed = false
			picker.SetHeight(8)
			d.picker = &picker
			return d, picker.Init(), false
		case 5:
			scope := "current"
			if d.scope == 1 {
				scope = "bucket"
			}
			if strings.TrimSpace(d.name.Value()) == "" {
				d.err = "Enter a file name."
				return d, nil, false
			}
			if d.name.Value() == "." || d.name.Value() == ".." || strings.ContainsAny(d.name.Value(), "/\\") || filepath.Base(d.name.Value()) != d.name.Value() {
				d.err = "File name must not contain a directory."
				return d, nil, false
			}
			if strings.TrimSpace(d.dir.Value()) == "" {
				d.err = "Choose a directory."
				return d, nil, false
			}
			if info, err := os.Stat(d.dir.Value()); err != nil || !info.IsDir() {
				d.err = "Choose an existing directory."
				return d, nil, false
			}
			path := filepath.Join(d.dir.Value(), d.name.Value())
			cmd, err := d.ui.exportCommand(fmt.Sprintf("%s %s %s", scope, exportFormats[d.format], path))
			if err != nil {
				d.err = conciseError(err)
				return d, nil, false
			}
			return d, cmd, true
		}
	}
	switch d.focus {
	case 2:
		model, cmd := d.dir.Update(msg)
		d.dir = model
		return d, cmd, false
	case 3:
		model, cmd := d.name.Update(msg)
		d.name = model
		return d, cmd, false
	}
	return d, nil, false
}

// View satisfies chatshell.Overlay — ported from ui.go's exportOverlay,
// minus its own canvas composition (chatshell.renderOverlay centres it).
func (d *exportDialogOverlay) View(width, height int) string {
	width = max(30, min(width, 76))
	inside := max(20, width-6)
	d.dir.SetWidth(inside - 12)
	d.name.SetWidth(inside - 12)
	label := func(index int, value string) string {
		if d.focus == index && d.picker == nil {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("› " + value)
		}
		return "  " + value
	}
	lines := []string{"Export RecordSet", "", label(0, "Scope: "+[]string{"Current RecordSet", "Bucket"}[d.scope]+"  ←/→"), label(1, "Format: "+exportFormats[d.format]+"  ←/→"), label(2, "Directory: "+d.dir.View()), label(3, "File name: "+d.name.View()), label(4, "Browse directories…"), label(5, "Export"), ""}
	if d.picker != nil {
		lines = append(lines, "Directory: "+d.picker.CurrentDirectory)
		for _, line := range strings.Split(strings.TrimRight(d.picker.View(), "\n"), "\n") {
			lines = append(lines, ansi.Truncate(line, inside, "…"))
		}
		lines = append(lines, "↑↓ move · → open · ← parent · Enter choose · Space current · Esc back")
	} else {
		lines = append(lines, "Tab next · Shift+Tab previous · Enter select · Esc cancel")
	}
	if d.err != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(ansi.Truncate(d.err, inside, "…")))
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, inside, "…")
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
}

var _ chatshell.Overlay = (*exportDialogOverlay)(nil)
