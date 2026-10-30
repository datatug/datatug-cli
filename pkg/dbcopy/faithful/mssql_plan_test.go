package faithful

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

func fkWarnings(p *ScriptPlan) []string {
	var out []string
	for _, w := range p.Warnings {
		if w.Kind == WarnFKActionNotCarried {
			out = append(out, w.Object+": "+w.Message)
		}
	}
	return out
}

func tableFK(p *ScriptPlan, table string, i int) ForeignKey { return p.Table(table).FKs[i] }

func TestMSSQLUniqueKeysOverNulls(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE one (id INTEGER PRIMARY KEY, a TEXT UNIQUE)`, `INSERT INTO one VALUES (1, NULL), (2, 'x')`,
		`CREATE TABLE many (id INTEGER PRIMARY KEY, a TEXT, b INTEGER, c TEXT NOT NULL, d TEXT, UNIQUE (a, b), CONSTRAINT named_u UNIQUE (d), UNIQUE (c))`,
		`CREATE UNIQUE INDEX many_c ON many (d)`, `CREATE UNIQUE INDEX many_ab ON many (a, c)`, `CREATE INDEX many_plain ON many (d)`,
		`INSERT INTO many VALUES (1, NULL, 1, 'p', NULL), (2, NULL, 1, 'q', NULL), (3, 'z', NULL, 'r', 'dd')`,
		`CREATE TABLE ref (id INTEGER PRIMARY KEY, a TEXT REFERENCES one(a), d TEXT REFERENCES many(d))`)
	one, many := p.Table("one"), p.Table("many")
	if len(one.Uniques) != 1 || len(one.Indexes) != 0 {
		t.Errorf("a key that holds one NULL stays a UNIQUE constraint: %+v %+v", one.Uniques, one.Indexes)
	}
	if len(many.Uniques) != 1 || many.Uniques[0].Columns[0] != "c" {
		t.Errorf("only the key of a NOT NULL column stays: %+v", many.Uniques)
	}
	byName := map[string]Index{}
	for _, ix := range many.Indexes {
		byName[ix.Name] = ix
	}
	want := map[string]string{
		"UQ_many_a_b": "[a] IS NOT NULL AND [b] IS NOT NULL",
		"named_u":     "[d] IS NOT NULL",
		"many_c":      "[d] IS NOT NULL",
		"many_ab":     "[a] IS NOT NULL",
		"many_plain":  "",
	}
	for name, where := range want {
		ix, ok := byName[name]
		if !ok || ix.Where != where {
			t.Errorf("index %s: %+v (%v), want where %q", name, ix, ok, where)
		}
	}
	if !byName["named_u"].Unique || byName["many_plain"].Unique {
		t.Error("uniqueness")
	}
	kinds := kindCounts(p.Warnings)
	if kinds[WarnUniqueFiltered] != 4 {
		t.Errorf("%d unique-filtered warnings: %v", kinds[WarnUniqueFiltered], p.Warnings)
	}
	// A foreign key cannot reference a filtered index; it can reference the constraint that stayed.
	if len(p.Table("ref").FKs) != 1 || p.Table("ref").FKs[0].RefTable != "one" {
		t.Errorf("%+v", p.Table("ref").FKs)
	}
	wantWarning(t, p.Warnings, WarnFKNotCarried, "ref(d)")
	// The index of an unnamed constraint never takes a name another index has.
	q := planMSSQL(t, `CREATE TABLE t (id INTEGER PRIMARY KEY, a TEXT, UNIQUE (a))`, `CREATE INDEX UQ_t_a ON t (id)`, `INSERT INTO t VALUES (1, NULL), (2, NULL)`)
	names := []string{}
	for _, ix := range q.Table("t").Indexes {
		names = append(names, ix.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] == names[1] || !strings.HasPrefix(names[1], "UQ_t_a_") && !strings.HasPrefix(names[0], "UQ_t_a_") {
		t.Errorf("%v", names)
	}
}

func TestMSSQLObjectNamesShareOneNamespace(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE tree (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE a (id INTEGER PRIMARY KEY, x TEXT, y TEXT, CONSTRAINT tree UNIQUE (x), CONSTRAINT shared UNIQUE (y))`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, x TEXT, p INTEGER, q INTEGER, r INTEGER, CONSTRAINT SHARED UNIQUE (x), CONSTRAINT "a" FOREIGN KEY (p) REFERENCES tree(id), CONSTRAINT shared FOREIGN KEY (q) REFERENCES tree(id), CONSTRAINT fk_ok FOREIGN KEY (r) REFERENCES tree(id), CONSTRAINT "chk" CHECK (p > 0), CONSTRAINT "tree" CHECK (r > 0))`)
	var lost []string
	for _, w := range p.Warnings {
		if w.Kind == WarnConstraintNameLost {
			lost = append(lost, w.Object)
		}
	}
	sort.Strings(lost)
	// the unique key named like a table, the second unique key of the name, the keys named like a table or a unique key, and the check named like a table
	if got := strings.Join(lost, ","); got != "a.tree,b.SHARED,b.a,b.shared,b.tree" {
		t.Errorf("names not carried: %s", got)
	}
	if fk := tableFK(p, "b", 0); fk.Name != "fk_ok" && p.Table("b").FKs[len(p.Table("b").FKs)-1].Name != "fk_ok" {
		t.Errorf("a name nothing else has is kept: %+v", p.Table("b").FKs)
	}
	names := (mssqlDialect{}).ObjectNames(p)
	for _, n := range []string{MySQLMarkerTable, "tree", "a", "b", "shared"} {
		found := false
		for _, o := range names {
			found = found || o == n
		}
		if !found {
			t.Errorf("%q is not among %v", n, names)
		}
	}
}

// SQL Server keeps the names of CHECK constraints, foreign keys, unique keys and tables in one list: a CHECK named like a
// foreign key (case aside) loses its name, and the foreign key keeps its own. Leaving the foreign key names out of
// ObjectNames fails this test (and the load, with Msg 2714 or 1750).
func TestMSSQLForeignKeyNamesShareTheNamespaceWithChecks(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY, n INTEGER CONSTRAINT "DUP" CHECK (n > 0), m INTEGER CONSTRAINT keep_me CHECK (m > 0))`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, pid INTEGER, CONSTRAINT dup FOREIGN KEY (pid) REFERENCES p(id))`)
	if fk := tableFK(p, "c", 0); fk.Name != "dup" {
		t.Errorf("the foreign key keeps its name: %+v", fk)
	}
	names := map[string]string{}
	for _, ck := range p.Table("p").Checks {
		names[strings.Trim(ck.Expr[:strings.Index(ck.Expr, " ")], "[]")] = ck.Name
	}
	if len(names) != 2 || names["n"] != "" {
		t.Errorf("a CHECK named like a foreign key keeps its name: %v", names)
	}
	if names["m"] != "keep_me" {
		t.Errorf("a CHECK nothing else is named like loses its name: %v", names)
	}
	lost := 0
	for _, w := range p.Warnings {
		if w.Kind == WarnConstraintNameLost && strings.Contains(w.Object, "DUP") {
			lost++
		}
	}
	if lost != 1 {
		t.Errorf("%d name-lost warnings for the CHECK, want 1: %+v", lost, p.Warnings)
	}
	found := false
	for _, n := range (mssqlDialect{}).ObjectNames(p) {
		found = found || n == "dup"
	}
	if !found {
		t.Error("the foreign key name is not among the object names")
	}
}

func TestMSSQLForeignKeysSQLServerCannotCreate(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE parent (id INTEGER PRIMARY KEY, u TEXT UNIQUE, plain INTEGER)`,
		`CREATE TABLE c1 (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id) ON DELETE RESTRICT ON UPDATE RESTRICT)`,
		`CREATE TABLE c2 (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id) ON DELETE SET DEFAULT ON UPDATE SET DEFAULT)`,
		`CREATE TABLE c3 (id INTEGER PRIMARY KEY, p INTEGER NOT NULL REFERENCES parent(id) ON DELETE SET NULL ON UPDATE CASCADE)`,
		`CREATE TABLE c4 (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(plain))`,
		`CREATE TABLE c5 (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TABLE c6 (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id) ON DELETE SET NULL)`)
	for _, c := range []struct{ table, od, ou string }{{"c1", "NO ACTION", "NO ACTION"}, {"c2", "NO ACTION", "NO ACTION"}, {"c3", "NO ACTION", "CASCADE"}, {"c5", "NO ACTION", "NO ACTION"}, {"c6", "SET NULL", "NO ACTION"}} {
		fk := tableFK(p, c.table, 0)
		if fk.OnDelete != c.od || fk.OnUpdate != c.ou || fk.Deferrable {
			t.Errorf("%s: %+v, want ON DELETE %s ON UPDATE %s", c.table, fk, c.od, c.ou)
		}
	}
	if len(p.Table("c4").FKs) != 0 {
		t.Error("a key onto a column that is no unique key is not carried")
	}
	w := strings.Join(fkWarnings(p), "\n")
	for _, want := range []string{"c1(p): ON DELETE RESTRICT does not exist", "c1(p): ON UPDATE RESTRICT does not exist", "c2(p): ON DELETE SET DEFAULT is not carried", "c3(p): ON DELETE SET NULL needs nullable columns", "c5(p): DEFERRABLE"} {
		if !strings.Contains(w, want) {
			t.Errorf("no warning %q in\n%s", want, w)
		}
	}
	wantWarning(t, p.Warnings, WarnFKNotCarried, "c4(p)")
	// A key whose parent table is not in the plan.
	d := mssqlDialect{}
	p.Tables = p.Tables[1:]
	d.dropUnsupportedFKs(p)
	wantWarning(t, p.Warnings, WarnFKNotCarried, "c1(p)")
	if len(p.Table("c1").FKs) != 0 {
		t.Error("the key of a missing parent is dropped")
	}
}

