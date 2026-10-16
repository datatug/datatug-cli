package faithful

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// A scenario is a sequence of copies and checks that touches every part of the engine. It runs three
// ways: against a real PostgreSQL server while recording (DATATUG_RECORD_SCENARIOS=1), against the
// recording with no server (the ordinary test run), and against the recording with one call made to
// fail (the chaos sweep: every error branch of the copy runs).

type scenarioEnv struct {
	t      testing.TB
	ctx    context.Context
	name   string
	plan   *faultPlan
	record bool
	pgN    int
	states []*replayState
	// copies and diffs are what the scenario's Copy and Verify calls returned, in order.
	copies []copyOutcome
	diffs  [][]Diff
	kept   map[string]Endpoint
}

// keep remembers an endpoint of the scenario, for the checks.
func (e *scenarioEnv) keep(name string, ep Endpoint) {
	if e.kept == nil {
		e.kept = map[string]Endpoint{}
	}
	e.kept[name] = ep
}

// sequenceOf is the AUTOINCREMENT counter of a table of the kept SQLite database "mid", "" for none.
func (e *scenarioEnv) sequenceOf(t testing.TB, table string) string {
	t.Helper()
	var seq sql.NullInt64
	err := e.kept["mid"].DB.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(seq.Int64)
}

type copyOutcome struct {
	rep *Report
	err error
}

func (e *scenarioEnv) copy(from, to Endpoint, o Options) {
	rep, err := Copy(e.ctx, from, to, o)
	e.copies = append(e.copies, copyOutcome{rep, err})
}

func (e *scenarioEnv) verify(a, b Endpoint) {
	d, _ := Verify(e.ctx, a, b)
	e.diffs = append(e.diffs, d)
}

func (e *scenarioEnv) pg() Endpoint {
	e.t.Helper()
	e.pgN++
	if e.record {
		return recordPG(e.t, e.name, e.pgN)
	}
	ep, state := replayPG(e.t, e.name, e.pgN, e.plan)
	e.states = append(e.states, state)
	return ep
}

// lite returns an empty SQLite database.
func (e *scenarioEnv) lite() Endpoint { return chaosSQLite(e.t, e.plan) }

// fixture builds a SQLite database with build on a plain connection, then opens it again through the
// fault plan.
func (e *scenarioEnv) fixture(statements ...string) Endpoint {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "fixture.sqlite")
	key := strings.Join(statements, "\x00")
	fixtureCache.Lock()
	image, ok := fixtureCache.m[key]
	fixtureCache.Unlock()
	if !ok {
		plain := openSQLite(e.t, path)
		execAll(e.t, plain.DB, statements...)
		_ = plain.DB.Close()
		var err error
		if image, err = os.ReadFile(path); err != nil {
			e.t.Fatal(err)
		}
		fixtureCache.Lock()
		fixtureCache.m[key] = image
		fixtureCache.Unlock()
	} else if err := os.WriteFile(path, image, 0o600); err != nil {
		e.t.Fatal(err)
	}
	return chaosSQLiteAt(e.t, path, e.plan)
}

// fixtureCache keeps the bytes of every fixture database so that the chaos sweep builds each once.
var fixtureCache = struct {
	sync.Mutex
	m map[string][]byte
}{m: map[string][]byte{}}

// quiet runs statements and ignores failures: under a fault plan they are expected.
func quiet(db *sql.DB, statements ...string) {
	for _, s := range statements {
		_, _ = db.Exec(s)
	}
}

