package cloudproject

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

const (
	summaryJSON  = `{"id":"sales","title":"Sales","access":"private"}`
	envsJSON     = `{"environments":[{"id":"dev","title":"Development"},{"id":"prod"}]}`
	catalogJSON  = `{"format":"x","connections":[{"id":"warehouse","title":"Warehouse"},{"title":"no id"}],"connectionsRevision":3}`
	queriesJSON  = `{"id":"~","items":[{"id":"top","title":"Top"}],"folders":[{"id":"reports","items":[{"id":"monthly"},{"title":"no id"}],"folders":[{"id":"deep","items":[{"id":"q"}]},null]}]}`
	fallbackJSON = `{"connections":[{"id":"demo-db"}]}`
)

type recorded struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (r *recorded) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

// backend serves the five reads; answers overrides a path's status and body.
func backend(t *testing.T, rec *recorded, answers map[string]func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		path := strings.TrimPrefix(r.URL.Path, "/v0/datatug/")
		if answer := answers[path]; answer != nil {
			answer(w)
			return
		}
		switch path {
		case "projects/project_summary":
			_, _ = io.WriteString(w, summaryJSON)
		case "environments/state":
			_, _ = io.WriteString(w, envsJSON)
		case "projects/connection_catalog":
			w.Header().Set("ETag", `"3"`)
			_, _ = io.WriteString(w, catalogJSON)
		case "queries/all_queries":
			_, _ = io.WriteString(w, queriesJSON)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
}

func client(server *httptest.Server) *Client {
	return &Client{BaseURL: server.URL + "/v0/", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "bearer-1", nil }}
}

var ref = Ref{SpaceID: "acme", ProjectID: "sales"}

