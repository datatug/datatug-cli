package dbmirror

import (
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEncodeIDExamples(t *testing.T) {
	for id, want := range map[string]string{
		"Vj3k9QpLm2XcR7tYa0Bd": "Vj3k9QpLm2XcR7tYa0Bd", // a generated id is kept
		"a.b-c_d@e+f=g,h":      "a.b-c_d@e+f=g,h",
		".hidden":              "~2Ehidden",
		"trailing.":            "trailing~2E",
		".":                    "~2E",
		"..":                   "~2E~2E",
		"a/b":                  "a~2Fb",
		`..\..\x`:              "~2E.~5C..~5Cx",
		"c:d":                  "c~3Ad",
		"with space ":          "with~20space~20",
		"tilde~7E":             "tilde~7E7E",
		"CON":                  "~43ON",
		"nul.json":             "~6Eul.json",
		"com1":                 "~63om1",
		"COMX":                 "COMX",
		"$records":             "~24records",
		"Zürich":               "Z~C3~BCrich",
		"ctl\x00\n":            "ctl~00~0A",
	} {
		if got := encodeID(id); got != want {
			t.Errorf("encodeID(%q) = %q, want %q", id, got, want)
		}
		if back, ok := decodeID(want); !ok || back != id {
			t.Errorf("decodeID(%q) = %q, %v", want, back, ok)
		}
	}
	for _, name := range []string{"", "~", "~4", "~4g", "~41", "~2e", ".hidden", "a~", "~2F~"} {
		if id, ok := decodeID(name); ok {
			t.Errorf("decodeID(%q) = %q, want not ok", name, id)
		}
	}
	if nameTooLong(strings.Repeat("a", maxNameBytes)) != "" || !strings.Contains(nameTooLong(strings.Repeat("a", maxNameBytes+1)), "256 bytes") {
		t.Error("nameTooLong")
	}
}

// TestEncodeIDProperties checks, over many random ids, that the encoding is
// reversible, never gives two ids one name, and gives only names that are one
// plain local path element on every supported system.
func TestEncodeIDProperties(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	alphabet := []byte("aA0.-_~/\\:*?\"<>| \t\x00\x7f$%@+=,é")
	names := map[string]string{}
	for range 20000 {
		n := 1 + random.Intn(12)
		id := make([]byte, n)
		for i := range id {
			if random.Intn(4) == 0 {
				id[i] = byte(random.Intn(256))
			} else {
				id[i] = alphabet[random.Intn(len(alphabet))]
			}
		}
		if random.Intn(10) == 0 {
			copy(id, []string{"CON", "aux", "Lpt9", "..", "."}[random.Intn(5)])
		}
		checkEncoded(t, string(id), names)
	}
}

func FuzzEncodeID(f *testing.F) {
	for _, seed := range []string{"a", ".", "..", "CON.txt", `a\b`, "~41", "é", "x."} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id string) {
		if id != "" {
			checkEncoded(t, id, map[string]string{})
		}
	})
}

func checkEncoded(t *testing.T, id string, names map[string]string) {
	t.Helper()
	name := encodeID(id)
	if back, ok := decodeID(name); !ok || back != id {
		t.Fatalf("decodeID(encodeID(%q)) = %q, %v", id, back, ok)
	}
	if other, taken := names[name]; taken && other != id {
		t.Fatalf("%q and %q both encode to %q", other, id, name)
	}
	names[name] = id
	if !utf8.ValidString(name) || !filepath.IsLocal(name) || strings.ContainsAny(name, `/\:*?"<>| `) || name != filepath.Base(name) ||
		strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.HasPrefix(name, "$") || windowsDevice(name) {
		t.Fatalf("encodeID(%q) = %q is not one safe path element", id, name)
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] >= 0x7f {
			t.Fatalf("encodeID(%q) = %q holds byte %#x", id, name, name[i])
		}
	}
}
