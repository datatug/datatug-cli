package endpoints

import (
	"net/url"
	"strconv"
	"sync"
)

// DevUIHost is the one host, besides https://datatug.app, whose pages may call the agent: the
// local web UI development host (http or https, with or without a port).
const DevUIHost = "datatug.localhost"

// IsSupportedOrigin reports whether origin is one the agent answers: exactly https://datatug.app
// (no port), or the exact host datatug.localhost with the scheme http or https and no port or a
// numeric one. Nothing else is on the list: not localhost or 127.0.0.1, not a subdomain of
// datatug.app, not another product's UI (founder decision 2026-10-15). The match is exact on
// scheme, host and port, never a prefix or a suffix test, and no flag or environment variable
// adds an origin.
//
// It is the one list of origins: the OPTIONS handler, the request guard in front of every route
// and the answers' Access-Control-Allow-Origin all use it. An origin is a scheme, a host and
// maybe a port, and nothing else (the Origin of a browser never has a path, a query, a fragment
// or a user), so a text that has more is not on the list whatever it starts or ends with.
//
// One exception, by founder direction of 2026-11-07 (spec: datatug/backstage
// local-frontend-distribution REQ:ui-host): when this server itself serves the web UI
// (`datatug serve --web-ui`), the exact origins of its own pages are listed too —
// http://<ui-host>:<port>, http://localhost:<port>, http://127.0.0.1:<port> and
// http://[::1]:<port> for its own port only (ConfigureServedUIOrigins).
func IsSupportedOrigin(origin string) bool {
	if !isOriginShaped(origin) {
		return false
	}
	if origin == "https://datatug.app" || isServedUIOrigin(origin) {
		return true
	}
	u, _ := url.Parse(origin) // isOriginShaped parsed it
	if u.Hostname() != DevUIHost {
		return false
	}
	port := u.Port()
	if port == "" {
		return u.Host == DevUIHost
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

// isOriginShaped reports whether origin is written as an origin is: scheme://host or
// scheme://host:port, with nothing after the host or port and no user in front of it.
func isOriginShaped(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery && u.String() == origin
}

var (
	servedUIMu      sync.RWMutex
	servedUIOrigins map[string]bool
)

// ConfigureServedUIOrigins lists the exact origins of the web UI pages this process serves
// itself; nil clears the list. Each is http://<host>:<port> for uiHost and the loopback names.
func ConfigureServedUIOrigins(uiHost string, port int) {
	servedUIMu.Lock()
	defer servedUIMu.Unlock()
	if port == 0 {
		servedUIOrigins = nil
		return
	}
	p := strconv.Itoa(port)
	servedUIOrigins = map[string]bool{}
	for _, h := range []string{uiHost, "localhost", "127.0.0.1", "[::1]"} {
		if h != "" {
			servedUIOrigins["http://"+h+":"+p] = true
		}
	}
}

func isServedUIOrigin(origin string) bool {
	servedUIMu.RLock()
	defer servedUIMu.RUnlock()
	return servedUIOrigins[origin]
}

// servedUIHostAllowed reports whether host:port is the origin of a web UI page this process serves.
func servedUIHostAllowed(host string, port int) bool {
	return isServedUIOrigin("http://" + host + ":" + strconv.Itoa(port))
}
