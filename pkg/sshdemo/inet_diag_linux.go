package sshdemo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

const (
	inetDiagReqV2Size = 56
	inetDiagMsgSize   = 72
	inetDiagTupleSize = 36 // ports followed by two 16-byte addresses
	inetDiagSequence  = 1  // one request per netlink socket
)

var (
	diagSocket     = unix.Socket
	diagClose      = unix.Close
	diagSetTimeout = unix.SetsockoptTimeval
	diagSend       = unix.Sendto
	diagReceive    = unix.Recvmsg
)

func loopbackSocketUID(conn net.Conn) (int, bool) {
	peer := conn.RemoteAddr().(*net.TCPAddr).AddrPort()
	local := conn.LocalAddr().(*net.TCPAddr).AddrPort()
	return lookupSocketUID(peer, local, querySocketUID)
}

func lookupSocketUID(peer, local netip.AddrPort, query func(byte, netip.AddrPort, netip.AddrPort) (int, error)) (int, bool) {
	peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
	local = netip.AddrPortFrom(local.Addr().Unmap(), local.Port())
	if !peer.Addr().IsLoopback() || !local.Addr().IsLoopback() || peer.Addr().Is4() != local.Addr().Is4() {
		return 0, false
	}
	if peer.Addr().Is4() {
		uid, err := query(unix.AF_INET, peer, local)
		if !errors.Is(err, unix.ENOENT) {
			return uid, err == nil
		}
		// A dual-stack client can connect to the IPv4 listener with a mapped
		// IPv6 socket. Try the same exact tuple in AF_INET6 if necessary.
	}
	uid, err := query(unix.AF_INET6, peer, local)
	return uid, err == nil
}

func querySocketUID(family byte, peer, local netip.AddrPort) (int, error) {
	fd, err := diagSocket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return 0, err
	}
	defer func() { _ = diagClose(fd) }()
	timeout := unix.Timeval{Usec: 200_000}
	if err := diagSetTimeout(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout); err != nil {
		return 0, err
	}
	request := inetDiagRequest(family, peer, local)
	if err := diagSend(fd, request[:], unix.MSG_DONTWAIT, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, err
	}
	var reply [512]byte
	n, _, flags, from, err := diagReceive(fd, reply[:], nil, 0)
	if err != nil {
		return 0, err
	}
	kernel, ok := from.(*unix.SockaddrNetlink)
	if !ok || kernel.Pid != 0 || flags&unix.MSG_TRUNC != 0 {
		return 0, unix.EPROTO
	}
	return parseInetDiagUID(reply[:n], peer, local)
}

func inetDiagRequest(family byte, peer, local netip.AddrPort) [unix.NLMSG_HDRLEN + inetDiagReqV2Size]byte {
	var request [unix.NLMSG_HDRLEN + inetDiagReqV2Size]byte
	binary.NativeEndian.PutUint32(request[:4], uint32(len(request)))
	binary.NativeEndian.PutUint16(request[4:6], unix.SOCK_DIAG_BY_FAMILY)
	binary.NativeEndian.PutUint16(request[6:8], unix.NLM_F_REQUEST) // exact lookup, no dump
	binary.NativeEndian.PutUint32(request[8:12], inetDiagSequence)
	body := request[unix.NLMSG_HDRLEN:]
	body[0], body[1] = family, unix.IPPROTO_TCP
	binary.NativeEndian.PutUint32(body[4:8], math.MaxUint32) // idiag_states
	tuple := inetDiagTuple(family, peer, local)
	copy(body[8:], tuple[:])
	// The binding interface is zero for loopback. The lookup has no cookie.
	binary.NativeEndian.PutUint32(body[48:52], math.MaxUint32)
	binary.NativeEndian.PutUint32(body[52:56], math.MaxUint32)
	return request
}

func inetDiagTuple(family byte, peer, local netip.AddrPort) [inetDiagTupleSize]byte {
	var tuple [inetDiagTupleSize]byte
	binary.BigEndian.PutUint16(tuple[:2], peer.Port())
	binary.BigEndian.PutUint16(tuple[2:4], local.Port())
	if family == unix.AF_INET {
		src, dst := peer.Addr().As4(), local.Addr().As4()
		copy(tuple[4:8], src[:])
		copy(tuple[20:24], dst[:])
	} else {
		src, dst := peer.Addr().As16(), local.Addr().As16()
		copy(tuple[4:20], src[:])
		copy(tuple[20:36], dst[:])
	}
	return tuple
}

func parseInetDiagUID(reply []byte, peer, local netip.AddrPort) (int, error) {
	if len(reply) < unix.NLMSG_HDRLEN || binary.NativeEndian.Uint32(reply[:4]) != uint32(len(reply)) ||
		binary.NativeEndian.Uint32(reply[8:12]) != inetDiagSequence {
		return 0, unix.EPROTO
	}
	kind := binary.NativeEndian.Uint16(reply[4:6])
	body := reply[unix.NLMSG_HDRLEN:]
	if kind == unix.NLMSG_ERROR && len(body) >= 4 {
		if errno := int32(binary.NativeEndian.Uint32(body[:4])); errno < 0 {
			return 0, unix.Errno(-errno)
		}
	}
	if kind != unix.SOCK_DIAG_BY_FAMILY || len(body) < inetDiagMsgSize {
		return 0, unix.EPROTO
	}

	family := body[0]
	if family != unix.AF_INET6 && (family != unix.AF_INET || !peer.Addr().Is4()) {
		return 0, unix.EPROTO
	}
	tuple := inetDiagTuple(family, peer, local)
	uid := binary.NativeEndian.Uint32(body[64:68])
	inode := binary.NativeEndian.Uint32(body[68:72])
	if !bytes.Equal(body[4:4+inetDiagTupleSize], tuple[:]) || inode == 0 || uint64(uid) > uint64(math.MaxInt) {
		return 0, unix.EPROTO
	}
	return int(uid), nil
}
