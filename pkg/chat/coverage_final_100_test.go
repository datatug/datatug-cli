package chat

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/tui/chatshell"
)

// 1. refresh.go:72: hiddenRefreshVersions with keep < 1
func TestRefreshHiddenVersionsCoverageRemaining(t *testing.T) {
	session := ChatSession{
		ID: "s1",
		RecordSets: map[string]RecordSet{
			"rs1": {ID: "rs1", Title: "RS1"},
		},
	}
	hRec, hHTTP := hiddenRefreshVersions(session, 0)
	if len(hRec) != 0 || len(hHTTP) != 0 {
		t.Fatalf("expected empty hidden maps, got %v, %v", hRec, hHTTP)
	}
}

// 2. store.go:641: loadWorkspace error when DB query fails (e.g. closed DB)
func TestStoreWorkspaceLoadError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session := ChatSession{ID: "s1"}
	_ = store.db.Close()
	err := store.loadWorkspace(ctx, &session)
	if err == nil {
		t.Fatal("expected error from loadWorkspace with closed db")
	}
}

// 3. saved_query_lookup.go uncovered branches
func TestSavedQueryLookupCoverageRemaining(t *testing.T) {
	// DiscoverSavedQueryLookup with empty args
	if got := DiscoverSavedQueryLookup(ForeignKeySnapshot{}, "", ""); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	// Multiple matching FKs in DiscoverSavedQueryLookup
	multiFK := ForeignKeySnapshot{
		Source: "s1",
		Keys: []ForeignKey{
			{Schema: "dbo", FromRelation: "Orders", FromFields: []string{"CustomerID"}, ToRelation: "Customers", ToFields: []string{"CustomerID"}},
			{Schema: "dbo", FromRelation: "Orders", FromFields: []string{"CustomerID"}, ToRelation: "Clients", ToFields: []string{"ClientID"}},
		},
		Columns: map[string][]string{
			"Customers": {"CustomerID", "Name"},
			"Clients":   {"ClientID", "Name"},
		},
	}
	if got := DiscoverSavedQueryLookup(multiFK, "dbo.Orders", "CustomerID"); got != nil {
		t.Fatalf("expected nil for ambiguous FKs, got %v", got)
	}

	// DiscoverSavedQueryParameterLookupWithMode with empty args
	if got := DiscoverSavedQueryParameterLookupWithMode(nil, ForeignKeySnapshot{}, "", "", "", false); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	// Invalid YAML in doc
	validSnapshot := ForeignKeySnapshot{
		Source: "s1",
		Keys: []ForeignKey{
			{Schema: "dbo", FromRelation: "Orders", FromFields: []string{"CustomerID"}, ToSchema: "dbo", ToRelation: "Customers", ToFields: []string{"CustomerID"}},
		},
		Columns: map[string][]string{
			"dbo.customers": {"CustomerID", "Name"},
		},
	}
	if got := DiscoverSavedQueryParameterLookupWithMode([]byte(":\n:invalid yaml"), validSnapshot, "cid", "dbo.Customers", "CustomerID", false); got != nil {
		t.Fatalf("expected nil for invalid yaml, got %v", got)
	}

	// multi=true with Op="="
	eqDoc := []byte(`
from:
  schema: dbo
  name: Orders
columns:
  - field: CustomerID
where:
  op: "=="
  left:
    field: CustomerID
  right:
    param: cid
`)
	if got := DiscoverSavedQueryParameterLookupWithMode(eqDoc, validSnapshot, "cid", "dbo.Customers", "CustomerID", true); got != nil {
		t.Fatalf("expected nil for multi=true with = op, got %v", got)
	}

	// multi=false with Op="In"
	inDoc := []byte(`
from:
  schema: dbo
  name: Orders
columns:
  - field: CustomerID
where:
  op: "In"
  left:
    field: CustomerID
  right:
    param: cid
`)
	if got := DiscoverSavedQueryParameterLookupWithMode(inDoc, validSnapshot, "cid", "dbo.Customers", "CustomerID", false); got != nil {
		t.Fatalf("expected nil for multi=false with In op, got %v", got)
	}

	// Invalid DTQL in doc
	invalidDTQLDoc := []byte(`
syntax: not_a_real_dtql_version_or_kind
where:
  op: "=="
  left:
    field: CustomerID
  right:
    param: cid
`)
	_ = DiscoverSavedQueryParameterLookupWithMode(invalidDTQLDoc, validSnapshot, "cid", "dbo.Customers", "CustomerID", false)

	// parameter in Left, field in Right (line 93)
	leftParamDoc := []byte(`
from:
  schema: dbo
  name: Orders
columns:
  - field: CustomerID
where:
  op: "=="
  left:
    param: cid
  right:
    field: CustomerID
`)
	if got := DiscoverSavedQueryParameterLookupWithMode(leftParamDoc, validSnapshot, "cid", "dbo.Customers", "CustomerID", false); got == nil {
		t.Fatalf("expected valid plan for left param, got nil")
	}

	// bound.Field == "" (line 98)
	noFieldDoc := []byte(`
from:
  schema: dbo
  name: Orders
where:
  op: "=="
  left:
    field: ""
  right:
    param: cid
`)
	if got := DiscoverSavedQueryParameterLookupWithMode(noFieldDoc, validSnapshot, "cid", "dbo.Customers", "CustomerID", false); got != nil {
		t.Fatalf("expected nil for empty bound field, got %v", got)
	}

	// bound.Source != "" and doesn't match query.From.Alias or query.From.Name (line 98)
	badSourceDoc := []byte(`
from:
  schema: dbo
  name: Orders
where:
  op: "=="
  left:
    field: CustomerID
    source: OtherTable
  right:
    param: cid
`)
	if got := DiscoverSavedQueryParameterLookupWithMode(badSourceDoc, validSnapshot, "cid", "dbo.Customers", "CustomerID", false); got != nil {
		t.Fatalf("expected nil for mismatched source, got %v", got)
	}

	// Multiple matching FKs in DiscoverSavedQueryParameterLookupWithMode (line 107)
	dupFKSnapshot := ForeignKeySnapshot{
		Source: "s1",
		Keys: []ForeignKey{
			{Schema: "dbo", FromRelation: "Orders", FromFields: []string{"CustomerID"}, ToSchema: "dbo", ToRelation: "Customers", ToFields: []string{"CustomerID"}},
			{Schema: "dbo", FromRelation: "Orders", FromFields: []string{"CustomerID"}, ToSchema: "dbo", ToRelation: "Customers", ToFields: []string{"CustomerID"}},
		},
		Columns: map[string][]string{
			"dbo.Customers": {"CustomerID"},
		},
	}
	if got := DiscoverSavedQueryParameterLookupWithMode(eqDoc, dupFKSnapshot, "cid", "dbo.Customers", "CustomerID", false); got != nil {
		t.Fatalf("expected nil for ambiguous FK matches, got %v", got)
	}

	// savedQueryLookupPlan with nil or invalid match (line 142)
	if got := savedQueryLookupPlan(validSnapshot, nil); got != nil {
		t.Fatalf("expected nil for nil match, got %v", got)
	}
	invalidMatch := &ForeignKey{
		FromFields: []string{},
	}
	if got := savedQueryLookupPlan(validSnapshot, invalidMatch); got != nil {
		t.Fatalf("expected nil for invalid match, got %v", got)
	}

	// savedQueryLookupPlan where match.ToFields[0] is not in columns (line 153)
	missingColSnapshot := ForeignKeySnapshot{
		Source: "s1",
		Columns: map[string][]string{
			"dbo.Customers": {"OtherCol"},
		},
	}
	matchMissingCol := &ForeignKey{
		FromFields: []string{"CustomerID"},
		ToSchema:   "dbo",
		ToRelation: "Customers",
		ToFields:   []string{"CustomerID"},
	}
	if got := savedQueryLookupPlan(missingColSnapshot, matchMissingCol); got != nil {
		t.Fatalf("expected nil for missing key column, got %v", got)
	}
}

