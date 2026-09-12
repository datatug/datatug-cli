package endpoints

import "strings"

// IsSupportedOrigin check provided origin is allowed. 127.0.0.1 is accepted
// interchangeably with localhost (lane C5, datatug-apps PR #59, verified
// with curl: a dev server or UI addressing the agent via 127.0.0.1 was
// refused even though the equivalent localhost origin was already allowed).
func IsSupportedOrigin(origin string) bool {
	if strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:") {
		return true
	}
	if strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "https://127.0.0.1:") {
		return true
	}
	switch origin {
	case "https://datatug.app", "https://app.incidentius.com":
		return true
	default:
		return strings.HasPrefix(origin, "https://") && strings.HasSuffix(origin, ".datatug.app")
	}
}
