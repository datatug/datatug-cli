package faithful

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ScriptStats is what the pre-scan of a SQLite column learns for the script export: the statistics the
// live copy uses, plus the ones a script target needs (byte lengths, text that is not UTF-8, decimal
// digits, the precision of times). The statistics of the live copy are not changed.
type ScriptStats struct {
	*colStats
	MaxBytes      int
	MaxU16        int // the longest text in UTF-16 code units, the length SQL Server counts
	MaxBlob       int
	BadUTF8       int64 // text values that are not valid UTF-8
	NonFinite     int64 // infinite floats
	Denormal      int64 // floats below the smallest normal double, which SQL Server flushes to zero
	LowYear       int64 // dates and timestamps before year 1000
	GregorianGap  int64 // dates and timestamps from 1582-10-05 to 1582-10-14, which the Julian-to-Gregorian switch skips (Oracle)
	GapExample    string
	MaxFsp        int // most decimals of a timestamp or time text
	MaxIntDigits  int // most integer digits of a value that is a decimal number
	MaxFracDigits int
	Empty         int64 // empty texts
	EmptyBlobs    int64 // empty blobs
	// RealExample is the first REAL value the column holds, as its shortest decimal ("" when it holds none).
	RealExample string
	// Decimal texts: how many are not in canonical spelling (with the first), and how many canonical ones have each
	// number of decimals (with the first of each), to count what a DECIMAL of a given scale spells differently.
	textNonCanon     int64
	nonCanonExample  string
	canonFrac        map[int]int64
	canonFracExample map[int]string
	// Decimal texts an engine that keeps no trailing zeros (Oracle's NUMBER) spells differently, with the first.
	trimmed        int64
	trimmedExample string
}

func newScriptStats(d declInfo) *ScriptStats { return &ScriptStats{colStats: newStats(d)} }

func (s *ScriptStats) digits(text string) {
	m := reDecParts.FindStringSubmatch(text)
	s.MaxIntDigits = max(s.MaxIntDigits, len(strings.TrimLeft(strings.TrimPrefix(m[1], "-"), "0")))
	s.MaxFracDigits = max(s.MaxFracDigits, len(m[2]))
}

func (s *ScriptStats) add(v any) {
	s.colStats.add(v)
	switch x := v.(type) {
	case int64:
		text := strconv.FormatInt(x, 10)
		s.digits(text)
		s.textLength(text) // an INTEGER in a column carried as text or bytes is written as its decimal text
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			s.NonFinite++
			return
		}
		if x != 0 && math.Abs(x) < 2.2250738585072014e-308 {
			s.Denormal++
		}
		s.digits(strconv.FormatFloat(x, 'f', -1, 64))
		if s.RealExample == "" {
			s.RealExample = strconv.FormatFloat(x, 'f', -1, 64)
		}
	case []byte:
		s.MaxBlob = max(s.MaxBlob, len(x))
		if len(x) == 0 {
			s.EmptyBlobs++
		}
	case string:
		s.MaxBytes = max(s.MaxBytes, len(x))
		s.MaxU16 = max(s.MaxU16, utf16Len(x))
		if x == "" {
			s.Empty++
		}
		if !utf8.ValidString(x) {
			s.BadUTF8++
		}
		switch {
		case reDate.MatchString(x):
			if x < "1000" {
				s.LowYear++
			}
			s.gap(x)
		case reTS.MatchString(x):
			if x < "1000" {
				s.LowYear++
			}
			s.gap(x)
			s.fsp(x)
		case reTime.MatchString(x):
			s.fsp(x)
		}
		if reDec.MatchString(x) {
			s.digits(x)
			s.spelling(x)
		}
	}
}

// gap counts a date or timestamp text in the days 1582-10-05 to 1582-10-14 that the Gregorian reform skipped: an engine
// with the historical calendar (Oracle) stores it ten days later.
func (s *ScriptStats) gap(x string) {
	if len(x) >= 10 && x[:10] >= "1582-10-05" && x[:10] <= "1582-10-14" && x[:7] == "1582-10" {
		if s.GregorianGap == 0 {
			s.GapExample = x
		}
		s.GregorianGap++
	}
}

// textLength notes the length of the text a value of another storage class is written as, when its column is
// carried as text or bytes: a bounded type must hold it.
func (s *ScriptStats) textLength(text string) {
	s.maxLen = max(s.maxLen, utf8.RuneCountInString(text))
	s.MaxBytes = max(s.MaxBytes, len(text))
	s.MaxU16 = max(s.MaxU16, utf16Len(text))
}

