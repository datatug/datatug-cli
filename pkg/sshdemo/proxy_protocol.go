package sshdemo

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/pires/go-proxyproto"
)

func proxyConnectionPolicy(cidrs []string) (proxyproto.ConnPolicyFunc, error) {
	var trusted []netip.Prefix
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("sshdemo: invalid --proxy-protocol-trust CIDR %q: %w", cidr, err)
		}
		trusted = append(trusted, prefix)
	}
	return func(opts proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
		peer, ok := opts.Upstream.(*net.TCPAddr)
		if !ok {
			return proxyproto.REQUIRE, proxyproto.ErrInvalidUpstream
		}
		ip := peer.AddrPort().Addr().Unmap()
		if ip.IsLoopback() || len(trusted) == 0 {
			return proxyproto.REQUIRE, nil
		}
		for _, prefix := range trusted {
			if prefix.Contains(ip) {
				return proxyproto.REQUIRE, nil
			}
		}
		return proxyproto.REQUIRE, proxyproto.ErrInvalidUpstream
	}, nil
}

func requireLocalProxyHeader(header *proxyproto.Header) error {
	if header.Version != 2 || header.Command != proxyproto.LOCAL {
		return errors.New("sshdemo: loopback connections require a PROXY v2 LOCAL header")
	}
	return nil
}
