package querywrite

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// MaxSegmentBytes bounds one folder or query-id segment, well under every
// common file system's 255-byte name limit, leaving room for the
// "<id>.query.<type>" suffix the store appends.
const MaxSegmentBytes = 200

// reservedTxnDirName is the entry datatug-core's revisioned filestore
// reserves at the queries root for its lock, journal and staging area.
const reservedTxnDirName = ".dt-query-txn"

// windowsIllegalChars are the characters Windows forbids in a file name; a
// query location must stay portable to every platform DataTug runs on.
const windowsIllegalChars = `<>:"/\|?*`

// windowsReservedNames are the device names Windows reserves whatever the
// extension, compared case-insensitively against a segment's name before
// its first "." with trailing spaces trimmed.
var windowsReservedNames = func() map[string]bool {
	names := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	for _, digit := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
		names["COM"+digit] = true
		names["LPT"+digit] = true
	}
	return names
}()

// windowsShortNamePattern matches a name shaped like an NTFS 8.3 short
// name ("LONGFO~1", "REPORT~12.TXT"). Windows generates one for every long
// name when 8dot3 name creation is on, and opening it opens the long name's
// file, so such a segment can name a folder other than the one it spells.
var windowsShortNamePattern = regexp.MustCompile(`(?i)^[^.~]{1,6}~[0-9]{1,2}(\.[^.]{1,3})?$`)

// isIgnorableRune reports whether r is a default-ignorable code point: a
// format character (Cf: the soft hyphen, the zero-width space and joiners,
// the byte-order mark, the Mongolian and bidi format controls), a variation
// selector, or one of the rest of the Unicode property (the Hangul fillers,
// the combining grapheme joiner). HFS+ ignores them when it compares names,
// and some Linux casefold tables have too, so a name holding one can be the
// same file as the name without it.
func isIgnorableRune(r rune) bool {
	return unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		unicode.Is(unicode.Variation_Selector, r)
}

// isNameableRune reports whether r is a character this Go build's Unicode
// tables know how to case-map and normalize: a letter, mark, number,
// punctuation, symbol or ordinary space. A code point outside them is
// unassigned, private use, a surrogate or a line/paragraph separator; an
// unassigned one may be a cased letter to a newer file system's own table
// than Go's, which could make it one file with a spelling the canonical
// form keeps apart.
func isNameableRune(r rune) bool {
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Zs)
}

// SegmentReason reports why segment cannot be one folder segment or the id
// of a saved query's location, or ("", true) when it can. It is the one
// segment validator every query write path calls.
//
// Its rules are datatug-core's revisioned filestore's
// (validateQuerySegmentReason in pkg/storage/filestore/query_location.go on
// the Phase 2 task 2 storage branch), applied here because the store the
// default build pins applies none of them: non-empty; not "." or ".."; no
// NUL, invalid UTF-8, control or bidirectional-text control character; none
// of windowsIllegalChars (so never a path separator); not the store's
// reserved ".dt-query-txn"; no leading "."; no trailing "." or space; at
// most MaxSegmentBytes; not a Windows device name. On top of them, "~"
// (datatug.RootSharedFolderName) is the legacy API's name for the queries
// root, so it is never a folder or an id of its own.
//
// Three more rules are this package's own, and are stricter than the
// store's, so a name this accepts is always one the store accepts. Each
// refuses a spelling that could be a file other than the one it names, and
// refusing is better than accepting a name authorization and the store
// might read differently (core's own validator should grow them too; until
// it does, every write path reaches the store through this function):
//
//   - no default-ignorable code point (isIgnorableRune), which HFS+ drops
//     when it compares names;
//   - no unassigned, private-use or line-separator code point
//     (isNameableRune), whose case mapping a newer file system may know and
//     this build's Unicode tables may not;
//   - nothing shaped like a Windows 8.3 short name
//     (windowsShortNamePattern), which on NTFS can open another name's
//     file.
func SegmentReason(segment string) (reason string, ok bool) {
	switch {
	case segment == "":
		return "must not be empty", false
	case segment == "." || segment == "..":
		return `must not be "." or ".."`, false
	case segment == datatug.RootSharedFolderName:
		return `must not be "~", which names the queries root`, false
	case strings.ContainsRune(segment, 0):
		return "must not contain a NUL byte", false
	case !utf8.ValidString(segment):
		return "must be valid UTF-8", false
	}
	for _, r := range segment {
		switch {
		case unicode.IsControl(r):
			return "must not contain control characters", false
		case unicode.Is(unicode.Bidi_Control, r):
			return "must not contain bidirectional-text control characters", false
		case isIgnorableRune(r):
			return "must not contain invisible (default-ignorable) characters", false
		case !isNameableRune(r):
			return "must not contain unassigned, private-use or line-separator characters", false
		}
	}
	switch {
	case strings.ContainsAny(segment, windowsIllegalChars):
		return "must not contain any of " + windowsIllegalChars, false
	case windowsShortNamePattern.MatchString(segment):
		return "looks like a Windows 8.3 short name, which can name another file there", false
	case segment == reservedTxnDirName:
		return "is reserved for the query store's transactions", false
	case strings.HasPrefix(segment, "."):
		return `must not start with "."`, false
	case strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " "):
		return `must not end with "." or a space`, false
	case len(segment) > MaxSegmentBytes:
		return "is longer than 200 bytes", false
	}
	base := segment
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	if windowsReservedNames[strings.ToUpper(strings.TrimRight(base, " "))] {
		return "is a Windows-reserved device name", false
	}
	return "", true
}
