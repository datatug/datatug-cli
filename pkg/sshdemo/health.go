//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const maxBannerBytes = 1024

// CheckHealth dials addr over TCP and verifies that the server answers with a
// banner line starting with "SSH-2.0-".
func CheckHealth(ctx context.Context, addr string, timeout time.Duration) error {
	return checkHealth(ctx, addr, timeout, nil)
}

// CheckHealthProxyLocal probes a server that listens with --proxy-protocol:
// it first sends a PROXY v2 LOCAL header, the command the PROXY protocol
// defines for health checks (the server keeps the connection's real address).
func CheckHealthProxyLocal(ctx context.Context, addr string, timeout time.Duration) error {
	return checkHealth(ctx, addr, timeout, EncodeProxyV2(nil, nil))
}

func checkHealth(ctx context.Context, addr string, timeout time.Duration, prelude []byte) error {
	return checkHealthWithDialer(ctx, addr, timeout, prelude, (&net.Dialer{}).DialContext)
}

func checkHealthWithDialer(ctx context.Context, addr string, timeout time.Duration, prelude []byte, dial func(context.Context, string, string) (net.Conn, error)) error {
	if addr == "" {
		addr = DefaultHealthAddr
	}
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dial(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("ssh-health: connect %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	deadline, _ := dialCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	if len(prelude) > 0 {
		if _, err := conn.Write(prelude); err != nil {
			return fmt.Errorf("ssh-health: send PROXY header to %s: %w", addr, err)
		}
	}

	line, err := bufio.NewReader(io.LimitReader(conn, maxBannerBytes)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("ssh-health: read banner from %s: %w", addr, err)
	}
	if !strings.HasPrefix(line, "SSH-2.0-") {
		return fmt.Errorf("ssh-health: unexpected banner from %s: %q", addr, strings.TrimSpace(line))
	}
	return nil
}
