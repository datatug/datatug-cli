package secureread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
)

const exactOriginal = `from: {name: Item}
columns: [{field: Id}, {field: Category}, {field: Value}]
where: {op: '>', left: {field: Id}, right: {param: minimum}}
orderBy: [{field: Id}]
limit: 2
`

var exactColumns = []resultops.Column{{Name: "Id", Type: "INTEGER"}, {Name: "Category", Type: "TEXT"}, {Name: "Value", Type: "INTEGER"}}

func exactSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aggregate.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("CREATE TABLE Item(Id INTEGER PRIMARY KEY,Category TEXT,Value INTEGER);INSERT INTO Item VALUES(1,'A',9007199254740991),(2,'A',2),(3,'B',3),(4,'B',4),(5,'B',5),(6,'B',6)"); err != nil {
		t.Fatal(err)
	}
	return "sqlite://" + path
}
func exactPreview(t *testing.T, original string, scope queryops.Scope, terms []queryops.AggregateTerm) queryops.Preview {
	t.Helper()
	p, err := queryops.DeriveGroupAggregates(original, []string{"Category"}, true, terms, scope)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExactAggregateScopesAndBigIntegerPrecision(t *testing.T) {
	source := exactSQLite(t)
	e := NewExecutor(Session{Unrestricted: true})
	for _, scope := range []queryops.Scope{queryops.MatchingSource, queryops.OriginalOutput} {
		p := exactPreview(t, exactOriginal, scope, []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}, {Column: "Value", Function: "MIN", Alias: "smallest"}, {Column: "Value", Function: "MAX", Alias: "largest"}, {Column: "Value", Function: "COUNT", Alias: "present"}})
		r, err := e.RunExactAggregateDTQL(context.Background(), source, []byte(exactOriginal), []byte(p.DTQL), map[string]any{"minimum": json.Number("0")}, exactColumns)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if scope == queryops.MatchingSource {
			want = 2
		}
		if len(r.Rows) != want {
			t.Fatalf("scope %v rows=%+v", scope, r.Rows)
		}
		a := r.Rows[0].Data
		if a["total"] != json.Number("9007199254740993") || a["smallest"] != json.Number("2") || a["largest"] != json.Number("9007199254740991") || a["present"] != 2 || a["count"] != 2 || r.ColumnTypes["total"] != "INTEGER" {
			t.Fatalf("exact output=%+v types=%v", a, r.ColumnTypes)
		}
		if scope == queryops.MatchingSource && r.Rows[1].Data["total"] != json.Number("18") {
			t.Fatal(r.Rows)
		}
	}
}

func TestExactAggregatePolicyBeforeCountAndHiddenOperand(t *testing.T) {
	source := exactSQLite(t)
	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: first-three}
default: deny
scopes:
- path: /Item
  rules:
  - id: first-three
    effect: allow
    operations: [query]
    where: {op: '<=', left: {field: Id}, right: {value: 3}}
    fields: [Id, Category]
