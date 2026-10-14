package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func reviewJoinUI(t *testing.T) *ChatUI {
	t.Helper()
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Invoice", DTQL: "from: {schema: main, name: Invoice}\ncolumns: [{field: InvoiceId}, {field: CustomerId}]\nlimit: 5\n", Result: secureread.Result{Columns: []string{"InvoiceId", "CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1, "CustomerId": 1}}, {Data: map[string]any{"InvoiceId": 2, "CustomerId": 2}}}}})
	u.sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u.loadSession(u.snapshot)
	u.shell.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	u.shell.FocusEntry(u.lastGridEntryID)
	return u
}

func TestUnifiedWorkspaceDecodedLFNeverEntersInlineJoin(t *testing.T) {
	t.Parallel()
	u := reviewJoinUI(t)
	id := u.activeRecordSetID()
	events := make(chan uv.Event, 4)
	if err := uv.NewTerminalReader(strings.NewReader("\n"), "dumb").StreamEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		key, ok := event.(uv.KeyPressEvent)
		if !ok || key.Code != 'j' || key.Mod != uv.ModCtrl {
			t.Fatalf("actual LF decode=%#v", event)
		}
		u.shell.Update(tea.KeyPressMsg(key))
	default:
		t.Fatal("LF decoder emitted no event")
	}
	if u.joinBlocksByRecordSetID[id].JoinFocused() || u.workspaceMutationPending || u.shell.ActiveOverlayHints() != nil {
		t.Fatal("decoded LF entered a JOIN interaction")
	}
	u.shell.Update(uitest.Key("space"))
	if u.workspaceMutationPending || len(u.snapshot.RecordSets) != 1 {
		t.Fatal("Space after LF applied JOIN")
	}
	u.shell.Update(uitest.Key("esc"))
	u.shell.Update(uitest.Key("esc"))
	uxSearchAction(t, u, "JOIN relationship")
	if !strings.Contains(flattenView(u.shell.View().Content), "JOIN relationship") {
		t.Fatalf("explicit Actions JOIN chooser unavailable: %s", flattenView(u.shell.View().Content))
	}
}

type reviewDelayedJoin struct{ started, release chan struct{} }

func (a *reviewDelayedJoin) Candidates(context.Context, RecordSet) ([]JoinCandidate, error) {
	return []JoinCandidate{{ID: "join", Source: RelationInstance{Relation: "Invoice"}, Target: RelationInstance{Relation: "Customer"}}}, nil
}
func (a *reviewDelayedJoin) Apply(ctx context.Context, r RecordSet, _ JoinCandidateID) (QueryResult, error) {
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		return QueryResult{}, ctx.Err()
	}
	return QueryResult{Title: "Joined", DTQL: r.DTQL, Result: r.Result}, nil
}
func TestUnifiedWorkspaceDelayedJoinRetainsNewerContext(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"result", "row", "composer", "editor", "modal"} {
		t.Run(change, func(t *testing.T) {
			u := reviewJoinUI(t)
			_, origin, _ := u.activeGrid()
			originID := origin.ID
			second, err := u.sessions.store.AppendQuery(t.Context(), u.sessionID, origin.OriginMessageID, origin.Source, QueryResult{Title: "Other", DTQL: origin.DTQL, Result: origin.Result})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := u.sessions.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			u.loadSession(snapshot)
			u.shell.FocusEntry(u.gridsByRecordSetID[originID].entryID)
			application := &reviewDelayedJoin{started: make(chan struct{}), release: make(chan struct{})}
			u.sessions.ConfigureJoinApplication(application)
			uxSearchAction(t, u, "JOIN relationship")
			_, cmd := u.shell.Update(uitest.Key("enter"))
			if cmd == nil {
				t.Fatal("JOIN apply missing")
			}
			completion := make(chan tea.Msg, 1)
			go func() { completion <- cmd() }()
			select {
			case <-application.started:
			case <-time.After(2 * time.Second):
				t.Fatal("JOIN never started")
			}
			var editor *workspaceQueryEditor
			switch change {
			case "result":
				u.shell.FocusEntry(u.gridsByRecordSetID[second.RecordSetID].entryID)
			case "row":
				u.shell.Update(uitest.Key("down"))
			case "composer":
				u.shell.Update(uitest.Key("f4"))
				u.shell.Update(uitest.Text("retained draft"))
				u.shell.Update(uitest.Key("left"))
			case "modal":
				u.shell.Update(uitest.Key("f1"))
			case "editor":
				editor = &workspaceQueryEditor{ui: u, text: textarea.New()}
				editor.text.SetValue("manual query draft")
				u.shell.SetCenterPanel(editor, true)
			}
			focus, entry, caret := u.shell.WorkspaceFocus(), u.shell.FocusedEntryID(), u.shell.ComposerCursor()
			g, selected, _ := u.activeGrid()
			row := g.sourceRowKey()
			generation := u.workspaceGeneration
			close(application.release)
			select {
			case msg := <-completion:
				u.shell.Update(msg)
			case <-time.After(2 * time.Second):
				t.Fatal("JOIN never completed")
			}
			current, record, _ := u.activeGrid()
			if u.workspaceGeneration != generation || u.shell.WorkspaceFocus() != focus || u.shell.FocusedEntryID() != entry || current != g || record.ID != selected.ID || current.sourceRowKey() != row || u.shell.ComposerCursor() != caret {
				t.Fatal("delayed JOIN replaced newer workspace context")
			}
			if change == "composer" && u.shell.ComposerText() != "retained draft" {
				t.Fatal("composer draft changed")
			}
			if editor != nil && (u.shell.CenterPanel() != editor || editor.text.Value() != "manual query draft") {
				t.Fatal("query editor replaced")
			}
			if len(u.snapshot.RecordSets) != 3 || u.workspaceMutationPending || !strings.Contains(u.workspaceFeedback, "current workspace is retained") {
				t.Fatal("durable completion was not reconciled without resetting context")
			}
		})
	}
}

