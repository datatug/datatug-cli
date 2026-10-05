package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A serve route answers a request only when it comes from one of the server's own pages or from
// a tool on the same machine: its Host is one the server was started for, its Origin (when it
// has one) is on the list of IsSupportedOrigin, and its Sec-Fetch-Site (when it has one) is
// same-origin, same-site or none, or its Origin is on the list. These tests hold the rule over
// the whole route table.

// guardRequest builds a request to path with the headers of a request that came from the web.
func guardRequest(method, path, host, origin, fetchSite string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if fetchSite != "" {
		r.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	return r
}

func TestRequestGuard_Rule(t *testing.T) {
	const listed = "https://datatug.app"
	for _, tc := range []struct {
		name        string
		host        string
		port        int
		origin      string
		fetchSite   string
		extraHost   string
		extraOrigin []string
		extraSite   []string
		allowed     bool
	}{
		// The tools of the machine: no Origin and no Sec-Fetch-Site.
		{name: "curl on the loopback address", host: "127.0.0.1:8989", port: 8989, allowed: true},
		{name: "curl on localhost", host: "localhost:8989", port: 8989, allowed: true},
		{name: "curl on the IPv6 loopback address", host: "[::1]:8989", port: 8989, allowed: true},
		{name: "another loopback address", host: "127.0.0.2:8989", port: 8989, allowed: true},
		{name: "the default port of HTTP, not written", host: "localhost", port: 80, allowed: true},
		{name: "the IPv6 loopback address with no port", host: "[::1]", port: 80, allowed: true},
		{name: "a server that does not know its port", host: "localhost:1234", port: 0, allowed: true},
		{name: "the host the person configured", host: "agent.example.com:8989", port: 8989, extraHost: "agent.example.com", allowed: true},
		{name: "the host the person configured, in capitals", host: "Agent.Example.COM:8989", port: 8989, extraHost: "agent.example.com", allowed: true},
		{name: "an IPv6 host the person configured", host: "[2001:db8::1]:8989", port: 8989, extraHost: "2001:db8::1", allowed: true},
		{name: "a host written with brackets in the configuration", host: "[::1]:8989", port: 8989, extraHost: "[::1]", allowed: true},

		// The pages of the web.
		{name: "a page of the app, cross-site", host: "127.0.0.1:8989", port: 8989, origin: listed, fetchSite: "cross-site", allowed: true},
		{name: "a page of localhost", host: "127.0.0.1:8989", port: 8989, origin: "http://localhost:4200", fetchSite: "same-site", allowed: true},
		{name: "same-origin with no Origin", host: "localhost:8989", port: 8989, fetchSite: "same-origin", allowed: true},
		{name: "a typed address (none)", host: "localhost:8989", port: 8989, fetchSite: "none", allowed: true},
		{name: "a listed Origin and no Sec-Fetch-Site", host: "localhost:8989", port: 8989, origin: listed, allowed: true},

		// Refused: the Host.
		{name: "a Host that is not the server's (rebinding)", host: "evil.example.com:8989", port: 8989, allowed: false},
		{name: "a Host that is not the server's, with a listed Origin", host: "evil.example.com:8989", port: 8989, origin: listed, fetchSite: "cross-site", allowed: false},
		{name: "the right name on another port", host: "localhost:9999", port: 8989, allowed: false},
		{name: "the default port when the server is on another", host: "localhost", port: 8989, allowed: false},
		{name: "no Host at all", host: "", port: 8989, allowed: false},
		{name: "a Host with an empty port", host: "localhost:", port: 8989, allowed: false},
		{name: "a Host with a port that is not a number", host: "localhost:abc", port: 8989, allowed: false},
		{name: "a Host with a port out of range", host: "localhost:99999", port: 0, allowed: false},
		{name: "an IPv6 address with no brackets", host: "::1", port: 8989, allowed: false},
		{name: "an IPv6 address with one bracket", host: "[::1", port: 80, allowed: false},
		{name: "a name that only starts like localhost", host: "localhost.evil.example.com:8989", port: 8989, allowed: false},
		{name: "a name that only ends like localhost", host: "evillocalhost:8989", port: 8989, allowed: false},
		{name: "an address that is not a loopback one", host: "192.168.1.5:8989", port: 8989, allowed: false},
		{name: "a wildcard address", host: "0.0.0.0:8989", port: 8989, allowed: false},
		{name: "a Host that is not the one configured", host: "other.example.com:8989", port: 8989, extraHost: "agent.example.com", allowed: false},

		// Refused: the Origin.
		{name: "an Origin that is not on the list", host: "localhost:8989", port: 8989, origin: "https://evil.example.com", fetchSite: "cross-site", allowed: false},
		{name: "an Origin that is not on the list, no Sec-Fetch-Site", host: "localhost:8989", port: 8989, origin: "https://evil.example.com", allowed: false},
		{name: "an Origin that is not on the list, same-site", host: "localhost:8989", port: 8989, origin: "http://evil.example.com", fetchSite: "same-site", allowed: false},
		{name: "an opaque Origin", host: "localhost:8989", port: 8989, origin: "null", allowed: false},
		{name: "an empty Origin", host: "localhost:8989", port: 8989, extraOrigin: []string{""}, allowed: false},
		{name: "two Origins", host: "localhost:8989", port: 8989, extraOrigin: []string{listed, listed}, allowed: false},
		{name: "an Origin with a path that ends like the app's", host: "localhost:8989", port: 8989, origin: "https://evil.example.com/.datatug.app", allowed: false},

		// Refused: the Sec-Fetch-Site.
		{name: "cross-site with no Origin", host: "localhost:8989", port: 8989, fetchSite: "cross-site", allowed: false},
		{name: "a value that is not one", host: "localhost:8989", port: 8989, fetchSite: "anything", allowed: false},
		{name: "an empty value", host: "localhost:8989", port: 8989, extraSite: []string{""}, allowed: false},
		{name: "two values", host: "localhost:8989", port: 8989, extraSite: []string{"same-origin", "same-origin"}, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := NewRequestGuard(tc.extraHost, tc.port)
			r := guardRequest(http.MethodGet, "/datatug/ping", tc.host, tc.origin, tc.fetchSite)
			for _, v := range tc.extraOrigin {
				r.Header.Add("Origin", v)
			}
			for _, v := range tc.extraSite {
				r.Header.Add("Sec-Fetch-Site", v)
			}
			reached := false
			w := httptest.NewRecorder()
			guard.Wrap(func(http.ResponseWriter, *http.Request) { reached = true })(w, r)
			assert.Equal(t, tc.allowed, reached)
			if tc.allowed {
				assert.Equal(t, http.StatusOK, w.Code)
				return
			}
			assertRefusal(t, w, r)
		})
	}
}

