package faithful

import (
	"encoding/hex"
	"math"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDialectRegistry(t *testing.T) {
	d, err := DialectByName("mysql")
	if err != nil || d.Name() != "mysql" || d.MaxIdentLen() != 64 {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := DialectByName("postgres"); err == nil || !strings.Contains(err.Error(), "implemented: mssql, mysql, oracle") {
		t.Errorf("an unimplemented dialect is refused with the list of the implemented ones: %v", err)
	}
	if (ColumnUse{}).Keyed() || !(ColumnUse{FK: true}).Keyed() || !(ColumnUse{PK: true}).Keyed() {
		t.Error("Keyed")
	}
}

func TestMySQLQuoteIdent(t *testing.T) {
	d := mysqlDialect{}
	for in, want := range map[string]string{"a": "`a`", "a`b": "`a``b`", "``": "``````", "Order Details": "`Order Details`", "Production.Product": "`Production.Product`"} {
		if got := d.QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMySQLStorableName(t *testing.T) {
	d := mysqlDialect{}
	ok := []string{"a", "Order Details", "Production.Product", "back`tick", `back\slash`, "ünï", "日本語", "a/b", "a;--b", "'q'", "select", "a b", " lead", strings.Repeat("é", 64), "x\"y"}
	for _, n := range ok {
		if why := d.StorableName(n); why != "" {
			t.Errorf("%q should be storable: %s", n, why)
		}
	}
	bad := map[string]string{
		"": "empty", "trail ": "ends in a space", strings.Repeat("a", 65): "65 characters", "a\nb": "U+000A", "a\rb": "U+000D", "a\x00b": "U+0000",
		"a\tb": "U+0009", "a\x7fb": "U+007F", "a\u0085b": "U+0085", "a b": "U+2028", "😀": "outside the Basic Multilingual Plane", "a\xffb": "not valid UTF-8",
	}
	for n, want := range bad {
		if why := d.StorableName(n); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want it to mention %q", n, why, want)
		}
	}
}

func TestSafeNameAndFold(t *testing.T) {
	d := mysqlDialect{}
	cases := []string{"", "trail ", "a\nb", "😀x", strings.Repeat("long", 40), "  "}
	seen := map[string]string{}
	for _, c := range cases {
		got := safeName(d, c)
		if why := d.StorableName(got); why != "" {
			t.Errorf("safeName(%q) = %q is not storable: %s", c, got, why)
		}
		if again := safeName(d, c); again != got {
			t.Errorf("safeName(%q) is not deterministic", c)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("safeName(%q) = safeName(%q) = %q", c, prev, got)
		}
		seen[got] = c
	}
	if safeName(d, "a\nb") != "a_b_"+nameHash("a\nb") {
		t.Errorf("%s", safeName(d, "a\nb"))
	}
	// MySQL compares column and index names without case and with every accent kept.
	if foldName("Ünï") != foldName("ünï") || foldName("a") == foldName("b") || foldName("É") != "é" || foldName("e") == foldName("é") || foldName("ünï") == foldName("uni") {
		t.Error("foldName")
	}
}

func TestMySQLLiteralFamilies(t *testing.T) {
	d := mysqlDialect{}
	text := Choice{Family: "text", Type: "TEXT"}
	cases := []struct {
		ch   Choice
		v    any
		want string
	}{
		{Choice{Family: "int"}, int64(-5), "-5"},
		{Choice{Family: "bool"}, int64(1), "1"},
		{Choice{Family: "float"}, 1.5, "1.5e+00"},
		{Choice{Family: "float"}, int64(3), "3e+00"},
		{Choice{Family: "float"}, math.Copysign(0, -1), "-0e+00"},
		{Choice{Family: "float"}, 5e-324, "5e-324"},
		{Choice{Family: "decimal"}, int64(7), "7"},
		{Choice{Family: "decimal"}, 0.1, "0.1"},
		{Choice{Family: "decimal"}, "-.5", "-.5"},
		{Choice{Family: "decimal"}, "1.50", "1.50"},
		{Choice{Family: "date"}, "2024-02-29", "'2024-02-29'"},
		{Choice{Family: "datetime"}, "2024-02-29 23:59:59.5", "'2024-02-29 23:59:59.5'"},
		{Choice{Family: "time"}, "23:59:59", "'23:59:59'"},
		{text, "plain", "'plain'"},
		{text, "it's", "'it''s'"},
		{text, "", "''"},
		{text, int64(12), "'12'"},
		{text, []byte("bytes"), "'bytes'"},
		{text, "back\\slash", "_utf8mb4 X'" + hex.EncodeToString([]byte("back\\slash")) + "'"},
		{text, "a\nb", "_utf8mb4 X'610a62'"},
		{text, "a\rb", "_utf8mb4 X'610d62'"},
		{text, "😀", "'😀'"},
		{Choice{Family: "bytes"}, []byte{0, 0xff}, "X'00ff'"},
		{Choice{Family: "bytes"}, []byte{}, "X''"},
		{Choice{Family: "bytes"}, "txt", "X'747874'"},
		{Choice{Family: "bytes"}, int64(12), "X'3132'"},
		{Choice{Family: "bytes"}, nil, "NULL"},
		{Choice{Family: "int"}, nil, "NULL"},
	}
	for _, c := range cases {
		got, err := d.Literal(c.ch, c.v)
		if err != nil || got != c.want {
			t.Errorf("Literal(%s, %#v) = %q, %v; want %q", c.ch.Family, c.v, got, err, c.want)
		}
	}
	bad := []struct {
		ch Choice
		v  any
	}{
		{Choice{Family: "int"}, "x"}, {Choice{Family: "float"}, "x"}, {Choice{Family: "decimal"}, "abc"}, {Choice{Family: "decimal"}, []byte("1")},
		{Choice{Family: "date"}, int64(1)}, {Choice{Family: "date"}, "2024-02-29'; DROP"}, {text, []byte{0xff}}, {text, true}, {text, 1.5}, {Choice{Family: "bytes"}, 1.5}, {Choice{Family: "weird"}, int64(1)},
	}
	for _, c := range bad {
		if got, err := d.Literal(c.ch, c.v); err == nil {
			t.Errorf("Literal(%s, %#v) = %q, want an error", c.ch.Family, c.v, got)
		}
	}
	if _, ok := valueBytes(true); ok {
		t.Error("valueBytes of a bool")
	}
}

// decodeLiteral is an independent reader of the literals the dialect writes: it returns the bytes a
// MySQL server reads from them.
func decodeLiteral(t testing.TB, lit string) string {
	t.Helper()
	switch {
	case strings.HasPrefix(lit, "_utf8mb4 X'") && strings.HasSuffix(lit, "'"):
		b, err := hex.DecodeString(lit[len("_utf8mb4 X'") : len(lit)-1])
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	case len(lit) >= 2 && lit[0] == '\'' && lit[len(lit)-1] == '\'':
		body := lit[1 : len(lit)-1]
		if strings.Contains(strings.ReplaceAll(body, "''", ""), "'") {
			t.Fatalf("a quote is not doubled in %q", lit)
		}
		return strings.ReplaceAll(body, "''", "'")
	}
	t.Fatalf("not a literal: %q", lit)
	return ""
}

// The hostile-literal property: whatever the text, its literal holds no CR, LF, NUL or other raw control
// character and no backslash outside a hex literal, it cannot end its own quotes early, and it reads back as
// the text.
func TestTextLiteralProperty(t *testing.T) {
	atoms := []string{"'", "''", "\\", "\n", "\r", "\r\n", "\x00", "\x1a", "\x7f", "\t", "\u0085", " ", " ", "GO", "\\!", "source ", "/", "--", "/*", "*/", ";", "`", "\"",
		"$(", "😀", "é", "日本", " ", "%s", "" + string(rune(0xFEFF)) + "", "" + string(rune(0xFFFF)) + "", "a", "0", "x'41'", "_utf8mb4 X'", "\\'", "'; DROP TABLE t; --"}
	rng := rand.New(rand.NewSource(20261016))
	d := mysqlDialect{}
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := rng.Intn(8); n >= 0; n-- {
			b.WriteString(atoms[rng.Intn(len(atoms))])
		}
		s := b.String()
		lit, err := d.Literal(Choice{Family: "text"}, s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(lit, "\r\n\x00") || strings.ContainsFunc(lit, func(r rune) bool {
			return r < 0x20 && r != '\t' || r == 0x7f || r >= 0x80 && r <= 0x9f || r == 0x2028 || r == 0x2029
		}) {
			t.Fatalf("the literal of %q holds a raw control character: %q", s, lit)
		}
		if !strings.HasPrefix(lit, "_utf8mb4 X'") && strings.Contains(lit, "\\") {
			t.Fatalf("the literal of %q holds a backslash outside a hex literal: %q", s, lit)
		}
		if got := decodeLiteral(t, lit); got != s {
			t.Fatalf("the literal %q of %q reads back as %q", lit, s, got)
		}
		if !utf8.ValidString(lit) {
			t.Fatalf("not UTF-8: %q", lit)
		}
	}
	// Bytes: always hex.
	for i := 0; i < 1000; i++ {
		b := make([]byte, rng.Intn(20))
		rng.Read(b)
		lit, _ := d.Literal(Choice{Family: "bytes"}, b)
		if !strings.HasPrefix(lit, "X'") || strings.Trim(lit[2:len(lit)-1], "0123456789abcdef") != "" {
			t.Fatalf("%q", lit)
		}
	}
}

func TestCommentText(t *testing.T) {
	if got := commentText("a\nb\r\nc\x00d e  f"); got != "a b c d e f" {
		t.Errorf("%q", got)
	}
	long := commentText(strings.Repeat("é", 500))
	if !utf8.ValidString(long) || !strings.HasSuffix(long, "...") {
		t.Errorf("%q", long)
	}
}