func cascadeAbbrev(fk ForeignKey) string {
	ab := map[string]string{"NO ACTION": "-", "": "-", "CASCADE": "C", "SET NULL": "N"}
	return ab[fk.OnDelete] + ab[fk.OnUpdate]
}

func TestMSSQLCascadePathsAreDowngraded(t *testing.T) {
	// A diamond: g is reached from p by a and by b.
	p := planMSSQL(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE a (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id) ON DELETE CASCADE ON UPDATE CASCADE)`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id) ON DELETE CASCADE ON UPDATE CASCADE)`,
		`CREATE TABLE g (id INTEGER PRIMARY KEY, a INTEGER REFERENCES a(id) ON DELETE CASCADE ON UPDATE SET NULL, b INTEGER REFERENCES b(id) ON DELETE CASCADE ON UPDATE CASCADE)`)
	got := cascadeAbbrev(tableFK(p, "a", 0)) + " " + cascadeAbbrev(tableFK(p, "b", 0))
	var gs []string
	for _, fk := range p.Table("g").FKs {
		gs = append(gs, fk.Columns[0]+"="+cascadeAbbrev(fk))
	}
	sort.Strings(gs)
	// The keys are taken in the order of the plan; g.b comes before g.a (SQLite lists them in reverse), and
	// keeps both actions; g.a keeps nothing a path to g already has.
	if got != "CC CC" || strings.Join(gs, ",") != "a=--,b=CC" {
		t.Errorf("%s | %v", got, gs)
	}
	if len(fkWarnings(p)) != 2 {
		t.Errorf("%v", fkWarnings(p))
	}
	for _, w := range fkWarnings(p) {
		if !strings.Contains(w, "by two paths") || !strings.Contains(w, "the key is carried with NO ACTION") {
			t.Errorf("%s", w)
		}
	}
	// A self reference and a cycle.
	q := planMSSQL(t,
		`CREATE TABLE s (id INTEGER PRIMARY KEY, up INTEGER REFERENCES s(id) ON DELETE SET NULL ON UPDATE CASCADE)`,
		`CREATE TABLE x (id INTEGER PRIMARY KEY, y INTEGER)`,
		`CREATE TABLE y (id INTEGER PRIMARY KEY, x INTEGER REFERENCES x(id) ON DELETE CASCADE)`)
	if cascadeAbbrev(tableFK(q, "s", 0)) != "--" || cascadeAbbrev(tableFK(q, "y", 0)) != "C-" {
		t.Errorf("%v", fkWarnings(q))
	}
	if !strings.Contains(strings.Join(fkWarnings(q), "\n"), "again, a cycle") {
		t.Errorf("%v", fkWarnings(q))
	}
}

