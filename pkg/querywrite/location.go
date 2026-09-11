package querywrite

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// knownLocationReasons are the reasons datatug-core's revisioned store
// states in its own fixed words when it refuses a query location
// (pkg/storage/filestore/query_location.go): its segment rules, and the
// two things it finds on disk. Any other reason is OS error text.
var knownLocationReasons = map[string]bool{
	"must not be empty":                                      true,
	`must not be "." or ".."`:                                true,
	"must not contain a NUL byte":                            true,
	"must be valid UTF-8":                                    true,
	"must not contain control characters":                    true,
	"must not contain bidirectional-text control characters": true,
	"must not contain any of " + windowsIllegalChars:         true,
	"reserved for the query transaction namespace":           true,
	`must not start with "."`:                                true,
	`must not end with "." or a space (illegal on Windows)`:  true,
	"exceeds maximum segment length":                         true,
	"is a Windows-reserved device name":                      true,
	"must not contain a path separator":                      true,
	"resolves through a symlink":                             true,
	"a path component exists and is not a directory":         true,
}

// folderSegmentReasonPattern reads the store's `folder path segment "x":
// reason` form.
var folderSegmentReasonPattern = regexp.MustCompile(`(?s)^folder path segment ("(?:[^"\\]|\\.)*"): (.*)$`)

// LocationMessage turns a query store's refusal of a location - the folder
// path, id and reason of a datatug.InvalidQueryLocationError - into the
// message a 400 may carry. It names the offending segment and nothing else
// of the server: the store builds some reasons from OS error text ("lstat
// /srv/project/queries/locked/sub: permission denied"), and neither that
// absolute path nor the OS text may reach a client. A reason the store
// states in its own fixed words (knownLocationReasons) is kept, so "folder
// path segment \"linked\": resolves through a symlink" still says what to
// fix; anything else becomes "cannot be accessed".
func LocationMessage(folderPath, id, reason string) string {
	subject, detail := "the query location", reason
	switch {
	case strings.HasPrefix(reason, "queries root: "):
		subject, detail = "the project's queries folder", strings.TrimPrefix(reason, "queries root: ")
	case strings.HasPrefix(reason, "id: "):
		subject, detail = fmt.Sprintf("query id %q", id), strings.TrimPrefix(reason, "id: ")
	default:
		if m := folderSegmentReasonPattern.FindStringSubmatch(reason); m != nil {
			if segment, err := strconv.Unquote(m[1]); err == nil {
				subject, detail = fmt.Sprintf("folder path segment %q", segment), m[2]
			}
		} else if folderPath != "" {
			subject = fmt.Sprintf("folder path %q", folderPath)
		}
	}
	if knownLocationReasons[detail] {
		return subject + ": " + detail
	}
	return subject + " cannot be accessed"
}
