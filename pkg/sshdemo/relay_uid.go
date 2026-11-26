//go:build linux

package sshdemo

import (
	"math"
	"net"
	"strconv"
	"sync"
)

const maxUnidentifiedRelaySockets = 64

type relayAccept struct {
	conn net.Conn
	err  error
}

type uidRelayListener struct {
	net.Listener
	uidBase, uidCount int
	limit             connectionLimit
	lookup            func(net.Conn) (int, bool)
	unidentified      chan struct{}
	accepted          chan relayAccept
	done              chan struct{}
	startOnce         sync.Once
	closeOnce         sync.Once
}

func newUIDRelayListener(ln net.Listener, base, count, perUID int) *uidRelayListener {
	return &uidRelayListener{
		Listener: ln, uidBase: base, uidCount: count,
		limit:        newConnectionLimit(min(count, math.MaxInt/perUID)*perUID, perUID),
		lookup:       loopbackSocketUID,
		unidentified: make(chan struct{}, maxUnidentifiedRelaySockets),
		accepted:     make(chan relayAccept),
		done:         make(chan struct{}),
	}
}

func (l *uidRelayListener) Accept() (net.Conn, error) {
	l.startOnce.Do(func() { go l.acceptLoop() })
	select {
	case result := <-l.accepted:
		return result.conn, result.err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *uidRelayListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func (l *uidRelayListener) acceptLoop() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			// Let net/http apply its normal retry/backoff for accept errors.
			select {
			case l.accepted <- relayAccept{err: err}:
				continue
			case <-l.done:
				return
			}
		}
		select {
		case l.unidentified <- struct{}{}:
			go l.identifyAndAdmit(conn)
		default:
			_ = conn.Close()
		}
	}
}

// Admission runs before net/http reads bytes. A pending HTTP connection
// and a hijacked tunnel hold the same UID reservation until close.
func (l *uidRelayListener) identifyAndAdmit(conn net.Conn) {
	defer func() { <-l.unidentified }()
	uid, ok := l.lookup(conn)
	if ok && uidInRange(uid, l.uidBase, l.uidCount) {
		key := strconv.Itoa(uid)
		if l.limit.acquire(key) {
			conn = &cappedConn{Conn: conn, release: func() { l.limit.release(key) }}
			select {
			case l.accepted <- relayAccept{conn: conn}:
				return
			case <-l.done:
			}
		}
	}
	_ = conn.Close()
}
