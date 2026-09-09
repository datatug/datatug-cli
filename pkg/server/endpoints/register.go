package endpoints

import (
	"context"
	"log"
	"net/http"

	"github.com/julienschmidt/httprouter"
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
	handle = handler
	getContextFromRequest = contextProvider
	registerRoutes(pathPrefix, router, wrap, mode == RegisterWriteOnlyHandlers, caps)
}
