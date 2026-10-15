package faithful

import (
	"strings"
	"unicode/utf8"
)

// jsonbUnchanged tells whether PostgreSQL's jsonb writes the json text s back exactly as it is. It
// recognises the one spelling jsonb prints: a single space after every comma and colon, no other
// white space, object keys in jsonb's order (shorter first, then byte order) and without
// duplicates, strings escaped the way jsonb escapes them, numbers without exponent, leading plus,
// negative zero or leading zeros. A text that is not in that spelling may or may not change; this
// says false for it, and the column is carried as json, which keeps every byte.
//
// The recognizer is checked against a real server in the PostgreSQL journey tests.
func jsonbUnchanged(s string) bool {
	p := &jsonbScan{s: s}
	if !p.value() {
		return false
	}
	return p.i == len(p.s)
}

type jsonbScan struct {
	s string
	i int
}

func (p *jsonbScan) lit(w string) bool {
	if strings.HasPrefix(p.s[p.i:], w) {
		p.i += len(w)
		return true
	}
	return false
}

func (p *jsonbScan) value() bool {
	if p.i >= len(p.s) {
		return false
	}
	switch p.s[p.i] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		_, ok := p.str()
		return ok
	case 't':
		return p.lit("true")
	case 'f':
		return p.lit("false")
	case 'n':
		return p.lit("null")
	}
	return p.number()
}

func (p *jsonbScan) array() bool {
	p.i++ // [
	if p.lit("]") {
		return true
	}
	for {
		if !p.value() {
			return false
		}
		if p.lit("]") {
			return true
		}
		if !p.lit(", ") {
			return false
		}
	}
}

// jsonbKeyLess is jsonb's key order: shorter keys first, equal lengths by bytes.
func jsonbKeyLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (p *jsonbScan) object() bool {
	p.i++ // {
	if p.lit("}") {
		return true
	}
	prev, first := "", true
	for {
		key, ok := p.str()
		if !ok {
			return false
		}
		if !first && !jsonbKeyLess(prev, key) {
			return false // out of order, or a duplicate
		}
		prev, first = key, false
		if !p.lit(": ") || !p.value() {
			return false
		}
		if p.lit("}") {
			return true
		}
		if !p.lit(", ") {
			return false
		}
	}
}

// str reads a string in jsonb's spelling and returns its decoded text.
func (p *jsonbScan) str() (string, bool) {
	if p.i >= len(p.s) || p.s[p.i] != '"' {
		return "", false
	}
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), true
		case c == '\\':
			if p.i+1 >= len(p.s) {
				return "", false
			}
			e := p.s[p.i+1]
			switch e {
			case '"', '\\':
				b.WriteByte(e)
				p.i += 2
			case 'b':
				b.WriteByte('\b')
				p.i += 2
			case 'f':
				b.WriteByte('\f')
				p.i += 2
			case 'n':
				b.WriteByte('\n')
				p.i += 2
			case 'r':
				b.WriteByte('\r')
				p.i += 2
			case 't':
				b.WriteByte('\t')
				p.i += 2
			case 'u':
				// jsonb writes \u00xx, in lower case, for the other control characters only.
				if p.i+6 > len(p.s) || p.s[p.i+2:p.i+4] != "00" {
					return "", false
				}
				h := p.s[p.i+4 : p.i+6]
				v := 0
				for _, r := range h {
					switch {
					case r >= '0' && r <= '9':
						v = v*16 + int(r-'0')
					case r >= 'a' && r <= 'f':
						v = v*16 + int(r-'a') + 10
					default:
						return "", false
					}
				}
				if v == 0 || v >= 0x20 || v == '\b' || v == '\f' || v == '\n' || v == '\r' || v == '\t' {
					return "", false
				}
				b.WriteByte(byte(v))
				p.i += 6
			default:
				return "", false
			}
		case c < 0x20:
			return "", false
		default:
			r, n := utf8.DecodeRuneInString(p.s[p.i:])
			if r == utf8.RuneError && n == 1 {
				return "", false
			}
			b.WriteString(p.s[p.i : p.i+n])
			p.i += n
		}
	}
	return "", false
}

func (p *jsonbScan) number() bool {
	start := p.i
	neg := false
	if p.i < len(p.s) && p.s[p.i] == '-' {
		neg = true
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	intStart := p.i
	if digits() == 0 {
		return false
	}
	if p.s[intStart] == '0' && p.i-intStart > 1 {
		return false
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return false
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		return false
	}
	return !neg || strings.Trim(p.s[start+1:p.i], "0.") != ""
}