// assertRefusal checks the one answer of a refusal: a 403 with the built sentence, no
// Access-Control-Allow-Origin, and nothing of the request's values.
func assertRefusal(t *testing.T, w *httptest.ResponseRecorder, r *http.Request) {
	t.Helper()
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"), "a refusal names no origin")
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
	assert.Equal(t, "ACCESS_DENIED", envelope.Error.Code)
	assert.Equal(t, requestRefusedMessage, envelope.Error.Message)
	for _, value := range append([]string{r.Host}, append(r.Header.Values("Origin"), r.Header.Values("Sec-Fetch-Site")...)...) {
		if len(value) >= 3 {
			assert.NotContains(t, w.Body.String(), value, "a refusal does not echo what the request said")
		}
	}
}

func TestRequestGuard_RefusalIsLoggedWithoutCrediting(t *testing.T) {
	logged := captureAgentLog(t)
	guard := NewRequestGuard("", 8989)
	r := guardRequest(http.MethodPost, "/datatug/ping", "evil.example.com:8989", "https://evil.example.com", "cross-site")
	guard.Wrap(func(http.ResponseWriter, *http.Request) { t.Fatal("reached") })(httptest.NewRecorder(), r)
	assert.Contains(t, logged.String(), "request refused")
	assert.Contains(t, logged.String(), `"evil.example.com:8989"`, "the operator sees which host was refused, quoted")

	// A value that is long, or holds a line break, is logged as one short quoted line.
	logged.Reset()
	r = guardRequest(http.MethodGet, "/x", "localhost:8989", "https://evil.example.com/"+strings.Repeat("a", 500)+"\nforged line", "")
	guard.Wrap(func(http.ResponseWriter, *http.Request) { t.Fatal("reached") })(httptest.NewRecorder(), r)
	assert.Equal(t, 1, strings.Count(logged.String(), "\n"), "one line: %q", logged.String())
	assert.Less(t, logged.Len(), 400)
}

