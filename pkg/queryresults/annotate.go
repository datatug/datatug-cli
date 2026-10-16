package queryresults

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxPromptReadBytes bounds how much raw input NormalizePrompt considers.
const MaxPromptReadBytes = 1 << 20

// annotateMu serializes the read-modify-write of meta.json inside this
// process; the rename keeps every reader on a complete file regardless.
var annotateMu sync.Mutex

// NormalizePrompt cleans raw prompt input: invalid UTF-8 becomes U+FFFD,
// carriage returns, every control character but newline and tab, and the
// Unicode format characters (U+200B-200D, U+2060, U+FEFF, U+202A-202E,
// U+2066-2069 and the rest of category Cf) are dropped, surrounding white space is trimmed, and the text is cut to
// MaxPromptBytes on a rune boundary. truncated says anything was cut by a
// size bound (the read bound or MaxPromptBytes).
func NormalizePrompt(raw []byte) (text string, truncated bool) {
	if len(raw) > MaxPromptReadBytes {
		raw, truncated = raw[:MaxPromptReadBytes], true
	}
	cleaned := strings.Map(func(r rune) rune {
		// Control characters, and the format characters that can disguise
		// text on display (bidi controls, zero-width characters, the byte
		// order mark, the word joiner) and the line and paragraph separators.
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(raw), "�"))
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > MaxPromptBytes {
		cut := MaxPromptBytes
		for !utf8.RuneStart(cleaned[cut]) {
			cut--
		}
		cleaned, truncated = strings.TrimSpace(cleaned[:cut]), true
	}
	return cleaned, truncated
}

// Annotate records prompt as the request that produced result id: it sets
// meta.json's "prompt" and leaves every other member as it was. The file is
// replaced atomically (private temporary file, sync, rename), so a reader
// sees the old or the new meta.json, never a mix. Annotating again
// overwrites. The result must be complete and unexpired; a failure carries
// the reason codes of Locate, ReasonVersion, or ReasonUnwritable. Members are
// rewritten in alphabetical key order with their values unchanged. Nothing
// locks across processes: two annotations racing leave the last one, and one
// racing rm or prune can fail with ReasonUnwritable.
func (s *Store) Annotate(id, text string, truncated bool, at time.Time) error {
	annotateMu.Lock()
	defer annotateMu.Unlock()
	locator, err := s.Locate(id)
	if err != nil {
		return err
	}
	content, err := readFile(locator.Meta, MaxMetaBytes)
	if err != nil || len(content) > MaxMetaBytes {
		return fail(ReasonIncomplete)
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(content, &members) != nil || members == nil {
		return fail(ReasonIncomplete)
	}
	// A result Load would refuse for its format or version is not edited.
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if json.Unmarshal(content, &head) != nil {
		return fail(ReasonIncomplete)
	}
	if head.Format != MetaFormat || head.Version != Version {
		return fail(ReasonVersion)
	}
	members["prompt"], _ = json.Marshal(Prompt{Text: text, Truncated: truncated, At: at.UTC().Format(time.RFC3339)})
	encoded, _ := json.Marshal(members)
	if len(encoded) > MaxMetaBytes {
		return fail(ReasonUnwritable)
	}
	// The new meta.json is in place once the rename succeeded, so a failure to
	// sync the directory afterwards is not reported.
	return replaceFile(filepath.Dir(locator.Meta), MetaFile, encoded)
}
