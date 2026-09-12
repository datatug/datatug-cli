package accesspolicies

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestCanonicalQueryID(t *testing.T) {
	const nfc, nfd = "café", "café"
	sameQuery := [][]string{
		{"revenue", "Revenue", "REVENUE", "rEvEnUe"},
		{nfc, nfd, "CAFÉ", "CAFÉ"},
		{"reports/revenue", "Reports/REVENUE"},
		{"straße", "STRASSE", "strasse"},
		{"k", "K", "K"}, // the Kelvin sign folds to k
	}
	for _, group := range sameQuery {
		want := CanonicalQueryID(group[0])
		for _, id := range group {
			if got := CanonicalQueryID(id); got != want {
				t.Errorf("CanonicalQueryID(%q) = %q, want %q (the same as %q)", id, got, want, group[0])
			}
		}
		if !norm.NFC.IsNormalString(want) {
			t.Errorf("CanonicalQueryID(%q) = %q is not NFC", group[0], want)
		}
	}
	for _, pair := range [][2]string{{"revenue", "revenues"}, {nfc, "cafe"}, {"reports/revenue", "reports-revenue"}} {
		if CanonicalQueryID(pair[0]) == CanonicalQueryID(pair[1]) {
			t.Errorf("%q and %q must stay different queries", pair[0], pair[1])
		}
	}
}