// A SET NULL key updates the child rows, and that update goes on through the keys that cascade updates.
func TestMSSQLSetNullStartsAnUpdate(t *testing.T) {
	stmts := func(e2 string) []string {
		return []string{
			`CREATE TABLE t4 (id INTEGER PRIMARY KEY)`,
			`CREATE TABLE t0 (id INTEGER PRIMARY KEY, c INTEGER REFERENCES t4(id) ON DELETE CASCADE ON UPDATE CASCADE)`,
			`CREATE TABLE t1 (id INTEGER PRIMARY KEY, c INTEGER REFERENCES t0(id) ` + e2 + `)`,
			`CREATE TABLE t3 (id INTEGER PRIMARY KEY, c2 INTEGER REFERENCES t4(id) ON DELETE CASCADE, c1 INTEGER REFERENCES t1(id) ON UPDATE CASCADE)`}
	}
	// With CASCADE the delete stays a delete and the update key adds no second path.
	p := planMSSQL(t, stmts("ON DELETE CASCADE ON UPDATE CASCADE")...)
	if len(fkWarnings(p)) != 0 {
		t.Errorf("%v", fkWarnings(p))
	}
	// With SET NULL the delete becomes an update at t1, which reaches t3 a second time.
	p = planMSSQL(t, stmts("ON DELETE SET NULL ON UPDATE CASCADE")...)
	if len(fkWarnings(p)) != 1 || !strings.Contains(fkWarnings(p)[0], "t3") {
		t.Errorf("%v", fkWarnings(p))
	}
}

