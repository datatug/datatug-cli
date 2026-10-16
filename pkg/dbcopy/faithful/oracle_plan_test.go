package faithful

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func warnMessages(p *ScriptPlan, kind string) []string {
	var out []string
	for _, w := range p.Warnings {
		if w.Kind == kind {
			out = append(out, w.Object+": "+w.Message)
		}
	}
	return out
}

func TestOracleEmptyValuesAreNull(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE e (id INTEGER PRIMARY KEY, a TEXT NOT NULL, b TEXT, c BLOB, d TEXT UNIQUE, f BLOB NOT NULL, g TEXT NOT NULL, k BLOB UNIQUE)`,
		`INSERT INTO e VALUES (1, '', '', x'', '', x'', 'x', x'')`, `INSERT INTO e VALUES (2, 'a', NULL, x'00', NULL, x'01', 'y', x'01')`)
	tb := p.Table("e")
	if tb.Col("a").NotNull || !tb.Col("g").NotNull || tb.Col("f").NotNull != true {
		t.Errorf("NOT NULL is dropped only where the column holds an empty value: a %v, g %v, f %v", tb.Col("a").NotNull, tb.Col("g").NotNull, tb.Col("f").NotNull)
	}
	msgs := warnMessages(p, WarnValuesNormalized)
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"e.a: 1 empty value(s) become NULL, because Oracle has no empty text: the column is created without NOT NULL", "e.b: 1 empty value(s) become NULL, because Oracle has no empty text\n", "e.d: 1 empty value", "e.k: 1 empty value"} {
		if !strings.Contains(joined+"\n", want) {
			t.Errorf("no %q in\n%s", want, joined)
		}
	}
	// A BLOB keeps an empty value (it is not NULL there), and so does a column of no empty value.
	if strings.Contains(joined, "e.c:") || strings.Contains(joined, "e.f:") || strings.Contains(joined, "e.g:") {
		t.Errorf("the BLOBs and e.g hold no empty text:\n%s", joined)
	}
	// An identity column holds numbers: it is never changed.
	if _, err := planOracleErr(t, `CREATE TABLE k (code TEXT PRIMARY KEY)`, `INSERT INTO k VALUES ('')`); err == nil || !strings.Contains(err.Error(), "primary key column k.code holds 1 empty value(s)") {
		t.Errorf("%v", err)
	}
	if _, err := planOracleErr(t, `CREATE TABLE k (code BLOB PRIMARY KEY)`, `INSERT INTO k VALUES (x'')`); err == nil || !strings.Contains(err.Error(), "primary key column") {
		t.Errorf("an empty RAW is NULL too: %v", err)
	}
}

func TestOracleRedundantIndexes(t *testing.T) {
	p := planOracle(t, `CREATE TABLE d (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT, e TEXT, UNIQUE (a, b), UNIQUE (a, b), UNIQUE (id))`,
		`CREATE INDEX d_pk ON d (id)`, `CREATE INDEX d_uq ON d (a, b)`, `CREATE INDEX d_c1 ON d (c)`, `CREATE UNIQUE INDEX d_c2 ON d (c)`, `CREATE INDEX d_c3 ON d (c)`,
		`CREATE INDEX d_e1 ON d (e DESC, a)`, `CREATE INDEX d_e2 ON d (e DESC, a)`, `CREATE INDEX d_e3 ON d (e, a)`, `CREATE INDEX d_a ON d (a)`, `CREATE INDEX d_ba ON d (b, a)`)
	tb := p.Table("d")
	var names []string
	for _, ix := range tb.Indexes {
		names = append(names, ix.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "d_a,d_ba,d_c2,d_e1,d_e3" {
		t.Errorf("the indexes left: %s", got)
	}
	if len(tb.Uniques) != 1 {
		t.Errorf("the second UNIQUE (a, b) and the UNIQUE (id) of the primary key are dropped: %d", len(tb.Uniques))
	}
	msgs := strings.Join(warnMessages(p, WarnIndexNotCarried), "\n")
	for _, want := range []string{"ORA-02261", "ORA-01408", "d.d_pk: the index has the columns of the primary key", "d.d_uq: the index has the columns of the unique key", "d.d_c1", "d.d_c3", "d.d_e2: the index has the columns of the index d_e1", "the unique key (id) repeats the primary key"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("no %q in\n%s", want, msgs)
		}
	}
	// The unique index is kept before the plain one with the same columns.
	for _, ix := range tb.Indexes {
		if ix.Name == "d_c2" && !ix.Unique {
			t.Error("d_c2 is unique")
		}
	}
}

func TestOraclePromotesTheUniqueIndexAKeyReferences(t *testing.T) {
	p := planOracle(t, `CREATE TABLE u (id INTEGER PRIMARY KEY, code TEXT, other TEXT, third TEXT, filt TEXT)`,
		`CREATE UNIQUE INDEX u_code ON u (code)`, `CREATE UNIQUE INDEX u_other ON u (other DESC)`, `CREATE UNIQUE INDEX u_third ON u (third)`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, code TEXT REFERENCES u(code), other TEXT REFERENCES u(other))`,
		`INSERT INTO u VALUES (1, 'x', 'o', 't', NULL)`, `INSERT INTO child VALUES (1, 'x', NULL)`)
	tb := p.Table("u")
	if len(tb.Uniques) != 1 || tb.Uniques[0].Name != "u_code" || len(tb.Indexes) != 2 {
		t.Errorf("only the referenced plain unique index is a constraint: %+v %+v", tb.Uniques, tb.Indexes)
	}
	if len(p.Table("child").FKs) != 1 {
		t.Errorf("the key to the descending unique index is not carried: %v", p.Table("child").FKs)
	}
	wantWarning(t, p.Warnings, WarnFKNotCarried, "child")
}