// utf16Len is the length of a text in UTF-16 code units: a character outside the Basic Multilingual Plane is two.
func utf16Len(x string) int {
	n := 0
	for _, r := range x {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// trimmedDecimalText is a decimal text as a type that keeps no trailing zeros spells it: the canonical spelling
// without the zeros after the point (1.50 is 1.5, 0.0000 is 0).
func trimmedDecimalText(x string) string {
	c := canonicalDecimalText(x)
	if strings.Contains(c, ".") {
		c = strings.TrimRight(strings.TrimRight(c, "0"), ".")
	}
	return c
}

func (s *ScriptStats) spelling(x string) {
	if t := trimmedDecimalText(x); t != x {
		s.trimmed++
		if s.trimmedExample == "" {
			s.trimmedExample = x + " -> " + t
		}
	}
	if canon := canonicalDecimalText(x); canon != x {
		s.textNonCanon++
		if s.nonCanonExample == "" {
			s.nonCanonExample = x + " -> " + canon
		}
		return
	}
	_, frac, _ := strings.Cut(x, ".")
	if s.canonFrac == nil {
		s.canonFrac, s.canonFracExample = map[int]int64{}, map[int]string{}
	}
	s.canonFrac[len(frac)]++
	if _, ok := s.canonFracExample[len(frac)]; !ok {
		s.canonFracExample[len(frac)] = x
	}
}

func (s *ScriptStats) fsp(x string) {
	if i := strings.IndexByte(x, '.'); i >= 0 {
		s.MaxFsp = max(s.MaxFsp, len(x)-i-1)
	}
}

// scanScriptStats reads every column of every table once, to learn what each holds.
func scanScriptStats(ctx context.Context, db Queryer, cat *Catalog) (map[string][]*ScriptStats, error) {
	out := map[string][]*ScriptStats{}
	for _, t := range cat.Tables {
		sts := make([]*ScriptStats, len(t.Columns))
		names := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			sts[i] = newScriptStats(parseDecl(c.SourceType))
			names[i] = c.Name
		}
		out[t.Name] = sts
		// After the values and the empty-blob flags comes, per column, the text SQLite makes of a REAL (NULL for any
		// other value): the length of what a column carried as text or bytes will hold.
		exprs := []string{sqliteSelectList(names)}
		for _, n := range names {
			exprs = append(exprs, "CASE WHEN typeof("+quoteIdent(n)+") = 'real' THEN CAST("+quoteIdent(n)+" AS TEXT) END")
		}
		rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteIdent(t.Name))
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", t.Name, err)
		}
		vals, ptrs := sqliteRowBuffer(len(names))
		realText := make([]any, len(names))
		for i := range realText {
			ptrs = append(ptrs, &realText[i])
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("table %q: %w", t.Name, err)
			}
			fixEmptyBlobs(vals)
			for i := range names {
				sts[i].add(vals[i])
				if txt, ok := realText[i].(string); ok {
					sts[i].textLength(txt)
				}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// realAsText is the SQLite expression that reads a column so a REAL arrives as the text SQLite itself makes of it
// (CAST(x AS TEXT): ".0" kept on a whole number, an exponent as "1.0e+21", "Inf"; how many digits it prints is the
// SQLite version's), not as a float64 that Go would spell 1, 1e+21, +Inf and -0. Any other value arrives as it is (the unary plus
// has the value of its operand and no declared type, so the driver does not turn a DATE or DATETIME text into a time).
func realAsText(name string) string {
	q := quoteIdent(name)
	return "CASE WHEN typeof(" + q + ") = 'real' THEN CAST(" + q + " AS TEXT) ELSE +" + q + " END"
}

// scriptSelectList reads the columns of a table value for value, as sqliteSelectList does, except that a column
// the script carries as text or bytes gives a REAL as SQLite's own text. It ends with the empty-blob flags.
func scriptSelectList(t *ScriptTable) string {
	exprs := make([]string, 0, 2*len(t.Columns))
	for _, c := range t.Columns {
		if c.Choice.Family == "text" || c.Choice.Family == "bytes" {
			exprs = append(exprs, realAsText(c.Src.Name))
		} else {
			exprs = append(exprs, "+"+quoteIdent(c.Src.Name))
		}
	}
	for _, c := range t.Columns {
		q := quoteIdent(c.Src.Name)
		exprs = append(exprs, "(typeof("+q+") = 'blob' AND length("+q+") = 0)")
	}
	return strings.Join(exprs, ", ")
}
