package chat

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

func TestPanelPolishExplorerSelectionDoesNotMoveLabels(t *testing.T) {
	u, _ := newTestChatUI(t, nil)
	u.catalog = workspaceTestCatalog()
	p := u.explorer
	p.explorerIndex = 0
	before := strings.Split(ansi.Strip(p.explorerNodesView(40, 30)), "\n")
	p.explorerIndex = 1
	after := strings.Split(ansi.Strip(p.explorerNodesView(40, 30)), "\n")
	for i := range before {
		if strings.TrimRight(before[i], " ") != strings.TrimRight(after[i], " ") {
			t.Fatalf("selection changed row %d coordinates: %q => %q", i, before[i], after[i])
		}
	}
}

func TestPanelPolishTablesInspectExactFullGroupAndReplaceResult(t *testing.T) {
	u := focusedGridTestUI(t)
	executor := &fakeExecutor{}
	u.sessions.ConfigureQueryExecutor(executor)
	u.catalog = ProjectCatalog{ID: "catalog", Title: "Catalog", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "source", SourceID: "first", Title: "First"}},
		{Reference: ContextReference{Kind: "source", SourceID: "other", Title: "Other"}},
		{Reference: ContextReference{Kind: "table", SourceID: "other", ObjectID: "hidden", Title: "OtherSourceOnly"}},
	}}
	for i := 0; i < 30; i++ {
		u.catalog.Objects = append(u.catalog.Objects, ProjectObject{Reference: ContextReference{Kind: "table", SourceID: "first", ObjectID: fmt.Sprint(i), Title: fmt.Sprintf("Table%02d", i)}})
	}
	u.shell.FocusLeftPanel()
	p := u.explorer
	p.explorerCollapsed["group:first:table"] = true
	p.explorerSearch = "Table00"
	for i, node := range p.explorerNodes() {
		if node.id == "group:first:table" {
			p.explorerIndex = i
		}
	}
	p.inspectExplorerSelection()
	view := ansi.Strip(u.workspace.View(32, 18, false))
	if !strings.Contains(view, "Table00") || strings.Contains(view, "OtherSourceOnly") || strings.Contains(view, "Row 1") || strings.Contains(view, "Column  Result") || strings.Contains(view, "Browse its objects") {
		t.Fatalf("wrong group inspection:\n%s", view)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	view = ansi.Strip(u.workspace.View(32, 18, true))
	if !strings.Contains(view, "Table29") {
		t.Fatalf("last admitted table is unreachable:\n%s", view)
	}
	// Selecting one object replaces the group and resets its scroll position.
	for i, node := range p.explorerNodes() {
		if node.label == "Table00" {
			p.explorerIndex = i
		}
	}
	p.inspectExplorerSelection()
	view = ansi.Strip(u.workspace.View(32, 18, false))
	if !strings.Contains(view, "Table: Table00") || strings.Contains(view, "Table29") || u.workspace.inspectorOffset != 0 {
		t.Fatalf("object did not replace group:\n%s", view)
	}
	u.shell.FocusEntry(u.lastGridEntryID)
	view = ansi.Strip(u.workspace.View(32, 18, false))
	if !strings.Contains(view, "Row 1 of 2") || strings.Contains(view, "Table: Table00") {
		t.Fatalf("result inspection did not return:\n%s", view)
	}
	if executor.calls != 0 {
		t.Fatalf("inspection executed %d queries", executor.calls)
	}
	u.shell.FocusLeftPanel()
	p.explorerSearch = "no-such-table"
	p.inspectExplorerSelection()
	view = ansi.Strip(u.workspace.View(32, 18, false))
	if !strings.Contains(view, "No matching objects") || strings.Contains(view, "Row 1") {
		t.Fatalf("empty selection left retained result inspection:\n%s", view)
	}
}

