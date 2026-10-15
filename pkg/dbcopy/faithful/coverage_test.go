package faithful

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBatchSize(t *testing.T) {
	cases := []struct {
		to    Engine
		ncols int
		want  int
	}{{Postgres, 5, 1000}, {Postgres, 100, 600}, {Postgres, 70000, 1}, {SQLite, 5, 500}, {SQLite, 100, 300}, {SQLite, 0, 500}}
	for _, c := range cases {
		if got := batchSize(c.to, c.ncols); got != c.want {
			t.Errorf("%s %d columns: %d, want %d", c.to, c.ncols, got, c.want)
		}
	}
}

func TestSQLScanningWithQuotes(t *testing.T) {
	// A doubled quote inside a string does not end it; a bracketed name is skipped whole.
	sql := `CREATE TABLE t ("a""b" TEXT, [c)d] INT, e TEXT CHECK (e <> 'it''s (' ), f INT)`
	if got := extractChecks(sql); len(got) != 1 || got[0].Expr != `e <> 'it''s ('` {
		t.Errorf("%+v", got)
	}
	if got := splitTopLevel(sql); len(got) != 4 {
		t.Errorf("%d parts: %q", len(got), got)
	}
	if matchParen(`('it''s)' )`, 0) != 10 {
		t.Error("matchParen with a doubled quote")
	}
	if got := bracketToQuoted(`'it''s' || [x]`); got != `'it''s' || "x"` {
		t.Errorf("%s", got)
	}
	if got := bracketToQuoted(`'unterminated`); got != `'unterminated` {
		t.Errorf("%s", got)
	}
	if splitTopLevel("no parens") != nil || splitTopLevel("(unclosed") != nil {
		t.Error("splitTopLevel without a body")
	}
	if matchParen(`("a""b" [x)])`, 0) != 12 {
		t.Error("matchParen with quoted names")
	}
	var seen []byte
	walkSQL(`a 'x''y' "p""q" [r] -- c`+"\n"+`b`, func(i, depth int) bool { seen = append(seen, `a 'x''y' "p""q" [r] -- c`[0]); return true })
	if len(seen) == 0 {
		t.Error("walkSQL")
	}
}

func TestVerifyHelpers(t *testing.T) {
	if normDefault("'12.50'") != "12.50" || normDefault("'abc'") != "'abc'" || normDefault(" now() ") != "<current timestamp>" {
		t.Error("normDefault")
	}
	tb := &Table{Indexes: []Index{{Name: "i", Unique: true, Columns: []IndexColumn{{Name: "a", Desc: true}}}, {Name: "j", Unique: true, Columns: []IndexColumn{{Name: "b"}}}}}
	if uniqSet(tb) != "(b)" || !strings.Contains(idxSet(tb), "a DESC") || !hasDesc(tb.Indexes[0]) {
		t.Errorf("%q %q", uniqSet(tb), idxSet(tb))
	}
	h := sha256.New()
	hashValue(h, time.Unix(0, 0).UTC())
	hashValue(h, nil)
	hashValue(h, int64(1))
	hashValue(h, 1.5)
	hashValue(h, "s")
	hashValue(h, []byte("b"))
}

func TestVerifyMissingColumn(t *testing.T) {
	ctx := context.Background()
	a, b := newSQLite(t), newSQLite(t)
	execAll(t, a.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, x TEXT, y TEXT)`, `INSERT INTO t VALUES (1, 'a', 'b')`)
	execAll(t, b.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, x TEXT)`, `INSERT INTO t VALUES (1, 'a')`)
	diffs, err := Verify(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"t.y column": false, "t column order": false}
	for _, d := range diffs {
		want[d.Object+" "+d.Aspect] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("not reported: %s in %v", k, diffs)
		}
	}
	// A table that is only in the right-hand database; a column only on the right.
	execAll(t, b.DB, `CREATE TABLE only_right (x)`, `ALTER TABLE t ADD COLUMN z TEXT`)
	diffs, err = Verify(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) < 3 {
		t.Errorf("%v", diffs)
	}
}

// replayCopy copies src into a replayed PostgreSQL whose recorded answer to a count is changed.
func TestCountMismatchRollsBack(t *testing.T) {
	b, err := os.ReadFile(scenarioFile("sqlite_to_pg_and_back", 1))
	if err != nil {
		t.Fatal(err)
	}
	var calls []callRec
	if err := json.Unmarshal(b, &calls); err != nil {
		t.Fatal(err)
	}
	changed := false
	for i := range calls {
		if calls[i].Kind == "query" && strings.HasPrefix(calls[i].SQL, `SELECT count(*) FROM "public"."Parent"`) && !changed {
			calls[i].Rows = [][]cell{{{T: "i", V: json.RawMessage("999")}}}
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("no count in the recording")
	}
	state := &replayState{calls: calls}
	db := sql.OpenDB(chaosConnector{open: func(context.Context) (driver.Conn, error) { return &replayConn{state: state}, nil }})
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	src := openSQLite(t, t.TempDir()+"/s.sqlite")
	execAll(t, src.DB, featureStatements...)
	_, err = Copy(context.Background(), src, Endpoint{Engine: Postgres, DB: db}, Options{})
	if err == nil || !strings.Contains(err.Error(), "999 are in the target") {
		t.Errorf("want a count mismatch, got %v", err)
	}
}

func TestLoadTableReportsAnEncoderFailure(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO t VALUES (1, 'x')`)
	x := &executor{ctx: context.Background(), from: src, to: Endpoint{Engine: SQLite}, srcQ: src.DB}
	tt := &TargetTable{Name: "t", PK: []string{"id"}, Columns: []*TargetColumn{
		{Name: "id", readExpr: `"id"`, enc: encInt},
		{Name: "v", readExpr: `"v"`, enc: encInt}, // a text is not an integer
	}}
	if _, err := x.loadTable(nil, tt); err == nil || !strings.Contains(err.Error(), `column "v"`) {
		t.Errorf("%v", err)
	}
	// A source whose query fails.
	bad := &TargetTable{Name: "missing", Columns: []*TargetColumn{{Name: "id", readExpr: `"id"`, enc: encInt}}}
	if _, err := x.loadTable(nil, bad); err == nil {
		t.Error("a missing table")
	}
}

func TestTolerated(t *testing.T) {
	if (&tolerated{err: errInjected}).Error() != errInjected.Error() {
		t.Error("tolerated")
	}
}
