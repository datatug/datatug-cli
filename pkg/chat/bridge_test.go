package chat

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

func TestBrowserBridgeSharesSessionWithTerminal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	agent := &contextualStub{turns: []Turn{{Text: "Three matching rows.", Queries: []QueryResult{{Title: "Count", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"Count"}, Rows: []secureread.Row{{Data: map[string]any{"Count": 3}}}}}}}, {Text: "Another browser reply."}, {Text: "Terminal reply."}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
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
	if terminal.webLinkVisible {
		t.Fatal("web link was visible before F5")
	}
	_, _ = terminal.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	if !terminal.webLinkVisible || !strings.Contains(terminal.View().Content, "Open web chat") {
		t.Fatal("F5 did not reveal web link")
	}
	_, _ = terminal.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	if terminal.webLinkVisible {
		t.Fatal("second F5 did not hide web link")
	}
	link, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
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