func TestOracleUniqueKeysOverNulls(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE n (id INTEGER PRIMARY KEY, a TEXT, b INTEGER, c TEXT, d TEXT, e TEXT NOT NULL, f TEXT, CONSTRAINT "named" UNIQUE (a, b), UNIQUE (c, d), UNIQUE (e, f), UNIQUE (f))`,
		`CREATE UNIQUE INDEX n_cd ON n (d, c, e)`, `CREATE UNIQUE INDEX n_f_desc ON n (f DESC)`, `CREATE UNIQUE INDEX n_ef ON n (e, f DESC)`,
		`INSERT INTO n VALUES (1, NULL, 1, NULL, NULL, 'a', NULL), (2, NULL, 1, NULL, NULL, 'b', NULL), (3, 'a', NULL, 'q', NULL, 'c', 'x'), (4, 'a', NULL, NULL, 'r', 'd', 'y')`)
	tb := p.Table("n")
	byName := map[string]Index{}
	for _, ix := range tb.Indexes {
		byName[ix.Name] = ix
	}
	if ix, ok := byName["named"]; !ok || !ix.Unique || ix.Where != `"a" IS NOT NULL AND "b" IS NOT NULL` {
		t.Errorf("a named key keeps its name as an index: %+v", ix)
	}
	if ix := byName["UQ_n_c_d"]; ix.Where != `"c" IS NOT NULL AND "d" IS NOT NULL` {
		t.Errorf("an unnamed key is named after its table and columns: %+v", ix)
	}
	if ix := byName["n_cd"]; ix.Where != `"d" IS NOT NULL AND "c" IS NOT NULL` {
		t.Errorf("a unique index is filtered as a key is, over the nullable columns only: %+v", ix)
	}
	// e is NOT NULL and f holds two NULLs: only f is in the condition, and the key (e, f) is filtered.
	if ix := byName["UQ_n_e_f"]; ix.Where != `"f" IS NOT NULL` {
		t.Errorf("%+v", ix)
	}
	// One column needs nothing: Oracle holds many NULLs in a unique key of one column.
	if len(tb.Uniques) != 1 || tb.Uniques[0].Columns[0] != "f" {
		t.Errorf("the key over one column stays a constraint: %+v", tb.Uniques)
	}
	// A descending unique key holds NULL as a value, so it is carried ascending.
	if ix := byName["n_f_desc"]; ix.Columns[0].Desc || ix.Where != "" {
		t.Errorf("%+v", ix)
	}
	if ix := byName["n_ef"]; ix.Columns[1].Desc || ix.Where != `"f" IS NOT NULL` {
		t.Errorf("%+v", ix)
	}
	if len(warnMessages(p, WarnUniqueFiltered)) != 5 {
		t.Errorf("%v", warnMessages(p, WarnUniqueFiltered))
	}
	if msgs := warnMessages(p, WarnIndexNotCarried); len(msgs) != 2 || !strings.Contains(msgs[0], "carried ascending") {
		t.Errorf("%v", msgs)
	}
	// One NULL in each column cannot collide, so the key stays a constraint; so does a key of columns that are NOT NULL.
	p = planOracle(t, `CREATE TABLE m (id INTEGER PRIMARY KEY, a TEXT, b TEXT NOT NULL, UNIQUE (a, b))`, `INSERT INTO m VALUES (1, NULL, 'x'), (2, 'a', 'y')`)
	if len(p.Table("m").Uniques) != 1 || len(p.Table("m").Indexes) != 0 || len(warnMessages(p, WarnUniqueFiltered)) != 0 {
		t.Errorf("%+v", p.Table("m"))
	}
	// Empty texts are NULL in Oracle: two of them make the key a filtered one.
	p = planOracle(t, `CREATE TABLE em (id INTEGER PRIMARY KEY, a TEXT NOT NULL, b TEXT NOT NULL, UNIQUE (a, b))`, `INSERT INTO em VALUES (1, 'x', ''), (2, 'y', '')`)
	if len(p.Table("em").Indexes) != 1 || p.Table("em").Indexes[0].Where != `"a" IS NOT NULL AND "b" IS NOT NULL` && p.Table("em").Indexes[0].Where != `"b" IS NOT NULL` {
		t.Errorf("%+v", p.Table("em"))
	}
	// A name the key cannot keep as an index (longer than 128 bytes, or taken) is rewritten.
	long := strings.Repeat("n", 130)
	p = planOracle(t, `CREATE TABLE r (id INTEGER PRIMARY KEY, a TEXT, b TEXT, CONSTRAINT "`+long+`" UNIQUE (a, b))`, `CREATE INDEX r_taken ON r (b)`, `INSERT INTO r VALUES (1, NULL, NULL), (2, NULL, NULL)`)
	found := false
	for _, ix := range p.Table("r").Indexes {
		found = found || ix.Unique && ix.Where != "" && ix.Name == "UQ_r_a_b"
	}
	if !found {
		t.Errorf("%+v", p.Table("r").Indexes)
	}
	// A generated name that is taken, or too long, is made safe.
	longTable := strings.Repeat("t", 120)
	p = planOracle(t, `CREATE TABLE "`+longTable+`" (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`, `CREATE INDEX "UQ_`+longTable+`_a_b" ON "`+longTable+`" (b)`, `INSERT INTO "`+longTable+`" VALUES (1, NULL, NULL), (2, NULL, NULL)`)
	for _, ix := range p.Table(longTable).Indexes {
		if ix.Unique && (len(ix.Name) > 128 || !strings.HasPrefix(ix.Name, "UQ_")) {
			t.Errorf("%q", ix.Name)
		}
	}
	p = planOracle(t, `CREATE TABLE s (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`, `CREATE INDEX "UQ_s_a_b" ON s (b)`, `INSERT INTO s VALUES (1, NULL, NULL), (2, NULL, NULL)`)
	for _, ix := range p.Table("s").Indexes {
		if ix.Unique && ix.Name == "UQ_s_a_b" {
			t.Error("the generated name is taken by an index of the schema")
		}
	}
}

func TestOracleSharedNamespaces(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE a (id INTEGER PRIMARY KEY, x TEXT, y TEXT, CONSTRAINT "shared" UNIQUE (x), CONSTRAINT "idx_name" UNIQUE (y))`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, x TEXT, CONSTRAINT "shared" UNIQUE (x), CONSTRAINT fk_dup FOREIGN KEY (id) REFERENCES a(id))`,
		`CREATE INDEX idx_name ON b (x, id)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, CONSTRAINT fk_dup FOREIGN KEY (id) REFERENCES a(id), CONSTRAINT "shared" FOREIGN KEY (id) REFERENCES b(id))`)
	var lost []string
	for _, w := range p.Warnings {
		if w.Kind == WarnConstraintNameLost {
			lost = append(lost, w.Object)
		}
	}
	if len(lost) != 4 {
		t.Errorf("the second unique 'shared', the unique 'idx_name' (an index of b has it), the second fk_dup and the foreign key 'shared' (a constraint of a has it): %v", lost)
	}
	names := map[string]string{}
	for _, u := range p.Table("a").Uniques {
		names[u.Columns[0]] = u.Name
	}
	if names["x"] != "shared" || names["y"] != "" || p.Table("b").Uniques[0].Name != "" {
		t.Errorf("%+v %+v", p.Table("a").Uniques, p.Table("b").Uniques)
	}
	if got := (oracleDialect{}).ObjectNames(p); len(got) != 2 {
		t.Errorf("the names of keys and foreign keys, which checks share a namespace with: %v", got)
	}
}

func TestOracleForeignKeys(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY, u TEXT UNIQUE, w TEXT)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, a INTEGER REFERENCES p(id) ON DELETE CASCADE ON UPDATE CASCADE, b INTEGER REFERENCES p(id) ON DELETE SET NULL, c INTEGER REFERENCES p(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
		  d INTEGER REFERENCES p(id) ON DELETE SET DEFAULT ON UPDATE SET DEFAULT, e INTEGER REFERENCES p(id) DEFERRABLE INITIALLY IMMEDIATE, f INTEGER REFERENCES p(id) DEFERRABLE INITIALLY DEFERRED, g TEXT REFERENCES p(w), h TEXT REFERENCES p(u), i INTEGER REFERENCES p(id) ON DELETE NO ACTION ON UPDATE NO ACTION,
		  j INTEGER NOT NULL REFERENCES p(id) ON DELETE SET NULL)`,
		`CREATE TABLE gone (id INTEGER PRIMARY KEY, x INTEGER REFERENCES missing(id))`)
	fks := map[string]ForeignKey{}
	for _, fk := range p.Table("c").FKs {
		fks[fk.Columns[0]] = fk
	}
	for col, want := range map[string][2]string{"a": {"NO ACTION", "CASCADE"}, "b": {"NO ACTION", "SET NULL"}, "c": {"NO ACTION", "NO ACTION"}, "d": {"NO ACTION", "NO ACTION"}, "e": {"NO ACTION", "NO ACTION"}, "i": {"NO ACTION", "NO ACTION"}, "j": {"NO ACTION", "SET NULL"}} {
		if fk, ok := fks[col]; !ok || fk.OnUpdate != want[0] || fk.OnDelete != want[1] {
			t.Errorf("%s: %+v, want update %s delete %s", col, fk, want[0], want[1])
		}
	}
	if !fks["e"].Deferrable || fks["e"].Deferred || !fks["f"].Deferrable || !fks["f"].Deferred || fks["a"].Deferrable {
		t.Errorf("a deferral is carried: %+v %+v", fks["e"], fks["f"])
	}
	if _, ok := fks["g"]; ok {
		t.Error("a key to columns that are no primary key or unique constraint is not carried")
	}
	if _, ok := fks["h"]; !ok {
		t.Error("a key to a unique constraint is carried")
	}
	wantWarning(t, p.Warnings, WarnFKNotCarried, "c(g)")
	wantWarning(t, p.Warnings, WarnFKNotCarried, "gone(x)")
	msgs := strings.Join(warnMessages(p, WarnFKActionNotCarried), "\n")
	for _, want := range []string{"ON UPDATE CASCADE does not exist in Oracle", "ON UPDATE RESTRICT", "ON UPDATE SET DEFAULT", "ON DELETE RESTRICT does not exist in Oracle", "ON DELETE SET DEFAULT does not exist in Oracle"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("no %q in\n%s", want, msgs)
		}
	}
	if strings.Count(msgs, "ON UPDATE") != 3 || strings.Count(msgs, "ON DELETE") != 2 {
		t.Errorf("a warning for each action that is lost, and none for NO ACTION:\n%s", msgs)
	}
}

