package faithful

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func pgScalar(t testing.TB, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return s.String
}

// TestPostgresRoundTripJourney copies a database that uses every feature to PostgreSQL and back, on a
// real server (DATATUG_TEST_POSTGRES_URL), and checks what each side holds.
func TestPostgresRoundTripJourney(t *testing.T) {
	ctx := context.Background()
	src := featureFixture(t)
	pg := newPGDatabase(t)

	rep, err := Copy(ctx, src, pg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "ok-with-warnings" {
		t.Fatalf("status %s", rep.Status)
	}
	// What is not carried is said, per object.
	for _, w := range []struct{ kind, object string }{
		{WarnViewNotCarried, "parent_names"},
		{WarnTriggerNotCarried, "parent_t"},
		{WarnIndexNotCarried, "parent_lower"},
		{WarnTypeWidened, "Parent.odd"},
		{WarnTypeWidened, "Parent.loose"},
		{WarnDefaultNotCarried, "Parent.expr"},
	} {
		if !hasWarning(rep, w.kind, w.object) {
			t.Errorf("no %s warning for %s in %v", w.kind, w.object, rep.Warnings)
		}
	}
	if rep.Totals.Tables != 4 || rep.Totals.Rows != 2+3+3+3 || rep.Totals.PrimaryKeys != 3 || rep.Totals.ForeignKeys != 2 {
		t.Errorf("totals %+v", rep.Totals)
	}

	// The PostgreSQL side.
	types := map[string]string{
		"id": "bigint", "name": "character varying(20)", "code": "character varying(3)", "born": "date",
		"seen": "timestamp without time zone", "odd": "text", "price": "numeric(10,2)", "exact": "numeric(10,2)",
		"ok": "boolean", "photo": "bytea", "small": "smallint", "ratio": "double precision", "note": "text",
		"loose": "text", "doc": "json", "created": "text",
	}
	for col, want := range types {
		got := pgScalar(t, pg.DB, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 'public."Parent"'::regclass AND attname = $1`, col)
		if got != want {
			t.Errorf("column %s: %s, want %s", col, got, want)
		}
	}
	if got := pgScalar(t, pg.DB, `SELECT attidentity::text FROM pg_attribute WHERE attrelid = 'public."Parent"'::regclass AND attname = 'id'`); got != "d" {
		t.Errorf("id is not an identity column: %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT pg_get_serial_sequence('"Parent"', 'id')`); got == "" {
		t.Error("no sequence behind id")
	}
	// The counter continues after sqlite_sequence's 10.
	if _, err := pg.DB.Exec(`INSERT INTO "Parent" (name, code, price) VALUES ('three', 'GHI', 1)`); err != nil {
		t.Fatal(err)
	}
	if got := pgScalar(t, pg.DB, `SELECT max(id)::text FROM "Parent"`); got != "11" {
		t.Errorf("next id %s, want 11", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM pg_constraint WHERE conrelid = 'public.grandchild'::regclass AND contype = 'f' AND confupdtype = 'c' AND confdeltype = 'n' AND array_length(conkey, 1) = 2`); got != "1" {
		t.Errorf("composite foreign key with its actions: %s", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT conname FROM pg_constraint WHERE conrelid = 'public.child'::regclass AND contype = 'f'`); got != "fk_parent" {
		t.Errorf("foreign key name %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT string_agg(a.attname, ',' ORDER BY k.o) FROM pg_constraint c CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY k(n, o) JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.n WHERE c.conrelid = 'public.child'::regclass AND c.contype = 'p'`); got != "parent_id,seq" {
		t.Errorf("composite primary key %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT born::text FROM "Parent" WHERE id = 1`); got != "1968-12-08" {
		t.Errorf("date %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT odd FROM "Parent" WHERE id = 1`); got != "2020-01-02" {
		t.Errorf("a DATETIME that holds a date is kept as it is: %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT seen::text FROM "Parent" WHERE id = 2`); got != "2020-01-02 03:04:05.25" {
		t.Errorf("timestamp %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT exact::text FROM "Parent" WHERE id = 1`); got != "12.50" {
		t.Errorf("exact decimal %q", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM pg_indexes WHERE schemaname = 'public' AND indexdef LIKE '%DESC%' AND indexdef LIKE '%parent_born%'`); got != "1" {
		t.Errorf("descending index: %s", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM pg_indexes WHERE indexname = 'parent_loose' AND indexdef LIKE '%WHERE%' AND indexdef LIKE '%UNIQUE%'`); got != "1" {
		t.Errorf("partial unique index: %s", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM pg_constraint WHERE conrelid = 'public."Parent"'::regclass AND contype = 'c'`); got != "2" {
		t.Errorf("check constraints: %s", got)
	}
	if got := pgScalar(t, pg.DB, `SELECT col_description('public."Parent"'::regclass, 5)`); !strings.HasPrefix(got, markerPrefix) || !strings.Contains(got, `"DATETIME"`) {
		t.Errorf("the declared type is recorded on the column: %q", got)
	}

	// A second copy into the same target is a conflict that changes nothing; recreate replaces.
	_, err = Copy(ctx, src, pg, Options{})
	if _, ok := err.(*ConflictError); !ok {
		t.Fatalf("want a ConflictError, got %v", err)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM "Parent"`); got != "3" {
		t.Errorf("a refused copy changed the target: %s rows", got)
	}
	if _, err := Copy(ctx, src, pg, Options{Overwrite: "recreate"}); err != nil {
		t.Fatal(err)
	}

	// And back: SQLite -> PostgreSQL -> SQLite is the database it started as, except what the
	// reports said.
	back := newSQLite(t)
	rep2, err := Copy(ctx, pg, back, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = rep2
	diffs, err := Verify(ctx, src, back)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		switch {
		case d.Object == "Parent.expr" && d.Aspect == "default", // an expression default is not carried to PostgreSQL
			d.Object == "Parent" && d.Aspect == "indexes",           // the expression index is not carried
			d.Object == "Parent.loose" && d.Aspect == "data digest": // a column of mixed storage classes is carried as text (type-widened)
			continue
		}
		t.Errorf("diff: %s", d)
	}
}

func TestPostgresCopyFailureLeavesTheTargetAsItWas(t *testing.T) {
	ctx := context.Background()
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE TABLE keep (id int PRIMARY KEY)`, `INSERT INTO keep VALUES (1)`)
	src := newSQLite(t)
	execAll(t, src.DB,
		`CREATE TABLE keep (id INTEGER PRIMARY KEY, v TEXT)`,
		`CREATE TABLE nopk (k TEXT PRIMARY KEY, v TEXT)`,
		`INSERT INTO nopk VALUES (NULL, 'x')`)
	_, err := Copy(ctx, src, pg, Options{Overwrite: "recreate"})
	if err == nil || !strings.Contains(err.Error(), "holds NULL") {
		t.Fatalf("want a refusal for the NULL key, got %v", err)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM keep`); got != "1" {
		t.Errorf("target changed: %s rows", got)
	}
}

func TestPostgresViolatedForeignKeyIsCreatedNotValid(t *testing.T) {
	ctx := context.Background()
	src := newSQLite(t)
	execAll(t, src.DB,
		`CREATE TABLE p (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO c VALUES (1, 99)`)
	pg := newPGDatabase(t)
	rep, err := Copy(ctx, src, pg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(rep, WarnFKNotValidated, "c(p)") {
		t.Errorf("warnings %v", rep.Warnings)
	}
	if got := pgScalar(t, pg.DB, `SELECT convalidated::text FROM pg_constraint WHERE conrelid = 'public.c'::regclass AND contype = 'f'`); got != "false" {
		t.Errorf("convalidated %s", got)
	}
}

func TestPostgresTargetSchema(t *testing.T) {
	ctx := context.Background()
	src := featureFixture(t)
	pg := newPGDatabase(t)
	pg.Schema = "copy_target"
	if _, err := Copy(ctx, src, pg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := pgScalar(t, pg.DB, `SELECT count(*)::text FROM copy_target.child`); got != "3" {
		t.Errorf("rows %s", got)
	}
}
