package accesspolicies

import (
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CanonicalQueryID returns the spelling of a saved query's id that project
// write authorization compares. Two ids with the same CanonicalQueryID get
// the same decision, on every platform, because both the resource being
// written (ProjectQueryResource) and every query id a policy path names
// (AuthorizeWrite's view of a loaded policy) are compared in this form.
//
// A query id names files, and the file systems DataTug serves from resolve
// more than one spelling to the same file. The canonical form is coarser
// than every one of them, so it can only ever merge more spellings into one
// decision, never split one file's spellings apart:
//
//   - APFS and Linux ext4 with casefolding compare names after canonical
//     decomposition and full case folding, so NFC "café" and NFD "café",
//     and "straße" and "STRASSE", are one name;
//   - HFS+ decomposes and folds case through its own older table, and
//     ignores the default-ignorable code points (a zero-width joiner, a
//     soft hyphen, a byte-order mark) entirely;
//   - NTFS and exFAT compare through an uppercase table, which merges
//     spellings full case folding does not - "ı" with "i" and "I".
//
// So one round of the canonical form decomposes (NFD), applies full case
// folding, drops every default-ignorable code point, and maps each
// remaining rune to the smallest rune of its case class - the transitive
// closure of simple upper-, lower- and title-casing and simple case
// folding, which covers every one of those tables' mappings, whichever
// direction each maps in. Full folding merges what maps to more than one
// rune ("ß" to "ss"), and the case class merges what the folding tables
// leave apart ("ı" with "I", and the Cherokee letters, whose folding maps
// in the opposite direction to most scripts').
//
// The result is a fixed point: rounds are applied until the string stops
// changing, so C(C(x)) == C(x) for every string. Convergence is exhaustive
// over every Unicode scalar value in the package's own tests; a string that
// somehow did not converge within canonicalRounds becomes one fixed
// unstableCanonicalID, which is coarser still - such ids all share one
// decision - and never splits a file's spellings.
func CanonicalQueryID(id string) string {
	current := id
	for range canonicalRounds {
		next := canonicalRound(current)
		if next == current {
			return norm.NFC.String(current)
		}
		current = next
	}
	return unstableCanonicalID
}

// canonicalRounds bounds CanonicalQueryID's iteration. Two rounds settle
// every scalar value and every string the package's tests probe; the bound
// is only there so no input can spin.
const canonicalRounds = 8

// unstableCanonicalID is the canonical id of a string that did not reach a
// fixed point (see CanonicalQueryID). It is one itself: U+FFFD has no case
// mapping, no decomposition and is not ignorable.
const unstableCanonicalID = "�"

// canonicalRound is one round of the canonical form: decompose, fold case,
// drop the invisible code points, map each rune to its case class, and
// decompose again so the result is comparable rune for rune.
func canonicalRound(s string) string {
	folder := folderPool.Get().(cases.Caser)
	folded := folder.String(norm.NFD.String(s))
	folderPool.Put(folder)
	var b strings.Builder
	b.Grow(len(folded))
	for _, r := range folded {
		if isInvisibleRune(r) {
			continue
		}
		b.WriteRune(canonicalRune(r))
	}
	return norm.NFD.String(b.String())
}

// folderPool holds full-case-folding casers. A cases.Caser is stateful and
// must not be shared between goroutines, and building one per call shows up
// in the exhaustive tests, so they are pooled.
var folderPool = sync.Pool{New: func() any { return cases.Fold() }}

// isInvisibleRune reports whether r is a default-ignorable code point: the
// format characters (Cf: the soft hyphen, the zero-width space and joiners,
// the bidi and Mongolian format controls, the byte-order mark), the
// variation selectors, and the rest of the Unicode property
// (Other_Default_Ignorable_Code_Point: the Hangul fillers, the combining
// grapheme joiner). HFS+ ignores them when it compares names, so a policy
// that spells a query id with one names the same file as one that does not,
// and they must share a decision. querywrite.SegmentReason refuses them in
// a request outright.
func isInvisibleRune(r rune) bool {
	return unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		unicode.Is(unicode.Variation_Selector, r)
}

// canonicalRune maps r to the representative of its case class.
func canonicalRune(r rune) rune {
	if representative, ok := caseClasses()[r]; ok {
		return representative
	}
	return r
}

// caseClasses maps every rune that shares a case class with another rune to
// that class's representative (see buildCaseClasses).
var caseClasses = sync.OnceValue(buildCaseClasses)

// buildCaseClasses groups the runes every case mapping Go knows relates -
// simple upper case, lower case, title case and simple case folding, the
// mappings the file systems' own tables are built from - into classes, and
// picks one representative for each: the smallest rune that is a starter
// with no canonical decomposition, so a round's output is stable under the
// decomposition and folding the next round applies.
func buildCaseClasses() map[rune]rune {
	sets := map[rune]rune{} // union-find parent links
	var find func(rune) rune
	find = func(r rune) rune {
		parent, ok := sets[r]
		if !ok {
			sets[r] = r
			return r
		}
		if parent == r {
			return r
		}
		root := find(parent)
		sets[r] = root
		return root
	}
	union := func(a, b rune) {
		rootA, rootB := find(a), find(b)
		if rootA == rootB {
			return
		}
		if rootB < rootA {
			rootA, rootB = rootB, rootA
		}
		sets[rootB] = rootA
	}
	related := func(r rune) []rune {
		out := []rune{unicode.ToUpper(r), unicode.ToLower(r), unicode.ToTitle(r)}
		for orbit := unicode.SimpleFold(r); orbit != r; orbit = unicode.SimpleFold(orbit) {
			out = append(out, orbit)
		}
		return out
	}
	queue := make([]rune, 0, 4096)
	for _, caseRange := range unicode.CaseRanges {
		for r := rune(caseRange.Lo); r <= rune(caseRange.Hi); r++ {
			queue = append(queue, r)
		}
	}
	seen := make(map[rune]bool, len(queue)*2)
	for len(queue) > 0 {
		r := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[r] {
			continue
		}
		seen[r] = true
		for _, other := range related(r) {
			union(r, other)
			if !seen[other] {
				queue = append(queue, other)
			}
		}
	}
	members := map[rune][]rune{}
	for r := range sets {
		root := find(r)
		members[root] = append(members[root], r)
	}
	classes := map[rune]rune{}
	for _, class := range members {
		representative := classRepresentative(class)
		for _, r := range class {
			if r != representative {
				classes[r] = representative
			}
		}
	}
	return classes
}

// classRepresentative picks a class's canonical rune: the smallest starter
// with no canonical decomposition, else the smallest rune with none, else
// the smallest rune.
func classRepresentative(class []rune) rune {
	best, bestScore := class[0], -1
	for _, r := range class {
		score := 0
		if properties := norm.NFD.PropertiesString(string(r)); properties.Decomposition() == nil {
			score = 1
			if properties.CCC() == 0 {
				score = 2
			}
		}
		if score > bestScore || (score == bestScore && r < best) {
			best, bestScore = r, score
		}
	}
	return best
}
