package accesspolicies

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CanonicalQueryID returns the spelling of a saved query's id that project
// write authorization compares: Unicode canonical caseless matching, that
// is full case folding between canonical decompositions, returned in NFC.
//
// A query id names files, and the file systems DataTug serves from resolve
// more than one spelling to the same file: APFS and HFS+ fold case and
// Unicode normalization ("Revenue", "revenue", NFC "café" and NFD "café"
// are one file), NTFS folds case. Comparing ids byte for byte would let a
// second spelling of a protected query past a deny rule written in the
// first. Two ids with the same CanonicalQueryID get the same decision, on
// every platform, because both the resource being written
// (ProjectQueryResource) and every query id a policy path names
// (AuthorizeWrite's view of a loaded policy) are compared in this form.
//
// Full case folding is coarser than any of those file systems, so it can
// only ever merge more spellings into one decision, never split one file's
// spellings apart: a deny on "straße" also covers "STRASSE".
func CanonicalQueryID(id string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFD.String(id)))
}