// 4. session_chat_settings.go
func TestSessionChatSettingsCoverageRemaining(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)

	// BrowserSettings: activeID mismatch
	if _, _, _, err := chat.BrowserSettings(ctx, "wrong-id"); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// SetBrowserVersions: activeID mismatch
	if err := chat.SetBrowserVersions(ctx, "wrong-id", 5); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// SetBrowserVersions: success
	if err := chat.SetBrowserVersions(ctx, chat.activeID, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Close store for error branches
	_ = store.Close()
	if _, _, _, err := chat.BrowserSettings(ctx, chat.activeID); err == nil {
		t.Fatal("expected error on closed store in BrowserSettings")
	}
	if err := chat.SetBrowserVersions(ctx, chat.activeID, 10); err == nil {
		t.Fatal("expected error on closed store in SetBrowserVersions")
	}
}

// 5. session_chat_http.go
func TestSessionChatHTTPCoverageRemaining(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)

	// BrowserHTTPSettings
	// 1) activeID mismatch
	if _, err := chat.BrowserHTTPSettings(ctx, "wrong-id", ""); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) invalid URL
	if _, err := chat.BrowserHTTPSettings(ctx, chat.activeID, "http://user:pass@example.com"); err == nil {
		t.Fatal("expected error for url with credentials")
	}
	// 3) success with settings
	_ = store.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.com", "Authorization", "Bearer tok")
	_ = store.SetHTTPRequestSetting(ctx, "project", "cookie", "https://api.example.com", "session_id", "abc")
	items, err := chat.BrowserHTTPSettings(ctx, chat.activeID, "https://api.example.com/v1")
	if err != nil || len(items) == 0 {
		t.Fatalf("expected items, got len %d, err %v", len(items), err)
	}

	// ChangeBrowserHTTPSetting
	// 1) activeID mismatch
	if err := chat.ChangeBrowserHTTPSetting(ctx, "wrong-id", "set", "cli", "header", "", "H", "V"); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) bad scope
	if err := chat.ChangeBrowserHTTPSetting(ctx, chat.activeID, "set", "bad-scope", "header", "", "H", "V"); err == nil {
		t.Fatal("expected bad scope error")
	}
	// 3) bad URL
	if err := chat.ChangeBrowserHTTPSetting(ctx, chat.activeID, "set", "cli", "header", "http://user:pass@example.com", "H", "V"); err == nil {
		t.Fatal("expected bad URL error")
	}
	// 4) bad action
	if err := chat.ChangeBrowserHTTPSetting(ctx, chat.activeID, "unknown-action", "cli", "header", "", "H", "V"); err == nil {
		t.Fatal("expected unknown action error")
	}
	// 5) set success
	if err := chat.ChangeBrowserHTTPSetting(ctx, chat.activeID, "set", "cli", "header", "https://api.example.com", "X-Test", "123"); err != nil {
		t.Fatalf("unexpected set error: %v", err)
	}
	// 6) remove success
	if err := chat.ChangeBrowserHTTPSetting(ctx, chat.activeID, "remove", "cli", "header", "https://api.example.com", "X-Test", ""); err != nil {
		t.Fatalf("unexpected remove error: %v", err)
	}

	// SendHTTPRequestActive
	// 1) activeID mismatch
	if err := chat.SendHTTPRequestActive(ctx, "wrong-id", BrowserHTTPRequest{}); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) unsupported method
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{Method: "UNSUPPORTED", URL: "https://example.com"}); err == nil {
		t.Fatal("expected unsupported method error")
	}
	// 3) GET with body
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{Method: "GET", URL: "https://example.com", Body: "payload"}); err == nil {
		t.Fatal("expected GET with body error")
	}
	// 4) body too large
	largeBody := strings.Repeat("A", maxHTTPRequestBytes+10)
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{Method: "POST", URL: "https://example.com", Body: largeBody}); err == nil {
		t.Fatal("expected body too large error")
	}
	// 5) invalid URL / credentials
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{Method: "POST", URL: "http://user:pass@example.com"}); err == nil {
		t.Fatal("expected invalid url error")
	}
	// 6) invalid header validation
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{
		Method:  "GET",
		URL:     "https://example.com",
		Headers: map[string]string{"Invalid Header\n": "val"},
	}); err == nil {
		t.Fatal("expected invalid header error")
	}
	// 7) replaceHeaders = true with successful execution
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{
		Method:         "GET",
		URL:            ts.URL + "/data",
		ReplaceHeaders: true,
		Headers:        map[string]string{"Accept": "application/json"},
	}); err != nil {
		t.Fatalf("unexpected SendHTTPRequestActive error: %v", err)
	}

	// 8) failure != "" -> AppendTurn (connect to a closed/offline port)
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{
		Method: "GET",
		URL:    "http://127.0.0.1:9999/does-not-exist",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Store errors in HTTP methods
	_ = store.Close()
	if _, err := chat.BrowserHTTPSettings(ctx, chat.activeID, ""); err == nil {
		t.Fatal("expected error on closed store in BrowserHTTPSettings")
	}
	if err := chat.ChangeBrowserHTTPSetting(ctx, chat.activeID, "set", "cli", "header", "", "H", "V"); err == nil {
		t.Fatal("expected error on closed store in ChangeBrowserHTTPSetting")
	}
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{Method: "GET", URL: "https://example.com"}); err == nil {
		t.Fatal("expected error on closed store in SendHTTPRequestActive")
	}
}