// A refusal that cannot be written is logged, and nothing else happens.
func TestRequestGuard_ARefusalThatCannotBeWrittenIsLogged(t *testing.T) {
	logged := captureAgentLog(t)
	w := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	NewRequestGuard("", 8989).Wrap(func(http.ResponseWriter, *http.Request) { t.Fatal("reached") })(w, guardRequest(http.MethodGet, "/", "evil.example.com:8989", "", ""))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, logged.String(), "failed to encode the refusal")
}

func TestNewRequestGuard_FromCapabilities(t *testing.T) {
	guard := Capabilities{ServedHost: "agent.example.com", ServedPort: 8989}.RequestGuard()
	reached := false
	guard.Wrap(func(http.ResponseWriter, *http.Request) { reached = true })(httptest.NewRecorder(), guardRequest(http.MethodGet, "/", "agent.example.com:8989", "", ""))
	assert.True(t, reached)
}

// recordedRoute is one entry of the route table.
type recordedRoute struct {
	method, path string
	handler      http.HandlerFunc
}

type routeRecorder struct{ routes []recordedRoute }

func (rr *routeRecorder) HandlerFunc(method, path string, handler http.HandlerFunc) {
	rr.routes = append(rr.routes, recordedRoute{method, path, handler})
}

// recordRoutes registers every route, in both modes, with the router that records them: through
// registerRoutes (what a server does) or, as the reference, through registerRoutesOn with the
// recorder itself, which is what the routes were before any guard.
func recordRoutes(guarded bool, caps Capabilities) []recordedRoute {
	var all []recordedRoute
	for _, writeOnly := range []bool{false, true} {
		recorder := &routeRecorder{}
		if guarded {
			registerRoutes("", recorder, nil, writeOnly, caps)
		} else {
			registerRoutesOn("", recorder, nil, writeOnly, caps)
		}
		all = append(all, recorder.routes...)
	}
	return all
}

// quietLog silences the "Registering ..." lines of the registration for the length of the test.
func quietLog(t *testing.T) {
	t.Helper()
	saved := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(saved) })
}

// stubHandle makes the routes that answer through apicore answer through a fixed handler.
func stubHandle(t *testing.T) {
	t.Helper()
	savedHandle, savedContext := handle, getContextFromRequest
	t.Cleanup(func() { handle, getContextFromRequest = savedHandle, savedContext })
	handle = func(w http.ResponseWriter, _ *http.Request, _ apicore.RequestDTO, _ verify.RequestOptions, status int, _ apicore.ContextProvider, _ apicore.Worker) {
		w.WriteHeader(status)
	}
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
}

// refusedRequests are the requests that no route answers: one for each of the three conditions.
func refusedRequests(method, path string) map[string]*http.Request {
	return map[string]*http.Request{
		"an Origin that is not on the list":                   guardRequest(method, path, "127.0.0.1:8989", "https://evil.example.com", "cross-site"),
		"a Host that is not the server's":                     guardRequest(method, path, "evil.example.com:8989", "", ""),
		"a Host that is not the server's and a listed Origin": guardRequest(method, path, "evil.example.com:8989", "https://datatug.app", "cross-site"),
		"a cross-site request with no Origin":                 guardRequest(method, path, "127.0.0.1:8989", "", "cross-site"),
	}
}

func TestRoutes_EveryRouteRefusesARequestThatIsNotItsOwn(t *testing.T) {
	quietLog(t)
	stubHandle(t)
	routes := recordRoutes(true, Capabilities{ServedPort: 8989})
	require.Greater(t, len(routes), 60, "the whole route table is recorded")

	for _, route := range routes {
		for name, request := range refusedRequests(route.method, route.path) {
			t.Run(route.method+" "+route.path+" / "+name, func(t *testing.T) {
				w := httptest.NewRecorder()
				route.handler(w, request)
				assertRefusal(t, w, request)
			})
		}
	}
}

