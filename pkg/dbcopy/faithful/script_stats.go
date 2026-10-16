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
	MaxBlob       int
	BadUTF8       int64 // text values that are not valid UTF-8
	NonFinite     int64 // infinite floats
	LowYear       int64 // dates and timestamps before year 1000
	MaxFsp        int   // most decimals of a timestamp or time text
	MaxIntDigits  int   // most integer digits of a value that is a decimal number
	MaxFracDigits int
	Empty         int64 // empty texts
	// Decimal texts: how many are not in canonical spelling (with the first), and how many canonical ones have each
	// number of decimals (with the first of each), to count what a DECIMAL of a given scale spells differently.
	textNonCanon     int64
	nonCanonExample  string
	canonFrac        map[int]int64
	canonFracExample map[int]string
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
		s.digits(strconv.FormatInt(x, 10))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			s.NonFinite++
			return
		}
		s.digits(strconv.FormatFloat(x, 'f', -1, 64))
	case []byte:
		s.MaxBlob = max(s.MaxBlob, len(x))
	case string:
		s.MaxBytes = max(s.MaxBytes, len(x))
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
		case reTS.MatchString(x):
			if x < "1000" {
				s.LowYear++
			}
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

func (s *ScriptStats) spelling(x string) {
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
		rows, err := db.QueryContext(ctx, "SELECT "+sqliteSelectList(names)+" FROM "+quoteIdent(t.Name))
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", t.Name, err)
		}
		vals, ptrs := sqliteRowBuffer(len(names))
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("table %q: %w", t.Name, err)
			}
			fixEmptyBlobs(vals)
			for i := range names {
				sts[i].add(vals[i])
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