// 6. session_chat.go queries, actions, joins, export
type mockSavedQueryService struct {
	queries    []SavedQuery
	listErr    error
	runHTTPRes QueryResult
	runHTTPErr error
	runDTQLRes QueryResult
	runDTQLErr error
	savedQuery SavedQuery
	saveErr    error
}

func (m *mockSavedQueryService) List(ctx context.Context) ([]SavedQuery, error) {
	return m.queries, m.listErr
}
func (m *mockSavedQueryService) Run(ctx context.Context, id string) (QueryResult, error) {
	return QueryResult{}, nil
}
func (m *mockSavedQueryService) RunHTTPWithVariables(ctx context.Context, id string, vars map[string]string) (QueryResult, error) {
	return m.runHTTPRes, m.runHTTPErr
}
func (m *mockSavedQueryService) RunDTQLWithVariables(ctx context.Context, id string, vars map[string]string) (QueryResult, error) {
	return m.runDTQLRes, m.runDTQLErr
}
func (m *mockSavedQueryService) Save(ctx context.Context, req SavedQuerySaveRequest) (SavedQuery, error) {
	return m.savedQuery, m.saveErr
}

type plainSavedQueryService struct {
	queries []SavedQuery
	listErr error
}

func (p plainSavedQueryService) List(ctx context.Context) ([]SavedQuery, error) {
	return p.queries, p.listErr
}
func (p plainSavedQueryService) Run(ctx context.Context, id string) (QueryResult, error) {
	return QueryResult{}, nil
}

