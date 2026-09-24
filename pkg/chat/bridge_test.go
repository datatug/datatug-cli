package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/gorilla/websocket"
)

func TestBrowserBridgeSharesSessionWithTerminal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	agent := &contextualStub{turns: []Turn{{Text: "Three matching rows.", Queries: []QueryResult{{Title: "Count", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"Count"}, Rows: []secureread.Row{{Data: map[string]any{"Count": 3}}}}}}}, {Text: "Another browser reply."}, {Text: "Terminal reply."}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", ProjectCatalog{ID: "demo-project-1"})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := NewSessionUI(ctx, sessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := StartBrowserBridge(sessions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	terminal.SetBrowserURL(bridge.URL)
	var openedURL string
	terminal.openBrowser = func(url string) error { openedURL = url; return nil }
	_, _ = terminal.Update(tea.WindowSizeMsg{Width: 200, Height: 36})
	if terminal.webLinkVisible {
		t.Fatal("web link was visible before F5")
	}
	_, openCmd := terminal.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	if openCmd == nil {
		t.Fatal("F5 did not request opening the browser")
	}
	_, _ = terminal.Update(openCmd())
	if openedURL != bridge.URL || terminal.webLinkVisible {
		t.Fatalf("F5 open result = %q, overlay=%v", openedURL, terminal.webLinkVisible)
	}
	terminal.openBrowser = func(string) error { return errors.New("no browser available") }
	_, openCmd = terminal.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	_, _ = terminal.Update(openCmd())
	visible := ansi.Strip(terminal.View().Content)
	if !terminal.webLinkVisible || !strings.Contains(visible, bridge.URL) {
		t.Fatalf("failed F5 did not show the URL: visible=%v urlBytes=%d", terminal.webLinkVisible, len(bridge.URL))
	}
	_, _ = terminal.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	_, _ = terminal.Update(bridgeTickMsg{})
	if len(terminal.snapshot.Messages) != 3 || !strings.Contains(terminal.View().Content, "Three matching rows") {
		t.Fatal("browser turn did not reach terminal")
	}
	gridIndex := -1
	for index := range terminal.entries {
		if terminal.entries[index].grid != nil {
			gridIndex = index
			break
		}
	}
	if !terminal.focusGrid(gridIndex) {
		t.Fatal("could not focus result grid")
	}
	body, _ = json.Marshal(map[string]string{"text": "Continue", "sessionId": initial.ID})
	if resp := request("POST", "/v1/chat/messages", body, token, "https://datatug.app"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second send: %d", resp.StatusCode)
	}
	_, _ = terminal.Update(bridgeTickMsg{})
	if !terminal.gridFocused || terminal.entries[terminal.activeGrid].recordSetID == "" {
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

func TestBrowserBridgeContinuesWhileDialogsAreOpen(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := NewSessionUI(ctx, sessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	terminal.SetBrowserURL("http://example.test")
	t.Cleanup(func() { terminal.bridgeStop() })
	tests := []struct {
		name  string
		open  func()
		close func()
	}{
		{"query parameters", func() { terminal.queryParameters = &queryParametersDialog{} }, func() { terminal.queryParameters = nil }},
		{"save query", func() { terminal.saveQueryDialog = &saveQueryDialog{} }, func() { terminal.saveQueryDialog = nil }},
		{"HTTP setting", func() { terminal.httpSettingDraft = &httpSettingDraft{} }, func() { terminal.httpSettingDraft = nil }},
		{"connect", func() { terminal.connectDialog = true }, func() { terminal.connectDialog = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.open()
			_, command := terminal.Update(bridgeTickMsg{})
			test.close()
			if command == nil {
				t.Fatal("browser change listener was not rearmed")
			}
		})
	}
}
