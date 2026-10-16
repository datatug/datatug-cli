package queryresults

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizePrompt(t *testing.T) {
	cases := []struct {
		name, in, want string
		truncated      bool
	}{
		{"plain", "show me orders", "show me orders", false},
		{"trimmed", " \n\t hello \r\n ", "hello", false},
		{"control characters dropped, newline and tab kept", "a\x00b\x07c\x1b[31m\rd\ne\tf\x7f", "abc[31md\ne\tf", false},
		{"invalid utf-8 replaced", "a\xffb", "a�b", false},
		{"format characters dropped", "a\u202eb\u2066c\u2069d\u200be\u200df\ufeffg\u2060h\u2028i\u2029j", "abcdefghij", false},
		{"empty", "", "", false},
		{"only controls", "\x00\x01", "", false},
		{"exactly the cap", strings.Repeat("a", MaxPromptBytes), strings.Repeat("a", MaxPromptBytes), false},
		{"one past the cap", strings.Repeat("a", MaxPromptBytes+1), strings.Repeat("a", MaxPromptBytes), true},
		// 3-byte runes: 8192 is not a multiple of 3, so the cut backs up to 8190
		{"multibyte boundary", strings.Repeat("€", MaxPromptBytes), strings.Repeat("€", MaxPromptBytes/3), true},
		{"cut leaves trailing space trimmed", strings.Repeat("a", MaxPromptBytes-1) + " b", strings.Repeat("a", MaxPromptBytes-1), true},
	}
	for _, tc := range cases {
		got, truncated := NormalizePrompt([]byte(tc.in))
		if got != tc.want || truncated != tc.truncated {
			t.Errorf("%s: got %d bytes truncated=%v, want %d bytes truncated=%v", tc.name, len(got), truncated, len(tc.want), tc.truncated)
		}
	}
	big := make([]byte, MaxPromptReadBytes+10)
	for i := range big {
		big[i] = 'x'
	}
	if got, truncated := NormalizePrompt(big); len(got) != MaxPromptBytes || !truncated {
		t.Errorf("read bound: %d %v", len(got), truncated)
	}
	// input past the read bound that cleans down to nothing is still flagged
	if got, truncated := NormalizePrompt(make([]byte, MaxPromptReadBytes+1)); got != "" || !truncated {
		t.Errorf("read bound controls: %q %v", got, truncated)
	}
}

func readMetaMembers(t *testing.T, store *Store, id string) map[string]json.RawMessage {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(store.Root(), id, MetaFile))
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(content, &members); err != nil {
		t.Fatal(err)
	}
	return members
}

func TestAnnotate(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "a1", time.Now(), "12345")
	before := readMetaMembers(t, store, "a1")
	sizeBefore := dirBytes(filepath.Join(store.Root(), "a1"))
	if _, has := before["prompt"]; has {
		t.Fatal("a fresh result has no prompt")
	}
	at := time.Date(2026, 10, 16, 12, 30, 0, 0, time.FixedZone("x", 3600))
	if err := store.Annotate("a1", "why <&> ☃", true, at); err != nil {
		t.Fatal(err)
	}
	after := readMetaMembers(t, store, "a1")
	var prompt Prompt
	if err := json.Unmarshal(after["prompt"], &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt != (Prompt{Text: "why <&> ☃", Truncated: true, At: "2026-10-16T11:30:00Z"}) {
		t.Fatalf("prompt %+v", prompt)
	}
	delete(after, "prompt")
	if len(after) != len(before) {
		t.Fatalf("members changed: %d vs %d", len(after), len(before))
	}
	for name, value := range before {
		if string(after[name]) != string(value) {
			t.Errorf("member %s changed", name)
		}
	}
	info, err := os.Lstat(filepath.Join(store.Root(), "a1", MetaFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("meta.json mode %v %v", info, err)
	}
	entries, _ := os.ReadDir(filepath.Join(store.Root(), "a1"))
	if len(entries) != 2 {
		t.Fatalf("temporary file left behind: %v", entries)
	}

	// the store's byte accounting counts the prompt with the rest of meta.json
	if sizeAfter := dirBytes(filepath.Join(store.Root(), "a1")); sizeAfter <= sizeBefore {
		t.Fatalf("dirBytes %d -> %d", sizeBefore, sizeAfter)
	}

	// it is still a complete result, and Load reads the prompt
	if _, err := store.Locate("a1"); err != nil {
		t.Fatal(err)
	}

	// idempotent overwrite
	if err := store.Annotate("a1", "second", false, at); err != nil {
		t.Fatal(err)
	}
	if err := store.Annotate("a1", "second", false, at); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readMetaMembers(t, store, "a1")["prompt"], &prompt); err != nil || prompt.Text != "second" || prompt.Truncated {
		t.Fatalf("overwrite %+v %v", prompt, err)
	}
}

