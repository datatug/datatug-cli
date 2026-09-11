package accesspolicies

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

// mapRunes maps every rune of s through mapping, the way a file system's
// own per-code-point case table does.
func mapRunes(s string, mapping func(rune) rune) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(mapping(r))
	}
	return b.String()
}

// everyScalarValue calls visit for every Unicode scalar value.
func everyScalarValue(visit func(r rune)) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue // a surrogate half is not a scalar value
		}
		visit(r)
	}
}

// TestCanonicalQueryID_IsAFixedPointForEveryScalarValue: the canonical form
// must be idempotent, or the resource and the policy's own view of the same
// id could canonicalize to two different strings and split one file's
// decisions.
func TestCanonicalQueryID_IsAFixedPointForEveryScalarValue(t *testing.T) {
	unstable, notFixed := 0, 0
	everyScalarValue(func(r rune) {
		id := string(r)
		canonical := CanonicalQueryID(id)
		if canonical == unstableCanonicalID && r != '�' {
			if unstable++; unstable <= 10 {
				t.Errorf("CanonicalQueryID(%U) did not reach a fixed point within %d rounds", r, canonicalRounds)
			}
			return
		}
		if again := CanonicalQueryID(canonical); again != canonical {
			if notFixed++; notFixed <= 10 {
				t.Errorf("CanonicalQueryID(%U) = %q, but canonicalizing that again gives %q", r, canonical, again)
			}
		}
	})
	if unstable > 0 || notFixed > 0 {
		t.Errorf("%d scalar values never converged and %d are not fixed points", unstable, notFixed)
	}
}

// canonicalProbeRunes are the runes the randomized strings are built from:
// everything known to make case folding, normalization or a file system's
// own comparison interesting, plus random scalar values.
func canonicalProbeRunes() []rune {
	runes := []rune("aAiIqQsSzZ0-_.")
	runes = append(runes, 'ı', 'İ', 'ß', 'ẞ', 'ſ', 'K', 'Å', 'Ω', 'ς', 'Σ', 'σ', 'ǅ', 'Ǆ', 'ǆ')
	runes = append(runes, 'Ꭰ', 'ꭰ', 'Ᏸ', 'ᏸ', 'Ᏺ', 'ᏺ')                     // Cherokee, whose folding maps up, not down
	runes = append(runes, '̀', '́', '̇', '̧', 'ͅ')                          // combining marks, one of them a folding one
	runes = append(runes, '\u200C', '\u200D', '\u00AD', '\uFEFF', '\u034F') // default-ignorable
	runes = append(runes, '\uFE0F', 0x1F600, 0x10400, 0x10428, 0x1E921)     // a variation selector, emoji, cased supplementary planes
	runes = append(runes, 'é', 'e', 'ẛ', 'ﬁ', 'ﬅ', 'ŉ', 'ǰ', 'ΐ', 'ᾀ')      // precomposed, ligatures, multi-rune folds
	runes = append(runes, '한', 'ᄒ', 'ᅡ', 'ᆫ', '中', 'ѐ', 'Ѐ')                // Hangul jamo and syllables, CJK, Cyrillic
	return runes
}

// TestCanonicalQueryID_IsAFixedPointForRandomStrings is the multi-rune half
// of the idempotence check: combining marks reorder, folding expands, and
// the result still has to be a fixed point.
func TestCanonicalQueryID_IsAFixedPointForRandomStrings(t *testing.T) {
	pool := canonicalProbeRunes()
	random := rand.New(rand.NewSource(20260911))
	for i := 0; i < 200000; i++ {
		var b strings.Builder
		for n := 1 + random.Intn(6); n > 0; n-- {
			if random.Intn(8) == 0 {
				b.WriteRune(rune(random.Intn(unicode.MaxRune + 1)))
				continue
			}
			b.WriteRune(pool[random.Intn(len(pool))])
		}
		id := b.String()
		canonical := CanonicalQueryID(id)
		if canonical == unstableCanonicalID && !strings.Contains(id, unstableCanonicalID) {
			t.Fatalf("CanonicalQueryID(%q) did not reach a fixed point within %d rounds", id, canonicalRounds)
		}
		if again := CanonicalQueryID(canonical); again != canonical {
			t.Fatalf("CanonicalQueryID(%q) = %q, but canonicalizing that again gives %q", id, canonical, again)
		}
	}
}

