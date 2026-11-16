package secureread

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
)

const phase1TablePolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: exact-tables}
default: deny
scopes:
  - path: /A
    rules: [{id: a, effect: allow, operations: [query]}]
  - path: /C
    rules: [{id: c, effect: allow, operations: [query]}]
`

const phase1AQuery = "from: {name: A}\ncolumns: [{field: label}]\n"

func phase1JoinQuery(table string) []byte {
	return []byte("from:\n  name: A\n  alias: a\n  joins:\n    - from: {name: " + table + ", alias: j}\n      on: [{left: {field: id, source: a}, op: '==', right: {field: id, source: j}}]\ncolumns: [{field: label, source: a, as: a_label}, {field: label, source: j, as: joined_label}]\n")
}

type phase1DispatchDB struct {
	dal.DB
	calls *int
}

func (db phase1DispatchDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	*db.calls++
	return db.DB.ExecuteQueryToRecordsReader(ctx, query)
}

func (db phase1DispatchDB) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	*db.calls++
	return db.DB.ExecuteQueryToRecordsetReader(ctx, query, options...)
}

func TestExactTableReadonlyNativeDispatchBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase1.sqlite")
	fixture, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE A (id INTEGER PRIMARY KEY, label TEXT); INSERT INTO A VALUES (1,'allowed-a')`,
		`CREATE TABLE B (id INTEGER PRIMARY KEY, label TEXT); INSERT INTO B VALUES (1,'forbidden-b')`,
		`CREATE TABLE C (id INTEGER PRIMARY KEY, label TEXT); INSERT INTO C VALUES (1,'allowed-c')`,
	} {
		if _, err := fixture.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	source := "sqlite://" + path
	old := openSourceFn
	t.Cleanup(func() { openSourceFn = old })
	calls, opens := 0, 0
	openSourceFn = func(ctx context.Context, url string, insecure bool, policies int) (dal.DB, func(), error) {
		opens++
		db, closeSource, err := old(ctx, url, insecure, policies)
		if err != nil {
			return nil, closeSource, err
		}
		return phase1DispatchDB{DB: db, calls: &calls}, closeSource, nil
	}
	executor := NewExecutor(aliceSession(t, phase1TablePolicy))
	for _, test := range []struct {
		name    string
		doc     []byte
		allowed bool
		label   string
	}{
		{"A", []byte(phase1AQuery), true, "allowed-a"},
		{"B", []byte("from: {name: B}\n"), false, ""},
		{"A+C", phase1JoinQuery("C"), true, "allowed-c"},
		{"A+B", phase1JoinQuery("B"), false, ""},
		{"absent", []byte("from: {name: Missing}\n"), false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := calls
			result, err := executor.RunTugQL(context.Background(), source, test.doc, nil)
			if !test.allowed {
				if !errors.Is(err, ErrAccessDenied) || calls != before || len(result.Rows) != 0 {
					t.Fatalf("denied query: err=%v nativeCalls=%d rows=%d", err, calls-before, len(result.Rows))
				}
				return
			}
			if err != nil || calls != before+1 || len(result.Rows) != 1 {
				t.Fatalf("allowed query: err=%v nativeCalls=%d result=%+v", err, calls-before, result)
			}
			field := "label"
			if test.name == "A+C" {
				field = "joined_label"
			}
			if result.Rows[0].Data[field] != test.label {
				t.Fatalf("incorrect actual native row: %+v", result.Rows)
			}
		})
	}
	before := calls
	// This public Go query path cannot smuggle an opaque write past table-only rules.
	if _, err := executor.RunStructured(context.Background(), source, dal.NewTextQuery("DELETE FROM A", nil), nil); !errors.Is(err, ErrAccessDenied) || calls != before {
		t.Fatalf("opaque shape reached native dispatch: %v", err)
	}
	beforeOpen := opens
	if _, err := executor.RunTugQL(context.Background(), source, []byte("delete: {from: A}\n"), nil); err == nil || opens != beforeOpen {
		t.Fatalf("mutation document opened a source: %v", err)
	}
	if _, err := executor.RunNativeSQL(context.Background(), "unsupported://must-not-open", "DELETE FROM A"); !errors.Is(err, ErrOpaqueSQLNotGranted) || opens != beforeOpen {
		t.Fatalf("ungranted raw query opened a source: %v", err)
	}
	ctx := access.WithPolicy(context.Background(), access.MustPolicy("mandatory-context", access.Collection("A", access.Deny(access.Query))))
	if _, err := executor.RunTugQL(ctx, source, []byte(phase1AQuery), nil); !errors.Is(err, ErrAccessDenied) || calls != before {
		t.Fatalf("context deny was dropped: %v", err)
	}
	// Final real read proves the rejected DELETE did not mutate the fixture.
	result, err := executor.RunTugQL(context.Background(), source, []byte(phase1AQuery), nil)
	if err != nil || len(result.Rows) != 1 || result.Rows[0].Data["label"] != "allowed-a" {
		t.Fatalf("data changed after denied writes: %+v %v", result, err)
	}
}

func TestExactTableReadonlyInGitDBExistingPolicy(t *testing.T) {
	source := newInGitDBFixture(t)
	executor := NewExecutor(aliceSession(t, productsOnlyPolicy))
	allowed, err := executor.RunStructured(context.Background(), source, productsQuery(), nil)
	if err != nil || len(allowed.Rows) != 3 {
		t.Fatalf("allowed collection: %+v %v", allowed, err)
	}
	denied, err := executor.RunStructured(context.Background(), source, customersQuery(), nil)
	if !errors.Is(err, ErrAccessDenied) || len(denied.Rows) != 0 {
		t.Fatalf("denied collection: %+v %v", denied, err)
	}
}
