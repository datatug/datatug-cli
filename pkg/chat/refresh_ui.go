package chat

import (
	"fmt"
	"net/url"

	tea "charm.land/bubbletea/v2"
)

// refreshSelectedCard refreshes only a focused result/document. It never asks
// the model to reinterpret an existing query.
func (u *UI) refreshSelectedCard() tea.Cmd {
	if u.busy || u.sessions == nil || u.sessions.store == nil {
		return nil
	}
	recordSetID, responseID := "", ""
	if u.gridFocused && u.activeGrid >= 0 && u.activeGrid < len(u.entries) {
		recordSetID = u.entries[u.activeGrid].recordSetID
		if record, ok := u.snapshot.RecordSets[recordSetID]; ok {
			responseID = record.HTTPResponseID
		}
	}
	if u.messageFocused && u.selectedMessage >= 0 && u.selectedMessage < len(u.entries) {
		responseID = u.entries[u.selectedMessage].httpResponseID
	}
	if responseID == "" && recordSetID == "" {
		return nil
	}
	if responseID == "" {
		record, ok := u.snapshot.RecordSets[recordSetID]
		if !ok || record.DTQL == "" {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Saved SQL and HTTP project results cannot be refreshed here. Run the project query again from /query."})
			u.rebuildHistory(true)
			return nil
		}
	}
	sessionID := u.sessionID
	ctx := u.ctx
	if responseID == "" {
		u.busy = true
		return func() tea.Msg {
			snapshot, err := u.sessions.RefreshRecordSet(ctx, sessionID, recordSetID)
			return httpMessage{sessionID: sessionID, snapshot: snapshot, err: err}
		}
	}
	previous, ok := u.snapshot.HTTPResponses[responseID]
	if !ok {
		return nil
	}
	if previous.Method != "" && previous.Method != "GET" {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Refresh is available for GET requests only. Open /http to send another request explicitly."})
		u.rebuildHistory(true)
		return nil
	}
	if previous.RequestHasQuery {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "This HTTP request had URL parameters that were deliberately not saved. Run /http get again to refresh it."})
		u.rebuildHistory(true)
		return nil
	}
	parsed, err := url.ParseRequestURI(previous.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "The saved HTTP URL cannot be refreshed safely."})
		u.rebuildHistory(true)
		return nil
	}
	u.busy = true
	store := u.sessions.store
	origin, _ := httpOrigin(previous.URL)
	settings, err := store.HTTPRequestSettings(ctx, origin)
	if err != nil {
		u.busy = false
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't load HTTP request settings."})
		u.rebuildHistory(true)
		return nil
	}
	return func() tea.Msg {
		response, query, failure := fetchHTTPResult(ctx, previous.URL, previous.URL, settings)
		origin, err := store.AppendUser(ctx, sessionID, "Refresh: GET "+previous.URL)
		if err != nil {
			return httpMessage{sessionID: sessionID, err: err}
		}
		if failure != "" {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, previous.URL, Turn{Text: failure})
		} else {
			response.RefreshParentID = previous.ID
			_, err = store.AppendHTTPResponse(ctx, sessionID, origin.ID, response, query)
		}
		if err != nil {
			return httpMessage{sessionID: sessionID, err: fmt.Errorf("save refresh: %w", err)}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return httpMessage{sessionID: sessionID, snapshot: snapshot, err: err}
	}
}