func TestOracleForeignKeyClassesShareOneType(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE pt (code VARCHAR(5) PRIMARY KEY, n TEXT)`, `CREATE TABLE ct (id INTEGER PRIMARY KEY, code TEXT REFERENCES pt(code), wide NVARCHAR(300))`,
		`CREATE TABLE pi (id INT4 PRIMARY KEY)`, `CREATE TABLE ci (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES pi(id), pid2 INT2 REFERENCES pi(id))`,
		`CREATE TABLE pn (id INTEGER PRIMARY KEY)`, `CREATE TABLE cn (id INTEGER PRIMARY KEY, ref TEXT REFERENCES pn(id))`,
		`CREATE TABLE pb (k BLOB PRIMARY KEY)`, `CREATE TABLE cb (id INTEGER PRIMARY KEY, k TEXT REFERENCES pb(k))`,
		`CREATE TABLE pd (v NUMERIC(5,2) PRIMARY KEY)`, `CREATE TABLE cd (id INTEGER PRIMARY KEY, v NUMERIC(10,4) REFERENCES pd(v))`,
		`CREATE TABLE pdt (v DATETIME PRIMARY KEY)`, `CREATE TABLE cdt (id INTEGER PRIMARY KEY, v DATETIME REFERENCES pdt(v))`,
		`INSERT INTO pt VALUES ('abcde', 'x')`, `INSERT INTO ct VALUES (1, 'abcde', NULL)`, `INSERT INTO pi VALUES (1)`, `INSERT INTO ci VALUES (1, 1, 1)`,
		`INSERT INTO pn VALUES (1)`, `INSERT INTO cn VALUES (1, '1')`, `INSERT INTO pb VALUES (x'0102')`, `INSERT INTO pd VALUES (1.5)`, `INSERT INTO cd VALUES (1, 1.5)`,
		`INSERT INTO pdt VALUES ('2024-01-01 00:00:00.5')`, `INSERT INTO cdt VALUES (1, '2024-01-01 00:00:00.5')`)
	for _, c := range [][3]string{{"pt", "code", "VARCHAR2(255 CHAR)"}, {"ct", "code", "VARCHAR2(255 CHAR)"}, {"pi", "id", "NUMBER(19,0)"}, {"ci", "pid", "NUMBER(19,0)"}, {"ci", "pid2", "NUMBER(19,0)"},
		{"pn", "id", "VARCHAR2(255 CHAR)"}, {"cn", "ref", "VARCHAR2(255 CHAR)"}, {"pb", "k", "RAW(255)"}, {"cb", "k", "RAW(255)"}, {"pd", "v", "NUMBER(10,4)"}, {"cd", "v", "NUMBER(10,4)"}, {"pdt", "v", "TIMESTAMP(1)"}, {"cdt", "v", "TIMESTAMP(1)"}} {
		if got := colType(t, p, c[0], c[1]); got != c[2] {
			t.Errorf("%s.%s: %s, want %s", c[0], c[1], got, c[2])
		}
	}
	// pi.id is declared INT4 and carried NUMBER(10,0), then joins ci.pid, which is wider: the class takes the widest.
	var widened []string
	for _, w := range p.Warnings {
		if w.Kind == WarnTypeWidened && strings.Contains(w.Message, "joins the columns of its foreign keys") {
			widened = append(widened, w.Object)
		}
	}
	for _, want := range []string{"pi.id", "ci.pid2"} {
		found := false
		for _, o := range widened {
			found = found || o == want
		}
		if !found {
			t.Errorf("no widening warning for %s in %v", want, widened)
		}
	}
	// pn.id was an identity column (an INTEGER PRIMARY KEY) and is text now: no counter.
	if p.Table("pn").Col("id").AutoInc {
		t.Error("a text column is no identity column")
	}
	wantWarning(t, p.Warnings, WarnIdentityNotCarried, "pn.id")
	if strings.Contains(oracleDialect{}.CreateTable(p.Table("pn")), "IDENTITY") {
		t.Error("no IDENTITY on a text column")
	}
}

func TestOracleKeyBudgets(t *testing.T) {
	long := func(n int, c byte) string { return "'" + strings.Repeat(string(c), n) + "'" }
	// Six columns of 900 bytes are 5,412 bytes: they fit. Seven do not.
	p, err := planOracleErr(t, `CREATE TABLE k6 (a TEXT, b TEXT, c TEXT, d TEXT, e TEXT, f TEXT, PRIMARY KEY (a, b, c, d, e, f))`,
		`INSERT INTO k6 VALUES (`+long(900, '1')+`,`+long(900, '2')+`,`+long(900, '3')+`,`+long(900, '4')+`,`+long(900, '5')+`,`+long(900, '6')+`)`)
	if err != nil || len(p.Table("k6").PK) != 6 {
		t.Fatal(err)
	}
	if _, err := planOracleErr(t, `CREATE TABLE k7 (a TEXT, b TEXT, c TEXT, d TEXT, e TEXT, f TEXT, g TEXT, PRIMARY KEY (a, b, c, d, e, f, g))`,
		`INSERT INTO k7 VALUES (`+long(900, '1')+`,`+long(900, '2')+`,`+long(900, '3')+`,`+long(900, '4')+`,`+long(900, '5')+`,`+long(900, '6')+`,`+long(900, '7')+`)`); err == nil ||
		!strings.Contains(err.Error(), `primary key of table "k7" is longer than Oracle allows (6314 bytes, at most 6000, at most 32 columns)`) {
		t.Errorf("%v", err)
	}
	// An index too long is not created; a unique key too long is an error.
	p = planOracle(t, `CREATE TABLE ix (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT, d TEXT)`, `CREATE INDEX ix_all ON ix (a, b, c, d)`, `CREATE UNIQUE INDEX ix_a ON ix (a)`,
		`INSERT INTO ix VALUES (1, `+long(1700, 'a')+`,`+long(1700, 'b')+`,`+long(1700, 'c')+`,`+long(1700, 'd')+`)`)
	if len(p.Table("ix").Indexes) != 1 || p.Table("ix").Indexes[0].Name != "ix_a" {
		t.Errorf("%+v", p.Table("ix").Indexes)
	}
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "ix.ix_all")
	if _, err := planOracleErr(t, `CREATE TABLE ux (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT, d TEXT, UNIQUE (a, b, c, d))`,
		`INSERT INTO ux VALUES (1, `+long(1700, 'a')+`,`+long(1700, 'b')+`,`+long(1700, 'c')+`,`+long(1700, 'd')+`)`); err == nil || !strings.Contains(err.Error(), "unique key of table") {
		t.Errorf("%v", err)
	}
	// A key of more than 32 columns, and a RAW key longer than 2,000 bytes.
	var cols, names []string
	for i := 0; i < 33; i++ {
		cols = append(cols, fmt.Sprintf("c%d INTEGER", i))
		names = append(names, fmt.Sprintf("c%d", i))
	}
	if _, err := planOracleErr(t, `CREATE TABLE wide (`+strings.Join(cols, ", ")+`, PRIMARY KEY (`+strings.Join(names, ", ")+`))`); err == nil || !strings.Contains(err.Error(), "at most 32 columns") {
		t.Errorf("%v", err)
	}
	if _, err := planOracleErr(t, `CREATE TABLE rk (k BLOB PRIMARY KEY)`, `INSERT INTO rk VALUES (zeroblob(2001))`); err == nil || !strings.Contains(err.Error(), "cannot be indexed") {
		t.Errorf("%v", err)
	}
	// The key cost of every type.
	for _, c := range []struct {
		ch   Choice
		size int
		want int
	}{{Choice{Family: "int"}, 0, 22}, {Choice{Family: "bool"}, 0, 22}, {Choice{Family: "decimal"}, 0, 22}, {Choice{Family: "float"}, 0, 8}, {Choice{Family: "date"}, 0, 7}, {Choice{Family: "datetime"}, 0, 11},
		{Choice{Family: "text", Len: 10}, 7, 7}, {Choice{Family: "text", Len: 10}, 0, 1}, {Choice{Family: "bytes", Len: 3}, 2, 2}, {Choice{Family: "bytes", Len: 3}, 0, 1}} {
		if got := oracleKeyBytes(c.ch, c.size); got != c.want {
			t.Errorf("oracleKeyBytes(%+v, %d) = %d, want %d", c.ch, c.size, got, c.want)
		}
	}
	for _, c := range []struct {
		ch   Choice
		size int
	}{{Choice{Family: "text"}, 3}, {Choice{Family: "text", Len: 10}, 4001}, {Choice{Family: "bytes"}, 3}, {Choice{Family: "bytes", Len: 2001}, 3}} {
		if got := oracleKeyBytes(c.ch, c.size); got < 1<<20 {
			t.Errorf("oracleKeyBytes(%+v, %d) = %d: not indexable", c.ch, c.size, got)
		}
	}
}

func TestOracleNamesAndCaseTwins(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE "Ünï_t" (id INTEGER PRIMARY KEY, "Case" TEXT, "CASE2" TEXT)`, `CREATE TABLE "ünï_t" (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE "q""uote" ("c""ol" INTEGER PRIMARY KEY, "ROWID" TEXT, "Rowid2" TEXT)`, `CREATE TABLE "ROWID" (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE "`+strings.Repeat("é", 64)+`" (id INTEGER PRIMARY KEY)`, `CREATE TABLE "`+strings.Repeat("é", 65)+`" (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE "_Demodb_Import2" (id INTEGER PRIMARY KEY)`)
	renamed := map[string]bool{}
	for _, w := range p.Warnings {
		if w.Kind == WarnNameRenamed {
			renamed[w.Object] = true
		}
	}
	for _, want := range []string{"q\"uote", "q\"uote.c\"ol", "q\"uote.ROWID", "ROWID", strings.Repeat("é", 65)} {
		if !renamed[want] {
			t.Errorf("%q is renamed: %v", want, renamed)
		}
	}
	if len(renamed) != 5 {
		t.Errorf("nothing else is renamed (case twins, accents and 128 bytes are kept): %v", renamed)
	}
	for _, name := range []string{"Ünï_t", "ünï_t", strings.Repeat("é", 64)} {
		if p.Table(name) == nil {
			t.Errorf("table %q is kept", name)
		}
	}
	msgs := warnMessages(p, WarnNameCaseCollision)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "quotes every name") {
		t.Errorf("%v", msgs)
	}
	// The marker table is the one name a table cannot have, as it is spelled.
	if _, err := planOracleErr(t, `CREATE TABLE "_demodb_import" (id INTEGER PRIMARY KEY)`); err == nil || !strings.Contains(err.Error(), "completion marker") {
		t.Errorf("%v", err)
	}
}
