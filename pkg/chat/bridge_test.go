package chat

// bridge_test.go covers StartBrowserBridge's own construction-error
// branches (crypto/rand.Read and the two net.Listen calls, via the
// readRandom/netListenTCP seams) and the /v1/chat/messages handler's
// AskActive-error branch (via SessionChat's storeAppendUserOverride seam),
// none of which chatui_bridge_test.go's extensive endpoint/lifecycle
// coverage reaches -- see that file for the rest of bridge.go's HTTP
// surface (method/origin/body/session-mismatch guards, the WebSocket
// disconnect/upgrade-failure/Close paths, and allowedRequest's own Host
// check).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBridgeHTTPUsesConfiguredHeadersAndCookies(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var receivedMethod, receivedHeader, receivedCookie, receivedBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod, receivedHeader, receivedCookie = r.Method, r.Header.Get("Authorization"), r.Header.Get("Cookie")
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	if err := store.SetHTTPRequestSetting(ctx, "project", "header", target.URL, "Authorization", "Bearer project"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHTTPRequestSetting(ctx, "project", "cookie", target.URL, "session", "private"); err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	address, token := bridgeToken(t, bridge.URL)
	settingsURL := "http://" + address + "/v1/chat/http_settings?origin=" + url.QueryEscape(target.URL)
	settingsReq, err := http.NewRequest(http.MethodGet, settingsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	settingsReq.Header.Set("Origin", "https://datatug.app")
	settingsReq.Header.Set("X-DataTug-Chat-Capability", token)
	settingsReq.Header.Set("X-DataTug-Chat-Session", snapshot.ID)
	settingsResp, err := http.DefaultClient.Do(settingsReq)
	if err != nil {
		t.Fatal(err)
	}
	settingsBody, _ := io.ReadAll(settingsResp.Body)
	_ = settingsResp.Body.Close()
	if settingsResp.StatusCode != http.StatusOK || !bytes.Contains(settingsBody, []byte(`"Authorization"`)) || bytes.Contains(settingsBody, []byte("Bearer project")) || bytes.Contains(settingsBody, []byte("private")) {
		t.Fatalf("masked settings: %d %s", settingsResp.StatusCode, settingsBody)
	}
	payload, _ := json.Marshal(map[string]any{"sessionId": snapshot.ID, "method": "POST", "url": target.URL + "/api?secret=hidden", "body": "hello"})
	req, err := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/http", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://datatug.app")
	req.Header.Set("X-DataTug-Chat-Capability", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("send HTTP: %d", resp.StatusCode)
	}
	if receivedMethod != "POST" || receivedHeader != "Bearer project" || receivedCookie != "session=private" || receivedBody != "hello" {
		t.Fatalf("request mismatch: method=%q header=%q cookie=%q body=%q", receivedMethod, receivedHeader, receivedCookie, receivedBody)
	}
	updated, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.HTTPResponses) != 1 || !strings.Contains(updated.Messages[len(updated.Messages)-2].Text, target.URL+"/api") {
		t.Fatalf("HTTP response was not synchronized: messages=%d responses=%d", len(updated.Messages), len(updated.HTTPResponses))
	}
	for _, message := range updated.Messages {
		if strings.Contains(message.Text, "secret=hidden") {
			t.Fatal("query string stored in transcript")
		}
	}
}

