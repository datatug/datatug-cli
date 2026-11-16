package watch

import "time"

// ExecutionCeiling validates a coordinator-admitted claim's original deadline
// and current lease, then narrows that deadline by independently authenticated
// original credential/enrollment ceilings. Zero or expired bounds fail closed.
// It authenticates nothing: a protected worker must obtain the claim and bounds
// from trusted admission and pin the returned ceiling once for this attempt.
// A later heartbeat can extend LeaseUntil, never that pinned ceiling.
func (c Claim) ExecutionCeiling(at time.Time, bounds ...time.Time) (time.Time, error) {
	if at.IsZero() || !c.Deadline.After(at) || !c.LeaseUntil.After(at) || c.LeaseUntil.After(c.Deadline) {
		return time.Time{}, ErrStaleLease
	}
	ceiling := c.Deadline
	for _, bound := range bounds {
		if bound.IsZero() || !bound.After(at) {
			return time.Time{}, ErrStaleLease
		}
		if bound.Before(ceiling) {
			ceiling = bound
		}
	}
	return ceiling, nil
}