func TestSessionChatQueriesAndActionsCoverageRemaining(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)

	// ListSavedQueries with nil service
	if _, err := chat.ListSavedQueries(ctx); err == nil {
		t.Fatal("expected error with nil service in ListSavedQueries")
	}

	mockSvc := &mockSavedQueryService{
		queries: []SavedQuery{
			{ID: "q1", Title: "Query 1", Type: "DTQL", Parameters: []SavedQueryParameter{{ID: "p1"}}},
			{ID: "q2", Title: "", Type: "HTTP"},
		},
	}
	chat.ConfigureSavedQueryService(mockSvc)
	list, err := chat.ListSavedQueries(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 queries, got %d, err %v", len(list), err)
	}

	// RunSavedDTQLActive / RunSavedHTTPActive:
	// 1) session mismatch
	if err := chat.RunSavedDTQLActive(ctx, "wrong-id", "q1", nil); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) service == nil
	chat.ConfigureSavedQueryService(nil)
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected error with nil service")
	}
	// 3) service.List error
	mockSvc.listErr = errors.New("list failed")
	chat.ConfigureSavedQueryService(mockSvc)
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected list error")
	}
	mockSvc.listErr = nil

	// 4) selected == nil or selected.Type != queryType
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "nonexistent", nil); err == nil {
		t.Fatal("expected not found error")
	}
	if err := chat.RunSavedHTTPActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected type mismatch error")
	}

	// 5) unknown query parameter
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", map[string]string{"unknown_param": "val"}); err == nil {
		t.Fatal("expected unknown query parameter error")
	}

	// 6) HTTP runner is unavailable on plain service
	chat.ConfigureSavedQueryService(plainSavedQueryService{queries: mockSvc.queries})
	if err := chat.RunSavedHTTPActive(ctx, chat.activeID, "q2", nil); err == nil {
		t.Fatal("expected HTTP runner unavailable")
	}
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected DTQL runner unavailable")
	}
	chat.ConfigureSavedQueryService(mockSvc)

	// 7) runner error
	mockSvc.runDTQLErr = errors.New("dtql failed")
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected query failed error")
	}
	mockSvc.runDTQLErr = nil

	// 8) success for DTQL and HTTP (empty result.Title -> fallback to selected.ID when selected.Title is empty)
	mockSvc.runDTQLRes = QueryResult{Title: "", Source: "sqlite:///chinook.db"}
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", map[string]string{"p1": "val"}); err != nil {
		t.Fatalf("unexpected RunSavedDTQLActive error: %v", err)
	}
	mockSvc.runHTTPRes = QueryResult{Title: "", Source: "https://api.example.com"}
	if err := chat.RunSavedHTTPActive(ctx, chat.activeID, "q2", nil); err != nil {
		t.Fatalf("unexpected RunSavedHTTPActive error: %v", err)
	}

	// SaveQueryActive
	// 1) session mismatch
	if err := chat.SaveQueryActive(ctx, "wrong-id", SavedQuerySaveRequest{}); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) service is not SavedQueryWriter
	chat.ConfigureSavedQueryService(plainSavedQueryService{})
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T"}); err == nil {
		t.Fatal("expected not SavedQueryWriter error")
	}
	chat.ConfigureSavedQueryService(mockSvc)
	// 3) empty title
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: ""}); err == nil {
		t.Fatal("expected empty title error")
	}
	// 4) unsupported query type
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T", Type: "SQL"}); err == nil {
		t.Fatal("expected unsupported type error")
	}
	// 5) empty query text
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T", Type: "DTQL", Text: ""}); err == nil {
		t.Fatal("expected empty text error")
	}
	// 6) HTTP with invalid origin
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T", Type: "HTTP", Text: "invalid-url"}); err == nil {
		t.Fatal("expected invalid url error")
	}
	// 7) HTTP with query parameters
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T", Type: "HTTP", Text: "https://example.com/api?a=1"}); err == nil {
		t.Fatal("expected HTTP query parameters error")
	}
	// 8) writer.Save error
	mockSvc.saveErr = errors.New("save failed")
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T", Type: "DTQL", Text: "SELECT 1"}); err == nil {
		t.Fatal("expected save failed error")
	}
	mockSvc.saveErr = nil
	// 9) success
	if err := chat.SaveQueryActive(ctx, chat.activeID, SavedQuerySaveRequest{Title: "T", Type: "DTQL", Text: "SELECT 1"}); err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}

	// JoinCandidatesActive
	// 1) session mismatch
	if _, err := chat.JoinCandidatesActive(ctx, "wrong-id", "rs1"); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) joinApplication == nil
	if cands, err := chat.JoinCandidatesActive(ctx, chat.activeID, "rs1"); err != nil || len(cands) != 0 {
		t.Fatalf("expected empty candidates, got %v, %v", cands, err)
	}
	// 3) recordSet not found
	chat.ConfigureJoinApplication(stubJoinApplication{})
	if _, err := chat.JoinCandidatesActive(ctx, chat.activeID, "nonexistent-rs"); err == nil {
		t.Fatal("expected recordSet not found error")
	}
	// 4) success
	// Append a query with a recordset to active session
	user, _ := store.AppendUser(ctx, chat.activeID, "user prompt")
	_, _ = store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///chinook.db", QueryResult{
		Title: "Q",
		Result: secureread.Result{
			Columns: []string{"id"},
			Rows:    []secureread.Row{{Data: map[string]any{"id": "1"}}},
		},
	})
	sess, _ := store.Load(ctx, chat.activeID)
	var validRSID string
	for id := range sess.RecordSets {
		validRSID = id
		break
	}
	chat.ConfigureJoinApplication(stubJoinApplication{
		candidates: []JoinCandidate{{ID: JoinCandidateID("c1")}},
	})
	cands, err := chat.JoinCandidatesActive(ctx, chat.activeID, validRSID)
	if err != nil || len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %v, %v", cands, err)
	}

	// ApplyJoinCandidateActive
	// 1) session mismatch
	if _, err := chat.ApplyJoinCandidateActive(ctx, "wrong-id", validRSID, "c1"); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) success
	chat.ConfigureJoinApplication(stubJoinApplication{
		applyResult: QueryResult{
			Title: "Joined",
			Result: secureread.Result{
				Columns: []string{"id", "title"},
				Rows:    []secureread.Row{{Data: map[string]any{"id": "1", "title": "Album"}}},
			},
		},
	})
	if _, err := chat.ApplyJoinCandidateActive(ctx, chat.activeID, validRSID, "c1"); err != nil {
		t.Fatalf("unexpected error in ApplyJoinCandidateActive: %v", err)
	}

	// BrowserSessionAction branches
	// "new"
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "new", ""); err != nil {
		t.Fatalf("unexpected new session error: %v", err)
	}
	// "switch" empty value
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "switch", ""); err == nil {
		t.Fatal("expected empty value error in switch")
	}
	// "switch" nonexistent session
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "switch", "nonexistent"); err == nil {
		t.Fatal("expected load error in switch")
	}
	// "rename" empty value
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "rename", "   "); err == nil {
		t.Fatal("expected empty title error in rename")
	}
	// "clear" without confirm
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "clear", "no"); err == nil {
		t.Fatal("expected confirm error in clear")
	}
	// "clear" with confirm
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "clear", "confirm"); err != nil {
		t.Fatalf("unexpected clear error: %v", err)
	}
	// "delete" without confirm
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "no"); err == nil {
		t.Fatal("expected confirm error in delete")
	}
	// "delete" with confirm
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm"); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	// unknown action
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "unknown", ""); err == nil {
		t.Fatal("expected unknown action error")
	}

	// ExportRecordsActive
	// 1) session mismatch
	if err := chat.ExportRecordsActive(ctx, "wrong-id", "", ExportCSV, nil); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// 2) empty bucket and empty recordSetID
	if err := chat.ExportRecordsActive(ctx, chat.activeID, "", ExportCSV, nil); err == nil {
		t.Fatal("expected empty export bucket error")
	}
	// 3) recordSetID not found
	if err := chat.ExportRecordsActive(ctx, chat.activeID, "nonexistent", ExportCSV, nil); err == nil {
		t.Fatal("expected record unavailable error")
	}

	// Additional closed store error paths:
	// store error in JoinCandidatesActive / ApplyJoinCandidateActive
	_ = store.Close()
	if _, err := chat.JoinCandidatesActive(ctx, chat.activeID, validRSID); err == nil {
		t.Fatal("expected error on closed store in JoinCandidatesActive")
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "new", ""); err == nil {
		t.Fatal("expected error on closed store in SessionAction new")
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "rename", "New Title"); err == nil {
		t.Fatal("expected error on closed store in SessionAction rename")
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "clear", "confirm"); err == nil {
		t.Fatal("expected error on closed store in SessionAction clear")
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm"); err == nil {
		t.Fatal("expected error on closed store in SessionAction delete")
	}
	if err := chat.ExportRecordsActive(ctx, chat.activeID, "", ExportCSV, nil); err == nil {
		t.Fatal("expected error on closed store in ExportRecordsActive")
	}
}

