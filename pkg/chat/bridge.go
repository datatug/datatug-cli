package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// bridgeTickMsg notifies a chat UI (UI or ChatUI) that SessionChat.SubscribeChanges
// reported a change — either the browser bridge persisted a new browser turn, or
// something else appended to the session out of band. Lives here (rather than
// ui.go) so it survives ui.go's deletion; both UIs share the same message type.
type bridgeTickMsg struct{}

var errBrowserExportTooLarge = errors.New("export exceeds browser download limit (16 MiB)")

type browserExportBuffer struct{ bytes.Buffer }

func (b *browserExportBuffer) Write(data []byte) (int, error) {
	if len(data) > (16<<20)-b.Len() {
		return 0, errBrowserExportTooLarge
	}
	return b.Buffer.Write(data)
}

// browserOpenResultMsg reports the outcome of ChatUI's F5-triggered
// u.openBrowser(url) attempt (chatui_pickers.go's globalKeys "f5" case).
// ChatUI.OnMsg only shows the hyperlink fallback (webLinkVisible) when Err
// is non-nil and still names the URL that was tried -- a stale result for
// an already-changed browserURL is ignored.
type browserOpenResultMsg struct {
	url string
	err error
}

// BrowserBridge exposes the active CLI chat to a browser on a loopback-only port.
// The capability stays in the web URL fragment and is sent in a request header.
type BrowserBridge struct {
	server        *http.Server
	listener      net.Listener
	URL           string
	connectionsMu sync.Mutex
	connections   map[*websocket.Conn]struct{}
}

// readRandom is a test-only seam over crypto/rand.Read (nil-effect in
// production, where it is exactly rand.Read): the OS entropy source itself
// failing is not reproducible from a test, so a test overrides this var
// instead. Founder directive: all new Go code aims for 100% coverage via
// seams, not left uncovered.
var readRandom = rand.Read

// netListenTCP is a test-only seam over net.Listen (nil-effect in
// production, where it is exactly net.Listen): StartBrowserBridge calls it
// once for the fixed port and, on failure, once more for an OS-assigned
// ephemeral port. The second call failing needs the loopback interface's
// whole port range exhausted, not reproducible from a test without this
// seam -- a test can override it to fail selectively (e.g. only for the
// ":0" fallback) while a real net.Listen call occupies the fixed port for
// the first call to fail naturally.
var netListenTCP = net.Listen

// wsWriteJSON is a test-only seam over (*websocket.Conn).WriteJSON
// (nil-effect in production, where it is exactly conn.WriteJSON): the
// /v1/chat/events handler's write immediately after a successful Upgrade
// (and its later writes on each SessionChat change) only fail on an
// unreproducible write-side race in real use, so a test overrides this var
// to force that error deterministically instead. StartBrowserBridge reads
// it exactly ONCE, into a local `writeJSON` variable, before starting the
// handler's background connection goroutines -- a test must therefore set
// its override BEFORE calling StartBrowserBridge (a stateful closure that
// counts its own calls if only a later write should fail), never swap this
// var mid-test: the handler never re-reads the package var afterward, so a
// later swap would race against nothing production-visible, but would also
// silently not take effect.
var wsWriteJSON = func(conn *websocket.Conn, v any) error { return conn.WriteJSON(v) }

// bridgeSubscribeChanges is a test-only seam over SessionChat.SubscribeChanges
// (nil-effect in production, where it is exactly sessions.SubscribeChanges):
// StartBrowserBridge's /v1/chat/events handler defers the real stop() right
// above its receive loop, so under the real SessionChat that loop's own
// closed-channel ("!ok") case can only ever fire after the loop has already
// returned via another case -- there is no way to close that specific
// subscription's channel from outside this one handler invocation. It stays
// as defensive code (a future SessionChat change, or another caller sharing
// the same subscription, could close it earlier) rather than being deleted,
// per the r6 fix round's correction of the r5 round's mistaken "delete
// provably-dead code" call on inspector_ui.go's own guard. A test overrides
// this var to hand the handler a channel it can close directly, standing in
// for "something closed this subscription early."
var bridgeSubscribeChanges = func(sessions *SessionChat) (<-chan struct{}, func()) {
	return sessions.SubscribeChanges()
}

