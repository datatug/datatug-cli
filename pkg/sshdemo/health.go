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
	if addr == "" {
		addr = DefaultHealthAddr
	}
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("ssh-health: connect %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	deadline, ok := dialCtx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}
	_ = conn.SetReadDeadline(deadline)

	line, err := bufio.NewReader(io.LimitReader(conn, maxBannerBytes)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("ssh-health: read banner from %s: %w", addr, err)
	}
	if !strings.HasPrefix(line, "SSH-2.0-") {
		return fmt.Errorf("ssh-health: unexpected banner from %s: %q", addr, strings.TrimSpace(line))
	}
	return nil
}
