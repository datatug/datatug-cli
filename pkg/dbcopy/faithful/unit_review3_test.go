package faithful

import (
	"strings"
	"testing"
)

func TestDefaultFitsDecimal(t *testing.T) {
	d := func(decl string) declInfo { return stat(decl).decl }
	for _, c := range []struct {
		def  string
		decl string
		want bool
	}{
		{"1.005", "DECIMAL(10,2)", false}, {"'1.005'", "DECIMAL_TEXT(10,2)", false}, {"1e-9", "DECIMAL(10,2)", false}, {"12345", "DECIMAL(3,1)", false},
		{"(1.5)", "DECIMAL(10,2)", true}, {"'1.5'", "DECIMAL_TEXT(10,2)", true}, {"99.9", "DECIMAL(3,1)", true}, {"-99.9", "DECIMAL(3,1)", true}, {"100", "DECIMAL(3,1)", false},
		{"1.005", "NUMERIC", true}, {"CURRENT_TIMESTAMP", "DECIMAL(10,2)", true}, {"", "DECIMAL(10,2)", true}, {"1e2", "DECIMAL(10,2)", true},
	} {
		if got := defaultFitsDecimal(c.def, d(c.decl)); got != c.want {
			t.Errorf("%s in %s: %v, want %v", c.def, c.decl, got, c.want)
		}
	}
}

func TestDecimalDefaultSpelling(t *testing.T) {
	for _, c := range []struct{ def, typ, want string }{
		{"'1.5'", "numeric(10,2)", "1.50"}, {"'2'", "numeric(10,2)", "2.00"}, {"'1.25'", "numeric(10,2)", "1.25"}, {"'.5'", "numeric", "0.5"}, {"'abc'", "numeric", "abc"},
	} {
		if got := decimalDefaultSpelling(c.def, c.typ); got != c.want {
			t.Errorf("%s in %s: %s, want %s", c.def, c.typ, got, c.want)
		}
	}
}

// A default that does not fit the declared precision widens the column even when every value fits.
func TestDefaultThatDoesNotFitWidensTheColumn(t *testing.T) {
	c := &Column{Name: "c", SourceType: "DECIMAL(10,2)", Default: "1.005"}
	ch := chooseSQLiteToPG(c, stat("DECIMAL(10,2)", int64(1), 1.5), false)
	if ch.typ != "numeric" || !strings.Contains(ch.note, "its default 1.005 does not fit") {
		t.Errorf("%s %q", ch.typ, ch.note)
	}
}

// What is counted as written differently is what the column gets: padded for numeric(p,s), as is for a
// plain numeric. A text that numeric would change is counted in a widened column too.
func TestNormalizedCountFollowsTheTypeOfTheColumn(t *testing.T) {
	ch := chooseSQLiteToPG(&Column{Name: "c", SourceType: "DECIMAL_TEXT(10,2)"}, stat("DECIMAL_TEXT(10,2)", ".5", "1.5", "1.234"), false)
	if ch.typ != "numeric" || ch.normalized != 1 || ch.example != ".5 -> 0.5" {
		t.Errorf("widened: %s %d %q", ch.typ, ch.normalized, ch.example)
	}
	ch = chooseSQLiteToPG(&Column{Name: "c", SourceType: "DECIMAL_TEXT(10,2)"}, stat("DECIMAL_TEXT(10,2)", "007.50", "1.234"), false)
	if ch.typ != "numeric" || ch.normalized != 1 || ch.example != "007.50 -> 7.50" {
		t.Errorf("leading zeros in a widened column: %s %d %q", ch.typ, ch.normalized, ch.example)
	}
	ch = chooseSQLiteToPG(&Column{Name: "c", SourceType: "DECIMAL_TEXT(10,2)"}, stat("DECIMAL_TEXT(10,2)", "1.5", "2"), false)
	if ch.typ != "numeric(10,2)" || ch.normalized != 2 {
		t.Errorf("padded: %s %d", ch.typ, ch.normalized)
	}
	ch = chooseSQLiteToPG(&Column{Name: "c", SourceType: "DECIMAL_TEXT(10,2)"}, stat("DECIMAL_TEXT(10,2)", ".5", "1.234"), true)
	if ch.typ != "text" || !strings.Contains(ch.note, "would write 1 of its text values") {
		t.Errorf("--decimals=text: %s %q", ch.typ, ch.note)
	}
}

// Year 0000 is not a PostgreSQL date.
func TestYearZeroIsNotADate(t *testing.T) {
	for _, c := range []struct{ decl, v string }{{"DATE", "0000-01-01"}, {"DATETIME", "0000-01-01 00:00:00"}, {"TIMESTAMPTZ", "0000-01-01 00:00:00+00"}} {
		if ch := chooseSQLiteToPG(&Column{Name: "c", SourceType: c.decl}, stat(c.decl, c.v), false); ch.typ != "text" {
			t.Errorf("%s %s: %s", c.decl, c.v, ch.typ)
		}
	}
}

// A numeric default that PostgreSQL stores with its scale is a text without it in a text-affinity column of SQLite.
func TestPostgresNumericDefaultWithFewerDecimalsThanTheScaleIsWarned(t *testing.T) {
	col := func(def string) *Column {
		return &Column{Name: "v", SourceType: "numeric(10,3)", Kind: KindDecimal, Precision: 10, Scale: 3, Default: def}
	}
	for def, warned := range map[string]bool{"1.5": true, "2": true, "1.500": false, "1e2": true} {
		cat := &Catalog{Tables: []*Table{{Name: "t", Columns: []*Column{col(def)}}}}
		p := PlanPGToSQLite(cat, func([]string, string) error { return nil })
		got := false
		for _, w := range p.Warnings {
			got = got || w.Kind == WarnDefaultTranslated
		}
		if got != warned {
			t.Errorf("default %s: warned %v, want %v (%v)", def, got, warned, p.Warnings)
		}
	}
}
