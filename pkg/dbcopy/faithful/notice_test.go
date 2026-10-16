package faithful

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// noticeAtoms are the pieces hostile notice text is made of: what ends a line in some reader, what starts a
// client command or a batch at the start of one, what ends or starts a block comment.
var noticeAtoms = []string{"\n", "\r", "\r\n", "\n\r", "\u0085", "\u2028", "\u2029", "\v", "\f", "\x1c", "\x1f", "\x00", "\x1b", "\x7f", "\ufeff", "�", "\xff", "\xc3",
	"\u202e", "\u2066", "\u200f", "\t", " ", "  ", "GO", "\nGO", "\n\\! cmd", "\nsource x", "\n/", "\\", "\\g", "*/", "/*", "*", "/", "/*!", "*/ DROP TABLE x; /*", "$(", "$(x)", "-- ", "--", ";", "'", "\"",
	"DELIMITER $$", ":r x", "!! cmd", "é", "日本", "😀", "Copyright (c) 2024 A", "0"}

// hostileNoticeText is the notice the MySQL and SQL Server journeys carry in the header of their fixture and fuzz
// datasets (TestWriteHostileNotice writes it): every hazard of a line, a client command and a comment, a leading byte
// order mark and CR LF line ends. It is built here, not kept as a file, because the repository's hooks refuse the
// trailing whitespace of its CR characters.
func hostileNoticeText() string {
	lines := []string{"Copyright (c) 2024 Hostile Notice Co.", "GO", "\\! touch /tmp/datatug-notice-pwned", "\\g", "source /etc/passwd", "/", "*/ DROP DATABASE x; /*",
		"DELIMITER $$", "exit", "quit", "!! touch /tmp/datatug-notice-pwned", ":r /etc/passwd", ":setvar x y", "$(ComSpec)", "USE master;", "SELECT 1; -- end",
		"  indented\ttabbed", "x*/*y"}
	return "\ufeff" + strings.Join(lines, "\r\n") + "\u2028GO\u2029source y\u0085/\v\fGO\ufeffGO\u202eevil\x1b[31m\a\x1fGO\n"
}