// 7. Ask and StreamAsk error persistence (lines 907 and 1030)
func TestSessionChatAskAndStreamAskAppendError(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)

	origTxCommit := txCommitFn
	defer func() { txCommitFn = origTxCommit }()

	// Trigger AppendTurn failure in Ask
	commitCount := 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("append turn tx commit fail")
		}
		return origTxCommit(tx)
	}
	if _, err := chat.Ask(ctx, "hello"); err == nil {
		t.Fatal("expected Ask error when AppendTurn fails")
	}

	// Trigger AppendTurn failure in StreamAsk
	streamChat, _ := newTestSessionChat(t)
	streamingAgent := &streamingStub{
		events: []ai.Event{
			{Type: ai.EventTextDelta, Text: "streaming response"},
			{Type: ai.EventCompleted},
		},
		last: Turn{Text: "streaming response"},
	}
	streamChat.agent = streamingAgent
	commitCount = 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("append turn tx commit fail")
		}
		return origTxCommit(tx)
	}
	seq, drain := streamChat.StreamAsk(ctx, "hello")
	for range seq {
	}
	if _, err := drain(); err == nil {
		t.Fatal("expected StreamAsk error when AppendTurn fails")
	}
}

func TestRefreshRecordSetBranches(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	// queryExecutor nil
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, "rs1"); err == nil {
		t.Fatal("expected error with nil queryExecutor")
	}

	exec := &fakeExecutor{result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}}}
	chat.ConfigureQueryExecutor(exec)

	// activeID mismatch
	if _, err := chat.RefreshRecordSet(ctx, "wrong-id", "rs1"); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatal("expected ErrActiveSessionChanged")
	}

	// recordSet not found
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, "nonexistent"); err == nil {
		t.Fatal("expected record unavailable error")
	}

	// record with HTTPResponseID != ""
	user, _ := store.AppendUser(ctx, chat.activeID, "hello")
	qHTTP, _ := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///chinook.db", QueryResult{Title: "Q1", Result: secureread.Result{Columns: []string{"id"}}, HTTPResponseID: "resp1"})
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qHTTP.RecordSetID); err == nil {
		t.Fatal("expected unavailable for refresh error for HTTP response record")
	}

	// record with unallowed source
	qBadSource, _ := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///unallowed.db", QueryResult{Title: "Q2", Result: secureread.Result{Columns: []string{"id"}}})
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qBadSource.RecordSetID); err == nil {
		t.Fatal("expected unallowed source error")
	}

	// record with invalid DTQL
	qBadDTQL, _ := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///chinook.db", QueryResult{Title: "Q3", DTQL: "invalid: [", Result: secureread.Result{Columns: []string{"id"}}})
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qBadDTQL.RecordSetID); err == nil {
		t.Fatal("expected invalid DTQL error")
	}

	// record with valid DTQL but RunDTQL fails
	validDTQL := "from: {name: Invoice}\nlimit: 10\n"
	qValid, _ := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///chinook.db", QueryResult{Title: "Q4", DTQL: validDTQL, Result: secureread.Result{Columns: []string{"id"}}})
	exec.err = errors.New("runtime failure")
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qValid.RecordSetID); err == nil {
		t.Fatal("expected query failed error")
	}
	exec.err = nil

	// store.AppendUser error
	origTxCommit := txCommitFn
	defer func() { txCommitFn = origTxCommit }()
	txCommitFn = func(tx *sql.Tx) error { return errors.New("append user tx fail") }
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qValid.RecordSetID); err == nil {
		t.Fatal("expected AppendUser error")
	}

	// store.AppendQuery error
	commitCount := 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("append query tx fail")
		}
		return origTxCommit(tx)
	}
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qValid.RecordSetID); err == nil {
		t.Fatal("expected AppendQuery error")
	}
	txCommitFn = origTxCommit

	// store closed -> Load error
	_ = store.Close()
	if _, err := chat.RefreshRecordSet(ctx, chat.activeID, qValid.RecordSetID); err == nil {
		t.Fatal("expected closed store error")
	}
}

