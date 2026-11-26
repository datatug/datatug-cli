//go:build linux

package sshdemo

import (
	"io"
	"net"
	"time"
)

const (
	transportWriteGrace  = 250 * time.Millisecond
	memoryPressureNotice = "This session is nearing its memory/storage allowance.\r\n"
	oomGroupNotice       = "This session ended because it hit its memory/storage allowance.\r\n"
)

func (g cgroup) memoryNearLimit() bool {
	current, err := g.counter("memory.current")
	if err != nil {
		return false
	}
	limit, err := g.counter("memory.max")
	return err == nil && current >= limit-limit/10
}

func (g cgroup) oomGroupKilledSince(before uint64) bool {
	count, err := g.event("memory.events", "oom_group_kill")
	return err == nil && count > before
}

func writeQuotaNotice(w io.Writer, conn net.Conn, message string) {
	withTransportDeadline(conn, func() { _, _ = io.WriteString(w, message) })
}

func withTransportDeadline(conn net.Conn, write func()) {
	// SSH channel flow control can block writes even when TCP is writable.
	done := make(chan struct{})
	go func() {
		write()
		close(done)
	}()
	timer := time.NewTimer(transportWriteGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = conn.Close()
	}
}
