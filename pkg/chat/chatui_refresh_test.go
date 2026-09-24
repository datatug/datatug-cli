package chat

// Ported from refresh_test.go's three UI-dependent cases (the other five
// never touched UI/ChatUI and stay in refresh_test.go unchanged).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestChatUIHTTPRefreshAddsChangedImmutableVersion is ported from
// TestHTTPRefreshAddsChangedImmutableVersion: Ctrl+R (globalKeys'
// refreshLastRecordSet, chatui_pickers.go) re-fetches a focused HTTP
// result and the new grid's versionBadge reflects the change.
func TestChatUIHTTPRefreshAddsChangedImmutableVersion(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
		} else {
			_, _ = w.Write([]byte(`[{"name":"Lin"}]`))
		}
	}))
	defer server.Close()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("initial HTTP grid not focusable")
	}
	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !consumed || cmd == nil {
		t.Fatal("Ctrl+R did not trigger refresh")
	}
	drainCmd(t, u, cmd)
	if requests.Load() != 2 || len(u.snapshot.HTTPResponses) != 2 || len(u.snapshot.RecordSets) != 2 {
		t.Fatalf("refresh versions not saved: requests=%d HTTP=%d records=%d", requests.Load(), len(u.snapshot.HTTPResponses), len(u.snapshot.RecordSets))
	}
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("refreshed grid not focusable")
	}
	g, _, ok := u.activeGrid()
	if !ok || g.versionBadge != "changed" {
		t.Fatal("refreshed card did not indicate changed data")
	}
}

// TestChatUISavedNonDTQLResultDoesNotOfferRefresh is ported from
// TestSavedNonDTQLResultDoesNotOfferRefresh.
func TestChatUISavedNonDTQLResultDoesNotOfferRefresh(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	origin, err := store.AppendUser(ctx, u.sessionID, "Run saved SQL")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendQuery(ctx, u.sessionID, origin.ID, "sqlite:///fixture.db", QueryResult{Title: "Saved SQL", Source: "sqlite:///fixture.db", Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(ctx, u.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("saved result was not focusable")
	}
	if strings.Contains(u.statusBar(160), "Ctrl+R refresh") {
		t.Fatal("unsupported result advertised refresh")
	}
	if cmd := u.refreshLastRecordSet(); cmd != nil || u.shell.Busy() {
		t.Fatal("unsupported result attempted a refresh")
	}
	if !strings.Contains(u.shell.View().Content, "Saved SQL and HTTP") {
		t.Fatal("unsupported refresh did not explain how to rerun the saved query")
	}
}

// TestChatUIHTTPVersionsAcrossDocumentKeepOneVisibleGrid is ported from
// TestHTTPVersionsAcrossDocumentKeepOneVisibleGrid: loadSession's
// hiddenRefreshVersions filtering (shared with ui.go) keeps only the
// latest kept version's grid visible in the transcript.
func TestChatUIHTTPVersionsAcrossDocumentKeepOneVisibleGrid(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "Versions")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetResultVersionsToKeep(ctx, 1); err != nil {
		t.Fatal(err)
	}
	previousResponseID, firstRecordID := "", ""
	for i := 0; i < 3; i++ {
		origin, err := store.AppendUser(ctx, session.ID, "HTTP version")
		if err != nil {
			t.Fatal(err)
		}
		response := HTTPResponse{URL: "https://example.test/data", StatusCode: 200, ContentType: "application/json", Body: []byte(`[{"id":1}]`), RefreshParentID: previousResponseID}
		var query *QueryResult
		if i != 1 {
			query = &QueryResult{Title: "Data", Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}}}
		}
		stored, err := store.AppendHTTPResponse(ctx, session.ID, origin.ID, response, query)
		if err != nil {
			t.Fatal(err)
		}
		previousResponseID = stored.ID
		if i == 0 {
			firstRecordID = query.RecordSetID
		}
		if i == 2 && query.RefreshParentID != firstRecordID {
			t.Fatalf("table refresh lineage skipped document: %q", query.RefreshParentID)
		}
	}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	if visible := len(u.gridsByRecordSetID); visible != 1 || len(u.snapshot.RecordSets) != 2 || len(u.snapshot.HTTPResponses) != 3 {
		t.Fatalf("visible=%d saved records=%d saved HTTP=%d", visible, len(u.snapshot.RecordSets), len(u.snapshot.HTTPResponses))
	}
}
