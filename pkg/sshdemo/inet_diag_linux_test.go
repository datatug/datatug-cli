package sshdemo

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

var (
	diagPeer  = netip.MustParseAddrPort("127.0.0.7:12345")
	diagLocal = netip.MustParseAddrPort("127.0.0.1:23456")
)

func diagReply(family byte) []byte {
	reply := make([]byte, unix.NLMSG_HDRLEN+inetDiagMsgSize)
	binary.NativeEndian.PutUint32(reply[:4], uint32(len(reply)))
	binary.NativeEndian.PutUint16(reply[4:6], unix.SOCK_DIAG_BY_FAMILY)
	binary.NativeEndian.PutUint32(reply[8:12], inetDiagSequence)
	body := reply[unix.NLMSG_HDRLEN:]
	body[0] = family
	tuple := inetDiagTuple(family, diagPeer, diagLocal)
	copy(body[4:], tuple[:])
	binary.NativeEndian.PutUint32(body[64:68], 20000)
	binary.NativeEndian.PutUint32(body[68:72], 123456)
	return reply
}

func TestInetDiagExactRequestAndReply(t *testing.T) {
	req := inetDiagRequest(unix.AF_INET, diagPeer, diagLocal)
	if binary.NativeEndian.Uint32(req[:4]) != uint32(len(req)) ||
		binary.NativeEndian.Uint16(req[4:6]) != unix.SOCK_DIAG_BY_FAMILY ||
		binary.NativeEndian.Uint16(req[6:8]) != unix.NLM_F_REQUEST ||
		req[16] != unix.AF_INET || req[17] != unix.IPPROTO_TCP ||
		binary.BigEndian.Uint16(req[24:26]) != 12345 || binary.BigEndian.Uint16(req[26:28]) != 23456 ||
		req[28] != 127 || req[31] != 7 || req[44] != 127 || req[47] != 1 {
		t.Fatalf("request did not contain the exact client tuple: %x", req)
	}
	for _, family := range []byte{unix.AF_INET, unix.AF_INET6} {
		reply := diagReply(family)
		uid, err := parseInetDiagUID(reply, diagPeer, diagLocal)
		if err != nil || uid != 20000 {
			t.Fatalf("family %d: uid = %d, err = %v", family, uid, err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
		want   error
	}{
		{"short header", func(b []byte) []byte { return b[:10] }, unix.EPROTO},
		{"wrong sequence", func(b []byte) []byte { b[8] = 0; return b }, unix.EPROTO},
		{"missing body", func(b []byte) []byte {
			b = b[:20]
			binary.NativeEndian.PutUint32(b[:4], uint32(len(b)))
			return b
		}, unix.EPROTO},
		{"unrelated port", func(b []byte) []byte { b[20] ^= 1; return b }, unix.EPROTO},
		{"no inode", func(b []byte) []byte { clear(b[len(b)-4:]); return b }, unix.EPROTO},
		{"unexpected family", func(b []byte) []byte { b[16] = unix.AF_UNIX; return b }, unix.EPROTO},
		{"kernel error", func(b []byte) []byte {
			binary.NativeEndian.PutUint16(b[4:6], unix.NLMSG_ERROR)
			errno := -int32(unix.ENOENT)
			binary.NativeEndian.PutUint32(b[16:20], uint32(errno))
			return b
		}, unix.ENOENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseInetDiagUID(tc.mutate(diagReply(unix.AF_INET)), diagPeer, diagLocal)
			if !errors.Is(err, tc.want) {
				t.Fatalf("parse error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSocketUIDFamilies(t *testing.T) {
	v6Peer := netip.MustParseAddrPort("[::1]:12345")
	v6Local := netip.MustParseAddrPort("[::1]:23456")
	for _, tc := range []struct {
		name         string
		peer, local  netip.AddrPort
		firstErr     error
		want4, want6 bool
		wantUID      bool
	}{
		{"IPv4", diagPeer, diagLocal, nil, true, false, true},
		{"mapped", diagPeer, diagLocal, unix.ENOENT, true, true, true},
		{"IPv6", v6Peer, v6Local, nil, false, true, true},
		{"lookup error", diagPeer, diagLocal, unix.EPERM, true, false, false},
		{"mixed", diagPeer, v6Local, nil, false, false, false},
		{"non-loopback", netip.MustParseAddrPort("192.0.2.1:42"), diagLocal, nil, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got4, got6 bool
			uid, ok := lookupSocketUID(tc.peer, tc.local, func(family byte, src, dst netip.AddrPort) (int, error) {
				if src != tc.peer || dst != tc.local {
					t.Fatalf("tuple = %v to %v", src, dst)
				}
				if family == unix.AF_INET {
					got4 = true
					return 20000, tc.firstErr
				}
				got6 = true
				return 20000, nil
			})
			if ok != tc.wantUID || ok && uid != 20000 || got4 != tc.want4 || got6 != tc.want6 {
				t.Fatalf("got uid (%d, %t), IPv4 %t, IPv6 %t", uid, ok, got4, got6)
			}
		})
	}
}

func TestInetDiagTransportFailures(t *testing.T) {
	socket, closeFD, setTimeout, send, receive := diagSocket, diagClose, diagSetTimeout, diagSend, diagReceive
	t.Cleanup(func() {
		diagSocket, diagClose, diagSetTimeout, diagSend, diagReceive = socket, closeFD, setTimeout, send, receive
	})
	for _, failure := range []string{"socket", "timeout", "send", "receive", "not netlink", "not kernel", "truncated"} {
		t.Run(failure, func(t *testing.T) {
			fail := func(step string) error {
				if step == failure {
					return errTestIO
				}
				return nil
			}
			closed := false
			diagSocket = func(int, int, int) (int, error) { return 42, fail("socket") }
			diagClose = func(fd int) error { closed = fd == 42; return nil }
			diagSetTimeout = func(_ int, level, opt int, timeout *unix.Timeval) error {
				if level != unix.SOL_SOCKET || opt != unix.SO_RCVTIMEO || timeout.Usec != 200_000 {
					t.Fatal("missing receive deadline")
				}
				return fail("timeout")
			}
			diagSend = func(_ int, _ []byte, flags int, _ unix.Sockaddr) error {
				if flags != unix.MSG_DONTWAIT {
					t.Fatal("netlink send can block")
				}
				return fail("send")
			}
			diagReceive = func(int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error) {
				switch failure {
				case "not netlink":
					return 0, 0, 0, &unix.SockaddrInet4{}, nil
				case "not kernel":
					return 0, 0, 0, &unix.SockaddrNetlink{Pid: 7}, nil
				case "truncated":
					return 0, 0, unix.MSG_TRUNC, &unix.SockaddrNetlink{}, nil
				default:
					return 0, 0, 0, nil, fail("receive")
				}
			}
			_, err := querySocketUID(unix.AF_INET, diagPeer, diagLocal)
			if err == nil || closed != (failure != "socket") {
				t.Fatalf("error = %v, fd closed = %t", err, closed)
			}
		})
	}
}

func TestInetDiagLiveSocket(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		t.Run(network, func(t *testing.T) {
			addr := "127.0.0.1:0"
			if network == "tcp6" {
				addr = "[::1]:0"
			}
			ln, err := net.Listen(network, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			peer := proxyDial(t, ln.Addr().String())
			defer func() { _ = peer.Close() }()
			conn, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if uid, ok := loopbackSocketUID(conn); !ok || uid != os.Getuid() {
				t.Fatalf("kernel reported uid (%d, %t), want %d", uid, ok, os.Getuid())
			}
		})
	}
}

func TestInetDiagLiveMappedSocket(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0); err != nil {
		t.Fatal(err)
	}
	target := ln.Addr().(*net.TCPAddr).AddrPort()
	mapped := &unix.SockaddrInet6{Port: int(target.Port()), Addr: target.Addr().As16()}
	if err := unix.Connect(fd, mapped); err != nil {
		t.Fatal(err)
	}
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if uid, ok := loopbackSocketUID(conn); !ok || uid != os.Getuid() {
		t.Fatalf("kernel reported mapped socket uid (%d, %t), want %d", uid, ok, os.Getuid())
	}
}