func TestAnnotateRefusals(t *testing.T) {
	store := newStore(t)
	at := time.Now()
	reasonIs(t, store.Annotate("../x", "p", false, at), ReasonBadID)
	reasonIs(t, store.Annotate("missing", "p", false, at), ReasonNotFound)

	writeResult(t, store, "e1", time.Now().Add(-2*Retention), "1")
	reasonIs(t, store.Annotate("e1", "p", false, at), ReasonExpired)

	// meta.json unreadable on the second read, after Locate accepted it
	writeResult(t, store, "i1", time.Now(), "1")
	calls := 0
	swap(t, &osOpen, func(name string) (*os.File, error) {
		if strings.HasSuffix(name, MetaFile) {
			calls++
			if calls == 2 {
				return nil, errBoom
			}
		}
		return os.Open(name)
	})
	reasonIs(t, store.Annotate("i1", "p", false, at), ReasonIncomplete)
}

func TestAnnotateNotAnObject(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "n1", time.Now(), "1")
	// readHeader accepts only an object with expiresAt, so a later swap models
	// a file replaced between the locate and the read.
	calls := 0
	swap(t, &osOpen, func(name string) (*os.File, error) {
		if strings.HasSuffix(name, MetaFile) {
			calls++
			if calls == 2 {
				if err := os.WriteFile(name, []byte("null"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Open(name)
	})
	reasonIs(t, store.Annotate("n1", "p", false, time.Now()), ReasonIncomplete)
}

func TestAnnotateTooLarge(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "b1", time.Now(), "1")
	metaPath := filepath.Join(store.Root(), "b1", MetaFile)
	// a meta.json near the bound that cannot take the prompt
	padding := strings.Repeat("x", MaxMetaBytes-400)
	doc := `{"format":"` + MetaFormat + `","version":1,"expiresAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","dataBytes":1,"pad":"` + padding + `"}`
	if len(doc) >= MaxMetaBytes {
		t.Fatal("fixture too large")
	}
	if err := os.WriteFile(metaPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	reasonIs(t, store.Annotate("b1", strings.Repeat("y", 1000), false, time.Now()), ReasonUnwritable)
	if after, _ := os.ReadFile(metaPath); string(after) != doc {
		t.Fatal("a refused annotation changed meta.json")
	}
}

func TestAnnotateWriteFailure(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "w1", time.Now(), "1")
	swap(t, &osRename, func(string, string) error { return errBoom })
	reasonIs(t, store.Annotate("w1", "p", false, time.Now()), ReasonUnwritable)
}

func TestAnnotateConcurrent(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "c1", time.Now(), "1")
	done := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() { done <- store.Annotate("c1", strings.Repeat("z", 100), false, time.Now()) }()
	}
	for i := 0; i < 16; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Locate("c1"); err != nil {
		t.Fatal(err)
	}
}

func TestAnnotateVersionAndSymlink(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "v1", time.Now(), "1")
	metaPath := filepath.Join(store.Root(), "v1", MetaFile)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for name, doc := range map[string]string{
		"future version": `{"format":"` + MetaFormat + `","version":2,"expiresAt":"` + expires + `","dataBytes":1}`,
		"other format":   `{"format":"other","version":1,"expiresAt":"` + expires + `","dataBytes":1}`,
		"no format":      `{"version":1,"expiresAt":"` + expires + `","dataBytes":1}`,
	} {
		if err := os.WriteFile(metaPath, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		reasonIs(t, store.Annotate("v1", "p", false, time.Now()), ReasonVersion)
		if after, _ := os.ReadFile(metaPath); string(after) != doc {
			t.Fatalf("%s: a refused annotation changed meta.json", name)
		}
	}
	// a version that is not a number cannot be read
	if err := os.WriteFile(metaPath, []byte(`{"version":[],"expiresAt":"`+expires+`","dataBytes":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reasonIs(t, store.Annotate("v1", "p", false, time.Now()), ReasonIncomplete)

	// a symlinked meta.json is never followed
	writeResult(t, store, "l1", time.Now(), "1")
	linkPath := filepath.Join(store.Root(), "l1", MetaFile)
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}
	reasonIs(t, store.Annotate("l1", "p", false, time.Now()), ReasonIncomplete)
	if after, _ := os.ReadFile(target); string(after) != "{}" {
		t.Fatal("the link target was written")
	}
}

func TestAnnotateIgnoresDirectorySyncFailure(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "d1", time.Now(), "1")
	swap(t, &syncDir, func(string) error { return errBoom })
	if err := store.Annotate("d1", "kept", false, time.Now()); err != nil {
		t.Fatalf("a directory sync failure after the rename is not a failure: %v", err)
	}
	var prompt Prompt
	if err := json.Unmarshal(readMetaMembers(t, store, "d1")["prompt"], &prompt); err != nil || prompt.Text != "kept" {
		t.Fatalf("prompt %+v %v", prompt, err)
	}
}
