package endpoints

import (
	"net/url"
	"strings"
)

// IsSupportedOrigin check provided origin is allowed. 127.0.0.1 is accepted
// interchangeably with localhost (lane C5, datatug-apps PR #59, verified
// with curl: a dev server or UI addressing the agent via 127.0.0.1 was
// refused even though the equivalent localhost origin was already allowed).
//
// It is the one list of origins: the OPTIONS handler, the request guard in front of every route
// and the answers' Access-Control-Allow-Origin all use it. An origin is a scheme, a host and
// maybe a port, and nothing else (the Origin of a browser never has a path, a query, a fragment
// or a user), so a text that has more is not on the list whatever it starts or ends with.
func IsSupportedOrigin(origin string) bool {
	if !isOriginShaped(origin) {
		return false
	}
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

// isOriginShaped reports whether origin is written as an origin is: scheme://host or
// scheme://host:port, with nothing after the host or port and no user in front of it.
func isOriginShaped(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery && u.String() == origin
}