func TestPanelPolishExplorerSearchAndFocusedHints(t *testing.T) {
	u, _ := newTestChatUI(t, nil)
	u.catalog = workspaceTestCatalog()
	p := u.explorer
	p.updateKey(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if !p.explorerSearchFocused || p.explorerSearch != "" {
		t.Fatalf("Ctrl+F did not focus Explorer search: %+v", p)
	}
	p.updateKey(tea.KeyPressMsg{Code: 'å', Text: "å"})
	if p.explorerSearch != "å" {
		t.Fatal("Option-produced literal text was lost")
	}
	focused := ansi.Strip(p.View(34, 14, true))
	unfocused := ansi.Strip(p.View(34, 14, false))
	if !strings.Contains(focused, "Ctrl+F") || !strings.Contains(focused, "Enter open") || strings.Contains(unfocused, "Enter open") {
		t.Fatalf("panel footer focus mismatch:\nfocused:\n%s\nunfocused:\n%s", focused, unfocused)
	}
}

func TestPanelPolishHelpActionsMouseFollowsWrappedAndScrolledText(t *testing.T) {
	u, _ := newTestChatUI(t, nil)
	o := u.newHotkeysOverlay()
	for _, width := range []int{80, 24, 12} {
		for _, offset := range []int{0, 1, 3} {
			o.offset = offset
			view := ansi.Strip(o.View(width, 35))
			for y, row := range strings.Split(view, "\n") {
				if ansi.StringWidth(row) > width {
					t.Fatalf("help overflow width %d: %q", width, row)
				}
				i := strings.Index(row, "Actions")
				if i < 0 {
					continue
				}
				_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth(row[:i]), Y: y})
				if !done || cmd == nil {
					t.Fatalf("visible Actions click missed at %dx%d offset %d: %q", ansi.StringWidth(row[:i]), y, offset, row)
				}
				break
			}
		}
	}
}

func TestPanelPolishBorderlessHelpActionsAndModalReturnAtTerminalSizes(t *testing.T) {
	for _, size := range [][2]int{{140, 42}, {100, 30}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			u, _ := newTestChatUI(t, nil)
			u.shell.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			u.shell.SetComposerText("keep outline draft")
			caret := u.shell.ComposerCursor()
			u.shell.PushOverlay(&workspaceRoutingDialog{})
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF1})
			raw := u.shell.View().Content
			view := ansi.Strip(raw)
			if !strings.Contains(view, "Keyboard help") {
				t.Fatalf("F1 content missing:\n%s", view)
			}
			if strings.ContainsAny(view, "╭╮╰╯┌┐└┘│─▌▎▔▁▏") || strings.Contains(view, "Border:") || strings.Contains(view, "Ctrl+B") {
				t.Fatalf("borderless F1 retains a border/marker or abandoned style control:\n%s", view)
			}
			for y, row := range strings.Split(view, "\n") {
				if at := strings.Index(row, "Keyboard help"); at >= 0 && (ansi.StringWidth(row[:at]) < 5 || y < 3) {
					t.Fatalf("F1 lacks outer margin plus content padding at %dx%d: %q", size[0], size[1], row)
				}
			}
			// Border glyphs alone do not establish an active marker. Only a
			// marker painted with the focus foreground violates quiet Help.
			r, g, b, _ := theme.FocusColor().RGBA()
			focusForeground := fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
			for _, row := range strings.Split(raw, "\n") {
				for at, cell := range row {
					if (cell == '▌' || cell == '▎') && strings.Contains(lastAnsiEscape(row[:at]), focusForeground) {
						t.Fatalf("F1 has a focus-blue active marker:\n%s", view)
					}
				}
			}
			for _, row := range strings.Split(view, "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatalf("borderless Help exceeds terminal width: %q", row)
				}
			}
			if len(strings.Split(view, "\n")) > size[1] {
				t.Fatal("borderless Help exceeds terminal height")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
			if strings.Contains(ansi.Strip(u.shell.View().Content), "Keyboard help") {
				t.Fatal("borderless Help did not scroll")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
			if !strings.Contains(ansi.Strip(u.shell.View().Content), "Keyboard help") {
				t.Fatal("borderless Help did not restore its scrolled content")
			}
			// Actual shell coordinates must reach the visible Actions label.
			drainCmd(t, u, clickWorkspaceText(t, u, "Actions"))
			if strings.Contains(ansi.Strip(u.shell.View().Content), "Keyboard help") {
				t.Fatal("Actions click missed the borderless content origin")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			if !strings.Contains(ansi.Strip(u.shell.View().Content), "Routing dialog") {
				t.Fatal("Actions did not return to the original modal")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			if u.shell.WorkspaceFocus() != "input" || u.shell.ComposerText() != "keep outline draft" || u.shell.ComposerCursor() != caret {
				t.Fatal("borderless Help flow lost prior composer focus, draft or caret")
			}
		})
	}
}

