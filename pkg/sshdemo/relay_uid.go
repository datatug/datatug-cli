//go:build linux

package sshdemo

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type uidRelayListener struct {
	net.Listener
	uidBase, uidCount int
	limit             connectionLimit
	lookup            func(net.Conn) (int, bool)
}

func newUIDRelayListener(ln net.Listener, base, count, perUID int) *uidRelayListener {
	return &uidRelayListener{
		Listener: ln, uidBase: base, uidCount: count,
		limit:  newConnectionLimit(min(count, math.MaxInt/perUID)*perUID, perUID),
		lookup: loopbackSocketUID,
	}
}

// Admit before net/http starts a handler or reads any bytes. A pending
// connection and a hijacked tunnel hold the same UID reservation until close.
func (l *uidRelayListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, ok := l.lookup(conn)
		if ok && uidInRange(uid, l.uidBase, l.uidCount) {
			key := strconv.Itoa(uid)
			if l.limit.acquire(key) {
				return &cappedConn{Conn: conn, release: func() { l.limit.release(key) }}, nil
			}
		}
		_ = conn.Close()
	}
}

func loopbackSocketUID(conn net.Conn) (int, bool) {
	peer := conn.RemoteAddr().(*net.TCPAddr).AddrPort()
	local := conn.LocalAddr().(*net.TCPAddr).AddrPort()
	return socketUIDIn("/proc/net", peer, local)
}

func socketUIDIn(procNet string, peer, local netip.AddrPort) (int, bool) {
	peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
	local = netip.AddrPortFrom(local.Addr().Unmap(), local.Port())
	for _, table := range []struct {
		name string
		ipv6 bool
	}{{"tcp", false}, {"tcp6", true}} {
		if !table.ipv6 && (!peer.Addr().Is4() || !local.Addr().Is4()) {
			continue
		}
		file, err := os.Open(filepath.Join(procNet, table.name))
		if err != nil {
			continue
		}
		uid, found := scanSocketUID(file, procSocketAddress(peer, table.ipv6), procSocketAddress(local, table.ipv6))
		_ = file.Close()
		if found {
			return uid, true
		}
	}
	return 0, false
}

// Linux renders each 32-bit word of the address in native byte order.
// As16 maps IPv4 endpoints for entries from IPv6 client sockets.
func procSocketAddress(addr netip.AddrPort, ipv6 bool) string {
	if !ipv6 {
		ip := addr.Addr().As4()
		return fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32(ip[:]), addr.Port())
	}
	ip := addr.Addr().As16()
	return fmt.Sprintf("%08X%08X%08X%08X:%04X",
		binary.NativeEndian.Uint32(ip[:4]), binary.NativeEndian.Uint32(ip[4:8]),
		binary.NativeEndian.Uint32(ip[8:12]), binary.NativeEndian.Uint32(ip[12:]), addr.Port())
}

func scanSocketUID(r io.Reader, peer, local string) (int, bool) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// Match the client side's full tuple. Ignore rows without a live
		// socket inode, where the kernel may report a placeholder UID.
		if len(fields) >= 10 && fields[1] == peer && fields[2] == local && fields[9] != "0" {
			uid, err := strconv.ParseUint(fields[7], 10, strconv.IntSize-1)
			return int(uid), err == nil
		}
	}
	return 0, false
}