// TestWriteHostileNotice writes hostileNoticeText to the file named by DATATUG_WRITE_HOSTILE_NOTICE; the journeys
// (scripts/check-journey-mysql.sh and scripts/check-journey-mssql.sh) give it to notice-file=.
func TestWriteHostileNotice(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_HOSTILE_NOTICE")
	if path == "" {
		t.Skip("DATATUG_WRITE_HOSTILE_NOTICE is not set")
	}
	if err := os.WriteFile(path, []byte(hostileNoticeText()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostileNotice(rng *rand.Rand) string {
	var b strings.Builder
	for i, n := 0, 1+rng.Intn(14); i < n; i++ {
		b.WriteString(noticeAtoms[rng.Intn(len(noticeAtoms))])
	}
	return b.String()
}

// checkNoticeLines requires what the header needs of a notice, whatever the notice was.
func checkNoticeLines(t testing.TB, notice string, lines []string) {
	t.Helper()
	for _, l := range lines {
		if l != "--" && !strings.HasPrefix(l, "-- ") {
			t.Fatalf("notice %q: line %q does not start with a comment introducer", notice, l)
		}
		if strings.HasSuffix(l, " ") {
			t.Fatalf("notice %q: line %q ends with a space", notice, l)
		}
		body := strings.TrimPrefix(strings.TrimPrefix(l, "--"), " ")
		if !utf8.ValidString(l) {
			t.Fatalf("notice %q: line %q is not valid UTF-8", notice, l)
		}
		for _, r := range body {
			if isLineEnd(r) || (isInert(r) && r != ' ') && r != '\t' {
				t.Fatalf("notice %q: line %q holds %U", notice, l, r)
			}
			if r == '\t' {
				t.Fatalf("notice %q: line %q holds a tab", notice, l)
			}
		}
		if strings.Contains(body, "*/") || strings.Contains(body, "/*") {
			t.Fatalf("notice %q: line %q holds a block comment delimiter", notice, l)
		}
	}
}

func TestNoticeLinesTable(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"empty", "", []string{}},
		{"blank only", " \n\t\r\n \u2028", []string{}},
		{"one line", "MIT License", []string{"-- MIT License"}},
		{"lf", "a\nb", []string{"-- a", "-- b"}},
		{"crlf is one end", "a\r\nb", []string{"-- a", "-- b"}},
		{"cr alone", "a\rb", []string{"-- a", "-- b"}},
		{"lf cr is two ends", "a\n\rb", []string{"-- a", "--", "-- b"}},
		{"nel ls ps vt ff fs gs rs us", "a\u0085b\u2028c\u2029d\ve\ff\x1cg\x1dh\x1ei\x1fj", []string{"-- a", "-- b", "-- c", "-- d", "-- e", "-- f", "-- g", "-- h", "-- i", "-- j"}},
		{"blank line kept, edges dropped", "\n\nA\n\n\nB\n\n", []string{"-- A", "--", "--", "-- B"}},
		{"trailing spaces cut, leading kept", "  a  \n\tb\t", []string{"--   a", "--     b"}},
		{"nul and controls are spaces", "a\x00b\x1bc\x7fd\u0080e", []string{"-- a b c d e"}},
		{"bom and bidi are spaces", "a\ufeffb\u202ec\u2066d\u200fe\u061cf", []string{"-- a b c d e f"}},
		{"invalid bytes are spaces", "a\xffb\xc3", []string{"-- a b"}},
		{"block comment end", "x */ y", []string{"-- x * / y"}},
		{"block comment start", "x /* y", []string{"-- x / * y"}},
		{"block comment mix", "/*/ */* **/ /**/", []string{"-- / * / * / * ** / / ** /"}},
		{"slash star across a dropped control", "a/\x00*b", []string{"-- a/ *b"}},
		{"slash alone and star alone", "a / b * c", []string{"-- a / b * c"}},
		{"star at the end of a line, slash at the start of the next", "a*\n/b", []string{"-- a*", "-- /b"}},
		{"line start slash", "ok\n/\nGO", []string{"-- ok", "-- /", "-- GO"}},
		{"unicode kept", "© 2024 日本 😀", []string{"-- © 2024 日本 😀"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := NoticeLines("-- ", c.in, nil)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") || len(got) != len(c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			checkNoticeLines(t, c.in, got)
		})
	}
}

func TestNoticeLinesAppliesTheDialectRule(t *testing.T) {
	got := NoticeLines("-- ", "a $(x)\n\n$(", func(s string) string { return strings.ReplaceAll(s, "$(", "$ (") })
	if want := []string{"-- a $ (x)", "--", "-- $ ("}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := NoticeLines("-- ", "a\n\nb", func(string) string { return "X  " }); strings.Join(got, "|") != "-- X|--|-- X" {
		t.Fatalf("a rule's result is trimmed and an empty line is not given to it: %q", got)
	}
	if got := NoticeLines("# ", "a", nil); got[0] != "# a" {
		t.Fatalf("the prefix is the dialect's: %q", got)
	}
}

func TestNoticeLinesHostileRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(425))
	for i := 0; i < 20000; i++ {
		n := hostileNotice(rng)
		checkNoticeLines(t, n, NoticeLines("-- ", n, nil))
		l := NoticeLines("-- ", n, mssqlNoticeLine)
		checkNoticeLines(t, n, l)
		for _, x := range l {
			if strings.Contains(x, "$(") {
				t.Fatalf("notice %q: %q holds $(", n, x)
			}
		}
	}
}

func FuzzNoticeLines(f *testing.F) {
	for _, a := range noticeAtoms {
		f.Add(a)
	}
	f.Add("Copyright\r\nGO\n/")
	f.Fuzz(func(t *testing.T, n string) {
		checkNoticeLines(t, n, NoticeLines("-- ", n, nil))
		l := NoticeLines("-- ", n, mssqlNoticeLine)
		checkNoticeLines(t, n, l)
		for _, x := range l {
			if strings.Contains(x, "$(") {
				t.Fatalf("notice %q: %q holds $(", n, x)
			}
		}
	})
}

