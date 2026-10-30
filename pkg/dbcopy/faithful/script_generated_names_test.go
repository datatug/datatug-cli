package faithful

import (
	"sort"
	"strings"
	"testing"
)

func lostNames(p *ScriptPlan) string {
	var lost []string
	for _, w := range p.Warnings {
		if w.Kind == WarnConstraintNameLost {
			lost = append(lost, w.Object)
		}
	}
	sort.Strings(lost)
	return strings.Join(lost, ",")
}

func checkNameOf(p *ScriptPlan, table string) []string {
	var names []string
	for _, c := range p.Table(table).Checks {
		names = append(names, c.Name)
	}
	return names
}

// A name of the shape MySQL gives to a constraint without one is not carried while a table of the script has such a
// constraint: the load would fail with error 3822 (check) or 1826 (foreign key). Every name that cannot collide is kept.
func TestMySQLGeneratedConstraintNamesAreReserved(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK (n > 0))`,
		`CREATE TABLE u (id INTEGER PRIMARY KEY, a INTEGER CONSTRAINT t_chk_1 CHECK (a > 0), b INTEGER CONSTRAINT "T_CHK_22" CHECK (b > 0), c INTEGER CONSTRAINT t_chk_x CHECK (c > 0),
			d INTEGER CONSTRAINT t_chk_01 CHECK (d > 0), e INTEGER CONSTRAINT t_chk_ CHECK (e > 0), f INTEGER CONSTRAINT t_chk_1x CHECK (f > 0), g INTEGER CONSTRAINT u_chk_1 CHECK (g > 0))`,
		`CREATE TABLE c1 (id INTEGER PRIMARY KEY, p INTEGER REFERENCES t(id))`,
		`CREATE TABLE c2 (id INTEGER PRIMARY KEY, p INTEGER, q INTEGER, r INTEGER, CONSTRAINT c1_ibfk_1 FOREIGN KEY (p) REFERENCES t(id), CONSTRAINT "C1_IBFK_2" FOREIGN KEY (q) REFERENCES t(id), CONSTRAINT c1_ibfk_x FOREIGN KEY (r) REFERENCES t(id))`))
	if got := lostNames(p); got != "c2.C1_IBFK_2,c2.c1_ibfk_1,u.T_CHK_22,u.t_chk_1,u.u_chk_1" { // u_chk_1: u's own checks lost their names, so u generates u_chk_<n> too
		t.Errorf("names not carried: %s", got)
	}
	if got := strings.Join(checkNameOf(p, "u"), ","); got != ",,t_chk_x,t_chk_01,t_chk_,t_chk_1x," {
		t.Errorf("check names of u: %s", got)
	}
	var fks []string
	for _, fk := range p.Table("c2").FKs {
		fks = append(fks, fk.Name)
	}
	sort.Strings(fks)
	if got := strings.Join(fks, ","); got != ",,c1_ibfk_x" {
		t.Errorf("foreign key names of c2: %s", got)
	}
	for _, w := range p.Warnings {
		if w.Kind == WarnConstraintNameLost && !strings.Contains(w.Message, "3822") && !strings.Contains(w.Message, "1826") {
			t.Errorf("a warning does not say why: %s", w.Message)
		}
	}
}

// A table without an unnamed constraint of the kind generates no name: its names, whatever their shape, load.
func TestMySQLGeneratedConstraintNamesNeedAnUnnamedConstraint(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CONSTRAINT t_chk_1 CHECK (n > 0))`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER, CONSTRAINT c_ibfk_1 FOREIGN KEY (p) REFERENCES t(id))`,
		`CREATE TABLE o (id INTEGER PRIMARY KEY, p INTEGER REFERENCES t(id))`))
	if got := lostNames(p); got != "" {
		t.Errorf("names not carried: %s", got)
	}
	if p.Table("c").FKs[0].Name != "c_ibfk_1" || checkNameOf(p, "t")[0] != "t_chk_1" {
		t.Error("a name nothing generates a twin of is kept")
	}
}

// A name that is dropped leaves its constraint without one, so MySQL generates another name: the rule runs until nothing
// changes. Here b_ibfk_1 is dropped (b has an unnamed key), which makes a's key unnamed, which reserves a_ibfk_<n> and
// drops z's a_ibfk_1.
func TestMySQLGeneratedConstraintNamesReachAFixpoint(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, p INTEGER REFERENCES t(id))`,
		`CREATE TABLE a (id INTEGER PRIMARY KEY, p INTEGER, CONSTRAINT b_ibfk_1 FOREIGN KEY (p) REFERENCES t(id))`,
		`CREATE TABLE z (id INTEGER PRIMARY KEY, p INTEGER, CONSTRAINT a_ibfk_1 FOREIGN KEY (p) REFERENCES t(id))`))
	if got := lostNames(p); got != "a.b_ibfk_1,z.a_ibfk_1" {
		t.Errorf("names not carried: %s", got)
	}
}

func TestIsCanonicalNumber(t *testing.T) {
	for s, want := range map[string]bool{"1": true, "22": true, "10": true, "": false, "0": false, "01": false, "1x": false, "-1": false, "x": false, "١": false} {
		if got := isCanonicalNumber(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
}

// The other dialects name nothing like that: the rule is MySQL's.
func TestOtherDialectsKeepNamesOfTheMySQLShape(t *testing.T) {
	for _, dialect := range []string{"mssql", "oracle"} {
		p, err := PlanScript(t.Context(), dialect, sqliteWith(t,
			`CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER CHECK (n > 0))`,
			`CREATE TABLE u (id INTEGER PRIMARY KEY, a INTEGER CONSTRAINT t_chk_1 CHECK (a > 0))`).DB)
		if err != nil {
			t.Fatal(err)
		}
		if got := checkNameOf(p, "u"); len(got) != 1 || got[0] != "t_chk_1" {
			t.Errorf("%s: %v", dialect, got)
		}
	}
}

// Foreign key names are compared without accents and with ß as s (the collation of the names of foreign keys), so the name
// "c_íbfk_1" is the name MySQL gives an unnamed key of table c, and "s_ibfk_1" the one of a table called ß.
func TestMySQLGeneratedForeignKeyNamesAreComparedAsMySQLDoes(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES t(id))`,
		`CREATE TABLE "ß" (id INTEGER PRIMARY KEY, p INTEGER REFERENCES t(id))`,
		`CREATE TABLE z (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER, d INTEGER, CONSTRAINT "c_íbfk_1" FOREIGN KEY (a) REFERENCES t(id), CONSTRAINT s_ibfk_2 FOREIGN KEY (b) REFERENCES t(id), CONSTRAINT keep_me FOREIGN KEY (d) REFERENCES t(id))`))
	if got := lostNames(p); got != "z.c_íbfk_1,z.s_ibfk_2" {
		t.Errorf("names not carried: %s", got)
	}
}
