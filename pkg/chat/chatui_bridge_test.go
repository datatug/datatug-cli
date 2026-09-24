package chat

// Ported from bridge_test.go's TestBrowserBridgeSharesSessionWithTerminal
// and TestBrowserBridgeContinuesWhileDialogsAreOpen. Porting these surfaced
// a real gap: ChatUI.SetBrowserURL set up the SubscribeChanges() channel
// but nothing ever consumed it, F5 did nothing, and no web-link hint was
// ever rendered — the browser <-> terminal live-sync ui.go had was
// completely disconnected in ChatUI. Closed in chatui.go (Run forwards
// bridge events into the running tea.Program, OnMsg's new bridgeTickMsg
// case reloads the session same as /switch) and chatui_pickers.go
// (globalKeys' new "f5" case) — see bridge.go for why bridgeTickMsg moved
// there.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/gorilla/websocket"
)

func TestChatUIBrowserBridgeSharesSessionWithTerminal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	agent := &contextualStub{turns: []Turn{{Text: "Three matching rows.", Queries: []QueryResult{{Title: "Count", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"Count"}, Rows: []secureread.Row{{Data: map[string]any{"Count": 3}}}}}}}, {Text: "Another browser reply."}, {Text: "Terminal reply."}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", ProjectCatalog{ID: "demo-project-1"})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := NewSessionChatUI(ctx, sessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	// Wide window: statusBar renders one line by construction (see
	// TestChatUIStatusHintsFollowFocus's own comment on the same
	// simplification), so a narrow width can truncate the F5 web-link
	// hint before it appears, same as any other trailing status segment.
	terminal.shell.Update(tea.WindowSizeMsg{Width: 220, Height: 30})
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	terminal.SetBrowserURL(bridge.URL)
	if terminal.webLinkVisible {
		t.Fatal("web link was visible before F5")
	}
	cmd, consumed := terminal.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5})
	if !consumed {
		t.Fatal("expected F5 to be consumed by globalKeys")
	}
	drainCmd(t, terminal, cmd)
	if !terminal.webLinkVisible || !strings.Contains(terminal.shell.View().Content, "Open web chat") {
		t.Fatal("F5 did not reveal web link")
	}
	cmd, _ = terminal.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5})
	drainCmd(t, terminal, cmd)
	if terminal.webLinkVisible {
		t.Fatal("second F5 did not hide web link")
	}
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link.Path, "/project/demo-project-1/chat") {
		t.Fatalf("project chat link path = %q", link.Path)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + fragment.Get("h")
	request := func(method, path string, body []byte, token, origin string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, address+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("X-DataTug-Chat-Capability", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	token := fragment.Get("t")
	if resp := request("GET", "/datatug/projects/project_summary?id=demo-project-1", nil, token, "https://datatug.app"); resp.StatusCode != http.StatusOK {
		t.Fatalf("project summary: %d", resp.StatusCode)
	}
	if resp := request("GET", "/datatug/projects/project_summary?id=another-project", nil, token, "https://datatug.app"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other project summary: %d", resp.StatusCode)
	}
	socket, _, err := (&websocket.Dialer{Subprotocols: []string{"datatug-chat", token}}).Dial("ws://"+fragment.Get("h")+"/v1/chat/events", http.Header{"Origin": []string{"https://datatug.app"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	var notification map[string]string
	if err := socket.ReadJSON(&notification); err != nil || notification["type"] != "changed" {
		t.Fatalf("initial socket event: %v %v", notification, err)
	}
	if resp := request("GET", "/v1/chat/session", nil, token, "https://other.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	if resp := request("GET", "/v1/chat/session", nil, "wrong", "https://datatug.app"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
	if resp := request("OPTIONS", "/v1/chat/messages", nil, "", "https://datatug.app"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight: %d", resp.StatusCode)
	}
	initial, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"text": "Count them", "sessionId": initial.ID})
	if resp := request("POST", "/v1/chat/messages", body, token, "https://datatug.app"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("send: %d", resp.StatusCode)
	}
	if err := socket.ReadJSON(&notification); err != nil || notification["type"] != "changed" {
		t.Fatalf("socket update: %v %v", notification, err)
	}
	drainCmd(t, terminal, terminal.OnMsg(bridgeTickMsg{}))
	if len(terminal.snapshot.Messages) != 3 || !strings.Contains(terminal.shell.View().Content, "Three matching rows") {
		t.Fatal("browser turn did not reach terminal")
	}
	if terminal.lastGridEntryID == "" || !terminal.shell.FocusEntry(terminal.lastGridEntryID) {
		t.Fatal("could not focus result grid")
	}
	body, _ = json.Marshal(map[string]string{"text": "Continue", "sessionId": initial.ID})
	if resp := request("POST", "/v1/chat/messages", body, token, "https://datatug.app"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second send: %d", resp.StatusCode)
	}
	drainCmd(t, terminal, terminal.OnMsg(bridgeTickMsg{}))
	if recordSetID := terminal.activeRecordSetID(); recordSetID == "" {
		t.Fatal("browser update displaced terminal grid focus")
	}
	if _, err := sessions.Ask(ctx, "From terminal"); err != nil {
		t.Fatal(err)
	}
	response := request("GET", "/v1/chat/session", nil, token, "https://datatug.app")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot: %d", response.StatusCode)
	}
	var snapshot ChatSession
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 7 || snapshot.Messages[6].Text != "Terminal reply." || len(snapshot.RecordSets) != 1 {
		t.Fatal("terminal turn did not reach browser snapshot")
	}
}

func TestChatUIBrowserBridgeContinuesWhileDialogsAreOpen(t *testing.T) {
	// Ported from bridge_test.go's TestBrowserBridgeContinuesWhileDialogsAreOpen.
	// ui.go tracked each dialog as its own nilable field that bridgeTickMsg's
	// handler had to route around; ChatUI/chatshell replaced all of that
	// with one overlay stack, so the property to prove is simpler and more
	// direct: a bridgeTickMsg's session reload must not disturb whichever
	// overlay is on top (dialogs are local UI state, not session state),
	// and must not panic or block regardless of which dialog is open.
	overlays := []func(u *ChatUI) tea.Cmd{
		func(u *ChatUI) tea.Cmd { return u.shell.PushOverlay(newHTTPRequestOverlay(u, httpRequestSpec{})) },
		func(u *ChatUI) tea.Cmd { return u.shell.PushOverlay(newSaveQueryOverlay(u, SavedQuerySaveRequest{})) },
		func(u *ChatUI) tea.Cmd { return u.shell.PushOverlay(newExportDialogOverlay(u)) },
		func(u *ChatUI) tea.Cmd { return u.shell.PushOverlay(&connectOverlay{ui: u}) },
	}
	names := []string{"http form", "save query", "export", "connect"}
	for i, open := range overlays {
		t.Run(names[i], func(t *testing.T) {
			u, _ := newTestChatUI(t, nil, Turn{})
			u.SetBrowserURL("http://example.test")
			t.Cleanup(func() {
				if u.bridgeStop != nil {
					u.bridgeStop()
				}
			})
			drainCmd(t, u, open(u))
			before := u.shell.View().Content
			_ = u.OnMsg(bridgeTickMsg{}) // must not panic with an overlay open
			if after := u.shell.View().Content; after != before {
				t.Fatalf("bridge tick changed the open %s dialog's view:\nbefore:\n%s\nafter:\n%s", names[i], before, after)
			}
		})
	}
}
