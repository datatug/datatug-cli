package faithful

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// foldName is a name as MySQL compares column and index names: without case and without accents.
func foldName(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

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
	return b.String() + "_" + nameHash(orig)
}