func TestMSSQLCascadeGraph(t *testing.T) {
	g := &cascadeGraph{}
	if g.refusal() != "" {
		t.Error("an empty graph is valid")
	}
	g.edges = []cascadeEdge{{"a", "b", "CASCADE", "NO ACTION"}, {"b", "c", "NO ACTION", "CASCADE"}, {"a", "c", "NO ACTION", "NO ACTION"}}
	if g.refusal() != "" {
		t.Errorf("%s", g.refusal())
	}
	g.edges[2] = cascadeEdge{"a", "c", "CASCADE", "NO ACTION"} // b -> c cascades updates only: a delete reaches c once
	if g.refusal() != "" {
		t.Errorf("%s", g.refusal())
	}
	g.edges[1] = cascadeEdge{"b", "c", "CASCADE", "NO ACTION"}
	if r := g.refusal(); !strings.Contains(r, "a DELETE of a row of a reaches c by two paths") {
		t.Errorf("%s", r)
	}
	g.edges = []cascadeEdge{{"a", "b", "NO ACTION", "CASCADE"}, {"b", "a", "NO ACTION", "SET NULL"}}
	if r := g.refusal(); !strings.Contains(r, "an UPDATE of a row of a reaches a again, a cycle") {
		t.Errorf("%s", r)
	}
	if len(g.next("a", true)) != 0 || len(g.next("a", false)) != 1 || len(g.next("zz", false)) != 0 {
		t.Error("next")
	}
}

// pathsBetween counts the paths from one table to another with a brute-force walk of the layered graph: a delete
// follows CASCADE keys, then SET NULL keys turn it into an update that follows the update keys.
func bruteRefusal(edges []cascadeEdge) bool {
	tables := map[string]bool{}
	for _, e := range edges {
		tables[e.parent], tables[e.child] = true, true
	}
	var walk func(table string, deleting bool, path []string, count map[string]int) bool
	walk = func(table string, deleting bool, path []string, count map[string]int) bool {
		for _, e := range edges {
			if e.parent != table {
				continue
			}
			next, nextDeleting, follows := e.child, false, false
			switch {
			case deleting && e.onDelete == "CASCADE":
				nextDeleting, follows = true, true
			case deleting && e.onDelete == "SET NULL":
				follows = true
			case !deleting && (e.onUpdate != "NO ACTION" || e.onDelete == "SET NULL"):
				follows = true
			}
			if !follows {
				continue
			}
			count[next]++
			if count[next] > 1 || next == path[0] {
				return true
			}
			if walk(next, nextDeleting, append(path, next), count) {
				return true
			}
		}
		return false
	}
	for root := range tables {
		for _, deleting := range []bool{true, false} {
			if walk(root, deleting, []string{root}, map[string]int{}) {
				return true
			}
		}
	}
	return false
}