func TestLoad_ReadsProjectThroughBackendOnly(t *testing.T) {
	rec := &recorded{}
	server := backend(t, rec, nil)
	defer server.Close()
	p, err := Load(context.Background(), client(server), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.ID != "sales" || p.Title != "Sales" || p.Ref != ref {
		t.Fatalf("project = %+v", p)
	}
	if len(p.Environments) != 2 || p.Environments[0] != (Item{ID: "dev", Title: "Development"}) {
		t.Fatalf("environments = %+v", p.Environments)
	}
	if len(p.Databases) != 1 || p.Databases[0].ID != "warehouse" {
		t.Fatalf("databases = %+v", p.Databases)
	}
	wantQueries := []string{"reports/deep/q", "reports/monthly", "top"}
	if len(p.Queries) != len(wantQueries) {
		t.Fatalf("queries = %+v", p.Queries)
	}
	for i, id := range wantQueries {
		if p.Queries[i].ID != id {
			t.Fatalf("queries = %+v", p.Queries)
		}
	}
	if p.EnvsErr != nil || p.DBsErr != nil || p.QueriesErr != nil {
		t.Fatalf("list errors = %v %v %v", p.EnvsErr, p.DBsErr, p.QueriesErr)
	}
	if len(rec.requests) != 4 {
		t.Fatalf("%d requests, want 4", len(rec.requests))
	}
	for _, r := range rec.requests {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer bearer-1" ||
			q.Get("storage") != "firestore" || q.Get("spaceID") != "acme" || q.Get("project") != "sales" {
			t.Fatalf("request %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
	}
}

func TestLoad_FallbackCatalogIsNoDatabasesAndSummaryIDDefaults(t *testing.T) {
	server := backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"projects/connection_catalog": func(w http.ResponseWriter) { _, _ = io.WriteString(w, fallbackJSON) },
		"projects/project_summary":    func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"title":"T"}`) },
	})
	defer server.Close()
	p, err := Load(context.Background(), client(server), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Databases == nil || len(p.Databases) != 0 {
		t.Fatalf("fallback catalog databases = %#v", p.Databases)
	}
	if p.ID != "sales" {
		t.Fatalf("id = %q", p.ID)
	}
}

func TestLoad_FailingListsStayOnProject(t *testing.T) {
	fail := func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }
	server := backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"environments/state": fail, "projects/connection_catalog": fail, "queries/all_queries": fail,
	})
	defer server.Close()
	p, err := Load(context.Background(), client(server), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range []error{p.EnvsErr, p.DBsErr, p.QueriesErr} {
		if !errors.Is(e, ErrUnavailable) {
			t.Fatalf("list error = %v", e)
		}
	}
}

func TestLoad_SummaryStatusMapsToErrors(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusNotFound:            ErrNotFound,
		http.StatusUnauthorized:        ErrUnauthorized,
		http.StatusForbidden:           ErrForbidden,
		http.StatusBadGateway:          ErrUnavailable,
		http.StatusInternalServerError: ErrUnavailable,
	} {
		server := backend(t, &recorded{}, map[string]func(http.ResponseWriter){
			"projects/project_summary": func(w http.ResponseWriter) { w.WriteHeader(status) },
		})
		_, err := Load(context.Background(), client(server), ref)
		server.Close()
		if !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, want %v", status, err, want)
		}
	}
}

func TestGet_OtherFailures(t *testing.T) {
	server := backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"projects/project_summary": func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadRequest) },
	})
	_, err := Load(context.Background(), client(server), ref)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("400: err = %v", err)
	}
	server.Close()

	server = backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"projects/project_summary": func(w http.ResponseWriter) { _, _ = io.WriteString(w, "not json") },
	})
	if _, err = Load(context.Background(), client(server), ref); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("bad json: err = %v", err)
	}
	server.Close()

	server = backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"projects/project_summary": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `"`+strings.Repeat("x", maxResponseBytes)+`"`)
		},
	})
	if _, err = Load(context.Background(), client(server), ref); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("large: err = %v", err)
	}
	server.Close()

	c := client(server) // closed: connection refused
	if _, err = Load(context.Background(), c, ref); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("closed server: err = %v", err)
	}

	c.Token = func(context.Context) (string, error) { return "", errors.New("no login") }
	if _, err = Load(context.Background(), c, ref); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("token: err = %v", err)
	}

	c = &Client{BaseURL: "http://bad host/", HTTP: http.DefaultClient, Token: func(context.Context) (string, error) { return "t", nil }}
	if _, err = Load(context.Background(), c, ref); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("reset") }
func (failingBody) Close() error             { return nil }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGet_BodyReadFailure(t *testing.T) {
	c := &Client{BaseURL: "https://api.example/v0/", Token: func(context.Context) (string, error) { return "t", nil },
		HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}, Header: http.Header{}}, nil
		})}}
	if _, err := Load(context.Background(), c, ref); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestTransportReason(t *testing.T) {
	if got := transportReason(context.DeadlineExceeded); got != "timed out" {
		t.Fatalf("deadline = %q", got)
	}
	if got := transportReason(errors.New("boom")); got != "boom" {
		t.Fatalf("plain = %q", got)
	}
}

func TestRef(t *testing.T) {
	if err := ref.Validate(); err != nil {
		t.Fatalf("valid ref: %v", err)
	}
	if err := (Ref{SpaceID: "a.b", ProjectID: "p"}).Validate(); err == nil || !strings.Contains(err.Error(), "space") {
		t.Fatalf("bad space: %v", err)
	}
	if err := (Ref{SpaceID: "a", ProjectID: strings.Repeat("p", 129)}).Validate(); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("bad project: %v", err)
	}
	if got := ref.Address(); got != "https://datatug.app/space/acme/store/firestore/project/sales" {
		t.Fatalf("address = %q", got)
	}
	if ValidIdentifier("") || !ValidIdentifier("a_B-1") {
		t.Fatal("ValidIdentifier")
	}
}

func TestOwnCatalogRecognisedByRevisionWithoutETag(t *testing.T) {
	server := backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"projects/connection_catalog": func(w http.ResponseWriter) { _, _ = io.WriteString(w, catalogJSON) },
	})
	defer server.Close()
	p, err := Load(context.Background(), client(server), ref)
	if err != nil || len(p.Databases) != 1 || p.Databases[0].ID != "warehouse" {
		t.Fatalf("databases = %+v, %v", p.Databases, err)
	}
}

func TestTitlesLoseControlCharacters(t *testing.T) {
	server := backend(t, &recorded{}, map[string]func(http.ResponseWriter){
		"projects/project_summary": func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"title":"Sa\u001b[31mles\u0007"}`) },
		"environments/state": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"environments":[{"id":"dev","title":"D\u009bev"}]}`)
		},
	})
	defer server.Close()
	p, err := Load(context.Background(), client(server), ref)
	if err != nil || p.Title != "Sa[31mles" || p.Environments[0].Title != "Dev" {
		t.Fatalf("title %q, env %+v, %v", p.Title, p.Environments, err)
	}
}

// TestNoFirestoreClient proves the package reads only through HTTP: none of
// its files imports a Firestore or Firebase client.
func TestNoFirestoreClient(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, "firestore") || strings.Contains(imp.Path.Value, "firebase") {
				t.Errorf("%s imports %s", e.Name(), imp.Path.Value)
			}
		}
	}
}
