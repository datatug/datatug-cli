package endpoints

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
)

// requestRefusedMessage is the whole of what a refusal says: a sentence built here, which holds
// nothing the request sent. The rule it states is the rule of RequestGuard.
const requestRefusedMessage = "this agent answers a request only when it comes from one of its own pages or from a tool on the same machine"

// RequestGuard is the one check that stands in front of every route of the server. A route
// answers a request only when it comes from one of the server's own pages or from a tool on the
// same machine:
//
//   - its Host is one that the server was started for: a loopback name or address (localhost,
//     127.0.0.1, ::1) or the host the person configured, with the port the server listens on. A
//     page of another site that makes the browser reach this server by a name of its own (DNS
//     rebinding) carries that name in Host;
//   - its Origin, when the request has one, is on the list that the OPTIONS handler uses
//     (IsSupportedOrigin);
//   - its Sec-Fetch-Site, when the request has one, is same-origin, same-site or none, or its
//     Origin is on the list. A request that carries neither header (the CLI, curl, a script) is
//     answered when its Host passes.
//
// When the server has an access secret, a request that is not a preflight or a code exchange must
// also carry the session token (`Authorization: Bearer`), or it is a 401.
//
// A refusal is a 403 with a fixed sentence: no echo of what the request said, and no
// Access-Control-Allow-Origin. Every other answer to a request whose Origin is on the list, an
// error as much as a success, carries Access-Control-Allow-Origin for that Origin and
// Vary: Origin (writeCORSOrigin).
type RequestGuard struct {
	// host is the host the person configured, lower case and with no brackets; "" when none.
	host string
	// port is the port the server listens on; 0 when it is not known, which accepts any port.
	port int
	// access, when set, makes every route but a preflight and the code exchange require the session
	// token (see agentaccess). nil: no token is required (a server built without one, in tests).
	access *agentaccess.Access
}

// NewRequestGuard builds the guard of a server that listens on servedHost and servedPort. The
// loopback names and addresses are always accepted, whatever servedHost is. A servedPort of 0
// says that the port is not known and any port is accepted.
func NewRequestGuard(servedHost string, servedPort int) RequestGuard {
	return RequestGuard{host: normalizeHostName(servedHost), port: servedPort}
}

// RequestGuard is the guard of a server started with these capabilities: the host and port it
// serves on are the ones of Capabilities.
func (c Capabilities) RequestGuard() RequestGuard {
	guard := NewRequestGuard(c.ServedHost, c.ServedPort)
	guard.access = c.Access
	return guard
}

// Wrap returns next behind the guard: the request is refused (see RequestGuard) or answered by
// next.
func (g RequestGuard) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reason := g.refusal(r); reason != "" {
			refuseRequest(w, r, http.StatusForbidden, requestRefusedMessage, reason)
			return
		}
		// The Origin, when there is one, is on the list here: every answer from now on (a 401, the
		// router's own 404 and 405, a route's error or success) carries the same CORS headers, so
		// the page reads the real status instead of a network failure (status 0).
		writeCORSOrigin(w, r)
		if g.access != nil && !tokenExempt(r) && !g.access.Authorized(r) {
			refuseRequest(w, r, http.StatusUnauthorized, tokenRequiredMessage, "no valid session token")
			return
		}
		next(w, r)
	}
}

// tokenRequiredMessage is what a request with no valid session token is told.
const tokenRequiredMessage = "this agent needs the session token of its link: open the link again from the card"

// tokenExempt is whether a request needs no token: a preflight (it carries none, and is answered
// by origin only) and the exchange of a one-time code, which is how a page gets the token.
func tokenExempt(r *http.Request) bool {
	return r.Method == http.MethodOptions ||
		(r.Method == http.MethodPost && r.URL.Path == agentaccess.ExchangePath)
}

// guardedRouter is the router that every route of the table is registered with: each handler is
// registered behind the guard, so a route that is added to the table has no way to be served
// without it.
type guardedRouter struct {
	router
	guard RequestGuard
}

// HandlerFunc registers handler behind the guard.
func (g guardedRouter) HandlerFunc(method, path string, handler http.HandlerFunc) {
	g.router.HandlerFunc(method, path, g.guard.Wrap(handler))
}

// refusal says in a fixed phrase why the request is not answered, or returns "" for a request
// that is.
func (g RequestGuard) refusal(r *http.Request) string {
	if !g.hostAllowed(r.Host) {
		return "the Host is not one that this server was started for"
	}
	origins := r.Header.Values("Origin")
	listed := len(origins) == 1 && IsSupportedOrigin(origins[0])
	if len(origins) > 0 && !listed {
		return "the Origin is not on the list"
	}
	sites := r.Header.Values("Sec-Fetch-Site")
	ownSite := len(sites) == 1 && ownFetchSite(sites[0])
	if len(sites) > 0 && !ownSite && !listed {
		return "the request is cross-site and its Origin is not on the list"
	}
	return ""
}

// ownFetchSite reports whether a Sec-Fetch-Site value says that the request was not made by a
// page of another site: the page is of the same origin or the same site, or the person typed the
// address (none).
func ownFetchSite(value string) bool {
	return value == "same-origin" || value == "same-site" || value == "none"
}

// hostAllowed reports whether value, the Host of a request, is a loopback name or address or the
// host that was configured, on the port of the server.
func (g RequestGuard) hostAllowed(value string) bool {
	name, port, ok := splitHostHeader(value)
	if !ok || (g.port != 0 && port != g.port) {
		return false
	}
	if name == "localhost" || (g.host != "" && name == g.host) {
		return true
	}
	address, err := netip.ParseAddr(name)
	return err == nil && address.IsLoopback()
}

// splitHostHeader reads a Host header: a name or an address, with a port or with none (which is
// the port of HTTP, 80). An IPv6 address is written in brackets. The name is returned in lower
// case, with no brackets.
func splitHostHeader(value string) (name string, port int, ok bool) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		// No port: a name, an address, or an IPv6 address in brackets.
		host, portText = value, "80"
		if strings.HasPrefix(value, "[") {
			if !strings.HasSuffix(value, "]") {
				return "", 0, false
			}
			host = value[1 : len(value)-1]
		} else if strings.Contains(value, ":") {
			return "", 0, false
		}
	}
	port, err = strconv.Atoi(portText)
	if err != nil || host == "" || port < 1 || port > 65535 {
		return "", 0, false
	}
	return strings.ToLower(host), port, true
}

// normalizeHostName returns a configured host as it is compared with the name of a Host header.
func normalizeHostName(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return strings.ToLower(host)
}

// refuseRequest answers the 403 of a refusal and logs why. What the request sent is logged,
// quoted and cut short, for the operator and never answered.
func refuseRequest(w http.ResponseWriter, r *http.Request, status int, message, reason string) {
	log.Printf("request refused: %s (%s Host=%s Origin=%s Sec-Fetch-Site=%s)", reason, r.Method,
		quotedForLog(r.Host), quotedForLog(strings.Join(r.Header.Values("Origin"), ",")), quotedForLog(strings.Join(r.Header.Values("Sec-Fetch-Site"), ",")))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(wireErrorEnvelope(newAccessDenied(message))); err != nil {
		log.Printf("request guard: failed to encode the refusal: %v", err)
	}
}

// maxLoggedRequestValue is how much of a header value of a refused request is logged.
const maxLoggedRequestValue = 80

// quotedForLog quotes a value a client sent for one line of the log, cutting it short.
func quotedForLog(value string) string {
	if len(value) > maxLoggedRequestValue {
		value = value[:maxLoggedRequestValue] + "..."
	}
	return strconv.Quote(value)
}