func TestPanelPolishHelpScrollbarPositionsAndMouseOwnership(t *testing.T) {
	for _, size := range [][2]int{{140, 42}, {100, 30}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			u, _ := newTestChatUI(t, nil)
			u.shell.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			u.shell.SetComposerText("keep scroll draft")
			caret := u.shell.ComposerCursor()
			o := u.newHotkeysOverlay()
			u.shell.PushOverlay(o)
			w, h := min(102, size[0]-8), size[1]-8
			first := strings.Split(ansi.Strip(o.View(w, h)), "\n")
			if !o.scrollbar.visible || len(first) != h || !strings.HasSuffix(first[0], "▐") || o.scrollbar.thumbSize < 1 || o.scrollbar.thumbSize >= h {
				t.Fatalf("first-page scrollbar invalid: %+v", o.scrollbar)
			}
			o.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
			o.View(w, h)
			if o.scrollbar.thumbStart == 0 || o.scrollbar.thumbStart+o.scrollbar.thumbSize >= h {
				t.Fatalf("middle thumb does not show content on both sides: %+v", o.scrollbar)
			}
			o.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
			last := strings.Split(ansi.Strip(o.View(w, h)), "\n")
			if !strings.HasSuffix(last[h-1], "▐") || !strings.Contains(strings.Join(last, " "), "retained results") {
				t.Fatal("last Help content/thumb is unreachable")
			}
			end := o.offset
			o.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			o.View(w, h)
			if o.offset != end {
				t.Fatal("scroll escaped the final viewport")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyHome})
			u.shell.View()
			if o.offset != 0 {
				t.Fatal("Home did not return Help to the first viewport")
			}
			u.shell.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
			u.shell.View()
			if o.offset == 0 || u.shell.ComposerText() != "keep scroll draft" || u.shell.ComposerCursor() != caret || u.shell.WorkspaceFocus() != "input" || len(u.snapshot.Messages) != 0 {
				t.Fatal("wheel did not scroll Help exclusively")
			}
			u.shell.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 1, Y: 1})
			if o.offset != 0 {
				t.Fatal("wheel did not return to the first viewport")
			}
			// The final content cell is now the outer right card cell: there
			// must be no trailing padding or app-background gutter after it.
			cardWidth := min(104, size[0]-6)
			rightEdge := (size[0]-cardWidth)/2 + cardWidth - 1
			bg, _ := theme.NeutralSurfaceColors(true)
			r, g, b, _ := bg.RGBA()
			cardBackground := fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
			barRows := 0
			for _, rawRow := range strings.Split(u.shell.View().Content, "\n") {
				at := strings.LastIndexAny(rawRow, "▕▐")
				if at < 0 {
					continue
				}
				barRows++
				if ansi.StringWidth(rawRow[:at]) != rightEdge || !strings.Contains(lastAnsiEscape(rawRow[:at]), cardBackground) {
					t.Fatalf("scrollbar is inset or lacks the card background at terminal width %d: %q", size[0], rawRow)
				}
			}
			if barRows != h {
				t.Fatalf("visible edge scrollbar has %d rows; want %d", barRows, h)
			}
			// Track paging uses actual translated shell coordinates; the gap
			// beside it is inert and must never execute an Actions hitbox.
			x, y := -1, -1
			for at, row := range strings.Split(ansi.Strip(u.shell.View().Content), "\n") {
				if i := strings.LastIndex(row, "▕"); i >= 0 {
					x, y = ansi.StringWidth(row[:i]), at
				}
			}
			if x < 0 {
				t.Fatal("visible track unavailable to click")
			}
			_, cmd := u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			if cmd != nil || o.offset == 0 {
				t.Fatal("track click did not page Help")
			}
			u.shell.View()
			before := o.offset
			_, cmd = u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x - 1, Y: y})
			if cmd != nil || o.offset != before {
				t.Fatal("scrollbar gap executed an action")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyHome})
			u.shell.View()
			drainCmd(t, u, clickWorkspaceText(t, u, "Actions"))
			if strings.Contains(ansi.Strip(u.shell.View().Content), "Keyboard help") {
				t.Fatal("scrollbar changed visible Actions coordinates")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			if u.shell.ComposerText() != "keep scroll draft" || u.shell.ComposerCursor() != caret {
				t.Fatal("scrollbar flow changed the composer draft/caret")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyHome})
			if u.shell.ComposerCursor() != 0 {
				t.Fatal("Home outside Help lost normal composer navigation")
			}
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
			if u.shell.ComposerCursor() != len([]rune("keep scroll draft")) || u.shell.ComposerText() != "keep scroll draft" {
				t.Fatal("End outside Help lost normal composer navigation")
			}
		})
	}
	u, _ := newTestChatUI(t, nil)
	o := u.newHotkeysOverlay()
	o.View(80, 500)
	if o.scrollbar.visible {
		t.Fatal("Help without overflow displays a scrollbar")
	}
	for _, size := range [][2]int{{3, 1}, {2, 2}, {1, 1}} {
		for _, row := range strings.Split(o.View(size[0], size[1]), "\n") {
			if ansi.StringWidth(row) > size[0] {
				t.Fatal("tiny scrollbar viewport overflows")
			}
		}
	}
}

