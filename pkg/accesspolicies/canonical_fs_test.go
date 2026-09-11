package accesspolicies

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

// FSProbeDirEnv names a directory on a real file system to probe: for every
// rune the file system accepts in a name, the probe creates that file and
// asks the file system which other spellings resolve to it. A spelling the
// file system resolves to the same file whose CanonicalQueryID differs is a
// bypass - authorization would treat them as two queries while the store
// writes one file.
//
// It is skipped unless the variable is set, because it creates and removes
// some 150,000 files. Point it at a directory on each file system that
// matters:
//
//	DATATUG_FS_PROBE_DIR=/tmp/probe-apfs go test -run TestCanonicalQueryID_AgreesWithTheFileSystem ./pkg/accesspolicies/
//	# and again at a mounted HFS+ or case-insensitive volume
const FSProbeDirEnv = "DATATUG_FS_PROBE_DIR"

// TestCanonicalQueryID_AgreesWithTheFileSystem probes the file system at
// $DATATUG_FS_PROBE_DIR (see FSProbeDirEnv). It fails on any spelling the
// file system merges and the canonical form splits; the other direction -
// the canonical form merging what the file system keeps apart - is the safe
// one and is only counted.
func TestCanonicalQueryID_AgreesWithTheFileSystem(t *testing.T) {
	dir := os.Getenv(FSProbeDirEnv)
	if dir == "" {
		t.Skipf("%s is not set", FSProbeDirEnv)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(dir, "Control")
	if err := os.WriteFile(control, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Lstat(filepath.Join(dir, "cONTROL"))
	caseInsensitive := err == nil
	if err := os.Remove(control); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: case-insensitive: %v", dir, caseInsensitive)

	fold := cases.Fold()
	upper, lower, title := cases.Upper(language.Und), cases.Lower(language.Und), cases.Title(language.Und)
	probed, merged, split := 0, 0, 0
	for r := rune(0x20); r <= 0x2FFFF; r++ {
		if !utf8.ValidRune(r) || r == '/' {
			continue
		}
		name := "q" + string(r) + "q"
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, syscall.EILSEQ) || errors.Is(err, syscall.EINVAL) || errors.Is(err, os.ErrExist) {
				continue // the file system refuses the name outright
			}
			t.Fatalf("create %q: %v", name, err)
		}
		_ = file.Close()
		probed++
		canonical := CanonicalQueryID(name)
		for _, variant := range fileSystemVariants(r, name, fold, upper, lower, title) {
			if variant == name || strings.ContainsRune(variant, '/') || strings.ContainsRune(variant, 0) {
				continue
			}
			_, err := os.Lstat(filepath.Join(dir, variant))
			switch sameFile := err == nil; {
			case sameFile && CanonicalQueryID(variant) != canonical:
				if split++; split <= 20 {
					t.Errorf("%s resolves %q to the file %q, but they canonicalize to %q and %q",
						dir, variant, name, CanonicalQueryID(variant), canonical)
				}
			case !sameFile && CanonicalQueryID(variant) == canonical:
				merged++
			}
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatalf("remove %q: %v", name, err)
		}
	}
	t.Logf("probed %d names; %d spellings the file system merges and the canonical form splits (must be 0); "+
		"%d the canonical form merges and the file system keeps apart (safe)", probed, split, merged)
	if split > 0 {
		t.Errorf("%d file-system-equal spellings canonicalize apart", split)
	}
}

// fileSystemVariants are the other spellings of name worth asking the file
// system about: every casing and normalization of it, and name without r at
// all (a file system that ignores r).
func fileSystemVariants(r rune, name string, fold, upper, lower, title cases.Caser) []string {
	variants := map[string]bool{
		"qq":                                   true,
		strings.ToUpper(name):                  true,
		strings.ToLower(name):                  true,
		strings.ToTitle(name):                  true,
		upper.String(name):                     true,
		lower.String(name):                     true,
		title.String(name):                     true,
		fold.String(name):                      true,
		norm.NFD.String(name):                  true,
		norm.NFC.String(name):                  true,
		CanonicalQueryID(name):                 true,
		"q" + string(unicode.ToUpper(r)) + "q": true,
		"q" + string(unicode.ToLower(r)) + "q": true,
		"q" + string(unicode.ToTitle(r)) + "q": true,
	}
	for orbit := unicode.SimpleFold(r); orbit != r; orbit = unicode.SimpleFold(orbit) {
		variants["q"+string(orbit)+"q"] = true
	}
	out := make([]string, 0, len(variants))
	for variant := range variants {
		out = append(out, variant)
	}
	return out
}