func TestSessionChatMoreBranches(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)

	// runSavedQueryActive: AppendUser error and AppendQuery error
	mockSvc := &mockSavedQueryService{
		queries:    []SavedQuery{{ID: "q1", Title: "Q", Type: "DTQL"}},
		runDTQLRes: QueryResult{Title: "T", Source: "sqlite:///chinook.db"},
	}
	chat.ConfigureSavedQueryService(mockSvc)

	origTxCommit := txCommitFn
	defer func() { txCommitFn = origTxCommit }()

	txCommitFn = func(tx *sql.Tx) error { return errors.New("user fail") }
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected AppendUser error in RunSavedDTQLActive")
	}

	commitCount := 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("query fail")
		}
		return origTxCommit(tx)
	}
	if err := chat.RunSavedDTQLActive(ctx, chat.activeID, "q1", nil); err == nil {
		t.Fatal("expected AppendQuery error in RunSavedDTQLActive")
	}
	txCommitFn = origTxCommit

	// BrowserSessionAction "switch": store.Activate fails
	origExec := execContextFn
	execContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		return nil, errors.New("activate fail")
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "switch", chat.activeID); err == nil {
		t.Fatal("expected activate error in switch")
	}
	execContextFn = origExec

	// BrowserSessionAction "delete": store.Activate or store.Create fails
	commitCount = 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("activate fail after delete")
		}
		return origTxCommit(tx)
	}
	_, _ = store.Create(ctx, "Second")
	_ = chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm")
	txCommitFn = origTxCommit

	// SendHTTPRequestActive: AppendUser error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer ts.Close()

	txCommitFn = func(tx *sql.Tx) error { return errors.New("user fail") }
	if err := chat.SendHTTPRequestActive(ctx, chat.activeID, BrowserHTTPRequest{Method: "GET", URL: ts.URL}); err == nil {
		t.Fatal("expected AppendUser error in SendHTTPRequestActive")
	}
	txCommitFn = origTxCommit
}

