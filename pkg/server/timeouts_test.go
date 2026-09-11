package server

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/querywrite"
)

// TestResponseWriteTimeoutOutlastsAQueryWrite: a query write that commits
// must always be able to answer. The store write is bounded by
// querywrite.Timeout and commits at the end of that bound, after a body
// read that may itself have taken requestReadTimeout, and the whole
// sequence runs inside http.Server's write deadline. If the deadline could
// expire first, a committed write would answer nothing, while the route's
// own 504 promises that a timeout wrote nothing.
func TestResponseWriteTimeoutOutlastsAQueryWrite(t *testing.T) {
	if responseWriteTimeout <= requestReadTimeout+querywrite.Timeout {
		t.Errorf("responseWriteTimeout = %v, want more than requestReadTimeout (%v) plus a query write's own bound (%v), "+
			"or a committed write could fail to answer", responseWriteTimeout, requestReadTimeout, querywrite.Timeout)
	}
}