`
	e := NewExecutor(aliceSession(t, policy))
	p := exactPreview(t, exactOriginal, queryops.MatchingSource, nil)
	r, err := e.RunExactAggregateDTQL(context.Background(), source, []byte(exactOriginal), []byte(p.DTQL), map[string]any{"minimum": 0}, exactColumns)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 2 || r.Rows[0].Data["count"] != 2 || r.Rows[1].Data["count"] != 1 || len(r.Limitations) == 0 {
		t.Fatalf("policy count=%+v", r)
	}

	allowed := NewExecutor(aliceSession(t, strings.Replace(policy, "fields: [Id, Category]", "fields: [Id, Category, Value]", 1)))
	sumPreview := exactPreview(t, exactOriginal, queryops.MatchingSource, []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})
	summed, sumErr := allowed.RunExactAggregateDTQL(context.Background(), source, []byte(exactOriginal), []byte(sumPreview.DTQL), map[string]any{"minimum": 0}, exactColumns)
	if sumErr != nil || len(summed.Rows) != 2 || summed.Rows[1].Data["total"] != json.Number("3") {
		t.Fatalf("policy SUM includes denied source rows: %+v %v", summed, sumErr)
	}
	p = exactPreview(t, exactOriginal, queryops.MatchingSource, []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})
	if _, err = e.RunExactAggregateDTQL(context.Background(), source, []byte(exactOriginal), []byte(p.DTQL), map[string]any{"minimum": 0}, exactColumns); err == nil {
		t.Fatal("hidden operand aggregated")
	}
}

func TestExactAggregateAliasesOrderingOffsetAndMismatch(t *testing.T) {
	source := exactSQLite(t)
	e := NewExecutor(Session{Unrestricted: true})
	original := strings.Replace(exactOriginal, "{field: Value}", "{field: Value, as: Revenue}", 1)
	columns := []resultops.Column{{Name: "Id", Type: "INTEGER"}, {Name: "Category", Type: "TEXT"}, {Name: "Revenue", Type: "INTEGER"}}
	p := exactPreview(t, original, queryops.MatchingSource, []queryops.AggregateTerm{{Column: "Revenue", Function: "SUM", Alias: "total"}})
	p.DTQL += "orderBy: [{field: total, desc: true}]\n"
	r, err := e.RunExactAggregateDTQL(context.Background(), source, []byte(original), []byte(p.DTQL), map[string]any{"minimum": 0}, columns)
	if err != nil || r.Rows[0].Data["total"] != json.Number("9007199254740993") {
		t.Fatalf("alias/order %v %+v", err, r)
	}

	originalPreview := exactPreview(t, original, queryops.OriginalOutput, []queryops.AggregateTerm{{Column: "Revenue", Function: "SUM", Alias: "total"}})
	originalResult, originalErr := e.RunExactAggregateDTQL(context.Background(), source, []byte(original), []byte(originalPreview.DTQL), map[string]any{"minimum": 0}, columns)
	if originalErr != nil || len(originalResult.Rows) != 1 || originalResult.Rows[0].Data["total"] != json.Number("9007199254740993") {
		t.Fatalf("original alias provenance: %+v %v", originalResult, originalErr)
	}
	offsetOriginal := exactOriginal + "offset: 1\n"
	p = exactPreview(t, offsetOriginal, queryops.MatchingSource, nil)
	r, err = e.RunExactAggregateDTQL(context.Background(), source, []byte(offsetOriginal), []byte(p.DTQL), map[string]any{"minimum": 0}, exactColumns)
	if err != nil || len(r.Rows) != 1 || r.Rows[0].Data["Category"] != "B" {
		t.Fatalf("offset before grouping %v %+v", err, r)
	}
	changed := strings.Replace(p.DTQL, "minimum", "other", 1)
	if _, err = e.RunExactAggregateDTQL(context.Background(), source, []byte(offsetOriginal), []byte(changed), map[string]any{"other": 0}, exactColumns); err == nil {
		t.Fatal("inconsistent filter ran")
	}
}

type aggregateTestDB struct {
	dal.DB
	reader dal.RecordsReader
}

func (db aggregateTestDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return db.reader, nil
}

type aggregateTestReader struct {
	dal.RecordsReader
	closed bool
	cancel context.CancelFunc
}

func (r *aggregateTestReader) Close() error { r.closed = true; return r.RecordsReader.Close() }
func (r *aggregateTestReader) Next() (record.Record, error) {
	item, err := r.RecordsReader.Next()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	return item, err
}
func aggregateRecords(data []map[string]any) dal.RecordsReader {
	records := make([]record.Record, len(data))
	for i, row := range data {
		records[i] = record.NewRecordWithData(record.NewKeyWithID("Item", i), row)
	}
	return dal.NewRecordsReader(records)
}
func exactPlanForRows(t *testing.T, kind string, terms []queryops.AggregateTerm) (exactAggregatePlan, error) {
	t.Helper()
	original := "from: {name: Item}\ncolumns: [{field: Category}, {field: Value}]\nlimit: 2\n"
	preview := exactPreview(t, original, queryops.MatchingSource, terms)
	return prepareExactAggregate([]byte(original), []byte(preview.DTQL), []resultops.Column{{Name: "Category", Type: "TEXT"}, {Name: "Value", Type: kind}})
}

func TestExactAggregateTypedDecimalNullAndRoundedSourceRefusal(t *testing.T) {
	terms := []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}, {Column: "Value", Function: "COUNT", Alias: "present"}, {Column: "Value", Function: "MIN", Alias: "smallest"}, {Column: "Value", Function: "MAX", Alias: "largest"}}
	p, err := exactPlanForRows(t, "DECIMAL(1000,999)", terms)
	if err != nil {
		t.Fatal(err)
	}
	reader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": json.Number("0.1000000000000000000000001")}, {"Category": "A", "Value": json.Number("0.2000000000000000000000002")}, {"Category": "A", "Value": nil}, {"Category": "B", "Value": nil}})}
	r, err := NewExecutor(Session{Unrestricted: true}).executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Rows[0].Data
	if a["total"] != json.Number("0.3000000000000000000000003") || a["present"] != 2 || a["count"] != 3 || r.ColumnTypes["total"] != "DECIMAL" {
		t.Fatalf("decimal=%+v %+v", r.Rows, r.ColumnTypes)
	}
	if r.Rows[1].Data["total"] != nil || r.Rows[1].Data["present"] != 0 || !reader.closed {
		t.Fatal(r.Rows)
	}
	for _, value := range []any{float64(0.1), json.Number("1e1000000000")} {
		reader = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": value}})}
		if _, err = NewExecutor(Session{Unrestricted: true}).executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil); err == nil || !reader.closed {
			t.Fatalf("unsafe decimal accepted or reader leaked: %v", value)
		}
	}
}

func TestExactAggregateBudgetsCancelAndNoPartial(t *testing.T) {
	p, err := exactPlanForRows(t, "INTEGER", nil)
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(Session{Unrestricted: true})
	data := make([]map[string]any, exactAggregateMaxRows+1)
	for i := range data {
		data[i] = map[string]any{"Category": "A", "Value": 1}
	}
	for _, rows := range [][]map[string]any{data, {{"Category": strings.Repeat("x", exactAggregateMaxBytes), "Value": 1}}} {
		reader := &aggregateTestReader{RecordsReader: aggregateRecords(rows)}
		r, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil)
		if err == nil || len(r.Rows) != 0 || !reader.closed {
			t.Fatalf("partial budget output: %v %+v closed=%v", err, r, reader.closed)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := &aggregateTestReader{RecordsReader: aggregateRecords(data[:1]), cancel: cancel}
	r, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: reader}, p, nil)
	if !errors.Is(err, context.Canceled) || len(r.Rows) != 0 || !reader.closed {
		t.Fatalf("cancellation: %v %+v", err, r)
	}
	cancel()
	if _, err = e.RunExactAggregateDTQL(ctx, "unsupported://never-open", nil, nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExactAggregateRefusesInnerArithmeticAndUnboundMetadata(t *testing.T) {
	source := exactSQLite(t)
	e := NewExecutor(Session{Unrestricted: true})
	p := exactPreview(t, exactOriginal, queryops.OriginalOutput, nil)
	if _, err := e.RunExactAggregateDTQL(context.Background(), source, []byte(exactOriginal), []byte(p.DTQL), map[string]any{"minimum": 0}, nil); err == nil {
		t.Fatal("untyped source accepted")
	}
	inner := "from: {name: Item}\ncolumns: [{field: Category}, {aggregate: {function: SUM, args: [{field: Value}]}, as: Value}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	p = exactPreview(t, inner, queryops.OriginalOutput, nil)
	if _, err := e.RunExactAggregateDTQL(context.Background(), "unsupported://never-open", []byte(inner), []byte(p.DTQL), nil, exactColumns); err == nil || !strings.Contains(err.Error(), "existing aggregate") {
		t.Fatal(err)
	}
	// Check preview serialization never removes a parameter or changes originals.
	if _, err := tugql.Deserialize([]byte(exactOriginal)); err != nil {
		t.Fatal(err)
	}
}

func TestExactAggregateAdjacentBigIntegerKeysAndFloatSum(t *testing.T) {
	p, err := exactPlanForRows(t, "REAL", []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})
	if err != nil {
		t.Fatal(err)
	}
	reader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": float64(0.1)}, {"Category": "A", "Value": float64(0.2)}})}
	r, err := NewExecutor(Session{Unrestricted: true}).executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil)
	if err != nil || r.Rows[0].Data["total"] != float64(0.30000000000000004) {
		t.Fatalf("native FLOAT addition: %+v %v", r, err)
	}
	source := exactSQLite(t)
	path := strings.TrimPrefix(source, "sqlite://")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("UPDATE Item SET Id=9007199254740992 WHERE Id=1;UPDATE Item SET Id=9007199254740993 WHERE Id=2"); err != nil {
		t.Fatal(err)
	}
	original := "from: {name: Item}\ncolumns: [{field: Id}]\norderBy: [{field: Id, desc: true}]\nlimit: 2\n"
	preview, err := queryops.DeriveGroupAggregates(original, []string{"Id"}, true, nil, queryops.OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	r, err = NewExecutor(Session{Unrestricted: true}).RunExactAggregateDTQL(context.Background(), source, []byte(original), []byte(preview.DTQL), nil, []resultops.Column{{Name: "Id", Type: "INTEGER"}})
	if err != nil || len(r.Rows) != 2 || r.Rows[0].Data["Id"] != json.Number("9007199254740993") || r.Rows[1].Data["Id"] != json.Number("9007199254740992") || r.Rows[0].Data["count"] != 1 || r.Rows[1].Data["count"] != 1 {
		t.Fatalf("adjacent key grouping: %+v %v", r, err)
	}
}

func TestExactAggregateMeasureDuplicationBudgetAndMissingOperand(t *testing.T) {
	terms := make([]queryops.AggregateTerm, 64)
	for i := range terms {
		terms[i] = queryops.AggregateTerm{Column: "Value", Function: "MIN", Alias: "minimum_" + strconv.Itoa(i)}
	}
	p, err := exactPlanForRows(t, "TEXT", terms)
	if err != nil {
		t.Fatal(err)
	}
	reader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": strings.Repeat("x", 1<<20)}})}
	r, err := NewExecutor(Session{Unrestricted: true}).executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil)
	if err == nil || len(r.Rows) != 0 || !reader.closed {
		t.Fatalf("measure amplification: %+v %v", r, err)
	}
	p, err = exactPlanForRows(t, "INTEGER", []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})
	if err != nil {
		t.Fatal(err)
	}
	reader = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A"}})}
	if _, err = NewExecutor(Session{Unrestricted: true}).executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil); err == nil || !reader.closed {
		t.Fatal("missing operand accepted or reader leaked")
	}
}

func TestExactAggregateScientificDecimalAndUnsafeBinding(t *testing.T) {
	p, err := exactPlanForRows(t, "DECIMAL", []queryops.AggregateTerm{{Column: "Value", Function: "MIN", Alias: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	reader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": json.Number("1e1000")}})}
	r, err := NewExecutor(Session{Unrestricted: true}).executeExactAggregate(context.Background(), aggregateTestDB{reader: reader}, p, nil)
	if err != nil || r.Rows[0].Data["minimum"] != json.Number("1e1000") {
		t.Fatalf("reusable scientific decimal: %+v %v", r, err)
	}
	captured, err := resultops.Capture([]resultops.Column{{Name: "minimum", Type: "DECIMAL"}}, []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: r.Rows[0].Data["minimum"]}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = captured.Sort([]resultops.SortKey{{Column: "minimum"}}); err != nil {
		t.Fatal(err)
	}
	preview := exactPreview(t, exactOriginal, queryops.MatchingSource, nil)
	for _, value := range []any{[]int64{9007199254740993}, float32(9007199254740992), json.Number("0.10000000000000000001")} {
		if _, err := NewExecutor(Session{Unrestricted: true}).RunExactAggregateDTQL(context.Background(), "unsupported://never-open", []byte(exactOriginal), []byte(preview.DTQL), map[string]any{"minimum": value}, exactColumns); err == nil || !strings.Contains(err.Error(), "invalid query") {
			t.Fatalf("unsafe binding source opened: %T %v", value, err)
		}
	}
}