func TestGridFullCoverage(t *testing.T) {
	// sanitizeMultilineText with control characters <= 0x1f
	s := sanitizeMultilineText("hello\x01world")
	if !strings.Contains(s, "hello world") {
		t.Fatalf("expected sanitized control char, got %q", s)
	}

	// truncateGridText with width <= 0 and StringWidth <= width
	if got := truncateGridText("abc", 0); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
	if got := truncateGridText("abc", 10); got != "abc" {
		t.Fatalf("expected abc, got %q", got)
	}

	// formatGridValue with invalid RFC3339 string and bytes
	if got := formatGridValue("date", "not-a-date"); got != "not-a-date" {
		t.Fatalf("expected not-a-date, got %q", got)
	}
	if got := formatGridValue("date", []byte("not-a-date")); got != "not-a-date" {
		t.Fatalf("expected not-a-date, got %q", got)
	}
	if got := formatGridValue("num", 12345); got != "12345" {
		t.Fatalf("expected 12345, got %q", got)
	}

	// isDateColumn
	if !isDateColumn("order_date") {
		t.Fatal("expected order_date to be recognized as date column")
	}
	if isDateColumn("candidate") {
		t.Fatal("expected candidate to NOT be recognized as date column")
	}
}

func TestChartsAndRenderChartFullCoverage(t *testing.T) {
	// renderChart width < 24 || height < 4
	if got := renderChart(ChartSpec{}, 20, 2); got != "Chart needs a wider pane." {
		t.Fatalf("unexpected message: %q", got)
	}
	// renderChart len(Points) == 0
	if got := renderChart(ChartSpec{}, 40, 10); got != "No chart data." {
		t.Fatalf("unexpected message: %q", got)
	}
	// renderChart ChartLine with month bucket and invalid date
	specMonth := ChartSpec{
		Kind:   ChartLine,
		Bucket: "month",
		Points: []ChartPoint{{Label: "2026-05", Value: 10}, {Label: "2026-06", Value: 20}},
	}
	_ = renderChart(specMonth, 80, 20)

	specBadDate := ChartSpec{
		Kind:   ChartLine,
		Points: []ChartPoint{{Label: "not-a-date", Value: 10}},
	}
	if got := renderChart(specBadDate, 80, 20); got != "Chart dates could not be displayed." {
		t.Fatalf("expected date error message, got %q", got)
	}

	// categoryCandidate with null value frequency
	colStats := secureread.ColumnStatistics{
		NonNullCount: 10,
		Cardinality:  2,
		Frequencies: []secureread.ValueFrequency{
			{Label: "A", Count: 5},
			{Type: secureread.ValueKindNull, Count: 5},
		},
	}
	cand, ok := categoryCandidate(10, colStats)
	if !ok || len(cand.Spec.Points) != 2 {
		t.Fatalf("expected valid category candidate, got %v, %v", cand, ok)
	}

	// sumLineCandidate incomplete or len(Buckets) < 2
	_, ok = sumLineCandidate(secureread.RecordSetStatistics{}, secureread.DateNumericSum{Incomplete: true})
	if ok {
		t.Fatal("expected false for incomplete")
	}
	// sumLineCandidate numeric column ending in ID/key
	statsID := secureread.RecordSetStatistics{
		Columns: []secureread.ColumnStatistics{{Name: "customer_id"}},
	}
	sumID := secureread.DateNumericSum{
		DateColumn:    "order_date",
		NumericColumn: "customer_id",
		Buckets:       []secureread.DateNumericBucket{{Bucket: "2026-01-01", Sum: 10}, {Bucket: "2026-01-02", Sum: 20}},
	}
	if _, ok := sumLineCandidate(statsID, sumID); ok {
		t.Fatal("expected false for column ending with id")
	}

	// dateBucketDay
	if got := dateBucketDay("2026"); got != "2026" {
		t.Fatalf("expected 2026, got %q", got)
	}
}

func TestAttachedJoinFullCoverage(t *testing.T) {
	// splitAttachedRelation without dot
	schema, rel := splitAttachedRelation("customers")
	if schema != "" || rel != "customers" {
		t.Fatalf("expected empty schema and customers rel, got %q, %q", schema, rel)
	}

	// promptMentionsRelation with y -> ies plural
	if !promptMentionsRelation("show all categories please", "category") {
		t.Fatal("expected true for category / categories")
	}

	// chooseAttachedJoin with 0 candidates
	if _, err := chooseAttachedJoin(nil, "prompt", "target"); err == nil {
		t.Fatal("expected error with 0 candidates")
	}

	// joinAttachedQuery errors and early exits
	ctx := context.Background()
	chat := &SessionChat{source: "sqlite:///fixture.db", joinApplication: ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot()}}

	// invalid DTQL
	qInvalidDTQL := QueryResult{Source: chat.source, DTQL: "invalid: ["}
	if _, _, err := chat.joinAttachedQuery(ctx, ChatSession{}, "prompt", qInvalidDTQL); err == nil {
		t.Fatal("expected DTQL deserialize error")
	}

	// DTQL with Aggregate
	qAgg := QueryResult{Source: chat.source, DTQL: "from: {schema: main, name: Invoice}\ncolumns: [{aggregate: {function: COUNT, args: [{star: true}]}, as: Count}]\nlimit: 5\n"}
	if _, applied, err := chat.joinAttachedQuery(ctx, ChatSession{}, "prompt", qAgg); err != nil || applied {
		t.Fatalf("expected not applied and no error, got %v, %v", applied, err)
	}

	// attachments with ref.Kind != "table"
	sessionNonTable := ChatSession{
		Workspace: WorkspaceState{
			Attachments: []ContextReference{{Kind: "recordset", ObjectID: "rs1"}},
		},
	}
	qValid := QueryResult{Source: chat.source, SourceID: "main_db", DTQL: "from: {schema: main, name: Invoice}\ncolumns: [{field: InvoiceId}]\n"}
	if _, applied, err := chat.joinAttachedQuery(ctx, sessionNonTable, "prompt", qValid); err != nil || applied {
		t.Fatalf("expected not applied and no error, got %v, %v", applied, err)
	}

	// attachments with mismatched sourceID
	sessionDiffSource := ChatSession{
		Workspace: WorkspaceState{
			Attachments: []ContextReference{{Kind: "table", SourceID: "other_db", ObjectID: "Customer"}},
		},
	}
	if _, applied, err := chat.joinAttachedQuery(ctx, sessionDiffSource, "prompt", qValid); err != nil || applied {
		t.Fatalf("expected not applied and no error, got %v, %v", applied, err)
	}

	// candidate error when executor is missing
	sessionAttached := ChatSession{
		Workspace: WorkspaceState{
			Attachments: []ContextReference{{Kind: "table", SourceID: "main_db", ObjectID: "Customer"}},
		},
	}
	if _, _, err := chat.joinAttachedQuery(ctx, sessionAttached, "prompt", qValid); err == nil {
		t.Fatal("expected candidate error without executor")
	}
}