func TestBrowserExportBufferRejectsOversizedWrite(t *testing.T) {
	var output browserExportBuffer
	if _, err := output.Write(bytes.Repeat([]byte{'x'}, 16<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte{'y'}); !errors.Is(err, errBrowserExportTooLarge) {
		t.Fatalf("oversized write: %v", err)
	}
}

// TestStartBrowserBridgeRandomFailure covers StartBrowserBridge's own
// crypto/rand.Read error branch via the readRandom seam: the OS entropy
// source failing is not reproducible for real, so this overrides the var.
func TestStartBrowserBridgeRandomFailure(t *testing.T) {
	restore := readRandom
	t.Cleanup(func() { readRandom = restore })
	injected := errors.New("injected rand failure")
	readRandom = func([]byte) (int, error) { return 0, injected }

	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(context.Background(), store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartBrowserBridge(sessions); err == nil || !errors.Is(err, injected) {
		t.Fatalf("StartBrowserBridge error = %v, want the injected rand error wrapped", err)
	}
}

func TestBridgeBrowserClearDeleteAndExport(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	before, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, before.ID)
	queryService := &savedQueryStub{queries: []SavedQuery{{ID: "customers", Title: "Customers", Type: "DTQL"}}}
	sessions.ConfigureSavedQueryService(queryService)
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bucket_add", RecordSetID: recordID}); err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	address, token := bridgeToken(t, bridge.URL)
	request := func(method, path, body, sessionID string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://datatug.app")
		req.Header.Set("X-DataTug-Chat-Capability", token)
		req.Header.Set("X-DataTug-Chat-Session", sessionID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := request(http.MethodGet, "/v1/chat/export?format=csv&recordSetId="+recordID, "", before.ID)
	payload, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(payload), "Prague") {
		t.Fatalf("result export: %d %q", resp.StatusCode, payload)
	}
	resp = request(http.MethodGet, "/v1/chat/cell_detail?recordSetId="+recordID+"&row=0&column=City", "", before.ID)
	var detail BrowserCellDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || detail.Value != "Prague" {
		t.Fatalf("cell detail: %d %#v", resp.StatusCode, detail)
	}
	resp = request(http.MethodGet, "/v1/chat/settings", "", before.ID)
	var settings struct {
		Versions int `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || settings.Versions < 1 {
		t.Fatalf("settings: %d %#v", resp.StatusCode, settings)
	}
	resp = request(http.MethodGet, "/v1/chat/queries", "", before.ID)
	var queries []SavedQuery
	if err := json.NewDecoder(resp.Body).Decode(&queries); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(queries) != 1 {
		t.Fatalf("queries: %d %#v", resp.StatusCode, queries)
	}
	resp = request(http.MethodPost, "/v1/chat/queries", `{"sessionId":"`+before.ID+`","action":"run_dtql","queryId":"customers"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || queryService.ranID != "customers" {
		t.Fatalf("run DTQL: %d %q", resp.StatusCode, queryService.ranID)
	}
	queryService.queries = append(queryService.queries, SavedQuery{ID: "http-query", Type: "HTTP"})
	resp = request(http.MethodPost, "/v1/chat/queries", `{"sessionId":"`+before.ID+`","action":"run_http","queryId":"http-query"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || queryService.ranID != "http-query" {
		t.Fatalf("run saved HTTP: %d %q", resp.StatusCode, queryService.ranID)
	}
	resp = request(http.MethodPost, "/v1/chat/queries", `{"sessionId":"`+before.ID+`","action":"run_dtql","queryId":"http-query"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || queryService.ranID != "http-query" {
		t.Fatalf("HTTP query execution was exposed: %d %q", resp.StatusCode, queryService.ranID)
	}
	resp = request(http.MethodPost, "/v1/chat/queries", `{"sessionId":"`+before.ID+`","action":"save","save":{"Title":"From browser","Type":"DTQL","Text":"from: customers"}}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || queryService.saved.Title != "From browser" {
		t.Fatalf("save query: %d %#v", resp.StatusCode, queryService.saved)
	}
	resp = request(http.MethodGet, "/v1/chat/export?format=csv", "", before.ID)
	payload, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(payload), "PK") {
		t.Fatalf("bucket export: %d %q", resp.StatusCode, payload)
	}
	resp = request(http.MethodGet, "/v1/chat/export?format=csv&recordSetId="+recordID, "", "stale")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale export: %d", resp.StatusCode)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", `{"sessionId":"stale","action":"delete","value":"confirm"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale delete: %d", resp.StatusCode)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", `{"sessionId":"`+before.ID+`","action":"clear"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unconfirmed clear: %d", resp.StatusCode)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", `{"sessionId":"`+before.ID+`","action":"clear","value":"confirm"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	cleared, err := sessions.Snapshot(ctx)
	if err != nil || len(cleared.RecordSets) != 0 || cleared.ID != before.ID {
		t.Fatalf("clear snapshot: %#v %v", cleared, err)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", `{"sessionId":"`+before.ID+`","action":"delete","value":"confirm"}`, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	after, err := sessions.Snapshot(ctx)
	if err != nil || after.ID == before.ID {
		t.Fatalf("delete snapshot: %#v %v", after, err)
	}
}

// TestStartBrowserBridgeBothListenCallsFail covers StartBrowserBridge's own
// final listen-error branch: the fixed port fails naturally (a real
// listener occupies 127.0.0.1:3284) and the ephemeral-port fallback is
// forced to fail too via the netListenTCP seam -- an OS-assigned port
// itself failing is not reproducible for real.
func TestStartBrowserBridgeBothListenCallsFail(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:3284")
	if err != nil {
		t.Skipf("port 3284 unavailable for the test setup: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	restore := netListenTCP
	t.Cleanup(func() { netListenTCP = restore })
	injected := errors.New("injected ephemeral listen failure")
	netListenTCP = func(network, address string) (net.Listener, error) {
		if address == "127.0.0.1:0" {
			return nil, injected
		}
		return net.Listen(network, address)
	}

	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(context.Background(), store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartBrowserBridge(sessions); err == nil || !errors.Is(err, injected) {
		t.Fatalf("StartBrowserBridge error = %v, want the injected ephemeral-listen error wrapped", err)
	}
}

// bridgeToken extracts the "h" (host:port) and "t" (capability token)
// fragment parameters gorilla-chat clients read from BrowserBridge.URL.
func bridgeToken(t *testing.T, bridgeURL string) (address, token string) {
	t.Helper()
	link, err := url.Parse(bridgeURL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	return fragment.Get("h"), fragment.Get("t")
}

func TestBridgeWorkspaceSharesStateAndRejectsStaleSession(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	sessions.catalog = ProjectCatalog{ID: "demo", Title: "Demo", Objects: []ProjectObject{{Reference: ContextReference{Kind: "project", ObjectID: "demo", Title: "Demo"}}}}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	address, token := bridgeToken(t, bridge.URL)
	request := func(method, path, body, capability string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://datatug.app")
		req.Header.Set("X-DataTug-Chat-Capability", capability)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := request(http.MethodOptions, "/v1/chat/join_candidates", "", "")
	if resp.StatusCode != http.StatusNoContent || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "X-DataTug-Chat-Session") {
		t.Fatalf("JOIN preflight: status %d, headers %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Headers"))
	}
	_ = resp.Body.Close()
	resp = request(http.MethodGet, "/v1/chat/catalog", "", token)
	var catalog ProjectCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if catalog.ID != "demo" || len(catalog.Objects) != 1 {
		t.Fatalf("catalog = %+v", catalog)
	}
	resp = request(http.MethodGet, "/v1/chat/catalog", "", "wrong")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized catalog: %d", resp.StatusCode)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	socket := dialBridgeEvents(t, bridge)
	defer func() { _ = socket.Close() }()
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	var notification map[string]string
	if err := socket.ReadJSON(&notification); err != nil {
		t.Fatal(err)
	}
	resp = request(http.MethodPost, "/v1/chat/workspace", `{"sessionId":"stale","action":{"kind":"set_tab","title":"Selected"}}`, token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale workspace: %d", resp.StatusCode)
	}
	resp = request(http.MethodPost, "/v1/chat/results", `{"sessionId":"stale","recordSetId":"missing","action":"refresh"}`, token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale result refresh: %d", resp.StatusCode)
	}
	body, err := json.Marshal(map[string]any{"sessionId": snapshot.ID, "action": WorkspaceAction{Kind: "set_tab", Title: "Selected"}})
	if err != nil {
		t.Fatal(err)
	}
	resp = request(http.MethodPost, "/v1/chat/workspace", string(body), token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("workspace mutation: %d", resp.StatusCode)
	}
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err != nil || notification["type"] != "changed" {
		t.Fatalf("workspace websocket event = %v, %v", notification, err)
	}
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Workspace.ActiveTab != "Selected" {
		t.Fatalf("active tab = %q", snapshot.Workspace.ActiveTab)
	}
	resp = request(http.MethodGet, "/v1/chat/sessions", "", token)
	var listed []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(listed) != 1 || listed[0].ID != snapshot.ID {
		t.Fatalf("session list = %+v", listed)
	}
	body, err = json.Marshal(map[string]string{"sessionId": snapshot.ID, "action": "rename", "value": "Browser name"})
	if err != nil {
		t.Fatal(err)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", string(body), token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rename: %d", resp.StatusCode)
	}
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Title != "Browser name" {
		t.Fatalf("renamed title = %q", snapshot.Title)
	}
	oldID := snapshot.ID
	body, err = json.Marshal(map[string]string{"sessionId": oldID, "action": "new"})
	if err != nil {
		t.Fatal(err)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", string(body), token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("new session: %d", resp.StatusCode)
	}
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID == oldID {
		t.Fatal("new session did not become active")
	}
	body, err = json.Marshal(map[string]string{"sessionId": snapshot.ID, "action": "switch", "value": oldID})
	if err != nil {
		t.Fatal(err)
	}
	resp = request(http.MethodPost, "/v1/chat/sessions", string(body), token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("switch session: %d", resp.StatusCode)
	}
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != oldID {
		t.Fatalf("switched session = %q", snapshot.ID)
	}
}

// TestBridgeMessagesHandlerAskActiveStoreErrorSurfaces covers the
// /v1/chat/messages handler's own sessions.AskActive-error branch (500):
// with the requested sessionId already matched against a real
// sessions.Snapshot call, AskActive itself only errors when
// SessionChat.prepareTurn's store append fails -- forced here via
// SessionChat's storeAppendUserOverride seam rather than left
// undocumented, per the founder directive that new code seams for
// coverage instead of leaving branches untested.
func TestBridgeMessagesHandlerAskActiveStoreErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected AppendUser failure")
	sessions.storeAppendUserOverride = func(context.Context, string, string) (ChatMessage, error) {
		return ChatMessage{}, injected
	}

	address, token := bridgeToken(t, bridge.URL)
	body, err := json.Marshal(map[string]string{"text": "hello", "sessionId": snapshot.ID})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/messages", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://datatug.app")
	req.Header.Set("X-DataTug-Chat-Capability", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("AskActive store error: status = %d, want 500", resp.StatusCode)
	}
}

// dialBridgeEvents opens a websocket client against the bridge's
// /v1/chat/events endpoint with a valid subprotocol token and Origin,
// mirroring what chatui_bridge_test.go's own websocket tests do.
func dialBridgeEvents(t *testing.T, bridge *BrowserBridge) *websocket.Conn {
	t.Helper()
	address, token := bridgeToken(t, bridge.URL)
	socket, _, err := (&websocket.Dialer{Subprotocols: []string{"datatug-chat", token}}).Dial("ws://"+address+"/v1/chat/events", http.Header{"Origin": []string{"https://datatug.app"}})
	if err != nil {
		t.Fatal(err)
	}
	return socket
}

// TestBridgeEventsHandlerInitialWriteJSONFailure covers the /v1/chat/events
// handler's own initial wsWriteJSON-error branch (the "changed" write
// issued immediately after a successful Upgrade): forced via the
// wsWriteJSON seam rather than the unreproducible real write-side race
// documented in this file's own history. StartBrowserBridge captures
// wsWriteJSON's value exactly once (see its doc comment), so the override
// MUST be installed before calling it -- installing it after (racing the
// handler's background connection goroutine, which is what this test
// originally did) is both ineffective and a genuine data race.
func TestBridgeEventsHandlerInitialWriteJSONFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}

	restore := wsWriteJSON
	t.Cleanup(func() { wsWriteJSON = restore })
	injected := errors.New("injected initial WriteJSON failure")
	wsWriteJSON = func(*websocket.Conn, any) error { return injected }

	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	socket := dialBridgeEvents(t, bridge)
	defer func() { _ = socket.Close() }()
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	// The handler returns immediately after the failed write without ever
	// sending the initial "changed" notification, closing the connection
	// (its deferred conn.Close()) -- the client's read must fail rather
	// than receive that notification or hang.
	var notification map[string]string
	if err := socket.ReadJSON(&notification); err == nil {
		t.Fatal("expected the server to close the connection after the injected write failure")
	}
}

// TestBridgeEventsHandlerChangesWriteJSONFailure covers the /v1/chat/events
// handler's own wsWriteJSON-error branch inside the `case <-changes:` loop,
// distinct from the initial write above: a stateful override (installed,
// like the test above, before StartBrowserBridge -- see wsWriteJSON's doc
// comment on why a mid-test swap is both ineffective and racy) lets the
// first (post-Upgrade) write through for real via `restore` and only fails
// the second write, the one triggered by a later SessionChat.notifyChanged.
func TestBridgeEventsHandlerChangesWriteJSONFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}

	restore := wsWriteJSON
	t.Cleanup(func() { wsWriteJSON = restore })
	injected := errors.New("injected changes-loop WriteJSON failure")
	var calls int32
	wsWriteJSON = func(conn *websocket.Conn, v any) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return restore(conn, v)
		}
		return injected
	}

	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	socket := dialBridgeEvents(t, bridge)
	defer func() { _ = socket.Close() }()
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	var notification map[string]string
	if err := socket.ReadJSON(&notification); err != nil || notification["type"] != "changed" {
		t.Fatalf("initial socket event: %v %v", notification, err)
	}

	if _, err := sessions.Rename(ctx, "renamed to trigger notifyChanged"); err != nil {
		t.Fatal(err)
	}

	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err == nil {
		t.Fatal("expected the server to close the connection after the injected changes-loop write failure")
	}
}

// TestBridgeEventsHandlerContextDoneReturns covers the /v1/chat/events
// handler select loop's own `case <-r.Context().Done()` branch: the
// bridgeBaseContext seam supplies a cancellable base context (http.Server's
// per-connection/request contexts are children of it), so canceling it
// here -- without the client ever closing its own socket, which would
// instead exercise the disconnected case -- forces the server to notice
// via r.Context() rather than a client-initiated close.
func TestBridgeEventsHandlerContextDoneReturns(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	restore := bridgeBaseContext
	t.Cleanup(func() { bridgeBaseContext = restore })
	bridgeBaseContext = func(net.Listener) context.Context { return base }

	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	socket := dialBridgeEvents(t, bridge)
	defer func() { _ = socket.Close() }()
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	var notification map[string]string
	if err := socket.ReadJSON(&notification); err != nil || notification["type"] != "changed" {
		t.Fatalf("initial socket event: %v %v", notification, err)
	}

	cancel()

	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err == nil {
		t.Fatal("expected the server to close the connection once its base context was canceled")
	}
}

// TestBridgeEventsHandlerChangesChannelClosedEarlyReturns covers the
// /v1/chat/events handler select loop's own `!ok` branch on the changes
// channel (bridge.go, restored in the r6 fix round -- see bridgeSubscribeChanges'
// and the loop's own doc comments for why it's genuinely unreachable
// through today's real SessionChat.SubscribeChanges, but stays as
// defensive code rather than being deleted). The bridgeSubscribeChanges
// seam hands the handler a channel this test can close directly -- standing
// in for "something closed this specific subscription early" -- something
// the real SubscribeChanges/stop() pairing can never do from outside this
// one handler invocation.
func TestBridgeEventsHandlerChangesChannelClosedEarlyReturns(t *testing.T) {
	fakeChanges := make(chan struct{})
	restore := bridgeSubscribeChanges
	t.Cleanup(func() { bridgeSubscribeChanges = restore })
	bridgeSubscribeChanges = func(*SessionChat) (<-chan struct{}, func()) {
		return fakeChanges, func() {}
	}

	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	socket := dialBridgeEvents(t, bridge)
	defer func() { _ = socket.Close() }()
	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	var notification map[string]string
	if err := socket.ReadJSON(&notification); err != nil || notification["type"] != "changed" {
		t.Fatalf("initial socket event: %v %v", notification, err)
	}

	close(fakeChanges)

	_ = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.ReadJSON(&notification); err == nil {
		t.Fatal("expected the server to close the connection once its changes channel closed early")
	}
}
