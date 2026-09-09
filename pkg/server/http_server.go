package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
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
func newDatatugStoreFactory(pathsByID map[string]string) func(id string) (storage.Store, error) {
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
