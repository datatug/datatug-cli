package commands

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

type cov100dRoundTripper func(*http.Request) (*http.Response, error)

func (f cov100dRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cov100dClient(status int, body string, err error, seen *string) *http.Client {
	return &http.Client{Transport: cov100dRoundTripper(func(r *http.Request) (*http.Response, error) {
		if seen != nil {
			*seen = r.URL.Path
		}
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func TestCov100dLookupBase(t *testing.T) {
	for _, bad := range []string{"http://[::1", "ftp://host", "https://user:pw@host", "https://host?q=1", "http://example.com", ""} {
		if _, err := lookupBase(&datatug.QueryFederation{OVDBBaseURL: bad}); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	for _, good := range []string{"https://host", "http://localhost:1", "http://127.0.0.1:1"} {
		if _, err := lookupBase(&datatug.QueryFederation{OVDBBaseURL: good}); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestCov100dNewLookupClientDoesNotFollowRedirects(t *testing.T) {
	client := newLookupClient()
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect: %v", err)
	}
}

func TestCov100dLookupValue(t *testing.T) {
	base, _ := url.Parse("https://ovdb.example/")
	lookup := datatug.QueryHTTPLookup{Database: "db", Collection: "Col", FromColumn: "k", Fields: []datatug.QueryLookupField{{Source: "s", Target: "t"}}}

	if _, err := lookupValue(base, nil, datatug.QueryHTTPLookup{Database: "bad db", Collection: "Col"}); err == nil {
		t.Fatal("bad database identifier accepted")
	}
	if _, err := lookupValue(base, nil, datatug.QueryHTTPLookup{Database: "db", Collection: "bad/col"}); err == nil {
		t.Fatal("bad collection identifier accepted")
	}

	run := func(client *http.Client, ctx context.Context, data any) (map[string]any, error) {
		t.Helper()
		fn, err := lookupValue(base, client, lookup)
		if err != nil {
			t.Fatal(err)
		}
		return fn(ctx, record.NewRecordWithData(record.NewKeyWithID("r", 1), data))
	}
	ctx := context.Background()

	if _, err := run(cov100dClient(200, "{}", nil, nil), ctx, "not an object"); err == nil || !strings.Contains(err.Error(), "non-object") {
		t.Fatalf("non-object row: %v", err)
	}
	for name, value := range map[string]any{"float": 1.5, "bool": true, "missing": nil} {
		if _, err := run(cov100dClient(200, "{}", nil, nil), ctx, map[string]any{"k": value}); err == nil || !strings.Contains(err.Error(), "requires a string or integer") {
			t.Fatalf("%s: %v", name, err)
		}
	}

	for name, value := range map[string]any{"string": "42", "int": 42, "int64": int64(42), "float": float64(42)} {
		var path string
		got, err := run(cov100dClient(200, `{"data":{"s":"v"}}`, nil, &path), ctx, map[string]any{"k": value})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got["t"] != "v" || path != "/v1/databases/db/records/Col/42" {
			t.Fatalf("%s: got %v path %s", name, got, path)
		}
	}

	var nilCtx context.Context
	if _, err := run(cov100dClient(200, "{}", nil, nil), nilCtx, map[string]any{"k": "1"}); err == nil {
		t.Fatal("nil context must fail request construction")
	}
	if _, err := run(cov100dClient(0, "", errors.New("dial failed"), nil), ctx, map[string]any{"k": "1"}); err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("transport error: %v", err)
	}
	if _, err := run(cov100dClient(503, "{}", nil, nil), ctx, map[string]any{"k": "1"}); err == nil || !strings.Contains(err.Error(), "failed (503)") {
		t.Fatalf("status: %v", err)
	}
	if _, err := run(cov100dClient(200, "{not json", nil, nil), ctx, map[string]any{"k": "1"}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := run(cov100dClient(200, `{"data":null}`, nil, nil), ctx, map[string]any{"k": "1"}); err == nil || !strings.Contains(err.Error(), "no JSON data") {
		t.Fatalf("null data: %v", err)
	}
}

func TestCov100dAppendLookupColumnsSkipsExisting(t *testing.T) {
	lookup := datatug.QueryHTTPLookup{Fields: []datatug.QueryLookupField{{Target: "a"}, {Target: "b"}}}
	got := appendLookupColumns([]string{"a"}, lookup)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("columns: %v", got)
	}
}

func TestCov100dApplySavedQueryLookupsErrors(t *testing.T) {
	ctx := context.Background()
	result := func() secureread.Result {
		return secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": "1"}}}}
	}
	if _, err := applySavedQueryLookups(ctx, result(), &datatug.QueryFederation{OVDBBaseURL: "ftp://x"}, io.Discard, true); err == nil {
		t.Fatal("bad base accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	badIdent := &datatug.QueryFederation{OVDBBaseURL: server.URL, Lookups: []datatug.QueryHTTPLookup{{Database: "bad db", Collection: "C", FromColumn: "id"}}}
	if _, err := applySavedQueryLookups(ctx, result(), badIdent, io.Discard, true); err == nil {
		t.Fatal("bad lookup identifiers accepted")
	}
	failing := &datatug.QueryFederation{OVDBBaseURL: server.URL, Lookups: []datatug.QueryHTTPLookup{{Database: "db", Collection: "C", FromColumn: "id", Concurrency: 1, Fields: []datatug.QueryLookupField{{Source: "s", Target: "t"}}}}}
	if _, err := applySavedQueryLookups(ctx, result(), failing, io.Discard, true); err == nil || !strings.Contains(err.Error(), "failed (500)") {
		t.Fatalf("failing lookup: %v", err)
	}
}

func TestCov100dStreamSavedQueryLookups(t *testing.T) {
	ctx := context.Background()
	source := &cov100dReader{}
	for _, federation := range []*datatug.QueryFederation{nil, {}} {
		got, err := streamSavedQueryLookups(ctx, source, federation, io.Discard, true)
		if err != nil || got != dal.RecordsReader(source) {
			t.Fatalf("passthrough: %v %v", got, err)
		}
	}
	lookup := datatug.QueryHTTPLookup{Database: "db", Collection: "C", FromColumn: "id"}
	if _, err := streamSavedQueryLookups(ctx, source, &datatug.QueryFederation{OVDBBaseURL: "ftp://x", Lookups: []datatug.QueryHTTPLookup{lookup}}, io.Discard, true); err == nil {
		t.Fatal("bad base accepted")
	}
	bad := lookup
	bad.Database = "bad db"
	if _, err := streamSavedQueryLookups(ctx, source, &datatug.QueryFederation{OVDBBaseURL: "https://host", Lookups: []datatug.QueryHTTPLookup{bad}}, io.Discard, true); err == nil {
		t.Fatal("bad lookup identifiers accepted")
	}
	// A nil source makes dal.ExecuteRecordLookupStream refuse to build the stream.
	if _, err := streamSavedQueryLookups(ctx, nil, &datatug.QueryFederation{OVDBBaseURL: "https://host", Lookups: []datatug.QueryHTTPLookup{lookup}}, io.Discard, true); err == nil {
		t.Fatal("nil source accepted")
	}
}
