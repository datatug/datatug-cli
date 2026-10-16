package faithful

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

func nameHash(orig string) string {
	sum := sha256.Sum256([]byte(orig))
	return hex.EncodeToString(sum[:4])
}

// safeName is the name under which a name the engine cannot store is written: the name with every
// character it cannot hold replaced by "_", cut to fit, and ended in a hash of the original, so two
// names never meet and the same name always gives the same result.
func safeName(d Dialect, orig string) string {
	limit := d.MaxIdentLen() - 9
	var b strings.Builder
	n := 0
	for _, r := range orig {
		if n >= limit {
			break
		}
		if d.StorableName(string(r)+"x") != "" {
			r = '_'
		}
		b.WriteRune(r)
		n++
	}
	out := b.String() + "_" + nameHash(orig)
	if d.StorableName(out) == "" {
		return out
	}
	// A character pair (a sqlcmd variable) or a length counted in units, which the test of one character at a
	// time cannot see: keep only plain letters and digits.
	var plain strings.Builder
	n = 0
	for _, r := range orig {
		if n >= limit/2 {
			break
		}
		if r > 0x7f || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			r = '_'
		}
		plain.WriteRune(r)
		n++
	}
	return plain.String() + "_" + nameHash(orig)
}