func TestPanelPolishTabFocusHintsPreserveDraft(t *testing.T) {
	u, _ := newTestChatUI(t, nil)
	u.shell.SetComposerText("keep draft")
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF11})
	if u.shell.WorkspaceFocus() != "session-tabs" {
		t.Fatal("F11 did not focus chat tabs")
	}
	for _, width := range []int{80, 140} {
		status := u.statusBar(width)
		for _, want := range []string{"choose chat", "Enter activate", "Esc return", "Ctrl+Tab next"} {
			if !strings.Contains(status, want) {
				t.Fatalf("missing tabs control %q: %s", want, status)
			}
		}
		if strings.Contains(status, "Enter send") || strings.Contains(status, "model:") || strings.Contains(status, "F1 Help") {
			t.Fatalf("tabs show another control's hints: %s", status)
		}
		if strings.Contains(status, "or + New") || !strings.Contains(status, "Ctrl+P Actions / New chat") || !containsWorkspaceAction(u.workspaceActions(), "new-chat") {
			t.Fatalf("tabs promise an unreachable target or hide New chat: %s", status)
		}
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if u.shell.ComposerText() != "keep draft" || u.shell.WorkspaceFocus() != "input" {
		t.Fatal("leaving tabs lost composer ownership/draft")
	}
}

func TestPanelPolishInlineKeysRetainDistinctStyleAndEditorOwnership(t *testing.T) {
	u, _ := newTestChatUI(t, nil)
	u.catalog = workspaceTestCatalog()
	u.explorer.explorerSearch = "missing-table"
	rows := strings.Split(u.explorer.explorerNodesView(60, 8), "\n")
	assertSeparatedHint := func(row, key, label string) {
		t.Helper()
		keyAt, labelAt := strings.Index(row, key), strings.Index(row, label)
		if keyAt < 0 || labelAt <= keyAt {
			t.Fatalf("key and label missing or reordered: %q", ansi.Strip(row))
		}
		keyStyle, labelStyle := lastAnsiEscape(row[:keyAt]), lastAnsiEscape(row[:labelAt])
		if !strings.Contains(keyStyle, "38;") || keyStyle == labelStyle {
			t.Fatalf("key lacks distinct explicit foreground before normal label: key=%q label=%q row=%q", keyStyle, labelStyle, row)
		}
	}
	assertSeparatedHint(rows[len(rows)-1], "Esc", " clears the filter.")
	// Styling must not change Esc's search ownership or the retained draft.
	u.shell.SetComposerText("keep draft")
	u.explorer.updateKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if u.explorer.explorerSearch != "" || u.shell.ComposerText() != "keep draft" {
		t.Fatal("empty-search recovery changed input ownership")
	}
	drainCmd(t, u, u.openWorkspaceQuery(u.catalog.Objects[2], false))
	p := u.queryEditor
	if p == nil {
		t.Fatal("query editor unavailable")
	}
	view := strings.Split(p.View(80, 15, true), "\n")
	footer := view[len(view)-1]
	assertSeparatedHint(footer, "Tab", " controls")
	assertSeparatedHint(footer, "Enter", " newline/run action")
	assertSeparatedHint(footer, "Esc", " back")
	p.selection = "SELECT"
	view = strings.Split(p.View(80, 15, true), "\n")
	assertSeparatedHint(view[len(view)-1], "Tab", " → Copy selected")
	// Tab still selects the Run control; decorative ANSI adds no focus stop.
	p.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if p.focus != 1 || u.shell.ComposerText() != "keep draft" {
		t.Fatal("inline styling changed editor focus or composer draft")
	}
}

func TestPanelPolishHelpColumnsAndNarrowWrap(t *testing.T) {
	rows := []workspaceHelpRow{{"Tab/Shift+Tab", "switch result views without executing a query"}, {"F12 then A", "archive directly"}}
	for _, width := range []int{2, 12, 32, 80} {
		for _, row := range rows {
			lines := renderWorkspaceHelpRow(row, width)
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatalf("help row overflows %d: %q", width, ansi.Strip(line))
				}
			}
			if width == 80 && strings.Index(ansi.Strip(lines[0]), strings.Fields(row.description)[0]) != 19 {
				t.Fatalf("descriptions not aligned: %q", ansi.Strip(lines[0]))
			}
		}
	}
}
