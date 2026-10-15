package faithful

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	plain := openSQLite(e.t, path)
	execAll(e.t, plain.DB, statements...)
	_ = plain.DB.Close()
	return chaosSQLiteAt(e.t, path, e.plan)
}

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
		_, _ = Copy(e.ctx, src, pg, Options{Progress: io.Discard})
		_, _ = Copy(e.ctx, src, pg, Options{})
		_, _ = Copy(e.ctx, src, pg, Options{Overwrite: "recreate", Exclude: []string{"keyless"}})
		_, _ = Copy(e.ctx, src, pg, Options{Overwrite: "recreate"})
		back := e.lite()
		_, _ = Copy(e.ctx, pg, back, Options{})
		_, _ = Verify(e.ctx, src, back)
		_, _ = Copy(e.ctx, back, pg2, Options{})
		_, _ = Verify(e.ctx, pg, pg2)
	}},
	{name: "pg_origin", pgs: 2, run: func(e *scenarioEnv) {
		pg := e.pg()
		quiet(pg.DB, pgFixtureStatements...)
		mid := e.lite()
		_, _ = Copy(e.ctx, pg, mid, Options{})
		_, _ = Copy(e.ctx, pg, e.lite(), Options{Include: []string{"dept", "emp"}})
		pg2 := e.pg()
		_, _ = Copy(e.ctx, mid, pg2, Options{})
		_, _ = Verify(e.ctx, pg, pg2)
	}},
	{name: "pg_oddities", pgs: 1, run: func(e *scenarioEnv) {
		pg := e.pg()
		quiet(pg.DB, oddPGStatements...)
		_, _ = Copy(e.ctx, pg, e.lite(), Options{})
		other := pg
		other.Schema = "other"
		_, _ = Copy(e.ctx, other, e.lite(), Options{})
		empty := pg
		empty.Schema = "nothing_here"
		_, _ = Copy(e.ctx, empty, e.lite(), Options{})
	}},
	{name: "sqlite_odd_to_pg", pgs: 1, run: func(e *scenarioEnv) {
		src := e.fixture(oddSQLiteStatements...)
		pg := e.pg()
		pg.Schema = "copy_target"
		_, _ = Copy(e.ctx, src, pg, Options{})
		nulls := e.fixture(`CREATE TABLE t (k TEXT PRIMARY KEY, v TEXT)`, `INSERT INTO t VALUES (NULL, 'x')`)
		_, _ = Copy(e.ctx, nulls, pg, Options{})
	}},
	{name: "sqlite_to_sqlite", pgs: 0, run: func(e *scenarioEnv) {
		src := e.fixture(featureStatements...)
		dst := e.lite()
		_, _ = Copy(e.ctx, src, dst, Options{})
		_, _ = Copy(e.ctx, src, dst, Options{})
		_, _ = Copy(e.ctx, src, dst, Options{Overwrite: "recreate"})
		_, _ = Copy(e.ctx, src, e.lite(), Options{Exclude: []string{"Parent"}})
		_, _ = Copy(e.ctx, src, e.lite(), Options{DryRun: true})
		_, _ = Verify(e.ctx, src, dst)
		many := e.fixture(`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`,
			`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 600) INSERT INTO big SELECT x, 'v' || x FROM c`)
		_, _ = Copy(e.ctx, many, e.lite(), Options{})
		odd := e.fixture(`CREATE TABLE r (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE) STRICT`,
			`CREATE TABLE w (k TEXT PRIMARY KEY, v) WITHOUT ROWID`,
			`CREATE TABLE d (id INTEGER PRIMARY KEY, p INTEGER REFERENCES r(id) DEFERRABLE INITIALLY DEFERRED)`,
			`CREATE TABLE gen (id INTEGER PRIMARY KEY, g INTEGER GENERATED ALWAYS AS (id*2) VIRTUAL)`,
			`INSERT INTO r VALUES (1, 'a')`, `INSERT INTO d VALUES (1, 5)`, `INSERT INTO gen (id) VALUES (3)`)
		_, _ = Copy(e.ctx, odd, e.lite(), Options{})
	}},
	{name: "sqlite_dry_run_to_pg", pgs: 0, run: func(e *scenarioEnv) {
		src := e.fixture(featureStatements...)
		_, _ = Copy(e.ctx, src, Endpoint{Engine: Postgres, Display: "postgres://dry"}, Options{DryRun: true})
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
			for k := 1; k <= n; k++ {
				for mode := faultMode(0); mode < faultModes; mode++ {
					plan := &faultPlan{failAt: k, mode: mode}
					runScenario(t, s, plan, false)
				}
			}
			t.Logf("%s: %d calls x %d ways", s.name, n, faultModes)
		})
	}
}

var _ = fmt.Sprint
