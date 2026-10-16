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
// carriage returns and every control character but newline and tab are
// dropped, surrounding white space is trimmed, and the text is cut to
// MaxPromptBytes on a rune boundary. truncated says anything was cut by a
// size bound (the read bound or MaxPromptBytes).
func NormalizePrompt(raw []byte) (text string, truncated bool) {
	if len(raw) > MaxPromptReadBytes {
		raw, truncated = raw[:MaxPromptReadBytes], true
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
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
// the reason codes of Locate, or ReasonUnwritable.
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
	members["prompt"], _ = json.Marshal(Prompt{Text: text, Truncated: truncated, At: at.UTC().Format(time.RFC3339)})
	encoded, _ := json.Marshal(members)
	if len(encoded) > MaxMetaBytes {
		return fail(ReasonUnwritable)
	}
	return writeAtomic(filepath.Dir(locator.Meta), MetaFile, encoded)
}