// bridgeBaseContext is a test-only seam over http.Server.BaseContext
// (nil-effect in production, where it yields the same context.Background()
// http.Server would use by default): the /v1/chat/events handler's
// `case <-r.Context().Done()` only fires on a genuine parent-context
// cancellation, which nothing in this package triggers in real use -- a
// test overrides this var to supply a cancellable base context, then
// cancels it to force that branch deterministically.
var bridgeBaseContext = func(net.Listener) context.Context { return context.Background() }

func StartBrowserBridge(sessions *SessionChat) (*BrowserBridge, error) {
	secret := make([]byte, 32)
	if _, err := readRandom(secret); err != nil {
		return nil, fmt.Errorf("create chat bridge capability: %w", err)
	}
	listener, err := netListenTCP("tcp", "127.0.0.1:3284")
	if err != nil {
		// Multiple local chats can coexist; the link carries the actual port.
		listener, err = netListenTCP("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return nil, fmt.Errorf("listen for browser chat: %w", err)
	}
	token := hex.EncodeToString(secret)
	bridge := &BrowserBridge{listener: listener, connections: make(map[*websocket.Conn]struct{})}
	port := listener.Addr().(*net.TCPAddr).Port
	path := "/chat"
	if projectID := sessions.catalog.ID; projectID != "" {
		path = fmt.Sprintf("/store/http-127.0.0.1:%d/project/%s/chat", port, url.PathEscape(projectID))
	}
	bridge.URL = fmt.Sprintf("https://datatug.app%s#h=127.0.0.1:%d&t=%s", path, port, token)
	// writeJSON captures wsWriteJSON's value ONCE here, synchronously in the
	// caller's goroutine, rather than the /v1/chat/events handler below
	// reading the package var directly on every write: that handler runs
	// in a long-lived background connection goroutine started by
	// bridge.server.Serve below, so a live re-read would race against a
	// test restoring wsWriteJSON in a later t.Cleanup once the handler
	// goroutine may still be running -- the same reasoning bridgeBaseContext
	// (captured into http.Server's own field, once, below) already follows.
	// A test that wants the first write to succeed for real and only a
	// later one to fail overrides wsWriteJSON with a stateful closure
	// BEFORE calling StartBrowserBridge, instead of swapping it mid-test.
	writeJSON := wsWriteJSON
	// subscribeChanges is captured ONCE here too, for the identical reason
	// writeJSON is above: the /v1/chat/events handler below runs in a
	// per-connection background goroutine, so a live re-read of the
	// package var would race a test's later restore. A test overrides
	// bridgeSubscribeChanges before calling StartBrowserBridge.
	subscribeChanges := bridgeSubscribeChanges
	mux := http.NewServeMux()
	// The browser uses the same session and workspace operations as the TUI.
	// Every mutation is scoped to the currently active session so an old tab
	// cannot change a session selected later in the terminal.
	mux.HandleFunc("/v1/chat/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessions.catalog)
	})
	mux.HandleFunc("/v1/chat/sessions", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method == http.MethodGet {
			items, err := sessions.List(r.Context())
			if err != nil {
				http.Error(w, "unable to list chats", http.StatusInternalServerError)
				return
			}
			type summary struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}
			out := make([]summary, 0, len(items))
			for _, item := range items {
				out = append(out, summary{ID: item.ID, Title: item.Title})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			SessionID string `json:"sessionId"`
			Action    string `json:"action"`
			Value     string `json:"value"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid session action", http.StatusBadRequest)
			return
		}
		if err := sessions.BrowserSessionAction(r.Context(), request.SessionID, request.Action, request.Value); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/workspace", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			SessionID string          `json:"sessionId"`
			Action    WorkspaceAction `json:"action"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid workspace action", http.StatusBadRequest)
			return
		}
		if _, err := sessions.ApplyWorkspaceActionActive(r.Context(), request.SessionID, request.Action); err != nil {
			if errors.Is(err, ErrActiveSessionChanged) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/join_candidates", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		items, err := sessions.JoinCandidatesActive(r.Context(), r.Header.Get("X-DataTug-Chat-Session"), r.URL.Query().Get("recordSetId"))
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})
	mux.HandleFunc("/v1/chat/cell_detail", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		row, err := strconv.Atoi(r.URL.Query().Get("row"))
		if err != nil {
			http.Error(w, "invalid row", http.StatusBadRequest)
			return
		}
		detail, err := sessions.CellDetailActive(r.Context(), r.Header.Get("X-DataTug-Chat-Session"), r.URL.Query().Get("recordSetId"), row, r.URL.Query().Get("column"))
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detail)
	})
	mux.HandleFunc("/v1/chat/results", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			SessionID   string          `json:"sessionId"`
			RecordSetID string          `json:"recordSetId"`
			Action      string          `json:"action"`
			CandidateID JoinCandidateID `json:"candidateId"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid result action", http.StatusBadRequest)
			return
		}
		var err error
		switch request.Action {
		case "join":
			_, err = sessions.ApplyJoinCandidateActive(r.Context(), request.SessionID, request.RecordSetID, request.CandidateID)
		case "refresh":
			_, err = sessions.RefreshRecordSet(r.Context(), request.SessionID, request.RecordSetID)
		default:
			http.Error(w, "unknown result action", http.StatusBadRequest)
			return
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/export", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		format, err := ParseExportFormat(r.URL.Query().Get("format"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := r.URL.Query().Get("recordSetId")
		var output browserExportBuffer
		if err := sessions.ExportRecordsActive(r.Context(), r.Header.Get("X-DataTug-Chat-Session"), id, format, &output); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			} else if errors.Is(err, errBrowserExportTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, err.Error(), status)
			return
		}
		extension := string(format)
		if id == "" && format != ExportXLSX && format != ExportSQLite {
			extension = "zip"
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=datatug-chat-export."+extension)
		_, _ = w.Write(output.Bytes())
	})
	mux.HandleFunc("/v1/chat/queries", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method == http.MethodGet {
			items, err := sessions.ListSavedQueries(r.Context())
			if err != nil {
				http.Error(w, "saved queries unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			SessionID string                `json:"sessionId"`
			Action    string                `json:"action"`
			QueryID   string                `json:"queryId"`
			Variables map[string]string     `json:"variables"`
			Save      SavedQuerySaveRequest `json:"save"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid query action", http.StatusBadRequest)
			return
		}
		var err error
		switch request.Action {
		case "run_dtql":
			err = sessions.RunSavedDTQLActive(r.Context(), request.SessionID, request.QueryID, request.Variables)
		case "save":
			err = sessions.SaveQueryActive(r.Context(), request.SessionID, request.Save)
		default:
			http.Error(w, "unknown query action", http.StatusBadRequest)
			return
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/settings", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method == http.MethodGet {
			count, environment, database, err := sessions.BrowserSettings(r.Context(), r.Header.Get("X-DataTug-Chat-Session"))
			if err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, ErrActiveSessionChanged) {
					status = http.StatusConflict
				}
				http.Error(w, "settings unavailable", status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"versions": count, "environment": environment, "database": database})
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			SessionID string `json:"sessionId"`
			Versions  int    `json:"versions"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid settings", http.StatusBadRequest)
			return
		}
		if err := sessions.SetBrowserVersions(r.Context(), request.SessionID, request.Versions); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrActiveSessionChanged) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/datatug/projects/project_summary", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("id") != sessions.catalog.ID || sessions.catalog.ID == "" {
			http.NotFound(w, r)
			return
		}
		title := sessions.catalog.Title
		if title == "" {
			title = sessions.catalog.ID
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": sessions.catalog.ID, "title": title, "access": "private"})
	})
	mux.HandleFunc("/v1/chat/session", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := sessions.Snapshot(r.Context())
		if err != nil {
			http.Error(w, "unable to load chat", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	mux.HandleFunc("/v1/chat/messages", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.authorize(w, r, token) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			Text      string `json:"text"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil || strings.TrimSpace(request.Text) == "" {
			http.Error(w, "enter a message", http.StatusBadRequest)
			return
		}
		snapshot, err := sessions.Snapshot(r.Context())
		if err != nil || request.SessionID != snapshot.ID {
			http.Error(w, "session changed; refresh chat", http.StatusConflict)
			return
		}
		if _, err := sessions.AskActive(r.Context(), request.SessionID, request.Text); err != nil {
			http.Error(w, "unable to send message", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/events", func(w http.ResponseWriter, r *http.Request) {
		if !bridge.allowedRequest(r) {
			http.Error(w, "origin or host not allowed", http.StatusForbidden)
			return
		}
		validToken := false
		for _, protocol := range websocket.Subprotocols(r) {
			if subtle.ConstantTimeCompare([]byte(protocol), []byte(token)) == 1 {
				validToken = true
			}
		}
		if !validToken {
			http.Error(w, "invalid chat capability", http.StatusUnauthorized)
			return
		}
		upgrader := websocket.Upgrader{Subprotocols: []string{"datatug-chat"}, CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		bridge.connectionsMu.Lock()
		bridge.connections[conn] = struct{}{}
		bridge.connectionsMu.Unlock()
		defer func() {
			bridge.connectionsMu.Lock()
			delete(bridge.connections, conn)
			bridge.connectionsMu.Unlock()
		}()
		conn.SetReadLimit(1024)
		changes, stop := subscribeChanges(sessions)
		defer stop()
		disconnected := make(chan struct{})
		go func() {
			defer close(disconnected)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		if err := writeJSON(conn, map[string]string{"type": "changed"}); err != nil {
			return
		}
		for {
			select {
			case _, ok := <-changes:
				// Defensive: under the real SessionChat.SubscribeChanges,
				// stop() (the only thing that ever closes this specific
				// channel) is deferred right above this loop, so !ok can
				// only fire after the loop has already returned via
				// another case -- see bridgeSubscribeChanges' doc comment.
				// Restored (r6 fix round) after the r5 round wrongly
				// deleted it as provably dead: it's genuinely unreachable
				// through today's SessionChat, but not through every
				// possible bridgeSubscribeChanges override or future
				// SessionChat change, so it stays as a real guard rather
				// than an assumption baked into the loop's shape.
				if !ok {
					return
				}
				if err := writeJSON(conn, map[string]string{"type": "changed"}); err != nil {
					return
				}
			case <-disconnected:
				return
			case <-r.Context().Done():
				return
			}
		}
	})
	bridge.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, BaseContext: bridgeBaseContext}
	go func() { _ = bridge.server.Serve(listener) }()
	return bridge, nil
}

func (b *BrowserBridge) authorize(w http.ResponseWriter, r *http.Request, token string) bool {
	if !b.allowedRequest(r) {
		http.Error(w, "origin or host not allowed", http.StatusForbidden)
		return false
	}
	origin := r.Header.Get("Origin")
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-DataTug-Chat-Capability, X-DataTug-Chat-Session")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Vary", "Origin")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-DataTug-Chat-Capability")), []byte(token)) != 1 {
		http.Error(w, "invalid chat capability", http.StatusUnauthorized)
		return false
	}
	return true
}

func (b *BrowserBridge) allowedRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	localDev := false
	if parsed, err := url.Parse(origin); err == nil {
		localDev = parsed.Scheme == "http" && parsed.Hostname() == "localhost" && parsed.Port() != ""
	}
	if origin != "https://datatug.app" && !localDev {
		return false
	}
	if r.Host != b.listener.Addr().String() && r.Host != fmt.Sprintf("localhost:%d", b.listener.Addr().(*net.TCPAddr).Port) {
		return false
	}
	return true
}

func (b *BrowserBridge) Close() error {
	b.connectionsMu.Lock()
	for connection := range b.connections {
		_ = connection.Close()
	}
	b.connectionsMu.Unlock()
	return b.server.Close()
}