func TestValidateNotice(t *testing.T) {
	for _, c := range []struct {
		name, in string
		bad      bool
	}{
		{"ok", "MIT License\n\nCopyright (c) X\n", false},
		{"empty is the validator's business", "", false},
		{"exactly the limit", strings.Repeat("a", MaxNoticeBytes), false},
		{"one byte over", strings.Repeat("a", MaxNoticeBytes+1), true},
		{"invalid utf-8", "a\xffb", true},
		{"nul", "a\x00b", true},
		{"hostile but valid", "\nGO\n\\! x\r\n\u2028*/", false},
	} {
		if err := ValidateNotice(c.in); (err != nil) != c.bad {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// exportWithNotice exports the edge fixture of a dialect with a header that carries the notice.
func exportWithNotice(t testing.TB, dialect, notice string) string {
	t.Helper()
	e := edgeSQLite(t)
	if dialect == "mssql" {
		e = mssqlEdgeSQLite(t)
	}
	h := testHeader()
	h.Notice = notice
	var buf bytes.Buffer
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: dialect, DB: e.DB, Header: h}, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestNoticeIsPartOfTheHeaderOnly(t *testing.T) {
	notice := []byte(strings.TrimPrefix(hostileNoticeText(), "\ufeff"))
	for _, dialect := range []string{"mysql", "mssql"} {
		t.Run(dialect, func(t *testing.T) {
			plain := exportWithNotice(t, dialect, "")
			got := exportWithNotice(t, dialect, string(notice))
			if again := exportWithNotice(t, dialect, string(notice)); again != got {
				t.Fatal("two exports are not byte for byte the same")
			}
			var fn func(string) string
			if dialect == "mssql" {
				fn = mssqlNoticeLine
			}
			block := append([]string{"-- "}, NoticeLines("-- ", string(notice), fn)...)
			block[0] = "--"
			want := strings.Join(block, "\n") + "\n"
			if strings.Count(got, want) != 1 {
				t.Fatalf("the notice block is not in the script exactly once:\n%s", want)
			}
			if strings.Replace(got, want, "", 1) != plain {
				t.Fatal("the notice changed more than its own lines")
			}
			gl := strings.Split(got, "\n")
			pl := strings.Split(plain, "\n")
			// no line of the script outside comments holds a byte the notice brought: lines that are not comments
			// are exactly those of the plain script
			var a, b []string
			for _, l := range gl {
				if !strings.HasPrefix(strings.TrimPrefix(l, mssqlBOM), "--") {
					a = append(a, l)
				}
			}
			for _, l := range pl {
				if !strings.HasPrefix(strings.TrimPrefix(l, mssqlBOM), "--") {
					b = append(b, l)
				}
			}
			if strings.Join(a, "\n") != strings.Join(b, "\n") {
				t.Fatal("the notice added a line that is not a comment")
			}
		})
	}
}

func TestNoticeHostileRandomScriptsKeepEveryLineOurs(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, dialect := range []string{"mysql", "mssql"} {
		plain := exportWithNotice(t, dialect, "")
		for i := 0; i < 150; i++ {
			n := hostileNotice(rng)
			// WriteScript does not validate: it must be safe for any text, valid or not
			got := exportWithNoticeUnchecked(t, dialect, n)
			ps := strings.Split(plain, "\n")
			gs := strings.Split(got, "\n")
			var nonc []string
			for _, l := range gs {
				if !strings.HasPrefix(strings.TrimPrefix(l, mssqlBOM), "--") {
					nonc = append(nonc, l)
				}
			}
			var pnonc []string
			for _, l := range ps {
				if !strings.HasPrefix(strings.TrimPrefix(l, mssqlBOM), "--") {
					pnonc = append(pnonc, l)
				}
			}
			if strings.Join(nonc, "\n") != strings.Join(pnonc, "\n") {
				t.Fatalf("%s: notice %q added a line that is not a comment", dialect, n)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("%s: notice %q made the script invalid UTF-8", dialect, n)
			}
			for _, r := range strings.TrimPrefix(got, mssqlBOM) {
				if (r == '\r' || r == 0x85 || r == 0x2028 || r == 0x2029) && !strings.Contains(plain, string(r)) {
					t.Fatalf("%s: notice %q put %U in the script", dialect, n, r)
				}
			}
		}
	}
}

// exportWithNoticeUnchecked plans the edge fixture and calls WriteScript, which does not validate the notice.
func exportWithNoticeUnchecked(t testing.TB, dialect, notice string) string {
	t.Helper()
	e := edgeSQLite(t)
	if dialect == "mssql" {
		e = mssqlEdgeSQLite(t)
	}
	p, err := PlanScript(context.Background(), dialect, e.DB)
	if err != nil {
		t.Fatal(err)
	}
	h := testHeader()
	h.Notice = notice
	var buf bytes.Buffer
	if _, err := WriteScript(context.Background(), &buf, e.DB, p, h); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestExportRefusesABadNoticeAsAUsageError(t *testing.T) {
	for name, n := range map[string]string{"nul": "a\x00b", "utf8": "a\xffb", "size": strings.Repeat("x", MaxNoticeBytes+1)} {
		for _, dialect := range []string{"mysql", "mssql"} {
			e := sqliteWith(t, "CREATE TABLE t (a INTEGER)")
			h := testHeader()
			h.Notice = n
			_, err := ExportScript(context.Background(), ScriptRequest{Dialect: dialect, DB: e.DB, Header: h}, &bytes.Buffer{})
			var u *ScriptUsageError
			if !errors.As(err, &u) {
				t.Errorf("%s/%s: %v", dialect, name, err)
			}
		}
	}
}

func TestNoticeOfThreeLicenceShapes(t *testing.T) {
	notice := "Copyright (c) 2024 Example\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND.\n"
	got := NoticeLines("-- ", notice, nil)
	want := "-- Copyright (c) 2024 Example\n--\n-- Permission is hereby granted, free of charge, to any person obtaining a copy\n-- THE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND."
	if strings.Join(got, "\n") != want {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	// a notice of one dialect's script is also the same text in the other's (no $( in it)
	if strings.Join(NoticeLines("-- ", notice, mssqlNoticeLine), "\n") != want {
		t.Fatal("mssql differs for plain text")
	}
}
