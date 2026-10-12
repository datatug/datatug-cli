package chat

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatUISlashQuerySingleMatchRunsDirectly(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Prague customers", Type: "DTQL"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query Prague"))
	if service.ranID != "x" {
		t.Fatalf("expected /query to run the single match, ranID=%q", service.ranID)
	}
	view := u.shell.View().Content
	if !strings.Contains(view, "Prague customers") {
		t.Fatalf("expected result in view:\n%s", view)
	}
}

func TestChatUISlashQueryMultipleMatchesLists(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "a", Title: "Prague A"}, {ID: "b", Title: "Prague B"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query Prague"))
	if service.ranID != "" {
		t.Fatalf("expected no query to run yet, ranID=%q", service.ranID)
	}
	view := u.shell.View().Content
	if !strings.Contains(view, "Prague A") || !strings.Contains(view, "Prague B") {
		t.Fatalf("expected both matches listed in view:\n%s", view)
	}
}

func TestChatUISlashQueryNoMatchReportsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query nothing"))
	view := u.shell.View().Content
	if !strings.Contains(view, "no saved project queries match") {
		t.Fatalf("expected no-match error in view:\n%s", view)
	}
}

func TestChatUIQueryWithParametersOpensOverlay(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Customers by city", Parameters: []SavedQueryParameter{{ID: "city", Title: "City", Required: true}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query Customers"))
	view := u.shell.View().Content
	if !strings.Contains(view, "Run saved query") || !strings.Contains(view, "City") {
		t.Fatalf("expected parameters overlay in view:\n%s", view)
	}
}

func TestQueryParametersOverlayRequiredFieldBlocksRun(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Parameters: []SavedQueryParameter{{ID: "city", Required: true}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, service.queries[0])
	d.focus = len(d.inputs) // "Run query"
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd != nil {
		t.Fatal("expected a missing required parameter to block Run, not close/run")
	}
	if d.err == "" {
		t.Fatal("expected a validation error message")
	}
}

func TestQueryParametersOverlayRunsWithVariables(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "By city", Parameters: []SavedQueryParameter{{ID: "city", Required: true}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, service.queries[0])
	d.inputs[0].SetValue("Prague")
	d.focus = 1
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd == nil {
		t.Fatalf("expected Enter on Run to close the overlay and return a run command; err=%q", d.err)
	}
	drainCmd(t, u, cmd)
	if service.ranID != "x" || service.vars["city"] != "Prague" {
		t.Fatalf("expected RunWithVariables(x, city=Prague), got ranID=%q vars=%v", service.ranID, service.vars)
	}
}

func TestQueryParametersOverlayEscCloses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x"})
	_, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("expected Esc to close the query parameters overlay")
	}
}

func TestChatUISavedQueryRunFailureReportsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Broken"}}, runErr: errors.New("boom")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query Broken"))
	view := u.shell.View().Content
	if !strings.Contains(view, "Query failed") {
		t.Fatalf("expected query failure text in view:\n%s", view)
	}
}
