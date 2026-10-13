package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

type columnCountingExecutor struct {
	*secureread.Executor
	calls  int
	cancel context.CancelFunc
}

func (e *columnCountingExecutor) RunDTQL(ctx context.Context, source string, doc []byte, params map[string]any) (secureread.Result, error) {
	e.calls++
	r, err := e.Executor.RunDTQL(ctx, source, doc, params)
	if e.cancel != nil {
		e.cancel()
	}
	return r, err
}

func resultColumnsFixture(t *testing.T, schema, document string) (*SessionChat, RecordSet, *columnCountingExecutor, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "columns.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	source := "sqlite://" + path
	scope := testScope()
	scope.Sources[scope.Database] = source
	store := openTestStore(t, testStorePath(t), scope)
	c, err := NewSessionChat(ctx, store, &contextualStub{}, source)
	if err != nil {
		t.Fatal(err)
	}
	executor := &columnCountingExecutor{Executor: secureread.NewExecutor(secureread.Session{Unrestricted: true})}
	c.ConfigureQueryExecutor(executor)
	result, err := executor.RunDTQL(ctx, source, []byte(document), nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, c.activeID, "Customers with invoice counts")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, c.activeID, user.ID, source, QueryResult{Title: "Invoice counts", SourceID: scope.Database, DTQL: document, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	executor.calls = 0
	return c, s.RecordSets[query.RecordSetID], executor, db
}

const resultColumnsSchema = `CREATE TABLE Customer(CustomerId INTEGER PRIMARY KEY,FirstName TEXT,LastName TEXT);CREATE TABLE Invoice(InvoiceId INTEGER PRIMARY KEY,CustomerId INTEGER REFERENCES Customer(CustomerId));INSERT INTO Customer VALUES(1,'Ada','Lovelace'),(2,'Grace','Hopper');INSERT INTO Invoice VALUES(1,1),(2,1),(3,2),(4,NULL),(5,NULL),(6,999);`
const resultColumnsDocument = "from: {name: Invoice, alias: i}\ncolumns: [{field: CustomerId, source: i}, {aggregate: {function: COUNT, args: [{star: true}]}, as: InvoiceCount}]\ngroupBy: [{field: CustomerId, source: i}]\norderBy: [{field: CustomerId, source: i}]\nlimit: 100\n"

func columnNameIDs(t *testing.T, c *SessionChat, parent RecordSet, names ...string) []string {
	t.Helper()
	rows, err := c.ResultColumns(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range names {
		found := false
		for _, row := range rows {
			if row.Name == name && row.Addable {
				ids = append(ids, row.ID)
				found = true
			}
		}
		if !found {
			t.Fatalf("column %s unavailable: %+v", name, rows)
		}
	}
	return ids
}

func TestResultColumnsGroupedSecureSQLitePreservesCountsAndOrigin(t *testing.T) {
	c, parent, e, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	ctx := context.Background()
	before, _ := json.Marshal(parent)
	ids := columnNameIDs(t, c, parent, "Customer.FirstName", "Customer.LastName")
	if e.calls != 0 {
		t.Fatal("discovery executed a query")
	}
	preview, err := c.resultColumnsPreview(ctx, c.activeID, parent.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.DTQL, "type: left") || !strings.Contains(preview.DTQL, "Customer_FirstName") {
		t.Fatal(preview.DTQL)
	}
	s, query, err := c.executeResultColumns(ctx, c.activeID, parent.ID, ids, preview.Fingerprint, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || query.DTQL != preview.DTQL {
		t.Fatalf("calls %d query %+v", e.calls, query)
	}
	after, _ := json.Marshal(s.RecordSets[parent.ID])
	if string(before) != string(after) {
		t.Fatal("original mutated")
	}
	child := s.RecordSets[query.RecordSetID]
	if child.Lineage == nil || child.Lineage.ParentRecordSetID != parent.ID || child.QueryID == parent.QueryID {
		t.Fatal("lineage missing")
	}
	want := map[string]string{}
	for _, r := range parent.Result.Rows {
		want[fmt.Sprint(r.Data["CustomerId"])] = fmt.Sprint(r.Data["InvoiceCount"])
	}
	if len(child.Result.Rows) != len(parent.Result.Rows) {
		t.Fatalf("cardinality changed: %+v", child.Result.Rows)
	}
	for _, r := range child.Result.Rows {
		k := fmt.Sprint(r.Data["CustomerId"])
		if want[k] != fmt.Sprint(r.Data["InvoiceCount"]) {
			t.Fatalf("count changed %+v", r)
		}
		if k == "1" && (r.Data["Customer_FirstName"] != "Ada" || r.Data["Customer_LastName"] != "Lovelace") {
			t.Fatal(r)
		}
	}
}

func TestResultColumnsRefusesNoCaseTextKeyBeforeExecution(t *testing.T) {
	schema := `CREATE TABLE Customer(CustomerId TEXT PRIMARY KEY COLLATE BINARY,FirstName TEXT,LastName TEXT);CREATE TABLE Invoice(InvoiceId INTEGER PRIMARY KEY,CustomerId TEXT COLLATE NOCASE REFERENCES Customer(CustomerId));INSERT INTO Customer VALUES('a','lower','a'),('A','upper','A');INSERT INTO Invoice VALUES(1,'a'),(2,'A');`
	c, parent, e, db := resultColumnsFixture(t, schema, resultColumnsDocument)
	var nativeGroups, nativeCount int
	if err := db.QueryRow("SELECT COUNT(*), SUM(n) FROM (SELECT COUNT(*) n FROM Invoice GROUP BY CustomerId)").Scan(&nativeGroups, &nativeCount); err != nil || nativeGroups != 1 || nativeCount != 2 {
		t.Fatalf("native NOCASE baseline %d/%d: %v", nativeGroups, nativeCount, err)
	}
	// The current secure DALgo evaluator uses its own grouping equality. Keep
	// its actual immutable baseline too; never claim SQLite-native collation.
	before, _ := json.Marshal(parent)
	rows, err := c.ResultColumns(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Name, "Customer.") && row.Addable {
			t.Fatal("unsafe equality offered", row)
		}
	}
	if e.calls != 0 {
		t.Fatal("metadata executed enrichment")
	}
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s.RecordSets[parent.ID])
	if string(before) != string(after) {
		t.Fatal("NOCASE baseline mutated")
	}
}

func TestResultColumnsStaleCancelAndUnsafeShapes(t *testing.T) {
	c, parent, e, db := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	ids := columnNameIDs(t, c, parent, "Customer.FirstName", "Customer.LastName")
	ctx := context.Background()
	p, err := c.resultColumnsPreview(ctx, c.activeID, parent.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("ALTER TABLE Customer ADD COLUMN Other TEXT"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.executeResultColumns(ctx, c.activeID, parent.ID, ids, p.Fingerprint, ""); err == nil || e.calls != 0 {
		t.Fatal("stale metadata executed", err)
	}
	p, err = c.resultColumnsPreview(ctx, c.activeID, parent.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = c.executeResultColumns(cancelCtx, c.activeID, parent.ID, ids, p.Fingerprint, ""); err == nil || e.calls != 0 {
		t.Fatal("cancel executed", err)
	}
	cancelCtx, cancel = context.WithCancel(ctx)
	e.cancel = cancel
	before, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.executeResultColumns(cancelCtx, c.activeID, parent.ID, ids, p.Fingerprint, ""); err == nil {
		t.Fatal("cancelled completion persisted")
	}
	after, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.RecordSets) != len(after.RecordSets) {
		t.Fatal("cancelled child persisted")
	}
	unsafe := parent
	unsafe.DTQL = strings.ReplaceAll(parent.DTQL, "COUNT", "SUM")
	if _, _, err = resultColumnQuery(unsafe); err == nil {
		t.Fatal("SUM accepted")
	}
}

func TestResultColumnsPreservesHavingFiltersParametersOrderOffset(t *testing.T) {
	doc := strings.Replace(resultColumnsDocument, "orderBy:", "where: {op: '>', left: {field: InvoiceId, source: i}, right: {value: 0}}\nhaving: {op: '>', left: {field: InvoiceCount}, right: {value: 0}}\noffset: 1\norderBy:", 1)
	c, parent, _, _ := resultColumnsFixture(t, resultColumnsSchema, doc)
	ids := columnNameIDs(t, c, parent, "Customer.FirstName", "Customer.LastName")
	p, err := c.resultColumnsPreview(context.Background(), c.activeID, parent.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	s, q, err := c.executeResultColumns(context.Background(), c.activeID, parent.ID, ids, p.Fingerprint, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.DTQL, "having:") || !strings.Contains(q.DTQL, "offset: 1") || len(q.Result.Rows) != len(parent.Result.Rows) {
		t.Fatal(q.DTQL)
	}
	if len(s.RecordSets) != 2 {
		t.Fatal("wrong result count")
	}
}

func TestResultColumnsDeniedSourceAndTargetNames(t *testing.T) {
	c, parent, _, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	for _, fields := range []string{"", "    fields: [CustomerId]\n"} {
		dir := t.TempDir()
		policy := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: invoice-only}\ndefault: deny\nscopes:\n- path: /Invoice\n  rules:\n  - id: allow\n    effect: allow\n    operations: [query]\n" + fields
		if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
		session, err := secureread.NewSession(secureread.SessionOptions{As: "reader", PoliciesDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		c.ConfigureQueryExecutor(secureread.NewExecutor(session))
		rows, err := c.ResultColumns(context.Background(), parent.ID)
		if fields != "" {
			if err == nil {
				t.Fatal("partial source fields advertised complete metadata")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if strings.HasPrefix(r.Name, "Customer.") {
				t.Fatal("denied target field disclosed", r)
			}
		}
	}
}

func TestResultColumnsRefusesCompositeDescAndNonUniqueTargets(t *testing.T) {
	for _, target := range []string{"CustomerId INTEGER, Other INTEGER, PRIMARY KEY(CustomerId,Other)", "CustomerId INTEGER PRIMARY KEY DESC", "CustomerId INTEGER"} {
		schema := "CREATE TABLE Customer(" + target + ",FirstName TEXT,LastName TEXT);CREATE TABLE Invoice(InvoiceId INTEGER PRIMARY KEY,CustomerId INTEGER REFERENCES Customer(CustomerId));INSERT INTO Invoice VALUES(1,999);"
		// A table-level composite constraint has to follow physical columns.
		if strings.Contains(target, "PRIMARY KEY(CustomerId,Other)") {
			schema = "CREATE TABLE Customer(CustomerId INTEGER,Other INTEGER,FirstName TEXT,LastName TEXT,PRIMARY KEY(CustomerId,Other));CREATE TABLE Invoice(InvoiceId INTEGER PRIMARY KEY,CustomerId INTEGER REFERENCES Customer(CustomerId));INSERT INTO Invoice VALUES(1,999);"
		}
		c, parent, e, _ := resultColumnsFixture(t, schema, resultColumnsDocument)
		rows, err := c.ResultColumns(context.Background(), parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if strings.HasPrefix(r.Name, "Customer.") && r.Addable {
				t.Fatal("non-rowid uniqueness offered", r)
			}
		}
		if e.calls != 0 {
			t.Fatal("metadata executed")
		}
	}
}

func TestResultColumnsSimpleProjectionAddsMultipleFields(t *testing.T) {
	doc := "from: {name: Customer}\ncolumns: [{field: CustomerId}]\norderBy: [{field: CustomerId}]\nlimit: 100\n"
	c, parent, e, _ := resultColumnsFixture(t, resultColumnsSchema, doc)
	ids := columnNameIDs(t, c, parent, "FirstName", "LastName")
	p, err := c.resultColumnsPreview(context.Background(), c.activeID, parent.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	_, q, err := c.executeResultColumns(context.Background(), c.activeID, parent.ID, ids, p.Fingerprint, "")
	if err != nil || e.calls != 1 || len(q.Result.Columns) != 3 || q.Result.Rows[0].Data["FirstName"] != "Ada" {
		t.Fatalf("simple projection %+v %v", q, err)
	}
}
