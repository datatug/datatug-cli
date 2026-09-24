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
)

var exportFormats = []string{"xlsx", "csv", "json", "yaml", "ingr", "dbf", "sqlite"}

// exportDialog only collects options. exportCommand owns validation and writing.
type exportDialog struct {
	scope  int
	format int
	focus  int // scope, format, directory, filename, browse, export
	dir    textinput.Model
	name   textinput.Model
	picker *filepicker.Model
	err    string
}

func (u *UI) openExportDialog() {
	if u.exporting {
		return
	}
	directory, err := os.Getwd()
	if err != nil {
		directory = "."
	}
	d := &exportDialog{dir: textinput.New(), name: textinput.New()}
	d.dir.Prompt, d.name.Prompt = "", ""
	d.dir.SetValue(directory)
	name := "recordset"
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		name = u.entries[u.activeGrid].grid.Title()
	} else if len(u.snapshot.Workspace.ExportBucket) > 0 {
		d.scope = 1
		name = "bucket"
	}
	d.name.SetValue(exportFileStem(name))
	u.exportDialog = d
}

func exportFileStem(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == '.' || r < 32 {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), " .-")
	if name == "" || name == "." || name == ".." {
		return "recordset"
	}
	return name
}

func (u *UI) updateExportDialog(msg tea.Msg) tea.Cmd {
	d := u.exportDialog
	if d == nil {
		return nil
	}
	if d.picker != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			switch key.String() {
			case "ctrl+c":
				return tea.Quit
			case "esc":
				d.picker = nil
				return nil
			case "space": // select the directory currently being viewed
				d.dir.SetValue(d.picker.CurrentDirectory)
				d.picker = nil
				return nil
			}
		}
		picker, cmd := d.picker.Update(msg)
		d.picker = &picker
		if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" && picker.Path != "" {
			if info, err := os.Stat(picker.Path); err == nil && info.IsDir() {
				d.dir.SetValue(picker.Path)
				d.picker = nil
				return nil
			}
		}
		return cmd
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	d.err = ""
	switch key.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		u.exportDialog = nil
		return nil
	case "tab", "shift+tab":
		d.dir.Blur()
		d.name.Blur()
		step := 1
		if key.String() == "shift+tab" {
			step = -1
		}
		d.focus = (d.focus + step + 6) % 6
		d.focusInput()
		return nil
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
			return nil
		}
	case "enter":
		switch d.focus {
		case 0, 1, 2, 3:
			d.dir.Blur()
			d.name.Blur()
			d.focus++
			d.focusInput()
			return nil
		case 4:
			if info, err := os.Stat(d.dir.Value()); err != nil || !info.IsDir() {
				d.err = "Choose an existing directory."
				return nil
			}
			picker := filepicker.New()
			picker.CurrentDirectory = d.dir.Value()
			picker.DirAllowed = true
			picker.FileAllowed = false
			picker.SetHeight(max(3, min(12, u.height-10)))
			d.picker = &picker
			return picker.Init()
		case 5:
			scope := "current"
			if d.scope == 1 {
				scope = "bucket"
			}
			if strings.TrimSpace(d.name.Value()) == "" {
				d.err = "Enter a file name."
				return nil
			}
			if d.name.Value() == "." || d.name.Value() == ".." || strings.ContainsAny(d.name.Value(), "/\\") || filepath.Base(d.name.Value()) != d.name.Value() {
				d.err = "File name must not contain a directory."
				return nil
			}
			if strings.TrimSpace(d.dir.Value()) == "" {
				d.err = "Choose a directory."
				return nil
			}
			if info, err := os.Stat(d.dir.Value()); err != nil || !info.IsDir() {
				d.err = "Choose an existing directory."
				return nil
			}
			path := filepath.Join(d.dir.Value(), d.name.Value())
			cmd, err := u.exportCommand(fmt.Sprintf("%s %s %s", scope, exportFormats[d.format], path))
			if err != nil {
				d.err = conciseError(err)
				return nil
			}
			u.exportDialog = nil
			return cmd
		}
	}
	switch d.focus {
	case 2:
		model, cmd := d.dir.Update(msg)
		d.dir = model
		return cmd
	case 3:
		model, cmd := d.name.Update(msg)
		d.name = model
		return cmd
	}
	return nil
}

func (d *exportDialog) focusInput() {
	switch d.focus {
	case 2:
		d.dir.Focus()
	case 3:
		d.name.Focus()
	}
}

func (u *UI) exportOverlay(background string) string {
	d := u.exportDialog
	if d == nil {
		return background
	}
	width := max(30, min(u.width-4, 76))
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
	box := lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}
