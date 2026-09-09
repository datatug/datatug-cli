package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
)

var agentHost string
var agentPort int

type HttpServer struct {
	s *http.Server
}

func NewHttpServer() HttpServer {
	return HttpServer{}
}

func (s *HttpServer) Shutdown(ctx context.Context) error {
	return s.s.Shutdown(ctx)
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
		if v, err = filestore.NewStore("files", pathsByID); err != nil {
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
func (s *HttpServer) ServeHTTP(pathsByID map[string]string, host string, port int, session secureread.Session) error {
	storage.NewDatatugStore = newDatatugStoreFactory(pathsByID)
	api.ConfigureSecureSession(session, pathsByID)

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

	router := httprouter.New()
	router.GlobalOPTIONS = http.HandlerFunc(globalOptionsHandler)
	router.HandlerFunc(http.MethodGet, "/", root)
	logWrapper := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/agent-info" {
				log.Println(r.Method, r.ContentLength, r.RequestURI)
			}
			handler(w, r)
		}
	}
	endpoints.RegisterDatatugHandlers("", router, endpoints.RegisterAllHandlers, logWrapper, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, apicore.Execute)

	s.s = &http.Server{
		Addr:           fmt.Sprintf("%v:%v", agentHost, agentPort),
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 16,
		Handler:        router,
	}
	log.Printf("Serving on: http://%v:%v", agentHost, agentPort)

	return s.s.ListenAndServe()
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
