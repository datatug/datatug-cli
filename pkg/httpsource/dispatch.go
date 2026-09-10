package httpsource

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dal-go/dalgo2http"
)

// dispatchOptions carries per-request HTTP-source dispatch choices: which
// dalgo2http.Mode to run in, whether every live fetch must fail without
// touching the network (--http-offline), and the per-collection HTTP
// deadline. It travels on the context (see ContextWithDispatch) rather than
// as an Open/Option parameter or a BackendRef.Open argument, because Open is
// reached through pkg/dbcopy.BackendRef.Open and pkg/secureread.Executor.
// RunStructured — both shared by every backend (sqlite, ingitdb, http) and
// by call sites that have nothing to do with per-request HTTP dispatch
// (semantic/related lookups, the legacy exec/select route, the CLI's own
// `datatug query run`/`run-saved` commands). Changing those signatures to
// carry an HTTP-only concern would ripple through every one of them; the
// context side-channel is the same pattern dalgo2http.
// ContextWithProvenanceObserver already established in this exact call
// chain (see provenance.go's Recorder) for the identical reason.
type dispatchOptions struct {
	mode    dalgo2http.Mode
	offline bool
	timeout time.Duration
}

type dispatchContextKey struct{}

// ContextWithDispatch returns ctx carrying explicit HTTP-source dispatch
// options for Open to read instead of its own defaults.
//
//   - mode == "" keeps Open's own default (dalgo2http.ModeLive — see Open's
//     doc comment for why that default changed from ModeLiveThenSnapshot).
//   - offline, when true, makes Open wire a Client that fails every live
//     request without ever dialing (see offlineTransport) — `datatug serve
//     --http-offline`'s mechanism.
//   - timeout, when > 0, overrides BuildCollection's own defaultTimeout for
//     every collection Open builds from projectDir.
func ContextWithDispatch(ctx context.Context, mode dalgo2http.Mode, offline bool, timeout time.Duration) context.Context {
	return context.WithValue(ctx, dispatchContextKey{}, dispatchOptions{mode: mode, offline: offline, timeout: timeout})
}

func dispatchFromContext(ctx context.Context) dispatchOptions {
	if o, ok := ctx.Value(dispatchContextKey{}).(dispatchOptions); ok {
		return o
	}
	return dispatchOptions{}
}

// errHTTPOffline is what every live HTTP fetch fails with when `datatug
// serve --http-offline` is set: doLiveFetch wraps it with dalgo2http.
// ErrUpstream exactly like a real network error (see dal-go/dalgo2http
// v0.2.0's request.go doLiveFetch — any client.Do failure other than a
// blocked address or a refused redirect is wrapped that way), so it reaches
// pkg/server/endpoints' error classifier as an honest SOURCE_UNAVAILABLE
// live failure, never a distinct code that would let a caller tell
// "genuinely unreachable" apart from "operator disabled the network".
var errHTTPOffline = errors.New("httpsource: network disabled by --http-offline")

// offlineTransport is an http.RoundTripper that never dials: every request
// fails immediately with errHTTPOffline. This is --http-offline's whole
// mechanism — a real user-facing switch for offline demos, not a test hook
// (see cmd_serve.go's --http-offline flag) — built entirely from dalgo2http.
// Config's already-public Client field, so it needs no dalgo2http change.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, errHTTPOffline
}

// offlineClient is shared across every offline Open call: it holds no
// per-request state, so one instance is safe for concurrent use exactly
// like http.DefaultClient.
var offlineClient = &http.Client{Transport: offlineTransport{}}
