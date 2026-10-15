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
	decBare                             int64 // ... that a numeric without a scale writes differently
	decBareExample                      string
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
			if _, err := time.Parse("2006-01-02", x); err == nil && yearInPG(x) {
				s.dateOK++
			}
		case reTS.MatchString(x):
			if _, err := time.Parse("2006-01-02 15:04:05.999999", x); err == nil && yearInPG(x) {
				s.tsOK++
			}
		case reTSTZ.MatchString(x):
			// A leap second, an hour 24 or a day that is not there is a text PostgreSQL would refuse or write as another instant.
			if _, err := time.Parse("2006-01-02 15:04:05.999999", x[:strings.LastIndexAny(x, "+-")]); err == nil && yearInPG(x) {
				s.tstzAny++
				if reTSTZUTC.MatchString(x) {
					s.tstzOK++
				}
			}
		case reTime.MatchString(x):
			if _, err := time.Parse("15:04:05.999999", x); err == nil {
				s.timeOK++
			}
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
	if isText && text != canon {
		// numeric without a scale spells the number this way (no padding to a scale).
		s.decBare++
		if s.decBareExample == "" {
			s.decBareExample = text + " -> " + canon
		}
	}
	scaled := s.decl.p >= 0
	if scaled && (len(intDigits) > s.decl.p-s.decl.s || len(frac) > s.decl.s) {
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
	if spelled != text {
		s.decNormalized++
		if s.decNormalExample == "" {
			s.decNormalExample = text + " -> " + spelled
		}
	}
}

// spellings is how many text values the numeric type that the column gets writes differently, and the
// first one: numeric(p,s) pads to the scale when every value fits it; a plain numeric, which a column
// whose values do not all fit becomes, does not pad.
func (s *colStats) spellings() (int64, string) {
	if s.decFit == s.decOK {
		return s.decNormalized, s.decNormalExample
	}
	return s.decBare, s.decBareExample
}

// yearInPG is false for year 0000, which Go parses and PostgreSQL refuses (it has no year 0).
func yearInPG(x string) bool { return !strings.HasPrefix(x, "0000") }

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
