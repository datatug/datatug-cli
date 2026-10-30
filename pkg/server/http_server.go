package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
)

var agentHost string
var agentPort int

// requestReadTimeout and responseWriteTimeout are http.Server's own
// deadlines for one request.
//
// The write deadline's clock starts when the request's headers have been
// read and covers everything after it: reading the body (up to
// requestReadTimeout), the handler's own work, and writing the response. A
// query write bounds its wait on the query store by querywrite.Timeout and
// can commit at the very end of that wait, so the write deadline has to
// outlast the whole sequence - a body read that took its full allowance
// followed by a store write that took its own. Otherwise a query write
// could commit while its response never reached the client, which is the
// one outcome the route's 504 contract must never produce: a timeout there
// means nothing was written. timeouts_test.go pins the relationship.
const (
	requestReadTimeout   = 10 * time.Second
	responseWriteTimeout = 60 * time.Second
)

var (
	filestoreNewStore       = filestore.NewStore
	listenAndServeFn        = func(s *http.Server) error { return s.ListenAndServe() }
	configureCompareCacheFn = api.ConfigureCompareCache
)

type HttpServer struct {
	s       *http.Server
	monitor *apimonitor.Monitor
	// IdleTimeout, when positive, makes ServeHTTP return nil after that long
	// without a request. Zero (the default) serves until stopped.
	IdleTimeout time.Duration
}

func NewHttpServer() HttpServer {
	return HttpServer{}
}

func (s *HttpServer) Shutdown(ctx context.Context) error {
	defer func() { _ = api.CloseSessionContext() }()
	if s.s == nil {
		return api.CloseExecutionEvidence()
	}
	err := errors.Join(s.s.Shutdown(ctx), api.CloseExecutionEvidence())
	if err == nil && s.monitor != nil {
		s.monitor.Stop(nil)
	}
	return err
}

// newDatatugStoreFactory builds the storage.NewDatatugStore implementation
// ServeHTTP wires up: a filestore-backed store over pathsByID, mirroring the
// working path project_base_command.go already uses for CLI commands.
//
// It also registers each project's directory with filestore.SetProjectPath
// (an existing helper that, before this change, nothing in this codebase
// ever called outside its own test — filestore.GetProjectPath always
// returned "" for a served project). The semantic endpoints
// (pkg/server/endpoints/semantic_*.go) need a project's real directory to
// read its queries/entities/recordsets/data trees directly — the same
// files-on-disk access pattern dbcopy/httpsource already use elsewhere —
// and storage.GetStore's own context-based store lookup does not work for a
// plain HTTP request under ServeHTTP (verified: it always fails with "no
// store configured", since nothing populates storage.StoreFromContext for a
// served request; see the semantic endpoints' PR body for how this was
// found). Wiring the already-exported SetProjectPath here is the minimal
// fix, not a new mechanism.
//
// The registration is first-registration-wins per process (skipped, not
// re-applied, when a project ID is already registered under a different
// path): SetProjectPath itself panics on that collision by design, and a
// live `datatug serve` process only ever calls ServeHTTP once, so this only
// matters for this package's own tests, several of which legitimately call
// ServeHTTP more than once in the same test binary with the same project ID
// reused across a fresh t.TempDir() each time (see
// security_matrix_test.go's newSecurityMatrixProject) — a real re-pointing
// of one project ID to a different directory within a single process would
// still be a bug worth surfacing, just not one this factory should crash
// the whole server over.
func newDatatugStoreFactory(pathsByID map[string]string) func(id string) (storage.Store, error) {
	for id, path := range pathsByID {
		if existing := filestore.GetProjectPath(id); existing == "" || existing == path {
			filestore.SetProjectPath(id, path)
		}
	}
	return func(id string) (v storage.Store, err error) {
		if v, err = filestoreNewStore("files", pathsByID); err != nil {
			err = fmt.Errorf("failed to create filestore for storage id=%v: %w", id, err)
			return
		}
		return
	}
}

