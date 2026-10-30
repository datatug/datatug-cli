package apimonitor

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

type response struct {
	http.ResponseWriter
	status int
	state  *requestState
}

func (r *response) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *response) WriteHeader(status int) {
	if r.status != 0 {
		return
	} // A final status was already committed or observed.
	r.ResponseWriter.WriteHeader(status)
	if status >= 200 || status == http.StatusSwitchingProtocols {
		r.status = status
	}
}
func (r *response) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(p)
}

type flushFunc func()

func (f flushFunc) Flush() { f() }

type flushErrorFunc func() error

func (f flushErrorFunc) FlushError() error { return f() }

type hijackFunc func() (net.Conn, *bufio.ReadWriter, error)

func (f hijackFunc) Hijack() (net.Conn, *bufio.ReadWriter, error) { return f() }

type pushFunc func(string, *http.PushOptions) error

func (f pushFunc) Push(target string, o *http.PushOptions) error { return f(target, o) }

type readFromFunc func(io.Reader) (int64, error)

func (f readFromFunc) ReadFrom(src io.Reader) (int64, error) { return f(src) }

type httpFlushError interface{ FlushError() error }
type httpUnwrapper interface{ Unwrap() http.ResponseWriter }

type trackedConn struct {
	net.Conn
	state *requestState
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.state.finishHijacked()
	return err
}

type capabilityDiscovery uint8

const (
	capabilityAbsent capabilityDiscovery = iota
	capabilityFound
	capabilityIndeterminate
)

// visitResponseWriters follows Unwrap metadata with a hard bound. Reaching the
// bound is indeterminate, not absent: callers must intercept tracked operations
// with ErrNotSupported instead of letting ResponseController bypass the facade.
func visitResponseWriters(w http.ResponseWriter, visit func(http.ResponseWriter) bool) capabilityDiscovery {
	for depth := 0; depth < 64; depth++ {
		if w == nil {
			return capabilityAbsent
		}
		if visit(w) {
			return capabilityFound
		}
		u, ok := w.(httpUnwrapper)
		if !ok {
			return capabilityAbsent
		}
		w = u.Unwrap()
	}
	return capabilityIndeterminate
}

// downstreamHijacker advertises Hijacker only when a real downstream
// implementation exists, including through standard Unwrap metadata. It reports
// indeterminate chains separately so wrapping cannot expose an untracked leaf.
func downstreamHijacker(w http.ResponseWriter) (h http.Hijacker, discovery capabilityDiscovery) {
	discovery = visitResponseWriters(w, func(current http.ResponseWriter) bool { h, _ = current.(http.Hijacker); return h != nil })
	return h, discovery
}

// downstreamFlush selects the first layer that supports either flush form,
// preserving ResponseController's per-layer FlushError-before-Flusher priority.
func downstreamFlush(w http.ResponseWriter) (f http.Flusher, fe httpFlushError, fok, feok bool, discovery capabilityDiscovery) {
	discovery = visitResponseWriters(w, func(current http.ResponseWriter) bool {
		fe, feok = current.(httpFlushError)
		f, fok = current.(http.Flusher)
		return feok || fok
	})
	return f, fe, fok, feok, discovery
}

