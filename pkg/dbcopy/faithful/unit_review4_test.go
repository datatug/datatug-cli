package faithful

import "testing"

// The spelling of a default is what PostgreSQL stores for the literal, whichever way SQLite had it written.
func TestDecimalDefaultSpellingOfEveryWayToWriteANumber(t *testing.T) {
	for _, c := range []struct{ def, typ, want string }{
		{"'+1.5'", "numeric(10,2)", "1.50"}, {"+1.5", "numeric", "1.5"}, {"'1e2'", "numeric(10,2)", "100.00"}, {"1e2", "numeric", "100"},
		{"1.5e1", "numeric", "15"}, {"1.50e1", "numeric", "15.0"}, {"0.10e-1", "numeric", "0.010"}, {"1E-9", "numeric", "0.000000001"}, {"'5.'", "numeric", "5"}, {"'-0.0'", "numeric", "0.0"},
		{"'1.50'", "numeric", "1.50"}, {"'007.5'", "numeric", "7.5"}, {"1e9999999", "numeric", "1e9999999"},
	} {
		if got := decimalDefaultSpelling(c.def, c.typ); got != c.want {
			t.Errorf("%s in %s: %s, want %s", c.def, c.typ, got, c.want)
		}
	}
	if defaultFitsDecimal("1e9999999", stat("DECIMAL(10,2)").decl) {
		t.Error("a number too large to read does not fit")
	}
}

// A column that is a plain numeric because of its default pads nothing: the warning counts the texts that
// a plain numeric spells differently, not the ones numeric(p,s) would have padded.
func TestNormalizedCountOfAColumnWidenedByItsDefault(t *testing.T) {
	c := &Column{Name: "c", SourceType: "DECIMAL_TEXT(10,2)", Default: "'1.005'"}
	ch := chooseSQLiteToPG(c, stat("DECIMAL_TEXT(10,2)", "1.5", "2", "007.5"), false)
	if ch.typ != "numeric" || ch.normalized != 1 || ch.example != "007.5 -> 7.5" {
		t.Errorf("%s %d %q", ch.typ, ch.normalized, ch.example)
	}
	ch = chooseSQLiteToPG(c, stat("DECIMAL_TEXT(10,2)", "1.5", "2"), false)
	if ch.typ != "numeric" || ch.normalized != 0 {
		t.Errorf("a plain numeric writes these as they are: %s %d", ch.typ, ch.normalized)
	}
}

// A NaN or Infinity that stays text in a column of numeric affinity is counted and said, without a server.
func TestNaNKeptAsTextIsSaid(t *testing.T) {
	loss := &lossCount{}
	enc := decimalToNumericAffinity(loss)
	for _, in := range []string{"NaN", "Infinity", "-Infinity", "1.5"} {
		if _, err := enc(in); err != nil {
			t.Fatal(err)
		}
	}
	if loss.special != 3 {
		t.Errorf("counted %d, want 3", loss.special)
	}
	x := &executor{plan: &Plan{}}
	x.warnLosses(&TargetTable{Name: "t", Columns: []*TargetColumn{{Name: "n", loss: loss}}})
	if !hasWarningText(&Report{Warnings: x.plan.Warnings}, "t.n", "3 numeric NaN / Infinity value(s) are kept as their text") {
		t.Errorf("%v", x.plan.Warnings)
	}
}
