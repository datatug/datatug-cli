package faithful

import (
	"regexp"
	"strconv"
	"strings"
)

// declClass is what a SQLite declared type means to this package.
type declClass int

const (
	declNone declClass = iota // no declared type
	declInt
	declFloat
	declDecimal // NUMERIC(p,s), DECIMAL(p,s), DECIMAL_TEXT(p,s)
	declBareNumeric
	declBool
	declDate
	declTimestamp
	declTimestampTZ
	declTime
	declChar // CHAR(n), VARCHAR(n) and friends
	declText
	declBlob
	declSpecial // JSON_TEXT, JSONB_TEXT, UUID_TEXT
	declUnknown
)

type declInfo struct {
	class   declClass
	base    string
	p, s    int // -1 when absent
	n       int // character length, 0 when absent
	width   int
	text    bool // DECIMAL_TEXT
	special string
}

var declArgs = regexp.MustCompile(`^\s*([^(]*?)\s*(?:\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?\s*$`)

// parseDecl classifies a SQLite declared type.
func parseDecl(decl string) declInfo {
	d := declInfo{p: -1, s: -1}
	m := declArgs.FindStringSubmatch(decl)
	base := strings.ToUpper(strings.TrimSpace(decl))
	var a1, a2 = -1, -1
	if m != nil {
		base = strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
		if m[2] != "" {
			a1, _ = strconv.Atoi(m[2])
		}
		if m[3] != "" {
			a2, _ = strconv.Atoi(m[3])
		}
	}
	d.base = base
	switch {
	case base == "":
		d.class = declNone
	case base == "JSON_TEXT" || base == "JSONB_TEXT" || base == "UUID_TEXT":
		d.class, d.special = declSpecial, strings.ToLower(strings.TrimSuffix(base, "_TEXT"))
	case base == "DECIMAL_TEXT":
		d.text = true
		if a1 >= 1 {
			d.class, d.p, d.s = declDecimal, a1, max(a2, 0)
		} else {
			d.class = declBareNumeric
		}
	case base == "NUMERIC" || base == "DECIMAL" || base == "NUMBER":
		if a1 >= 1 {
			d.class, d.p, d.s = declDecimal, a1, max(a2, 0)
		} else {
			d.class = declBareNumeric
		}
	case base == "MONEY":
		d.class = declBareNumeric
	case base == "BOOLEAN" || base == "BOOL":
		d.class = declBool
	case base == "DATE":
		d.class = declDate
	case base == "DATETIME" || base == "TIMESTAMP":
		d.class = declTimestamp
	case base == "TIMESTAMPTZ":
		d.class = declTimestampTZ
	case base == "TIME":
		d.class = declTime
	case base == "INT2":
		d.class, d.width = declInt, 2
	case base == "INT4":
		d.class, d.width = declInt, 4
	case strings.Contains(base, "INT"):
		d.class, d.width = declInt, 8
	case base == "FLOAT4":
		d.class, d.width = declFloat, 4
	case strings.Contains(base, "REAL"), strings.Contains(base, "FLOA"), strings.Contains(base, "DOUB"):
		d.class, d.width = declFloat, 8
	case strings.Contains(base, "CHAR") && a1 > 0:
		d.class, d.n = declChar, a1
	case strings.Contains(base, "CHAR"), strings.Contains(base, "CLOB"), strings.Contains(base, "TEXT"), base == "STRING":
		d.class = declText
	case strings.Contains(base, "BLOB"), base == "BYTEA", base == "BINARY", base == "VARBINARY":
		d.class = declBlob
	default:
		d.class = declUnknown
	}
	return d
}