// ServeHTTP starts HTTP server. session is the fixed secureread.Session this
// process's whole life runs under (REQ:principal-selection) — every read the
// web UI can trigger through the endpoints registered below goes through it
// (REQ:server-acl-all-reads); see apps/datatugapp/commands/cmd_serve.go for
// how it is built from --as/--role/--group and the project's policy set.
// caps gates this process's write/opaque-SQL capabilities closed by default
// (api.Capabilities's own doc comment) — see cmd_serve.go's --allow-writes/
// --allow-opaque-sql flags.
func (s *HttpServer) ServeHTTP(pathsByID map[string]string, host string, port int, session secureread.Session, caps api.Capabilities) (err error) {
	defer func() { _ = api.CloseSessionContext() }()
	s.monitor, err = apimonitor.Default.NewServerChecked("HTTP API")
	if err != nil {
		return err
	}
	defer func() { s.monitor.Stop(err) }()
	// A server serves the projects of a map, and a nil one is a server that serves none (a
	// session counts as configured by having a map: see api.ConfigureSecureSession).
	if pathsByID == nil {
		pathsByID = map[string]string{}
	}
	storage.NewDatatugStore = newDatatugStoreFactory(pathsByID)
	if err := api.ConfigureExecutionEvidence(pathsByID, caps.IncidentStores, executionstore.Options{
		PrivateDir: caps.EvidencePrivateDir,
		ByteCap:    caps.EvidenceByteCap,
		Retention:  caps.EvidenceRetention,
	}); err != nil {
		return fmt.Errorf("configure execution evidence: %w", err)
	}
	defer func() { _ = api.CloseExecutionEvidence() }()
	if err := configureCompareCacheFn(comparecache.Options{PrivateDir: caps.EvidencePrivateDir}); err != nil {
		api.LogCompareCacheSkip("configure", err)
	}
	defer func() { _ = api.CloseCompareCache() }()
	api.ConfigureSecureSession(session, pathsByID, caps)
	api.WarnMissingSourceFiles(context.Background(), pathsByID)

	// apicore.Execute (wired in below) panics if this hook is left nil
	// (sneat-go-core/apicore.VerifyRequest) — datatug serve has no
	// per-request bearer token, so every request reports the one
	// secureread.Session principal ConfigureSecureSession just set above.
	// See pkg/api/auth_hook.go for why this hook (rather than routing
	// local-agent endpoints around apicore.Execute) was chosen.
	apicore.GetAuthTokenFromHttpRequest = api.AuthTokenFromHTTPRequest

	if host == "" {
		agentHost = "localhost"
	} else {
		agentHost = host
	}

	if port == 0 {
		agentPort = 8989
	} else {
		agentPort = port
	}

	// The routes of the table, the page at "/" and the answer to every OPTIONS request are all
	// registered behind the one request guard (endpoints.RequestGuard): a request is answered
	// only when it comes from one of this server's own pages or from a tool on this machine.
	endpointCaps := endpoints.Capabilities{
		AllowWrites:          caps.AllowWrites,
		AllowLiveConnections: caps.AllowLiveConnections,
		ServedHost:           agentHost,
		ServedPort:           agentPort,
		Access:               caps.Access,
	}
	guard := endpointCaps.RequestGuard()
	router := httprouter.New()
	router.GlobalOPTIONS = http.HandlerFunc(guard.Wrap(globalOptionsHandler))
	// What the router answers by itself (unknown path, wrong method) is behind the guard too, so
	// an unauthenticated caller cannot tell which routes exist.
	router.NotFound = http.HandlerFunc(guard.Wrap(notFound))
	router.MethodNotAllowed = http.HandlerFunc(guard.Wrap(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}))
	router.HandlerFunc(http.MethodGet, "/", guard.Wrap(root))
	endpoints.RegisterDatatugHandlersWithCapabilities("", router, endpoints.RegisterAllHandlers, logRequests, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, apicore.Execute, endpointCaps)

	handler := s.monitor.Wrap(router)
	var idle *idleWatcher
	if s.IdleTimeout > 0 {
		idle = newIdleWatcher(s.IdleTimeout, time.Now)
		handler = idle.wrap(handler)
	}
	s.s = &http.Server{
		Addr:           fmt.Sprintf("%v:%v", agentHost, agentPort),
		ReadTimeout:    requestReadTimeout,
		WriteTimeout:   responseWriteTimeout,
		MaxHeaderBytes: 1 << 16,
		Handler:        handler,
		BaseContext: func(listener net.Listener) context.Context {
			s.monitor.Live("http://" + listener.Addr().String())
			return context.Background()
		},
	}
	log.Printf("Serving on: http://%v:%v", agentHost, agentPort)

	if idle != nil {
		done := make(chan struct{})
		defer close(done)
		ticker := time.NewTicker(idleCheckInterval)
		defer ticker.Stop()
		srv := s.s
		go idle.watch(done, ticker.C, func() {
			log.Printf("serve: no request for %s; shutting down", s.IdleTimeout)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		})
	}
	err = listenAndServeFn(s.s)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// notFound answers a path that no route serves. A preflight of such a path is answered as the
// preflight of any other path (globalOptionsHandler): the router hands an OPTIONS request to
// GlobalOPTIONS only for a path some route serves, and a refused preflight makes the browser drop
// the real request, so the page would see a network failure (status 0) instead of the 404 that
// the real request gets.
func notFound(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		globalOptionsHandler(w, r)
		return
	}
	http.NotFound(w, r)
}

func root(writer http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprintf(writer, `
<html>
<head>
	<title>DataTug Agent</title>
	<style>body{font-family: Verdana}</style> 
</head>
<body>
	<h1>DataTug API</h1>
	<hr>
	Serving project from %v
	<hr>

	<h2>API endpoints</h2>
	<ul>
		<li><a href=/project>GetProjectStore</a></li>
	</ul>

	<h2>Test endpoints</h2>
	<ul>
		<li><a href=/ping>Ping (pong) - simply returns a "pong" string</a></li>
		<li>
			<a href=/projects>/projects</a> - list of projects hosted by this agent
		</li>
	</ul>

<footer>
	&copy; 2020 <a href=https://datatug.app target=_blank>DataTug.app</a>
</footer>
</body>
</html>
`, filestore.GetProjectPath(storage.SingleProjectID))
}

// resultsPathSegment is where a stored result's id is in a request path.
const resultsPathSegment = "/datatug/results/"

// loggedPath is path as the log shows it: a stored result's id is replaced by
// :id, so the log never holds one.
func loggedPath(path string) string {
	if i := strings.Index(path, resultsPathSegment); i >= 0 {
		return path[:i+len(resultsPathSegment)] + ":id"
	}
	return path
}

// logRequests logs the method, the length of the body and the path of every request
// but the agent-info poll, then runs handler. The query string is not logged: a
// client can put a source string in it (an environment, a database, a source), and
// percent-encoding hides its punctuation, not its password.
func logRequests(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent-info" {
			log.Println(r.Method, r.ContentLength, loggedPath(r.URL.Path))
		}
		handler(w, r)
	}
}
