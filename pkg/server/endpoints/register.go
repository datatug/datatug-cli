package endpoints

import (
	"context"
	"log"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

type wrapper = func(f http.HandlerFunc) http.HandlerFunc

type RegisterMode = int

const (
	RegisterWriteOnlyHandlers RegisterMode = iota
	RegisterAllHandlers
)

// Capabilities gates whether this process's mutation routes register at
// all (api-contract.md "Security and errors": "this read journey must not
// expose an unauthenticated mutation endpoint as a side effect" —
// REQ:principal-selection: "Unneeded write endpoints MUST fail closed").
// AllowWrites defaults false: a `datatug serve` process serving the Phase 1
// read journey registers every mutation route behind requireWriteCapability
// (write_capability.go), which refuses with ACCESS_DENIED before any
// project file is touched, unless the caller explicitly opted in (see
// cmd_serve.go's --allow-writes flag).
type Capabilities struct {
	AllowWrites bool
	// AllowLiveConnections opens the routes that connect to a database server that the served
	// project records, under the identity of the person who runs the server (today
	// dbserver-databases, which lists the databases of a SQL Server server). It defaults false:
	// without it such a route answers 403 before anything is connected to (see
	// requireLiveConnections, and `datatug serve --allow-live-connections`).
	AllowLiveConnections bool
	// ServedHost and ServedPort are the host and the port that the server listens on. The Host
	// of a request must be a loopback name or address, or ServedHost, on ServedPort (see
	// RequestGuard). A ServedPort of 0 accepts any port.
	ServedHost string
	ServedPort int
}

// RegisterDatatugHandlers registers datatug HTTP handlers with every write
// route closed (Capabilities{}) — kept for callers (this package's own
// tests) that do not need write access. Real `datatug serve` startup uses
// RegisterDatatugHandlersWithCapabilities.
func RegisterDatatugHandlers(
	pathPrefix string,
	router *httprouter.Router,
	mode RegisterMode,
	wrap wrapper,
	contextProvider func(r *http.Request) (context.Context, error),
	handler Handler,
) {
	RegisterDatatugHandlersWithCapabilities(pathPrefix, router, mode, wrap, contextProvider, handler, Capabilities{})
}

// RegisterDatatugHandlersWithCapabilities is RegisterDatatugHandlers plus an
// explicit Capabilities gate for this process's mutation routes.
func RegisterDatatugHandlersWithCapabilities(
	pathPrefix string,
	router *httprouter.Router,
	mode RegisterMode,
	wrap wrapper,
	contextProvider func(r *http.Request) (context.Context, error),
	handler Handler,
	caps Capabilities,
) {
	log.Println("Registering DataTug handlers on pathPrefix:", pathPrefix)
	if handler == nil {
		panic("handler is not provided")
	}
	handle = withLocalOrigin(handler)
	getContextFromRequest = contextProvider
	registerRoutes(pathPrefix, router, wrap, mode == RegisterWriteOnlyHandlers, caps)
}

// Legacy apicore.Execute has a separate host list. Validate with this agent's
// one strict origin policy, then delegate every authentication/content check
// with Origin removed from a clone, preserving the approved response header.
func withLocalOrigin(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request, dto apicore.RequestDTO, options verify.RequestOptions, status int, contextProvider apicore.ContextProvider, worker apicore.Worker) {
		origins := r.Header.Values("Origin")
		if len(origins) > 0 {
			if len(origins) != 1 || !IsSupportedOrigin(origins[0]) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			writeCORSOrigin(w, r)
			r = r.Clone(r.Context())
			r.Header.Del("Origin")
		}
		next(w, r, dto, options, status, contextProvider, worker)
	}
}