// TestCanonicalQueryID_IsCoarserThanEveryFileSystemTable models each file
// system's own name comparison and requires the canonical form to merge at
// least as much:
//
//   - NTFS and exFAT compare through an uppercase table (simple upper
//     casing, which merges "ı" with "I");
//   - HFS+ compares through a lower-case table after decomposition;
//   - APFS and ext4's casefold compare after canonical decomposition and
//     full case folding;
//   - all of them relate the members of a simple-case-folding orbit.
//
// Splitting any of those pairs would be a bypass: the two spellings are one
// file, so a rule on one must decide the other.
func TestCanonicalQueryID_IsCoarserThanEveryFileSystemTable(t *testing.T) {
	fold := cases.Fold()
	upper, lower, title := cases.Upper(language.Und), cases.Lower(language.Und), cases.Title(language.Und)
	split := 0
	report := func(mapping string, r, other rune, s, variant string) {
		if split++; split <= 20 {
			t.Errorf("%s maps %U to %U, which the file system reads as one name, but %q and %q canonicalize to %q and %q",
				mapping, r, other, s, variant, CanonicalQueryID(s), CanonicalQueryID(variant))
		}
	}
	everyScalarValue(func(r rune) {
		s := "q" + string(r) + "q"
		canonical := CanonicalQueryID(s)
		same := func(mapping string, other rune, variant string) {
			if CanonicalQueryID(variant) != canonical {
				report(mapping, r, other, s, variant)
			}
		}
		// NTFS and exFAT map each UTF-16 unit through their uppercase
		// table and never normalize, so the mapping applies to the name as
		// written.
		for mapping, mapped := range map[string]rune{
			"the NTFS and exFAT uppercase table (simple upper case)": unicode.ToUpper(r),
			"the same tables' title mapping (simple title case)":     unicode.ToTitle(r),
		} {
			if mapped != r {
				same(mapping, mapped, "q"+string(mapped)+"q")
			}
		}
		// HFS+ stores and compares decomposed names, so its lower-case
		// table is applied to the decomposition, never to a precomposed
		// character: "İ" is "I" plus a combining dot there, which is not
		// "i" on it or on any other file system here.
		if lowered := mapRunes(norm.NFD.String(s), unicode.ToLower); lowered != s {
			same("the HFS+ lower-case table, applied to the decomposed name", r, lowered)
		}
		for orbit := unicode.SimpleFold(r); orbit != r; orbit = unicode.SimpleFold(orbit) {
			same("simple case folding", orbit, "q"+string(orbit)+"q")
		}
		if folded := fold.String(s); folded != s {
			same("full case folding (APFS, ext4 casefold)", r, folded)
		}
		for _, form := range []norm.Form{norm.NFD, norm.NFC} {
			if normalized := form.String(s); normalized != s {
				same("canonical normalization (APFS, HFS+, ext4 casefold)", r, normalized)
			}
		}
		if isInvisibleRune(r) {
			same("HFS+ ignores the default-ignorable code points", r, "qq")
		}
		// The locale-independent casers x/text applies to whole strings.
		for name, caser := range map[string]cases.Caser{"upper": upper, "lower": lower, "title": title} {
			if cased := caser.String(s); cased != s {
				same("full "+name+" casing", r, cased)
			}
		}
	})
	if split > 0 {
		t.Errorf("%d file-system-equal spellings canonicalize apart", split)
	}
}

// TestCanonicalQueryID_KnownFamilies pins the families the review and the
// verify pass found, and the ones a reader will look for.
func TestCanonicalQueryID_KnownFamilies(t *testing.T) {
	const nfc, nfd = "café", "café"
	sameQuery := [][]string{
		{"revenue", "Revenue", "REVENUE", "rEvEnUe"},
		{nfc, nfd, "CAFÉ", "CAFÉ"},
		{"reports/revenue", "Reports/REVENUE"},
		{"straße", "STRASSE", "strasse", "STRAẞE", "straẞe"},
		{"k", "K", "K"},             // the Kelvin sign folds to k
		{"Ω", "ω", "Ω"},             // the Ohm sign
		{"Å", "å", "Å", "Å", "å"}, // the Angstrom sign and both decompositions
		{"Ꭰ-report", "ꭰ-report"},    // Cherokee: folding maps up, casing maps down
		{"ᏸᏺ", "ᏸᏺ"},
		{"ınvoices", "invoices", "INVOICES", "Invoices"}, // NTFS upcases "ı" to "I"
		{"İstanbul", "İstanbul", "i̇stanbul"},           // a dotted capital I decomposes
		{"σ", "ς", "Σ"},
		{"rev\u200Cenue", "rev\u200Denue", "rev\uFEFFenue", "rev\u00ADenue", "revenue"}, // HFS+ ignores these
	}
	for _, family := range sameQuery {
		want := CanonicalQueryID(family[0])
		for _, id := range family {
			if got := CanonicalQueryID(id); got != want {
				t.Errorf("CanonicalQueryID(%q) = %q, want %q (the same as %q)", id, got, want, family[0])
			}
		}
		if !norm.NFC.IsNormalString(want) {
			t.Errorf("CanonicalQueryID(%q) = %q is not NFC", family[0], want)
		}
		if again := CanonicalQueryID(want); again != want {
			t.Errorf("CanonicalQueryID(%q) = %q is not a fixed point: %q", family[0], want, again)
		}
	}
	for _, pair := range [][2]string{
		{"revenue", "revenues"},
		{nfc, "cafe"},
		{"reports/revenue", "reports-revenue"},
		{"İstanbul", "istanbul"}, // a dotted capital I is not a plain i on any of these file systems
		{"Ꭰ-report", "Ꭱ-report"},
	} {
		if CanonicalQueryID(pair[0]) == CanonicalQueryID(pair[1]) {
			t.Errorf("%q and %q must stay different queries", pair[0], pair[1])
		}
	}
}

// An id that somehow did not converge shares one decision with every other
// such id, rather than a spelling of its own.
func TestCanonicalQueryID_UnstableIsItselfAFixedPoint(t *testing.T) {
	if got := CanonicalQueryID(unstableCanonicalID); got != unstableCanonicalID {
		t.Errorf("CanonicalQueryID(%q) = %q, want it unchanged", unstableCanonicalID, got)
	}
}
