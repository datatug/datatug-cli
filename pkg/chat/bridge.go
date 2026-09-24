package chat

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// BrowserBridge exposes the active CLI chat to a browser on a loopback-only port.
// The capability stays in the web URL fragment and is sent in a request header.
type BrowserBridge struct {
	server        *http.Server
	listener      net.Listener
	URL           string
	connectionsMu sync.Mutex
	connections   map[*websocket.Conn]struct{}
}

func StartBrowserBridge(sessions *SessionChat) (*BrowserBridge, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("create chat bridge capability: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:3284")
	if err != nil {
		// Multiple local chats can coexist; the link carries the actual port.
		listener, err = net.Listen("tcp", "127.0.0.1:0")
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
	mux := http.NewServeMux()
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
		changes, stop := sessions.SubscribeChanges()
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
		if err := conn.WriteJSON(map[string]string{"type": "changed"}); err != nil {
			return
		}
		for {
			select {
			case _, ok := <-changes:
				if !ok {
					return
				}
				if err := conn.WriteJSON(map[string]string{"type": "changed"}); err != nil {
					return
				}
			case <-disconnected:
				return
			case <-r.Context().Done():
				return
			}
		}
	})
	bridge.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
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
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-DataTug-Chat-Capability")
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
