package querywrite

import (
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
		}
	}
	switch {
	case strings.ContainsAny(segment, windowsIllegalChars):
		return "must not contain any of " + windowsIllegalChars, false
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