func TestUnifiedWorkspaceLongRelationshipListsFollowSelection(t *testing.T) {
	t.Parallel()
	u := reviewJoinUI(t)
	candidates := make([]JoinCandidate, 24)
	for i := range candidates {
		candidates[i] = JoinCandidate{ID: JoinCandidateID(fmt.Sprint(i)), Source: RelationInstance{Relation: "Invoice"}, Target: RelationInstance{Relation: fmt.Sprintf("Target%02d", i)}, Fields: []JoinFieldPair{{SourceField: "CustomerId", TargetField: fmt.Sprintf("Key%02d", i)}}}
	}
	o := &joinPickerOverlay{ui: u, candidates: candidates, sessionID: u.sessionID, generation: u.workspaceGeneration}
	u.shell.PushOverlay(o)
	for range 23 {
		u.shell.Update(uitest.Key("down"))
	}
	view := flattenView(u.shell.View().Content)
	if !strings.Contains(view, "› · Target23") || !strings.Contains(view, "Invoice.CustomerId → Target23.Key23") {
		t.Fatalf("selected JOIN/detail invisible: %s", view)
	}
	u.shell.Update(uitest.Key("up"))
	if !strings.Contains(flattenView(u.shell.View().Content), "› · Target22") {
		t.Fatal("JOIN selection did not follow arrows")
	}
	o.index = 9
	if !strings.Contains(ansi.Strip(o.View(60, 6)), "› 0 Target09") {
		t.Fatal("viewport renumbered stable tenth slot")
	}
	u.shell.Update(uitest.Key("esc"))
	p := u.workspace
	g, r, _ := u.activeGrid()
	p.relatedFocused = true
	p.tab = 1
	p.relatedRecordID = r.ID
	p.relatedRowKey = g.sourceRowKey()
	p.relatedSessionID = u.sessionID
	p.relatedGeneration = u.workspaceGeneration
	for i := range 24 {
		p.relatedRecords = append(p.relatedRecords, relatedRecord{key: ForeignKey{ToRelation: fmt.Sprintf("Related%02d", i)}})
	}
	u.shell.FocusRightPanel()
	for range 23 {
		u.shell.Update(uitest.Key("down"))
	}
	if !strings.Contains(flattenView(u.shell.View().Content), "› · → Related23") {
		t.Fatalf("Related selection invisible: %s", flattenView(u.shell.View().Content))
	}
	p.relatedIndex = 9
	if !strings.Contains(p.relatedView(40, 5), "› 0 → Related09") {
		t.Fatal("Related viewport renumbered tenth slot")
	}
}

