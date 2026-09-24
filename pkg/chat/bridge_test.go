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
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
