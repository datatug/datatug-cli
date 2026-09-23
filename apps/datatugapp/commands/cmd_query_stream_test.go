package commands

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	_ "modernc.org/sqlite"
)

const streamingJoinDTQL = `from:
  database: orders
  name: Invoice
  alias: o
  joins:
    - from: {database: countries, name: Country, alias: c}
      on: [{left: {field: country_id, source: o}, op: '==', right: {field: id, source: c}}]
columns:
  - {field: id, source: o, as: invoiceId}
  - {field: name, source: c, as: countryName}
`

func streamFixture(t *testing.T, count int) map[string]string {
	t.Helper()
	dir := t.TempDir()
	makeDB := func(name, schema string) (*sql.DB, string) {
		path := filepath.Join(dir, name+".sqlite")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
		return db, "sqlite://" + path
	}
	orders, orderURL := makeDB("orders", `CREATE TABLE Invoice(id INTEGER PRIMARY KEY, country_id INTEGER NOT NULL)`)
	countries, countryURL := makeDB("countries", `CREATE TABLE Country(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	if _, err := countries.Exec(`INSERT INTO Country VALUES (1,'Ireland')`); err != nil {
		t.Fatal(err)
	}
	tx, err := orders.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO Invoice VALUES (?,1)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		if _, err := stmt.Exec(i); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"orders": orderURL, "countries": countryURL}
}

func TestStreamedFederatedJSONLWithHTTPAndCSV(t *testing.T) {
	const count = 1200
	urls := streamFixture(t, count)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/databases/extra/records/Country/") {
			t.Errorf("path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"region":"Europe"}}`)
	}))
	defer server.Close()
	federation := &datatug.QueryFederation{OVDBBaseURL: server.URL, Lookups: []datatug.QueryHTTPLookup{{Database: "extra", Collection: "Country", FromColumn: "invoiceId", Concurrency: 8, Fields: []datatug.QueryLookupField{{Source: "region", Target: "region"}}}}}
	ctx := context.Background()
	stream, err := secureread.NewExecutor(secureread.Session{Unrestricted: true}).StreamFederatedDTQL(ctx, []byte(streamingJoinDTQL), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var progress bytes.Buffer
	reader, err := streamSavedQueryLookups(ctx, stream.Reader, federation, &progress, false)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeStreamedRows(ctx, &out, "jsonl", []string{"invoiceId", "countryName", "region"}, reader); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != count {
		t.Fatalf("got %d lines, want %d", len(lines), count)
	}
	if !strings.Contains(lines[0], `"region":"Europe"`) || !strings.Contains(lines[count-1], fmt.Sprintf(`"invoiceId":%d`, count)) {
		t.Fatalf("first=%s last=%s", lines[0], lines[count-1])
	}
	if !strings.Contains(progress.String(), "1200 completed, 0 in flight, 0 pending") {
		t.Fatalf("progress tail: %s", progress.String()[max(0, len(progress.String())-250):])
	}
	// A second read proves deterministic CSV columns and header without buffering.
	csvStream, err := secureread.NewExecutor(secureread.Session{Unrestricted: true}).StreamFederatedDTQL(ctx, []byte(streamingJoinDTQL), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer csvStream.Close()
	var csvOut bytes.Buffer
	if err := writeStreamedRows(ctx, &csvOut, "csv", explicitStreamColumns(csvStream.Query), csvStream.Reader); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(csvOut.String(), "$key,invoiceId,countryName\n") || strings.Count(csvOut.String(), "\n") != count+1 {
		t.Fatalf("CSV shape: %q", csvOut.String()[:min(100, csvOut.Len())])
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("output stopped") }

func TestStreamedFederatedCancellationAndOutputError(t *testing.T) {
	urls := streamFixture(t, 5)
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := executor.StreamFederatedDTQL(ctx, []byte(streamingJoinDTQL), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	cancel()
	if err := writeStreamedRows(ctx, io.Discard, "jsonl", nil, stream.Reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	stream2, err := executor.StreamFederatedDTQL(context.Background(), []byte(streamingJoinDTQL), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream2.Close()
	if err := writeStreamedRows(context.Background(), failedOutput{}, "jsonl", nil, stream2.Reader); err == nil || err.Error() != "output stopped" {
		t.Fatalf("output error: %v", err)
	}
}

func TestStreamingShapeAndColumns(t *testing.T) {
	query, err := dtql.Deserialize([]byte(streamingJoinDTQL))
	if err != nil {
		t.Fatal(err)
	}
	if !isBoundedFederatedRowShape(query) {
		t.Fatal("flat join rejected")
	}
	if got := explicitStreamColumns(query); len(got) != 2 || got[0] != "invoiceId" || got[1] != "countryName" {
		t.Fatalf("columns: %v", got)
	}
	ordered, err := dtql.Deserialize([]byte(streamingJoinDTQL + "orderBy: [{field: id, source: o}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if isBoundedFederatedRowShape(ordered) {
		t.Fatal("ordered join accepted as bounded stream")
	}
}