func TestMSSQLCascadeAnalysisAgainstABruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	acts := []string{"NO ACTION", "CASCADE", "SET NULL", "CASCADE"}
	for round := 0; round < 150; round++ {
		n := 2 + rng.Intn(6)
		var stmts []string
		for i := 0; i < n; i++ {
			cols, fks := "id INTEGER PRIMARY KEY", ""
			for j := 0; j < rng.Intn(4); j++ {
				cols += fmt.Sprintf(", c%d INTEGER", j)
				if rng.Intn(4) > 0 {
					fks += fmt.Sprintf(", FOREIGN KEY (c%d) REFERENCES t%d(id) ON DELETE %s ON UPDATE %s", j, rng.Intn(n), acts[rng.Intn(4)], acts[rng.Intn(4)])
				}
			}
			stmts = append(stmts, fmt.Sprintf("CREATE TABLE t%d (%s%s)", i, cols, fks))
		}
		p := planMSSQL(t, stmts...)
		var edges []cascadeEdge
		for _, tb := range p.Tables {
			for _, fk := range tb.FKs {
				edges = append(edges, cascadeEdge{fk.RefTable, tb.Name, fk.OnDelete, fk.OnUpdate})
			}
		}
		if bruteRefusal(edges) {
			t.Fatalf("round %d: the plan keeps actions SQL Server refuses:\n%s", round, strings.Join(stmts, "\n"))
		}
		// Maximal: restoring any one downgraded action alone breaks the graph.
		for _, w := range p.Warnings {
			if w.Kind != WarnFKActionNotCarried || !strings.Contains(w.Message, "cascade path") {
				continue
			}
			var tb string
			var col string
			if _, err := fmt.Sscanf(strings.NewReplacer("(", " ", ")", " ").Replace(w.Object), "%s %s", &tb, &col); err != nil {
				t.Fatal(err)
			}
			var kind, action string
			if strings.HasPrefix(w.Message, "ON DELETE ") {
				kind, action = "ON DELETE", strings.TrimSuffix(strings.TrimPrefix(w.Message, "ON DELETE "), " is not carried")
			} else {
				kind, action = "ON UPDATE", strings.TrimSuffix(strings.TrimPrefix(w.Message, "ON UPDATE "), " is not carried")
			}
			action = action[:strings.Index(action, " is not carried")]
			probe := cascadeEdge{onDelete: "NO ACTION", onUpdate: "NO ACTION"}
			for _, fk := range p.Table(tb).FKs {
				if fk.Columns[0] == col {
					probe.parent, probe.child = fk.RefTable, tb
				}
			}
			if kind == "ON DELETE" {
				probe.onDelete = action
			} else {
				probe.onUpdate = action
			}
			if !bruteRefusal(append(append([]cascadeEdge{}, edges...), probe)) {
				t.Fatalf("round %d: %s %s of %s could be kept:\n%s", round, kind, action, w.Object, strings.Join(stmts, "\n"))
			}
		}
	}
}