type scenario struct {
	name string
	pgs  int // how many PostgreSQL databases it uses
	run  func(e *scenarioEnv)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var featureStatements = []string{
	`CREATE TABLE "Parent" (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name VARCHAR(20) NOT NULL,
		code CHAR(3) DEFAULT 'ABC',
		born DATE,
		seen DATETIME,
		odd DATETIME,
		price DECIMAL(10,2) NOT NULL DEFAULT 0,
		exact DECIMAL_TEXT(10,2),
		ok BOOLEAN DEFAULT 1,
		photo BLOB,
		small INT2,
		ratio REAL,
		note TEXT,
		loose,
		doc JSON_TEXT,
		created TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expr INTEGER DEFAULT (1+2),
		UNIQUE (name, code),
		CONSTRAINT chk_price CHECK (price >= 0),
		CHECK (code GLOB '[A-Z][A-Z][A-Z]')
	)`,
	`CREATE INDEX parent_born ON "Parent" (born DESC, name)`,
	`CREATE UNIQUE INDEX parent_loose ON "Parent" (note) WHERE note IS NOT NULL`,
	`CREATE INDEX parent_lower ON "Parent" (lower(name))`,
	`CREATE TABLE child (
		parent_id INTEGER NOT NULL,
		seq INTEGER NOT NULL,
		label TEXT,
		PRIMARY KEY (parent_id, seq),
		CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES "Parent"(id) ON DELETE CASCADE ON UPDATE NO ACTION
	)`,
	`CREATE TABLE grandchild (
		a INTEGER, b INTEGER, n INTEGER PRIMARY KEY,
		FOREIGN KEY (a, b) REFERENCES child(parent_id, seq) ON DELETE SET NULL ON UPDATE CASCADE
	)`,
	`CREATE TABLE keyless (x TEXT, y INTEGER)`,
	`CREATE TABLE emptyauto (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
	`CREATE VIEW parent_names AS SELECT name FROM "Parent"`,
	`CREATE TRIGGER parent_t AFTER INSERT ON "Parent" BEGIN SELECT 1; END`,
	`INSERT INTO "Parent" (id, name, code, born, seen, odd, price, exact, ok, photo, small, ratio, note, loose, doc)
	 VALUES (1, 'one', 'ABC', '1968-12-08', '2020-01-02 03:04:05', '2020-01-02', 1.98, '12.50', 1, X'00FF10', 7, 0.5, 'n1', 5, '{"a": 1}'),
	        (2, 'two', 'DEF', NULL, '2020-01-02 03:04:05.25', '2020-01-02 03:04', 3, '0.05', 0, NULL, NULL, NULL, NULL, 'text', '[1,2]')`,
	`DELETE FROM sqlite_sequence`, `INSERT INTO sqlite_sequence (name, seq) VALUES ('Parent', 10)`,
	`INSERT INTO child VALUES (1, 1, 'a'), (1, 2, 'b'), (2, 1, 'c')`,
	`INSERT INTO grandchild VALUES (1, 1, 1), (2, 1, 2), (NULL, NULL, 3)`,
	`INSERT INTO keyless VALUES ('a', 1), ('a', 1), (NULL, NULL)`,
}

var pgFixtureStatements = []string{
	`CREATE TABLE dept (
		id serial PRIMARY KEY,
		name varchar(30) NOT NULL UNIQUE,
		code char(3) DEFAULT 'ABC',
		budget numeric(12,2) DEFAULT 0,
		active boolean DEFAULT true,
		opened date,
		at timestamp,
		atz timestamptz,
		t time,
		tiny smallint,
		mid integer,
		f4 real,
		f8 double precision,
		data bytea,
		doc jsonb,
		uid uuid,
		tags text[],
		note text DEFAULT 'x',
		made text DEFAULT to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'),
		stamp timestamptz DEFAULT now(),
		CONSTRAINT dept_budget_chk CHECK (budget >= 0),
		CONSTRAINT dept_code_chk CHECK (code ~ '^[A-Z]{3}$'),
		CONSTRAINT dept_note_chk CHECK (note = ANY (ARRAY['x', 'y']))
	)`,
	`COMMENT ON COLUMN dept.note IS 'a comment'`,
	`CREATE TABLE emp (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		dept_id integer REFERENCES dept ON DELETE CASCADE ON UPDATE RESTRICT DEFERRABLE INITIALLY DEFERRED,
		boss bigint REFERENCES emp(id) ON DELETE SET NULL,
		name text NOT NULL COLLATE "C",
		double_id integer GENERATED ALWAYS AS (dept_id * 2) STORED
	)`,
	`CREATE TABLE link (a integer, b integer, PRIMARY KEY (a, b), FOREIGN KEY (a) REFERENCES dept(id))`,
	`CREATE TABLE noident (id integer GENERATED BY DEFAULT AS IDENTITY, k text, PRIMARY KEY (id, k))`,
	`CREATE INDEX emp_dept ON emp (dept_id DESC)`,
	`CREATE UNIQUE INDEX emp_boss ON emp (boss) WHERE boss IS NOT NULL`,
	`CREATE INDEX emp_lower ON emp (lower(name))`,
	`CREATE INDEX emp_hash ON emp USING hash (name)`,
	`CREATE INDEX emp_incl ON emp (dept_id) INCLUDE (name)`,
	`CREATE INDEX emp_partial_bad ON emp (dept_id) WHERE name ~~ 'a%'`,
	`CREATE VIEW emp_names AS SELECT name FROM emp`,
	`CREATE MATERIALIZED VIEW emp_m AS SELECT name FROM emp`,
	`CREATE FUNCTION noop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
	`CREATE TRIGGER emp_t BEFORE INSERT ON emp FOR EACH ROW EXECUTE FUNCTION noop()`,
	`INSERT INTO dept (name, budget, opened, at, atz, t, tiny, mid, f4, f8, data, doc, uid, tags)
	 VALUES ('eng', 12.50, '1968-12-08', '2020-01-02 03:04:05.25', '2020-01-02 03:04:05+00', '03:04:05', 7, 70000, 1.1, 2.5, '\x00ff', '{"a": 1}', '123e4567-e89b-12d3-a456-426614174000', '{a,b}'),
	        ('ops', 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`,
	`INSERT INTO emp (dept_id, boss, name) VALUES (1, NULL, 'ann'), (1, 1, 'bob'), (2, NULL, 'cy')`,
	`INSERT INTO link VALUES (1, 1), (1, 2)`,
	`INSERT INTO noident (k) VALUES ('a'), ('b')`,
}

// valueStatements are the values that a copy used to change without a word: an empty blob, a number
// a 4-byte real cannot hold, json that jsonb would rewrite, an offset that timestamptz would turn
// into UTC, a negative zero in a decimal text, and a text default that is a number.
var valueStatements = []string{
	`CREATE TABLE vals (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		b BLOB, f4 FLOAT4, f4w FLOAT4, jb JSONB_TEXT, jbw JSONB_TEXT, tz TIMESTAMPTZ, tzw TIMESTAMPTZ,
		dt DECIMAL_TEXT(10,2) DEFAULT '0.00', dtn DECIMAL_TEXT(10,2), plain NUMERIC, wide DECIMAL_TEXT(5,1),
		s TEXT, nul TEXT, big INTEGER, mix FLOAT8
	)`,
	`INSERT INTO vals (id, b, f4, f4w, jb, jbw, tz, tzw, dt, dtn, plain, wide, s, nul, big, mix) VALUES
		(1, X'', 1.5, 16777217.0, '{"a": 1, "bb": [1, 2]}', '{"b":1,"a":2}', '2020-01-01 10:00:00+00', '2020-01-01 10:00:00+02', '0.00', '-0.00', '.5', '1.25', '', 'a' || char(0) || 'b', 9007199254740993, 0.25),
		(2, X'00', 0.1, 0.30000000000000004, '[]', '{"a":1,"a":2}', '2020-01-01 10:00:00.5+00', '2020-06-01 00:00:00-05:30', '12.50', '0.10', NULL, NULL, NULL, NULL, 1, 7),
		(3, NULL, NULL, 5e-45, 'null', '"s"', NULL, NULL, NULL, NULL, '3', '2', 'x', 'y', NULL, NULL)`,
	`DELETE FROM sqlite_sequence`, `INSERT INTO sqlite_sequence (name, seq) VALUES ('vals', 100)`,
	`CREATE TABLE tnum (id INTEGER PRIMARY KEY, t TEXT DEFAULT 0.00, big DECIMAL(25,2))`, `INSERT INTO tnum VALUES (1, 'a', 1234567890123456789), (2, 'b', 9223372036854775807), (3, 'c', 1.5), (4, 'd', NULL)`,
	`CREATE TABLE times (id INTEGER PRIMARY KEY, tm TIME, tz TIMESTAMPTZ, tz2 TIMESTAMPTZ)`, `INSERT INTO times VALUES (1, '23:59:60', '2016-12-31 23:59:60+00', '2021-02-30 00:00:00+00'), (2, '25:00:00', '2020-01-01 24:00:00+00', '2020-01-01 00:00:00+00')`,
}

// pgValueStatements are values and settings that made the copy of a PostgreSQL source depend on the
// server: the date, interval and float output settings of the database, and values SQLite cannot hold.
var pgValueStatements = []string{
	`DO $$ BEGIN
		EXECUTE format('ALTER DATABASE %I SET datestyle = ''SQL, DMY''', current_database());
		EXECUTE format('ALTER DATABASE %I SET intervalstyle = ''sql_standard''', current_database());
		EXECUTE format('ALTER DATABASE %I SET extra_float_digits = -3', current_database());
		EXECUTE format('ALTER DATABASE %I SET timezone = ''Asia/Tokyo''', current_database());
		EXECUTE format('ALTER DATABASE %I SET statement_timeout = ''1ms''', current_database());
	END $$`,
	`CREATE TABLE fl (id serial PRIMARY KEY, r real, d double precision, b bytea, c char(4), iv interval, ts timestamp, tz timestamptz, dt date)`,
	`INSERT INTO fl (r, d, b, c, iv, ts, tz, dt) VALUES
		('NaN', 'NaN', '\x', 'ab', '1 day 02:03:04', '2020-01-02 03:04:05.5', '2020-01-02 03:04:05+02', '2020-01-02'),
		('-0', '-0', '\x00', 'abcd', '-1 mon', 'infinity', '-infinity', 'infinity'),
		('Infinity', '-Infinity', NULL, NULL, NULL, NULL, NULL, NULL),
		(1.1, 0.1, '\xff', 'x', '00:00:01', '2000-01-01', '2000-01-01 00:00:00+00', '2000-01-01')`,
	`CREATE TABLE sq_new (id integer GENERATED BY DEFAULT AS IDENTITY (START WITH 100 INCREMENT BY 5) PRIMARY KEY, v text)`,
	`CREATE TABLE sq_plain (id serial PRIMARY KEY)`,
	`CREATE TABLE sq_used (id serial PRIMARY KEY, v text)`,
	`INSERT INTO sq_used (v) VALUES ('a'), ('b')`,
	`CREATE TABLE sq_clash (id serial PRIMARY KEY)`,
	`INSERT INTO sq_clash (id) VALUES (1), (2)`,
	`CREATE TABLE sq_called (id integer GENERATED BY DEFAULT AS IDENTITY (START WITH 10 INCREMENT BY 3) PRIMARY KEY)`,
	`INSERT INTO sq_called DEFAULT VALUES`,
	`INSERT INTO sq_called DEFAULT VALUES`,
	`CREATE SEQUENCE loose_seq`,
	`CREATE TABLE sq_loose (id integer PRIMARY KEY DEFAULT nextval('loose_seq'), v text)`,
}

// oddPGStatements are the things only a PostgreSQL source can hold that a copy has to report.
var oddPGStatements = []string{
	`CREATE SCHEMA other`,
	`CREATE TABLE other.o (id int PRIMARY KEY)`,
	`CREATE TABLE base (id int PRIMARY KEY, grp int, r int4range, EXCLUDE USING gist (r WITH &&))`,
	`CREATE TABLE kid (id int PRIMARY KEY, base_id int, o_id int, FOREIGN KEY (base_id) REFERENCES base(id) MATCH FULL, FOREIGN KEY (o_id) REFERENCES other.o(id))`,
	`CREATE TABLE parted (id int, day date) PARTITION BY RANGE (day)`,
	`CREATE TABLE parted_2020 PARTITION OF parted FOR VALUES FROM ('2020-01-01') TO ('2021-01-01')`,
	`ALTER TABLE base ENABLE ROW LEVEL SECURITY`,
	`CREATE FOREIGN DATA WRAPPER dummy_fdw`,
	`CREATE SERVER dummy_srv FOREIGN DATA WRAPPER dummy_fdw`,
	`CREATE FOREIGN TABLE ft (a int) SERVER dummy_srv`,
	`CREATE TABLE marked (id int PRIMARY KEY, v text)`,
	`COMMENT ON COLUMN marked.v IS 'datatug:{"src":"sqlite","type":"NVARCHAR(10)"}'`,
	`CREATE TABLE badmarked (id int PRIMARY KEY, v text)`,
	`COMMENT ON COLUMN badmarked.v IS 'datatug:not json'`,
	`CREATE UNIQUE INDEX sqlite_autoindex_marked_1 ON marked (v)`,
	`ALTER TABLE marked ADD COLUMN d2 uuid DEFAULT gen_random_uuid()`,
	`CREATE TABLE seqs (id serial PRIMARY KEY, v text)`,
	`INSERT INTO seqs (v) VALUES ('a')`,
	`INSERT INTO base VALUES (1, 1, '[1,3)')`,
}

// oddSQLiteStatements make PostgreSQL refuse what the source accepted.
var oddSQLiteStatements = []string{
	`CREATE TABLE p (id INTEGER PRIMARY KEY, u INTEGER)`,
	`CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
	`CREATE TABLE nonkey (id INTEGER PRIMARY KEY, ref INTEGER REFERENCES p(u))`,
	`CREATE TABLE badthings (id INTEGER PRIMARY KEY, x TEXT, CHECK (typeof(x) = 'text'))`,
	`CREATE INDEX badidx ON badthings (id) WHERE typeof(x) = 'text'`,
	`CREATE TABLE types (id INTEGER PRIMARY KEY, a NUMERIC(5,2), b DATETIME, c TIMESTAMPTZ, d TIME, e UUID_TEXT, f FLOAT4, g INT4, h BOOLEAN, i, j BLOB, k TEXT)`,
	`INSERT INTO p VALUES (1, 1), (2, 1)`,
	`INSERT INTO c VALUES (1, 99)`,
	`INSERT INTO nonkey VALUES (1, 1)`,
	`INSERT INTO badthings VALUES (1, 'x')`,
	`INSERT INTO types VALUES (1, 1.255, '2020-01-02 03:04:05.50', '2020-01-02 03:04:05+00', '03:04:05', 'not-a-uuid', 1.5, 5000000000, 2, NULL, 'text', X'01'), (2, 'abc', '2020-01-02 03:04:05', '2020-01-02 03:04:05+00', '03:04:05', '123e4567-e89b-12d3-a456-426614174000', 2, 5, 1, 7, X'02', 'a' || char(0) || 'b')`,
}

var scenarios = []scenario{
	{name: "sqlite_to_pg_and_back", pgs: 2, run: func(e *scenarioEnv) {
		src := e.fixture(featureStatements...)
		pg, pg2 := e.pg(), e.pg()
		e.copy(src, pg, Options{Progress: io.Discard})
		e.copy(src, pg, Options{})
		e.copy(src, pg, Options{Overwrite: "recreate", Exclude: []string{"keyless"}})
		e.copy(src, pg, Options{Overwrite: "recreate"})
		back := e.lite()
		e.copy(pg, back, Options{})
		e.verify(src, back)
		e.copy(back, pg2, Options{})
		e.verify(pg, pg2)
	}},
	{name: "pg_origin", pgs: 2, run: func(e *scenarioEnv) {
		pg := e.pg()
		quiet(pg.DB, pgFixtureStatements...)
		mid := e.lite()
		e.copy(pg, mid, Options{})
		e.copy(pg, e.lite(), Options{Include: []string{"dept", "emp"}})
		pg2 := e.pg()
		e.copy(mid, pg2, Options{})
		e.verify(pg, pg2)
	}},
	{name: "pg_oddities", pgs: 1, run: func(e *scenarioEnv) {
		pg := e.pg()
		quiet(pg.DB, oddPGStatements...)
		e.copy(pg, e.lite(), Options{})
		other := pg
		other.Schema = "other"
		e.copy(other, e.lite(), Options{})
		empty := pg
		empty.Schema = "nothing_here"
		e.copy(empty, e.lite(), Options{})
	}},
	{name: "sqlite_odd_to_pg", pgs: 1, run: func(e *scenarioEnv) {
		src := e.fixture(oddSQLiteStatements...)
		pg := e.pg()
		pg.Schema = "copy_target"
		e.copy(src, pg, Options{})
		nulls := e.fixture(`CREATE TABLE t (k TEXT PRIMARY KEY, v TEXT)`, `INSERT INTO t VALUES (NULL, 'x')`)
		e.copy(nulls, pg, Options{})
	}},
	{name: "sqlite_to_sqlite", pgs: 0, run: func(e *scenarioEnv) {
		src := e.fixture(featureStatements...)
		dst := e.lite()
		e.copy(src, dst, Options{})
		e.copy(src, dst, Options{})
		e.copy(src, dst, Options{Overwrite: "recreate"})
		e.copy(src, e.lite(), Options{Exclude: []string{"Parent"}})
		e.copy(src, e.lite(), Options{DryRun: true})
		e.verify(src, dst)
		many := e.fixture(`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`,
			`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 600) INSERT INTO big SELECT x, 'v' || x FROM c`)
		e.copy(many, e.lite(), Options{})
		odd := e.fixture(`CREATE TABLE r (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE) STRICT`,
			`CREATE TABLE w (k TEXT PRIMARY KEY, v) WITHOUT ROWID`,
			`CREATE TABLE d (id INTEGER PRIMARY KEY, p INTEGER REFERENCES r(id) DEFERRABLE INITIALLY DEFERRED)`,
			`CREATE TABLE gen (id INTEGER PRIMARY KEY, g INTEGER GENERATED ALWAYS AS (id*2) VIRTUAL)`,
			`INSERT INTO r VALUES (1, 'a')`, `INSERT INTO d VALUES (1, 5)`, `INSERT INTO gen (id) VALUES (3)`)
		e.copy(odd, e.lite(), Options{})
		// Where, limit and reload between two SQLite files.
		part := e.lite()
		e.copy(src, part, Options{Include: []string{"Parent", "child"}, Filters: map[string]RowFilter{
			"Parent": {Where: []Cond{{"id", "=", "2"}}}, "child": {Limit: 1}}})
		e.copy(src, part, Options{Include: []string{"Parent", "child"}, Overwrite: "reload"})
		quiet(part.DB, `ALTER TABLE child DROP COLUMN label`)
		e.copy(src, part, Options{Include: []string{"Parent", "child"}, Overwrite: "reload"})
		// A table that an earlier copy wrote beside the data is not data.
		side := e.fixture(`CREATE TABLE _dalgo_time_columns (t TEXT, c TEXT)`, `CREATE TABLE real_t (id INTEGER PRIMARY KEY)`, `INSERT INTO real_t VALUES (1)`)
		e.copy(side, e.lite(), Options{})
		e.copy(side, e.lite(), Options{Include: []string{legacySidecar}})
		e.copy(src, e.lite(), Options{Overwrite: "merge"})
	}},
	{name: "sqlite_values_to_pg", pgs: 2, run: func(e *scenarioEnv) {
		src := e.fixture(valueStatements...)
		pg, pg2 := e.pg(), e.pg()
		e.copy(src, pg, Options{})
		back := e.lite()
		e.copy(pg, back, Options{})
		e.verify(src, back)
		e.copy(back, pg2, Options{})
		e.verify(pg, pg2)
		// A copy that is run again replaces what it wrote, in one transaction.
		e.copy(src, pg, Options{Overwrite: "reload"})
		// --decimals=text keeps the decimal texts as they are.
		e.copy(src, pg, Options{Overwrite: "recreate", DecimalsText: true})
	}},
	{name: "pg_values", pgs: 2, run: func(e *scenarioEnv) {
		pg := e.pg()
		quiet(pg.DB, pgValueStatements...)
		mid := e.lite()
		e.keep("mid", mid)
		e.copy(pg, mid, Options{})
		pg2 := e.pg()
		e.copy(mid, pg2, Options{})
		e.verify(pg, pg2)
	}},
	{name: "filters_and_reload", pgs: 2, run: func(e *scenarioEnv) {
		src := e.fixture(featureStatements...)
		pg := e.pg()
		// A filtered copy: the parent rows that are left out make the foreign key NOT VALID.
		e.copy(src, pg, Options{Filters: map[string]RowFilter{
			"Parent":     {Where: []Cond{{"id", "=", "2"}}},
			"child":      {Limit: 5},
			"grandchild": {Where: []Cond{{"n", "in", "1,3"}}},
		}})
		// Filters that name what is not there, or cannot be read.
		e.copy(src, pg, Options{DryRun: true, Filters: map[string]RowFilter{"nope": {Limit: 1}}})
		e.copy(src, pg, Options{DryRun: true, Exclude: []string{"child"}, Filters: map[string]RowFilter{"child": {Limit: 1}}})
		e.copy(src, pg, Options{DryRun: true, Filters: map[string]RowFilter{"child": {Where: []Cond{{"nope", "=", "1"}}}}})
		e.copy(src, pg, Options{DryRun: true, Filters: map[string]RowFilter{"child": {Where: []Cond{{"seq", "=", "x"}}}}})
		e.copy(src, pg, Options{DryRun: true, Filters: map[string]RowFilter{"child": {Where: []Cond{{"seq", "<>", "1"}}}}})
		e.copy(src, pg, Options{DryRun: true, Filters: map[string]RowFilter{"child": {Where: []Cond{{"seq", "in", "1,x"}}}}})
		// Reload empties the tables and loads them again, and refuses a table that does not fit.
		e.copy(src, pg, Options{Overwrite: "reload"})
		quiet(pg.DB, `ALTER TABLE "public".child DROP COLUMN label`, `COMMENT ON CONSTRAINT fk_parent ON "public".child IS 'the key that matters'`)
		e.copy(src, pg, Options{Overwrite: "reload"})
		quiet(pg.DB, `ALTER TABLE "public".child ADD COLUMN label text`, `ALTER TABLE "public".keyless ALTER COLUMN y TYPE text`)
		e.copy(src, pg, Options{Overwrite: "reload"})
		quiet(pg.DB, `ALTER TABLE "public".keyless ALTER COLUMN y TYPE integer USING y::integer`, `ALTER TABLE "public".emptyauto ADD COLUMN z integer NOT NULL`)
		e.copy(src, pg, Options{Overwrite: "reload"})
		quiet(pg.DB, `ALTER TABLE "public".emptyauto DROP COLUMN z`, `ALTER TABLE "public".emptyauto DROP CONSTRAINT emptyauto_pkey`)
		e.copy(src, pg, Options{Overwrite: "reload"})
		quiet(pg.DB, `ALTER TABLE "public".emptyauto ADD PRIMARY KEY (id)`, `ALTER TABLE "public".child ADD COLUMN extra integer DEFAULT 1`)
		e.copy(src, pg, Options{Overwrite: "reload"})
		// A reload that leaves a parent out keeps the key, NOT VALID, and says so.
		e.copy(src, pg, Options{Overwrite: "reload", Filters: map[string]RowFilter{"Parent": {Where: []Cond{{"id", "=", "2"}}}}})
		quiet(pg.DB, `DROP TABLE "public".keyless`, `CREATE VIEW "public".keyless AS SELECT 1 AS x`)
		e.copy(src, pg, Options{Overwrite: "reload"})
		// A table is reloaded alone: its key to a table that is not part of the copy stays on the target and is not said to be missing.
		e.copy(src, pg, Options{Overwrite: "reload", Include: []string{"child"}})
		// The counter of a column that takes its numbers from a sequence it does not own is moved too; a column with none is said.
		family := []string{"Parent", "child", "grandchild"}
		quiet(pg.DB, `ALTER TABLE "public"."Parent" ALTER COLUMN id DROP IDENTITY`, `CREATE SEQUENCE "public".shared_seq`,
			`ALTER TABLE "public"."Parent" ALTER COLUMN id SET DEFAULT nextval('shared_seq')`)
		e.copy(src, pg, Options{Overwrite: "reload", Include: family})
		quiet(pg.DB, `ALTER TABLE "public"."Parent" ALTER COLUMN id DROP DEFAULT`)
		e.copy(src, pg, Options{Overwrite: "reload", Include: family})
		// A sequence that is used and already past the rows is not moved back.
		quiet(pg.DB, `ALTER TABLE "public"."Parent" ALTER COLUMN id SET DEFAULT nextval('shared_seq')`, `SELECT setval('"public".shared_seq', 50)`)
		e.copy(src, pg, Options{Overwrite: "reload", Include: family})
		// A sequence that cannot be set to the next id (beyond its maximum) or that counts down is left alone, and said.
		quiet(pg.DB, `ALTER TABLE "public"."Parent" ALTER COLUMN id SET DEFAULT nextval('shared_seq')`, `ALTER SEQUENCE "public".shared_seq RESTART 1 MAXVALUE 5`)
		e.copy(src, pg, Options{Overwrite: "reload", Include: family})
		quiet(pg.DB, `ALTER SEQUENCE "public".shared_seq INCREMENT BY -1 MINVALUE 1 MAXVALUE 100 RESTART 100`)
		e.copy(src, pg, Options{Overwrite: "reload", Include: family})
	}},
	{name: "filters_from_pg", pgs: 1, run: func(e *scenarioEnv) {
		// From a PostgreSQL source: where and limit read through the server.
		pgsrc := e.pg()
		quiet(pgsrc.DB, pgFixtureStatements...)
		lite := e.lite()
		e.copy(pgsrc, lite, Options{Filters: map[string]RowFilter{
			"dept": {Where: []Cond{{"name", "in", "eng,ops"}, {"budget", ">=", "1"}, {"opened", "<", "2000-01-01"}, {"active", "=", "true"},
				{"tiny", "<", "10"}, {"f8", ">", "1.5"}, {"at", ">=", "2020-01-02T03:04:05Z"}}},
			"emp": {Where: []Cond{{"id", "<=", "5"}}, Limit: 2},
		}})
		e.copy(pgsrc, lite, Options{Overwrite: "reload"})
		quiet(lite.DB, `ALTER TABLE dept DROP COLUMN uid`)
		e.copy(pgsrc, lite, Options{Overwrite: "reload"})
	}},
	{name: "sqlite_dry_run_to_pg", pgs: 0, run: func(e *scenarioEnv) {
		src := e.fixture(featureStatements...)
		e.copy(src, Endpoint{Engine: Postgres, Display: "postgres://dry"}, Options{DryRun: true})
	}},
}

func runScenario(t testing.TB, s scenario, plan *faultPlan, record bool) *scenarioEnv {
	e := &scenarioEnv{t: t, ctx: context.Background(), name: s.name, plan: plan, record: record}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("scenario %s panicked (fault %d/%d): %v", s.name, plan.failAt, plan.mode, r)
			}
		}()
		s.run(e)
	}()
	return e
}

// TestRecordScenarios writes the recordings the other tests replay. It needs a local server and is
// run by hand: DATATUG_RECORD_SCENARIOS=1 DATATUG_TEST_POSTGRES_URL=... go test -run TestRecordScenarios.
func TestRecordScenarios(t *testing.T) {
	if os.Getenv("DATATUG_RECORD_SCENARIOS") != "1" {
		t.Skip("DATATUG_RECORD_SCENARIOS is not 1")
	}
	for _, s := range scenarios {
		if s.pgs == 0 {
			continue
		}
		t.Run(s.name, func(t *testing.T) { runScenario(t, s, &faultPlan{}, true) })
	}
}

// TestReplayScenarios runs every scenario against its recording with no failure injected and checks
// that the copy asked the server exactly what it asked when the recording was made.
func TestReplayScenarios(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			plan := &faultPlan{}
			e := runScenario(t, s, plan, false)
			if os.Getenv("DATATUG_SCENARIO_LOG") == "1" {
				for i, c := range e.copies {
					msg := "ok"
					if c.err != nil {
						msg = c.err.Error()
					}
					t.Logf("copy %d: %s", i+1, firstLine(msg))
					if c.rep != nil {
						for _, w := range c.rep.Warnings {
							t.Logf("    %s", w)
						}
					}
				}
				for i, d := range e.diffs {
					t.Logf("verify %d: %d difference(s)", i+1, len(d))
					for _, x := range d {
						t.Logf("    %s", x)
					}
				}
			}
			if check := scenarioChecks[s.name]; check != nil {
				check(t, e)
			}
			for i, st := range e.states {
				if len(st.mismatch) > 0 {
					t.Errorf("database %d: the copy no longer asks what was recorded (re-record with DATATUG_RECORD_SCENARIOS=1): %s", i+1, strings.Join(st.mismatch, "; "))
				}
				if st.i != len(st.calls) {
					t.Errorf("database %d: %d of %d recorded calls were made", i+1, st.i, len(st.calls))
				}
			}
		})
	}
}

// TestChaosSweep runs every scenario once for each call it makes and each way a call can fail.
func TestChaosSweep(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			base := &faultPlan{}
			runScenario(t, s, base, false)
			n := base.total()
			if n == 0 {
				t.Fatal("the scenario made no call")
			}
			// The runs are independent, so they share the cores: the largest scenario would otherwise be
			// one core's work while the others sit idle.
			type run struct {
				k    int
				mode faultMode
			}
			jobs := make(chan run)
			var wg sync.WaitGroup
			for w := 0; w < runtime.GOMAXPROCS(0); w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for r := range jobs {
						// Each run has a goroutine of its own: a t.Fatal inside it ends that run only, not the worker.
						done := make(chan struct{})
						go func() {
							defer close(done)
							runScenario(t, s, &faultPlan{failAt: r.k, mode: r.mode}, false)
						}()
						<-done
					}
				}()
			}
			for k := 1; k <= n; k++ {
				for mode := faultMode(0); mode < faultModes; mode++ {
					if mode != faultError && !base.queries[k] {
						continue // a row fault only differs from an error on a call that returns rows
					}
					jobs <- run{k, mode}
				}
			}
			close(jobs)
			wg.Wait()
			t.Logf("%s: %d calls x %d ways", s.name, n, faultModes)
		})
	}
}

var _ = fmt.Sprint
