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
	dateOK, tsOK, tstzOK, timeOK        int64 // text values in the canonical form of the type
	decOK                               int64 // values that are a well formed decimal number
	decFit                              int64 // ... that also fit the declared precision and scale
	jsonOK, uuidOK                      int64
}

var (
	reDate  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reTS    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?$`)
	reTSTZ  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?[+-]\d{2}(:\d{2})?$`)
	reTime  = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}(\.\d{0,5}[1-9])?$`)
	reDec   = regexp.MustCompile(`^-?(\d+(\.\d+)?|\.\d+)$`)
	reDecEx = regexp.MustCompile(`^-?(\d+)(?:\.(\d+))?$`)
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
		s.decimal(strconv.FormatInt(x, 10), false)
	case float64:
		s.reals++
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return
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
			s.tstzOK++
		case reTime.MatchString(x):
			s.timeOK++
		}
		if json.Valid([]byte(x)) {
			s.jsonOK++
		}
		if reUUID.MatchString(x) {
			s.uuidOK++
		}
		s.decimal(x, true)
	}
}

// decimal checks a value as a decimal number: text must have exactly the declared scale.
func (s *colStats) decimal(text string, isText bool) {
	if !reDec.MatchString(text) {
		return
	}
	s.decOK++
	if s.decl.p < 0 {
		s.decFit++
		return
	}
	m := reDecEx.FindStringSubmatch(text)
	if m == nil {
		return
	}
	intDigits := strings.TrimLeft(m[1], "0")
	frac := m[2]
	if isText {
		if len(frac) != s.decl.s || (len(m[1]) > 1 && m[1][0] == '0') {
			return
		}
	} else if len(frac) > s.decl.s {
		return
	}
	if len(intDigits) > s.decl.p-s.decl.s {
		return
	}
	s.decFit++
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