func TestRecordSetUIFullCoverage(t *testing.T) {
	resp := HTTPResponse{
		StatusCode:     http.StatusOK,
		Method:         "GET",
		URL:            "https://example.com/initial",
		FinalURL:       "https://example.com/final",
		RequestHeaders: map[string][]string{"Accept": {"application/json"}},
		Redirects: []HTTPRedirect{
			{StatusCode: 301, URL: "https://example.com/final", Elapsed: 50 * time.Millisecond},
		},
	}
	content := resp.headersContent(80)
	if !strings.Contains(content, "Redirects:") || !strings.Contains(content, "Final:") {
		t.Fatalf("expected redirects in content: %q", content)
	}
}

func TestBookmarkProjectionOutOfRange(t *testing.T) {
	record := RecordSet{
		Result: secureread.Result{
			Columns: []string{"id"},
			Rows:    []secureread.Row{{Data: map[string]any{"id": 1}}},
		},
	}
	sel := &Selection{
		Rows: []int{-1, 999},
	}
	res, srcRows := projectSnapshot(record, nil, sel)
	if len(res.Rows) != 0 || len(srcRows) != 0 {
		t.Fatalf("expected 0 rows for out of range indices, got %d, %d", len(res.Rows), len(srcRows))
	}
}

func TestBrowserCellDetailFullCoverage(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)

	// activeID mismatch
	if _, err := chat.CellDetailActive(ctx, "wrong-id", "rs1", 0, "col"); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatal("expected ErrActiveSessionChanged")
	}

	// recordSetID not found or row out of range
	user, _ := store.AppendUser(ctx, chat.activeID, "user")
	query, _ := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///chinook.db", QueryResult{
		Title: "Q",
		Result: secureread.Result{
			Columns: []string{"id", "name"},
			Rows:    []secureread.Row{{Data: map[string]any{"id": 1, "name": "Test"}}},
		},
	})
	if _, err := chat.CellDetailActive(ctx, chat.activeID, "nonexistent", 0, "id"); err == nil {
		t.Fatal("expected unavailable for nonexistent recordset")
	}
	if _, err := chat.CellDetailActive(ctx, chat.activeID, query.RecordSetID, 10, "id"); err == nil {
		t.Fatal("expected unavailable for row out of range")
	}

	// column not found
	if _, err := chat.CellDetailActive(ctx, chat.activeID, query.RecordSetID, 0, "nonexistent_col"); err == nil {
		t.Fatal("expected unavailable for column not found")
	}

	// success without ForeignKeyJoinApplication
	detail, err := chat.CellDetailActive(ctx, chat.activeID, query.RecordSetID, 0, "id")
	if err != nil || detail.Column != "id" {
		t.Fatalf("unexpected detail: %+v, %v", detail, err)
	}

	// store closed -> Load error
	_ = store.Close()
	if _, err := chat.CellDetailActive(ctx, chat.activeID, query.RecordSetID, 0, "id"); err == nil {
		t.Fatal("expected closed store error")
	}
}

func TestChatUIAndComponentsCoverage(t *testing.T) {
	// join_block default key
	b := newTestJoinBlock()
	b.joinFocused = true
	updated, _ := b.Update(tea.KeyPressMsg{Code: 'z'})
	if updated == nil {
		t.Fatal("expected non-nil updated join block")
	}

	// chatui_chips OnChipsChange with existing attachments
	u, _ := newChipTestChatUI(t)
	chips := []chatshell.Chip{
		{ID: "chinook:table:Customer", Label: "Customer"},
	}
	_ = u.OnChipsChange(chips)
}