func TestUnifiedWorkspaceComposerSelectTextPreservesDraftAndCaret(t *testing.T) {
	t.Parallel()
	u := reviewJoinUI(t)
	u.shell.Update(uitest.Key("f4"))
	u.shell.SetComposerText("before 世界 after")
	for range 6 {
		u.shell.Update(uitest.Key("left"))
	}
	caret := u.shell.ComposerCursor()
	draft := u.shell.ComposerText()
	messages := len(u.snapshot.Messages)
	u.shell.Update(uitest.Key("tab"))
	for _, width := range []int{80, 150} {
		_, hints := u.statusHints(width)
		text := strings.Join(hints, " · ")
		if !strings.Contains(text, "Enter Select text") || !strings.Contains(text, "Tab editing") || strings.Contains(text, "Enter send") {
			t.Fatalf("focused action hints wrong at%d: %s", width, text)
		}
	}
	u.shell.Update(uitest.Key("enter"))
	if !strings.Contains(flattenView(u.shell.View().Content), "Select text") {
		t.Fatal("Tab/Enter did not open explicit composer selection")
	}
	for range 2 {
		u.shell.Update(uitest.Key("left"))
	}
	u.shell.Update(uitest.Key("f1"))
	if !strings.Contains(flattenView(u.shell.View().Content), "arrows extend selection") {
		t.Fatal("F1 described the underlying composer instead of selection mode")
	}
	u.shell.Update(uitest.Key("esc"))
	mouseCopy := clickWorkspaceText(t, u, "Copy Ctrl+C")
	if mouseCopy == nil || fmt.Sprint(mouseCopy()) != "世界" {
		t.Fatal("visible Copy control lost selected Unicode range")
	}
	_, copyCmd := u.shell.Update(uitest.Key("ctrl+c"))
	if copyCmd == nil || fmt.Sprint(copyCmd()) != "世界" {
		t.Fatal("modal Ctrl+C did not copy exact Unicode range")
	}
	_, acceptCmd := u.shell.Update(uitest.Key("enter"))
	if acceptCmd == nil {
		t.Fatal("Accept did not copy selected range")
	}
	if u.shell.ComposerText() != draft || u.shell.ComposerCursor() != caret || u.shell.WorkspaceFocus() != "input" || len(u.snapshot.Messages) != messages {
		t.Fatalf("selection state draft=%q want=%q caret=%d want=%d focus=%s messages=%d", u.shell.ComposerText(), draft, u.shell.ComposerCursor(), caret, u.shell.WorkspaceFocus(), len(u.snapshot.Messages))
	}
	if !strings.Contains(u.workspaceFeedback, "2 selected characters") {
		t.Fatal("selected range was not accepted")
	}
	// A fresh modal can be reached with the visible mouse action and cancelled.
	clickWorkspaceText(t, u, "Select text")
	u.shell.Update(uitest.Key("home"))
	clickWorkspaceText(t, u, "Cancel Esc")
	if u.shell.ComposerText() != draft || u.shell.ComposerCursor() != caret {
		t.Fatal("cancel changed draft/caret")
	}
	u.shell.Update(uitest.Key("shift+right"))
	if u.shell.WorkspaceFocus() != "sidebar" {
		t.Fatal("selection alternative reclaimed workspace Shift arrows")
	}
}

func TestUnifiedWorkspaceSelectionViewportTracksLongUnicodeCaret(t *testing.T) {
	t.Parallel()
	text := []rune(strings.Repeat("世界 abc\n", 30) + "最後")
	s := &queryTextSelection{text: text, anchor: len(text) - 2, cursor: len(text), copyOnAccept: true}
	view := ansi.Strip(s.View(20, 7))
	if !strings.Contains(view, "最後▏") || len(strings.Split(view, "\n")) > 7 || !strings.Contains(view, "Copy") {
		t.Fatalf("long selection caret/control cropped: %s", view)
	}
	s.Update(uitest.Key("home"))
	if !strings.Contains(ansi.Strip(s.View(20, 7)), "▏世界") {
		t.Fatal("selection viewport did not follow moved caret")
	}
}

func TestUnifiedWorkspaceCompactGroupAndMembersKeepSelectionVisible(t *testing.T) {
	t.Parallel()
	for _, members := range []bool{false, true} {
		t.Run(fmt.Sprint(members), func(t *testing.T) {
			rows := make([]secureread.Row, 24)
			for i := range rows {
				key := fmt.Sprintf("K%02d", i)
				if members {
					key = "one group"
				}
				rows[i] = secureread.Row{Data: map[string]any{"Category": key, "Label": fmt.Sprintf("Row%02d", i)}}
			}
			u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Many rows", DTQL: "from: {name: Item}\ncolumns: [{field: Category}, {field: Label}]\nlimit: 24\n", Result: secureread.Result{Columns: []string{"Category", "Label"}, Rows: rows}})
			_, record, _ := u.activeGrid()
			u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", SourceID: record.Database, ObjectID: "main.Item"}, Columns: []string{"Category", "Label"}, ColumnTypes: map[string]string{"Category": "TEXT", "Label": "TEXT"}}}}
			uxSearchAction(t, u, "Group loaded")
			u.shell.Update(uitest.Key("1"))
			u.shell.Update(uitest.Key("enter"))
			p, ok := u.shell.CenterPanel().(*localGroupsPanel)
			if !ok {
				t.Fatalf("group panel missing: %s feedback=%s", flattenView(u.shell.View().Content), u.workspaceFeedback)
			}
			p.View(40, 7, true)
			if members {
				p.Update(uitest.Key("enter"))
				p = u.shell.CenterPanel().(*localGroupsPanel)
			}
			for range 23 {
				p.View(40, 7, true)
				p.Update(uitest.Key("down"))
			}
			view := ansi.Strip(p.View(40, 7, true))
			want := "K23"
			if members {
				want = "Row23"
			}
			if !strings.Contains(view, want) || !strings.Contains(view, "Esc back") || len(strings.Split(view, "\n")) > 7 {
				t.Fatalf("compact selected %s/control cropped: %s", want, view)
			}
		})
	}
}