func TestMSSQLForeignKeyColumnsShareOneType(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE pt (code VARCHAR(5) PRIMARY KEY, n INT2 UNIQUE)`, `INSERT INTO pt VALUES ('abc', 1)`,
		`CREATE TABLE ct (id INTEGER PRIMARY KEY, code TEXT REFERENCES pt(code), n INTEGER REFERENCES pt(n), big NVARCHAR(300) REFERENCES pt(code))`,
		`INSERT INTO ct VALUES (1, 'abc', 1, 'abc')`,
		`CREATE TABLE mixed_p (k INTEGER PRIMARY KEY)`, `CREATE TABLE mixed_c (id INTEGER PRIMARY KEY, k TEXT REFERENCES mixed_p(k))`, `INSERT INTO mixed_p VALUES (1)`, `INSERT INTO mixed_c VALUES (1, '1')`,
		`CREATE TABLE bytes_p (k BLOB PRIMARY KEY)`, `CREATE TABLE bytes_c (id INTEGER PRIMARY KEY, k TEXT REFERENCES bytes_p(k))`, `INSERT INTO bytes_p VALUES (x'0102')`, `INSERT INTO bytes_c VALUES (1, NULL)`,
		`CREATE TABLE dec_p (k NUMERIC(10,2) PRIMARY KEY)`, `CREATE TABLE dec_c (id INTEGER PRIMARY KEY, k NUMERIC(12,3) REFERENCES dec_p(k))`, `INSERT INTO dec_p VALUES (1.5)`, `INSERT INTO dec_c VALUES (1, 1.5)`,
		`CREATE TABLE bool_p (k BOOLEAN PRIMARY KEY)`, `CREATE TABLE bool_c (id INTEGER PRIMARY KEY, k BOOLEAN REFERENCES bool_p(k))`, `INSERT INTO bool_p VALUES (1)`, `INSERT INTO bool_c VALUES (1, 1)`,
		`CREATE TABLE ts_p (k DATETIME PRIMARY KEY)`, `CREATE TABLE ts_c (id INTEGER PRIMARY KEY, k DATETIME REFERENCES ts_p(k))`, `INSERT INTO ts_p VALUES ('2024-01-01 00:00:00.123')`, `INSERT INTO ts_p VALUES ('2024-01-01 00:00:01')`, `INSERT INTO ts_c VALUES (1, '2024-01-01 00:00:01')`)
	same := func(a, b [2]string) {
		t.Helper()
		x, y := colType(t, p, a[0], a[1]), colType(t, p, b[0], b[1])
		if x != y {
			t.Errorf("%s.%s is %s and %s.%s is %s: SQL Server refuses a key between them", a[0], a[1], x, b[0], b[1], y)
		}
	}
	same([2]string{"pt", "code"}, [2]string{"ct", "code"})
	same([2]string{"pt", "code"}, [2]string{"ct", "big"})
	same([2]string{"pt", "n"}, [2]string{"ct", "n"})
	same([2]string{"mixed_p", "k"}, [2]string{"mixed_c", "k"})
	same([2]string{"bytes_p", "k"}, [2]string{"bytes_c", "k"})
	same([2]string{"dec_p", "k"}, [2]string{"dec_c", "k"})
	same([2]string{"bool_p", "k"}, [2]string{"bool_c", "k"})
	same([2]string{"ts_p", "k"}, [2]string{"ts_c", "k"})
	if got := colType(t, p, "pt", "code"); got != "NVARCHAR(300)" {
		t.Errorf("the widest member decides: %s", got)
	}
	if got := colType(t, p, "pt", "n"); got != "BIGINT" {
		t.Errorf("the widest integer decides: %s", got)
	}
	if got := colType(t, p, "mixed_p", "k"); !strings.HasPrefix(got, "NVARCHAR(") {
		t.Errorf("an integer and a text become a text: %s", got)
	}
	if got := colType(t, p, "bytes_p", "k"); !strings.HasPrefix(got, "VARBINARY(") {
		t.Errorf("a blob and a text become bytes: %s", got)
	}
	if got := colType(t, p, "dec_p", "k"); got != "DECIMAL(10,3)" && got != "DECIMAL(12,3)" {
		t.Errorf("%s", got)
	}
	if got := colType(t, p, "ts_c", "k"); got != "DATETIME2(3)" {
		t.Errorf("the most decimals decide: %s", got)
	}
	wantWarning(t, p.Warnings, WarnTypeWidened, "pt.n")
}

func TestMSSQLKeyBudgets(t *testing.T) {
	long := func(n int) string { return fmt.Sprintf("printf('%%0%dd', 1)", n) }
	// A primary key of 3 x 200 characters is 1,200 bytes: more than the 900 of a clustered key.
	_, err := PlanScript2(t, `CREATE TABLE k (a TEXT, b TEXT, c TEXT, PRIMARY KEY (a, b, c))`, `INSERT INTO k VALUES (`+long(200)+`, `+long(200)+`, `+long(200)+`)`)
	_ = err // PlanScript2 plans for mysql: the SQL Server plan is below
	p, err := PlanScript(t.Context(), "mssql", sqliteWith(t, `CREATE TABLE k (a TEXT, b TEXT, c TEXT, PRIMARY KEY (a, b, c))`, `INSERT INTO k VALUES (`+long(200)+`, `+long(200)+`, `+long(200)+`)`).DB)
	if err == nil || !strings.Contains(err.Error(), "primary key of table \"k\" is longer than SQL Server allows (1200 bytes, at most 900") {
		t.Fatalf("%v %v", p, err)
	}
	// A unique key may take 1,700 bytes: 2 x 400 characters is 1,600.
	p = planMSSQL(t, `CREATE TABLE k (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`, `INSERT INTO k VALUES (1, `+long(400)+`, `+long(400)+`)`)
	if colType(t, p, "k", "a") != "NVARCHAR(400)" {
		t.Errorf("a key shortened to its longest value: %s", colType(t, p, "k", "a"))
	}
	_, err = PlanScript(t.Context(), "mssql", sqliteWith(t, `CREATE TABLE k (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`, `INSERT INTO k VALUES (1, `+long(500)+`, `+long(500)+`)`).DB)
	if err == nil || !strings.Contains(err.Error(), "unique key of table \"k\" is longer than SQL Server allows (2000 bytes, at most 1700") {
		t.Errorf("%v", err)
	}
	// An index too long is not carried, and the others stay.
	p = planMSSQL(t, `CREATE TABLE k (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT)`, `CREATE INDEX k_abc ON k (a, b, c)`, `CREATE UNIQUE INDEX k_ua ON k (a, b, c)`, `CREATE INDEX k_a ON k (a)`,
		`INSERT INTO k VALUES (1, `+long(300)+`, `+long(300)+`, `+long(300)+`)`)
	if len(p.Table("k").Indexes) != 1 || p.Table("k").Indexes[0].Name != "k_a" {
		t.Errorf("%+v", p.Table("k").Indexes)
	}
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "k.k_abc")
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "k.k_ua")
	// A text key longer than NVARCHAR(4000) cannot be indexed at all.
	p = planMSSQL(t, `CREATE TABLE k (id INTEGER PRIMARY KEY, a TEXT)`, `CREATE INDEX k_a ON k (a)`, `INSERT INTO k VALUES (1, `+long(5000)+`)`)
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "k.k_a")
	// More than 32 columns.
	cols := make([]string, 33)
	for i := range cols {
		cols[i] = fmt.Sprintf("c%d INTEGER", i)
	}
	names := make([]string, 33)
	for i := range names {
		names[i] = fmt.Sprintf("c%d", i)
	}
	p = planMSSQL(t, `CREATE TABLE w (id INTEGER PRIMARY KEY, `+strings.Join(cols, ", ")+`)`, `CREATE INDEX w_all ON w (`+strings.Join(names, ", ")+`)`)
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "w.w_all")
}

func TestMSSQLKeyBytes(t *testing.T) {
	for _, c := range []struct {
		ch   Choice
		want int
	}{
		{Choice{Family: "int", Bits: 16}, 2}, {Choice{Family: "int", Bits: 64}, 8}, {Choice{Family: "bool"}, 1}, {Choice{Family: "float"}, 8},
		{Choice{Family: "decimal", Prec: 9}, 5}, {Choice{Family: "decimal", Prec: 19}, 9}, {Choice{Family: "decimal", Prec: 28}, 13}, {Choice{Family: "decimal", Prec: 38}, 17},
		{Choice{Family: "date"}, 3}, {Choice{Family: "datetime", Fsp: 0}, 6}, {Choice{Family: "datetime", Fsp: 7}, 8}, {Choice{Family: "time", Fsp: 0}, 3}, {Choice{Family: "time", Fsp: 7}, 5},
		{Choice{Family: "text", Len: 10}, 20}, {Choice{Family: "bytes", Len: 10}, 10},
	} {
		if got := mssqlKeyBytes(c.ch); got != c.want {
			t.Errorf("%+v: %d, want %d", c.ch, got, c.want)
		}
	}
	for _, ch := range []Choice{{Family: "text"}, {Family: "text", Len: 4001}, {Family: "bytes"}, {Family: "bytes", Len: 8001}} {
		if got := mssqlKeyBytes(ch); got < 1<<20 {
			t.Errorf("%+v can never be a key: %d", ch, got)
		}
	}
}

// The marker table name is compared the way SQL Server compares table names: by case and trailing spaces.
func TestMSSQLMarkerNameIsFolded(t *testing.T) {
	for _, name := range []string{"_demodb_import", "_DemoDB_Import", "_demodb_import  "} {
		_, err := PlanScript(t.Context(), "mssql", sqliteWith(t, `CREATE TABLE "`+name+`" (a)`).DB)
		if err == nil || !strings.Contains(err.Error(), "completion marker") {
			t.Errorf("%q: %v", name, err)
		}
	}
}