func (r *response) wrap() http.ResponseWriter {
	bits := 0
	f, fe, fok, feok, flushDiscovery := downstreamFlush(r.ResponseWriter)
	if fok {
		bits |= 1
	}
	if feok || flushDiscovery == capabilityIndeterminate {
		bits |= 2
	}
	h, hijackDiscovery := downstreamHijacker(r.ResponseWriter)
	if hijackDiscovery == capabilityFound || hijackDiscovery == capabilityIndeterminate {
		bits |= 4
	}
	p, pok := r.ResponseWriter.(http.Pusher)
	if pok {
		bits |= 8
	}
	q, qok := r.ResponseWriter.(io.ReaderFrom)
	if qok {
		bits |= 16
	}
	ff := flushFunc(func() {
		f.Flush()
		if r.status == 0 {
			r.status = http.StatusOK
		}
	})
	fef := flushErrorFunc(func() error {
		if flushDiscovery == capabilityIndeterminate {
			return http.ErrNotSupported
		}
		err := fe.FlushError()
		// A successful flush commits the underlying writer's implicit 200.
		// Record that observation without forwarding a second WriteHeader.
		if err == nil && r.status == 0 {
			r.status = http.StatusOK
		}
		return err
	})
	hh := hijackFunc(func() (net.Conn, *bufio.ReadWriter, error) {
		if hijackDiscovery != capabilityFound || h == nil {
			return nil, nil, http.ErrNotSupported
		}
		c, b, err := h.Hijack()
		if err == nil && r.state != nil {
			r.state.markHijacked(r.status)
			if c != nil {
				c = &trackedConn{Conn: c, state: r.state}
			}
		}
		return c, b, err
	})
	pp := pushFunc(func(t string, o *http.PushOptions) error { return p.Push(t, o) })
	qq := readFromFunc(func(src io.Reader) (int64, error) {
		if r.status == 0 {
			r.WriteHeader(http.StatusOK)
		}
		return q.ReadFrom(src)
	})
	switch bits {
	default:
		return r
	case 1:
		return struct {
			*response
			http.Flusher
		}{r, ff}
	case 2:
		return struct {
			*response
			httpFlushError
		}{r, fef}
	case 3:
		return struct {
			*response
			http.Flusher
			httpFlushError
		}{r, ff, fef}
	case 4:
		return struct {
			*response
			http.Hijacker
		}{r, hh}
	case 5:
		return struct {
			*response
			http.Flusher
			http.Hijacker
		}{r, ff, hh}
	case 6:
		return struct {
			*response
			httpFlushError
			http.Hijacker
		}{r, fef, hh}
	case 7:
		return struct {
			*response
			http.Flusher
			httpFlushError
			http.Hijacker
		}{r, ff, fef, hh}
	case 8:
		return struct {
			*response
			http.Pusher
		}{r, pp}
	case 9:
		return struct {
			*response
			http.Flusher
			http.Pusher
		}{r, ff, pp}
	case 10:
		return struct {
			*response
			httpFlushError
			http.Pusher
		}{r, fef, pp}
	case 11:
		return struct {
			*response
			http.Flusher
			httpFlushError
			http.Pusher
		}{r, ff, fef, pp}
	case 12:
		return struct {
			*response
			http.Hijacker
			http.Pusher
		}{r, hh, pp}
	case 13:
		return struct {
			*response
			http.Flusher
			http.Hijacker
			http.Pusher
		}{r, ff, hh, pp}
	case 14:
		return struct {
			*response
			httpFlushError
			http.Hijacker
			http.Pusher
		}{r, fef, hh, pp}
	case 15:
		return struct {
			*response
			http.Flusher
			httpFlushError
			http.Hijacker
			http.Pusher
		}{r, ff, fef, hh, pp}
	case 16:
		return struct {
			*response
			io.ReaderFrom
		}{r, qq}
	case 17:
		return struct {
			*response
			http.Flusher
			io.ReaderFrom
		}{r, ff, qq}
	case 18:
		return struct {
			*response
			httpFlushError
			io.ReaderFrom
		}{r, fef, qq}
	case 19:
		return struct {
			*response
			http.Flusher
			httpFlushError
			io.ReaderFrom
		}{r, ff, fef, qq}
	case 20:
		return struct {
			*response
			http.Hijacker
			io.ReaderFrom
		}{r, hh, qq}
	case 21:
		return struct {
			*response
			http.Flusher
			http.Hijacker
			io.ReaderFrom
		}{r, ff, hh, qq}
	case 22:
		return struct {
			*response
			httpFlushError
			http.Hijacker
			io.ReaderFrom
		}{r, fef, hh, qq}
	case 23:
		return struct {
			*response
			http.Flusher
			httpFlushError
			http.Hijacker
			io.ReaderFrom
		}{r, ff, fef, hh, qq}
	case 24:
		return struct {
			*response
			http.Pusher
			io.ReaderFrom
		}{r, pp, qq}
	case 25:
		return struct {
			*response
			http.Flusher
			http.Pusher
			io.ReaderFrom
		}{r, ff, pp, qq}
	case 26:
		return struct {
			*response
			httpFlushError
			http.Pusher
			io.ReaderFrom
		}{r, fef, pp, qq}
	case 27:
		return struct {
			*response
			http.Flusher
			httpFlushError
			http.Pusher
			io.ReaderFrom
		}{r, ff, fef, pp, qq}
	case 28:
		return struct {
			*response
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{r, hh, pp, qq}
	case 29:
		return struct {
			*response
			http.Flusher
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{r, ff, hh, pp, qq}
	case 30:
		return struct {
			*response
			httpFlushError
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{r, fef, hh, pp, qq}
	case 31:
		return struct {
			*response
			http.Flusher
			httpFlushError
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{r, ff, fef, hh, pp, qq}
	}
}
