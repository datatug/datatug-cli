package sshdemo

import "errors"

// ErrCleanupBoundary identifies an invariant violation in workspace traversal.
var ErrCleanupBoundary = errors.New("cleanup left the workspace device or root")

// Cleanup helper stdout protocol. The helper reports progress on its first
// deletion, so the supervisor can observe it even after a timeout or OOM kill.
const (
	CleanupProgress  = "\x01"
	CleanupInvariant = "\x02"
)
