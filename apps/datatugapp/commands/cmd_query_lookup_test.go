package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestApplySavedQueryLookups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/databases/countries/records/Country/") {
			t.Errorf("path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_, _ = fmt.Fprint(w, `{"key":"Country/1","data":{"population":100}}`)
	}))
	defer server.Close()
	result := secureread.Result{Columns: []string{"countryId"}, Rows: []secureread.Row{{Data: map[string]any{"countryId": 1}}, {Data: map[string]any{"countryId": 2}}}}
	federation := &datatug.QueryFederation{OVDBBaseURL: server.URL, Lookups: []datatug.QueryHTTPLookup{{Database: "countries", Collection: "Country", FromColumn: "countryId", Fields: []datatug.QueryLookupField{{Source: "population", Target: "populationFromHttp"}}, Concurrency: 2}}}
	var progress bytes.Buffer
	got, err := applySavedQueryLookups(context.Background(), result, federation, &progress, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rows[0].Data["populationFromHttp"] != float64(100) || got.Rows[1].Data["populationFromHttp"] != float64(100) {
		t.Fatalf("rows: %+v", got.Rows)
	}
	if !strings.Contains(progress.String(), "2 completed, 0 in flight, 0 pending") {
		t.Fatalf("progress: %s", progress.String())
	}
}