// The same request from an allowed origin is served exactly as it is by the route with no guard:
// the guard adds nothing to an answer and takes nothing from it.
func TestRoutes_EveryRouteServesARequestThatIsItsOwnAsBefore(t *testing.T) {
	quietLog(t)
	stubHandle(t)
	caps := Capabilities{ServedPort: 8989}
	guarded, reference := recordRoutes(true, caps), recordRoutes(false, caps)
	require.Equal(t, len(reference), len(guarded))

	allowed := map[string]func(method, path string) *http.Request{
		"a tool on the machine": func(method, path string) *http.Request { return guardRequest(method, path, "127.0.0.1:8989", "", "") },
		"a page of the app": func(method, path string) *http.Request {
			return guardRequest(method, path, "localhost:8989", "https://datatug.app", "cross-site")
		},
		"a page of localhost": func(method, path string) *http.Request {
			return guardRequest(method, path, "127.0.0.1:8989", "http://localhost:4200", "same-site")
		},
	}
	for i, route := range guarded {
		require.Equal(t, reference[i].method+" "+reference[i].path, route.method+" "+route.path)
		for name, build := range allowed {
			t.Run(route.method+" "+route.path+" / "+name, func(t *testing.T) {
				want := serveRecorded(reference[i].handler, build(route.method, route.path))
				got := serveRecorded(route.handler, build(route.method, route.path))
				assert.Equal(t, want, got)
				assert.NotContains(t, got, requestRefusedMessage)
			})
		}
	}
}

// serveRecorded runs the handler and returns everything it answered, as text; a handler that
// panics is the answer "panic", which both sides give.
func serveRecorded(handler http.HandlerFunc, r *http.Request) (answer string) {
	w := httptest.NewRecorder()
	defer func() {
		if recovered := recover(); recovered != nil {
			answer = "panic"
		}
	}()
	handler(w, r)
	header := w.Header()
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", w.Code)
	for _, key := range keys {
		if key == "X-Request-Id" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", key, strings.Join(header[key], ","))
	}
	// A request ID is minted per answer.
	b.WriteString(requestIDPattern.ReplaceAllString(w.Body.String(), `"requestId":"-"`))
	return b.String()
}

// requestIDPattern is the request ID a contract error carries: minted for each answer.
var requestIDPattern = regexp.MustCompile(`"requestId":"[^"]*"`)

// registrationMethods are the methods of a router that register a route.
var registrationMethods = map[string]bool{
	"Handle": true, "Handler": true, "HandlerFunc": true, "ServeFiles": true,
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "HEAD": true, "OPTIONS": true,
}

// isGuardWrap reports whether expr is a call of a Wrap method: guard.Wrap(handler).
func isGuardWrap(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Wrap"
}

// Every route is registered through the guard. A route registered any other way is not in the
// table that the tests above walk, so this reads the code of the two packages that register
// routes (the routes of the table and the server's own pages) and fails for a registration that
// does not go through the guard: its handler wrapped by a guard's Wrap, or forwarded by the two
// functions that are the guard's own way in (route in 0_interface.go and the guarded router of
// request_guard.go).
func TestRoutes_NoRouteIsRegisteredOutsideTheGuard(t *testing.T) {
	forwarders := map[string]bool{"0_interface.go": true, "request_guard.go": true}
	registered := map[string]int{}
	for _, dir := range []string{".", ".."} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			file = filepath.ToSlash(file)
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			require.NoError(t, err)
			ast.Inspect(parsed, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.CallExpr:
					selector, ok := n.Fun.(*ast.SelectorExpr)
					if !ok || !registrationMethods[selector.Sel.Name] || len(n.Args) == 0 {
						return true
					}
					if pkg, isPackage := selector.X.(*ast.Ident); isPackage && pkg.Name == "http" {
						return true // http.HandlerFunc(f) is a conversion, not a registration
					}
					registered[file]++
					if !isGuardWrap(n.Args[len(n.Args)-1]) && !forwarders[file] {
						t.Errorf("%s: %s registers a route that does not go through the request guard", file, selector.Sel.Name)
					}
				case *ast.AssignStmt:
					for i, left := range n.Lhs {
						if selector, ok := left.(*ast.SelectorExpr); ok && selector.Sel.Name == "GlobalOPTIONS" {
							registered[file]++
							conversion, isCall := n.Rhs[i].(*ast.CallExpr)
							if !isCall || len(conversion.Args) != 1 || !isGuardWrap(conversion.Args[0]) {
								t.Errorf("%s: the handler of every OPTIONS request does not go through the request guard", file)
							}
						}
					}
				}
				return true
			})
		}
	}
	assert.NotZero(t, registered["0_interface.go"], "the route table is registered in 0_interface.go")
	assert.Equal(t, 2, registered["../http_server.go"], "the server registers its pages and its OPTIONS handler in http_server.go")
}
