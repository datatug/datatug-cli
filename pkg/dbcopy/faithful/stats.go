package faithful

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// colStats is what a pre-scan of a SQLite column learns, to pick a PostgreSQL type that holds every
// value exactly.
type colStats struct {
	decl declInfo

	n, nulls, ints, reals, texts, blobs int64
	minInt, maxInt                      int64
	maxLen                              int
	textWithNUL                         int64
	nonBool                             int64 // integer values that are not 0 or 1
	dateOK, tsOK, tstzOK, timeOK        int64 // text values in the canonical form of the type (tstzOK: ends in +00)
	tstzAny                             int64 // timestamptz texts with any offset
	jsonbOK                             int64 // json texts that jsonb writes back unchanged
	f32Bad                              int64 // numbers that a 4-byte real does not hold exactly
	intInexact                          int64 // integers a double precision does not hold exactly
	decOK                               int64 // values that are a well formed decimal number
	decFit                              int64 // ... that also fit the declared precision and scale
	jsonOK, uuidOK                      int64
	decNormalized                       int64 // decimal texts that PostgreSQL writes differently (.5 -> 0.5)
	decNormalExample                    string
}

var (
	reDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reTS   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?$`)
	reTSTZ = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?[+-]\d{2}(:\d{2})?$`)
	// reTSTZUTC is the one spelling PostgreSQL writes back for an instant when the session is on UTC.
	reTSTZUTC  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?\+00$`)
	reTime     = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?$`)
	reDec      = regexp.MustCompile(`^-?(\d+(\.\d+)?|\.\d+)$`)
	reDecParts = regexp.MustCompile(`^(-?\d*)(?:\.(\d*))?$`)
)

func newStats(d declInfo) *colStats { return &colStats{decl: d} }

// add records one value as the SQLite driver returns it.
func (s *colStats) add(v any) {
	s.n++
	switch x := v.(type) {
	case nil:
		s.nulls++
	case int64:
		s.ints++
		if s.ints == 1 {
			s.minInt, s.maxInt = x, x
		}
		s.minInt, s.maxInt = min(s.minInt, x), max(s.maxInt, x)
		if x != 0 && x != 1 {
			s.nonBool++
		}
		if x > 1<<53 || x < -(1<<53) {
			s.intInexact++
		}
		if !f32Exact(float64(x)) {
			s.f32Bad++
		}
		s.decimal(strconv.FormatInt(x, 10), false)
	case float64:
		s.reals++
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return
		}
		if !f32Exact(x) {
			s.f32Bad++
		}
		s.decimal(strconv.FormatFloat(x, 'f', -1, 64), false)
	case []byte:
		s.blobs++
	case string:
		s.texts++
		if l := utf8.RuneCountInString(x); l > s.maxLen {
			s.maxLen = l
		}
		if strings.IndexByte(x, 0) >= 0 {
			s.textWithNUL++
		}
		switch {
		case reDate.MatchString(x):
			if _, err := time.Parse("2006-01-02", x); err == nil {
				s.dateOK++
			}
		case reTS.MatchString(x):
			if _, err := time.Parse("2006-01-02 15:04:05.999999", x); err == nil {
				s.tsOK++
			}
		case reTSTZ.MatchString(x):
			s.tstzAny++
			if reTSTZUTC.MatchString(x) {
				s.tstzOK++
			}
		case reTime.MatchString(x):
			s.timeOK++
		}
		if json.Valid([]byte(x)) {
			s.jsonOK++
			if jsonbUnchanged(x) {
				s.jsonbOK++
			}
		}
		if reUUID.MatchString(x) {
			s.uuidOK++
		}
		s.decimal(x, true)
	}
}

// decimal checks a value as a decimal number. For the declared precision and scale: a value fits when
// its integer digits fit and it has no more decimals than the scale (a rounding would change it). A
// text that fits but that numeric would spell differently (a leading dot or zeros, a negative zero,
// fewer decimals than the scale) is counted in decNormalized, with the first one as an example.
func (s *colStats) decimal(text string, isText bool) {
	if !reDec.MatchString(text) {
		return
	}
	s.decOK++
	canon := canonicalDecimalText(text)
	m := reDecParts.FindStringSubmatch(text)
	intDigits, frac := strings.TrimLeft(strings.TrimPrefix(m[1], "-"), "0"), m[2]
	scaled := s.decl.p >= 0
	if scaled && (len(intDigits) > s.decl.p-s.decl.s || len(frac) > s.decl.s) {
		if isText {
			s.noteSpelling(text, canon, text != canon)
		}
		return
	}
	s.decFit++
	if !isText {
		return
	}
	spelled := canon
	if scaled && s.decl.s > len(frac) {
		if !strings.Contains(spelled, ".") {
			spelled += "."
		}
		spelled += strings.Repeat("0", s.decl.s-len(frac))
	}
	s.noteSpelling(text, spelled, spelled != text)
}

func (s *colStats) noteSpelling(text, spelled string, differs bool) {
	if !differs {
		return
	}
	s.decNormalized++
	if s.decNormalExample == "" {
		s.decNormalExample = text + " -> " + spelled
	}
}

func (s *colStats) only(classes ...string) bool {
	has := map[string]int64{"int": s.ints, "real": s.reals, "text": s.texts, "blob": s.blobs}
	for k, v := range has {
		if v == 0 {
			continue
		}
		ok := false
		for _, c := range classes {
			if c == k {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (s *colStats) allNull() bool { return s.n == s.nulls }

var reDefNumber = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

var reUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// canonicalDecimalText writes a decimal text the way PostgreSQL's numeric does: no plus sign, one
// leading zero, no extra leading zeros, the digits after the point as they are.
func canonicalDecimalText(t string) string {
	sign := ""
	if strings.HasPrefix(t, "-") {
		sign, t = "-", t[1:]
	}
	intPart, frac, hasFrac := strings.Cut(t, ".")
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	out := intPart
	if hasFrac {
		out += "." + frac
	}
	if strings.Trim(out, "0.") == "" {
		sign = "" // PostgreSQL's numeric has no negative zero
	}
	return sign + out
}

// f32Exact tells whether a 4-byte real holds x as the decimal it is written with: the shortest
// decimal that identifies float32(x) is the shortest decimal that identifies x. That is the case for
// every value PostgreSQL's real prints (1.1, 16777216) and fails for a double that a real would
// change (0.30000000000000004, 16777217).
func f32Exact(x float64) bool {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return true
	}
	f := float32(x)
	if math.IsInf(float64(f), 0) {
		return false
	}
	return strconv.FormatFloat(float64(f), 'g', -1, 32) == strconv.FormatFloat(x, 'g', -1, 64)
}
