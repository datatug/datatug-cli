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
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	var openedURL string
	terminal.openBrowser = func(url string) error { openedURL = url; return nil }
	if terminal.webLinkVisible {
		t.Fatal("web link was visible before F5")
	}
	// A successful open (M6's web handoff) fires u.openBrowser and leaves
	// the hyperlink fallback hidden -- the real browser opened.
	cmd, consumed := terminal.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5})
	if !consumed {
		t.Fatal("expected F5 to be consumed by globalKeys")
	}
	drainCmd(t, terminal, cmd)
	if openedURL != bridge.URL || terminal.webLinkVisible {
		t.Fatalf("F5 open result = %q, overlay=%v", openedURL, terminal.webLinkVisible)
	}
	// A failed open falls back to showing the hyperlink.
	terminal.openBrowser = func(string) error { return errors.New("no browser available") }
	cmd, _ = terminal.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5})
	drainCmd(t, terminal, cmd)
	if !terminal.webLinkVisible || !strings.Contains(terminal.shell.View().Content, "Open web chat") {
		t.Fatal("failed F5 did not reveal web link")
	}
	cmd, consumed = terminal.globalKeys(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !consumed {
		t.Fatal("expected Esc to be consumed by globalKeys while the fallback link is visible")
	}
	drainCmd(t, terminal, cmd)
	if terminal.webLinkVisible {
		t.Fatal("Esc did not hide failed-browser dialog")
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

// erroringConversation always fails AskWithContext, for exercising the
// browser bridge's own AskActive-error branch.
type erroringConversation struct{}

func (erroringConversation) AskWithContext(context.Context, string, string) (Turn, error) {
	return Turn{}, errors.New("model unavailable")
}

// TestBrowserBridgeListenerFallsBackWhenDefaultPortIsTaken covers
// StartBrowserBridge's own fallback branch: when 127.0.0.1:3284 is already
// bound (a second local chat running), it falls back to an ephemeral port
// instead of failing.
func TestBrowserBridgeListenerFallsBackWhenDefaultPortIsTaken(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:3284")
	if err != nil {
		t.Skipf("port 3284 unavailable for the test itself: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatalf("StartBrowserBridge with the default port taken: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	// bridge.URL's fragment carries "h=127.0.0.1:<port>&t=..."; extract
	// that port and assert it isn't 3284 rather than asserting the literal
	// substring "127.0.0.1:3284" is absent -- the OS-assigned fallback
	// port can itself contain "3284" as a substring (e.g. 32845), which
	// would make the old assertion flaky.
	idx := strings.Index(bridge.URL, "#h=127.0.0.1:")
	if idx < 0 {
		t.Fatalf("bridge URL missing expected h= fragment: %q", bridge.URL)
	}
	rest := bridge.URL[idx+len("#h=127.0.0.1:"):]
	if amp := strings.IndexByte(rest, '&'); amp >= 0 {
		rest = rest[:amp]
	}
	if rest == "3284" {
		t.Fatalf("bridge fell back to a different port but URL still names 3284: %q", bridge.URL)
	}
	if _, err := strconv.Atoi(rest); err != nil {
		t.Fatalf("bridge URL port %q did not parse as a number: %v", rest, err)
	}
}

// TestBrowserBridgeEndpointErrorBranches covers the HTTP endpoints' own
// method/origin/body/session-mismatch/agent-error branches, none of which
// TestChatUIBrowserBridgeSharesSessionWithTerminal's happy-path walk
// exercises.
func TestBrowserBridgeEndpointErrorBranches(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	agent := &erroringConversation{}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", ProjectCatalog{ID: "demo-project-2"})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + fragment.Get("h")
	token := fragment.Get("t")
	request := func(method, path string, body []byte, tok, origin string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, address+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if tok != "" {
			req.Header.Set("X-DataTug-Chat-Capability", tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	if resp := request("GET", "/datatug/projects/project_summary?id=demo-project-2", nil, token, "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed origin on project_summary: %d", resp.StatusCode)
	}
	if resp := request("POST", "/datatug/projects/project_summary?id=demo-project-2", nil, token, "https://datatug.app"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method on project_summary: %d", resp.StatusCode)
	}
	if resp := request("POST", "/v1/chat/session", nil, token, "https://datatug.app"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method on chat/session: %d", resp.StatusCode)
	}
	if resp := request("GET", "/v1/chat/messages", nil, token, "https://datatug.app"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method on chat/messages: %d", resp.StatusCode)
	}
	if resp := request("POST", "/v1/chat/messages", []byte("not json"), token, "https://datatug.app"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body: %d", resp.StatusCode)
	}
	if resp := request("POST", "/v1/chat/messages", []byte(`{"text":"  "}`), token, "https://datatug.app"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank text: %d", resp.StatusCode)
	}
	if resp := request("POST", "/v1/chat/messages", []byte(`{"text":"hi","sessionId":"wrong-session"}`), token, "https://datatug.app"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("mismatched sessionId: %d", resp.StatusCode)
	}
	// AskActive's own error branch (500) is not reachable from here: a
	// failing agent (erroringConversation, used above only to keep this
	// test independent of a real model) is absorbed by SessionChat.ask's
	// finalizeTurn into a friendly Turn.Text with a nil error -- the bridge
	// only sees a Go error from AskActive when SessionChat.prepareTurn or
	// the store append itself fails, which (with the session-ID check
	// already passed above) needs the store to fail between two calls in
	// the same synchronous request. That is now covered separately, via
	// SessionChat's storeAppendUserOverride seam, by bridge_test.go's
	// TestBridgeMessagesHandlerAskActiveStoreErrorSurfaces.

	// The WebSocket endpoint: origin/host-disallowed (403) and an invalid
	// subprotocol token (401) -- both checked before any upgrade attempt.
	if resp := request("GET", "/v1/chat/events", nil, "", "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed origin on chat/events: %d", resp.StatusCode)
	}
	wsReq, err := http.NewRequest("GET", address+"/v1/chat/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	wsReq.Header.Set("Origin", "https://datatug.app")
	wsReq.Header.Set("Sec-WebSocket-Protocol", "datatug-chat, wrong-token")
	resp, err := http.DefaultClient.Do(wsReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid subprotocol token on chat/events: %d", resp.StatusCode)
	}
}

// TestBrowserBridgeChatSessionSnapshotErrorSurfaces covers /v1/chat/session's
// own sessions.Snapshot error branch, forced by closing the underlying
// store.
func TestBrowserBridgeChatSessionSnapshotErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", "http://"+fragment.Get("h")+"/v1/chat/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://datatug.app")
	req.Header.Set("X-DataTug-Chat-Capability", fragment.Get("t"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("chat/session with a closed store: %d", resp.StatusCode)
	}
}

// TestBrowserBridgeEventsLoopDetectsClientDisconnect covers the /v1/chat/events
// handler's own ReadMessage-error/disconnected-channel branch: the server
// must stop its select loop (and drop the connection from bridge.connections)
// once the client closes its end, rather than blocking forever.
func TestBrowserBridgeEventsLoopDetectsClientDisconnect(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	token := fragment.Get("t")
	socket, _, err := (&websocket.Dialer{Subprotocols: []string{"datatug-chat", token}}).Dial("ws://"+fragment.Get("h")+"/v1/chat/events", http.Header{"Origin": []string{"https://datatug.app"}})
	if err != nil {
		t.Fatal(err)
	}
	var notification map[string]string
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err != nil {
		t.Fatalf("initial socket event: %v", err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		bridge.connectionsMu.Lock()
		remaining := len(bridge.connections)
		bridge.connectionsMu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never noticed the client disconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBrowserBridgeEventsUpgradeFailureIsHandled covers the /v1/chat/events
// handler's own websocket-Upgrade-error branch: a request that passes
// auth/subprotocol but never asks for a protocol upgrade at all.
func TestBrowserBridgeEventsUpgradeFailureIsHandled(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", "http://"+fragment.Get("h")+"/v1/chat/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://datatug.app")
	req.Header.Set("Sec-WebSocket-Protocol", "datatug-chat, "+fragment.Get("t"))
	// No Connection:Upgrade/Sec-WebSocket-Key headers: gorilla/websocket's
	// Upgrader.Upgrade refuses and writes its own error response, and the
	// handler simply returns.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("expected the upgrade to fail without the required headers")
	}
}

// TestBrowserBridgeCloseClosesLiveConnections covers Close's own
// connections-map loop: closing the bridge while a websocket connection is
// still open must close that connection too, not just the listener.
func TestBrowserBridgeCloseClosesLiveConnections(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	token := fragment.Get("t")
	socket, _, err := (&websocket.Dialer{Subprotocols: []string{"datatug-chat", token}}).Dial("ws://"+fragment.Get("h")+"/v1/chat/events", http.Header{"Origin": []string{"https://datatug.app"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socket.Close() }()
	var notification map[string]string
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err != nil {
		t.Fatalf("initial socket event: %v", err)
	}
	// Close the bridge (not the client socket) first, so the server-side
	// connections map still holds this connection when Close's loop runs.
	if err := bridge.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The server should have closed its end: a subsequent read must fail
	// rather than hang.
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err == nil {
		t.Fatal("expected the server to have closed the connection")
	}
}

// TestBrowserBridgeAllowedRequestHostMismatch covers allowedRequest's own
// Host-mismatch branch directly: an allowed Origin with a Host header that
// names neither the listener's address nor its localhost:port form.
func TestBrowserBridgeAllowedRequestHostMismatch(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	req, err := http.NewRequest("GET", "http://example.test/v1/chat/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://datatug.app")
	req.Host = "not-the-listener-host:9999"
	if bridge.allowedRequest(req) {
		t.Fatal("expected a Host mismatch to be disallowed")
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
