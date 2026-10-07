package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

type postgresBigQueryCaptureSink struct{ writer *bqwriter.LoadWriter }

func (s postgresBigQueryCaptureSink) Preflight(ctx context.Context, tables []dbcopy.CopyTable) (dbcopy.CopySinkPlan, error) {
	inner, err := (dbcopy.BigQueryCopySink{Writer: s.writer}).Preflight(ctx, tables)
	if err != nil {
		return nil, err
	}
	return &postgresBigQueryCapturePlan{inner: inner, rowsByTable: map[string]int64{}, ndjsonByTable: map[string][]map[string]any{}}, nil
}

type postgresBigQueryCapturePlan struct {
	inner         dbcopy.CopySinkPlan
	rowsByTable   map[string]int64
	ndjsonByTable map[string][]map[string]any
}

func (p *postgresBigQueryCapturePlan) EncodeRow(table dbcopy.CopyTable, row dbschema.SourceRow) ([]byte, error) {
	return p.inner.EncodeRow(table, row)
}

func (p *postgresBigQueryCapturePlan) TargetName(table dbcopy.CopyTable) string {
	return p.inner.TargetName(table)
}

func (p *postgresBigQueryCapturePlan) Prepare(context.Context) error { return nil }

func (p *postgresBigQueryCapturePlan) LoadTable(_ context.Context, table dbcopy.CopyTable, rows io.Reader) (string, int64, error) {
	target := p.TargetName(table)
	scanner := bufio.NewScanner(rows)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return target, p.rowsByTable[target], fmt.Errorf("decode staged BigQuery row: %w", err)
		}
		p.ndjsonByTable[target] = append(p.ndjsonByTable[target], row)
		p.rowsByTable[target]++
	}
	return target, p.rowsByTable[target], scanner.Err()
}

type noNetworkBigQueryTransport struct{}

func (noNetworkBigQueryTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected BigQuery HTTP request in offline source journey")
}

func TestPostgresToBigQueryProviderNeutralCopyPreservesRows(t *testing.T) {
	server := newRealPgServer(t)
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	execAll(t, server.scanURL,
		`ALTER TABLE invoice ALTER COLUMN total TYPE numeric(30,8)`,
		`INSERT INTO invoice (id, customer_id, total, status) VALUES (31, 1, 12345678901234567890.12345678, 'paid')`,
		`INSERT INTO audit_log (at, message) VALUES ('2024-10-01 08:30:45', 'source row')`,
		`CREATE TABLE pg_no_key (payload text, n integer)`,
		`INSERT INTO pg_no_key VALUES ('duplicate', 7), ('duplicate', 7)`)
	sourceRef, err := dbcopy.Parse(server.scanURL)
	if err != nil {
		t.Fatal(err)
	}
	// The PostgreSQL reader's default-schema listing returns unqualified
	// CollectionRefs. The BigQuery mapper only adds a schema prefix when the
	// source ref carries one, so these target table names intentionally remain
	// unqualified for the default public-schema copy.
	sourceRef = sourceRef.WithFlag("--from")
	source, err := sourceRef.OpenForCopy(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := source.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	writer, err := bqwriter.NewLoadWriter(context.Background(), bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "offline_test", Location: "US",
		HTTPClient: &http.Client{Transport: noNetworkBigQueryTransport{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := postgresBigQueryCaptureSink{writer: writer}
	plan := &postgresBigQueryCapturePlan{}
	// The sink creates its plan during preflight; retain it afterward through a wrapper.
	capturingSink := postgresBigQueryCaptureSinkWithPlan{postgresBigQueryCaptureSink: sink, plan: plan}
	summary, err := dbcopy.CopyToSink(context.Background(), source, capturingSink, dbcopy.CopyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowsCopied != 8 || plan.rowsByTable["customer"] != 2 || plan.rowsByTable["customer_names"] != 2 || plan.rowsByTable["invoice"] != 1 || plan.rowsByTable["audit_log"] != 1 || plan.rowsByTable["pg_no_key"] != 2 {
		t.Fatalf("PostgreSQL copy rows: summary=%#v counts=%#v", summary, plan.rowsByTable)
	}
	var foundTotal bool
	for _, row := range plan.ndjsonByTable["invoice"] {
		if row["id"] == "31" && row["total"] == "12345678901234567890.12345678" && row["status"] == "paid" {
			foundTotal = true
		}
	}
	if !foundTotal {
		t.Fatalf("typed PostgreSQL numeric row did not retain exact value: %#v", plan.ndjsonByTable["invoice"])
	}
	var foundTimestamp bool
	for _, row := range plan.ndjsonByTable["audit_log"] {
		if row["at"] == "2024-10-01T08:30:45" && row["message"] == "source row" {
			foundTimestamp = true
		}
	}
	if !foundTimestamp {
		t.Fatalf("PostgreSQL timestamp row did not retain its wall-clock value: %#v", plan.ndjsonByTable["audit_log"])
	}
	if got := plan.ndjsonByTable["pg_no_key"]; len(got) != 2 || got[0]["payload"] != "duplicate" || got[1]["payload"] != "duplicate" {
		t.Fatalf("keyless duplicate PostgreSQL rows were not preserved: %#v", got)
	}
}

type postgresBigQueryCaptureSinkWithPlan struct {
	postgresBigQueryCaptureSink
	plan *postgresBigQueryCapturePlan
}

func (s postgresBigQueryCaptureSinkWithPlan) Preflight(ctx context.Context, tables []dbcopy.CopyTable) (dbcopy.CopySinkPlan, error) {
	inner, err := (dbcopy.BigQueryCopySink{Writer: s.writer}).Preflight(ctx, tables)
	if err != nil {
		return nil, err
	}
	s.plan.inner = inner
	s.plan.rowsByTable = map[string]int64{}
	s.plan.ndjsonByTable = map[string][]map[string]any{}
	return s.plan, nil
}
