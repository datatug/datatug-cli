//go:build linux

package sshdemo

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	gossh "golang.org/x/crypto/ssh"
)

const (
	queueRefreshInterval = time.Second
	clearQueueScreen     = "\x1b[2J\x1b[H"
	queueTimedOut        = "max-wait"
	queueQuit            = "quit"
	queueWriteFailed     = "write-failed"
)

type slotAdmission struct {
	slot     Slot
	at       time.Time
	deadline time.Time
}

type queueEntry struct {
	clientIP    string
	joined      time.Time
	expires     time.Time
	duration    time.Duration
	position    int
	width       atomic.Int32
	done        chan struct{}
	refresh     chan struct{}
	admission   slotAdmission
	leaveReason string
}

func (m *SlotManager) admitOrQueue(ip string, cfg Config, now time.Time) (slotAdmission, *queueEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.queueClosed {
		return slotAdmission{}, nil, ErrGlobalLimit
	}
	if len(m.waiting) == 0 {
		admission, err := m.admit(ip, cfg.MaxSessionDuration, now)
		if err == nil {
			return admission, nil, nil
		}
	}
	if len(m.waiting) >= cfg.MaxQueue {
		return slotAdmission{}, nil, ErrGlobalLimit
	}
	entry := &queueEntry{
		clientIP: ip, joined: now, expires: now.Add(cfg.MaxQueueWait), duration: cfg.MaxSessionDuration,
		position: len(m.waiting) + 1, done: make(chan struct{}), refresh: make(chan struct{}, 1),
	}
	m.waiting = append(m.waiting, entry)
	return slotAdmission{}, entry, nil
}

func (m *SlotManager) admit(ip string, duration time.Duration, now time.Time) (slotAdmission, error) {
	deadline := now.Add(duration)
	slot, err := m.reserveSlot(ip, deadline)
	return slotAdmission{slot: slot, at: now, deadline: deadline}, err
}

// admitWaiting runs under the slot lock, including when Release frees a slot
// or a cleanup or memory recheck makes it usable. The reservation belongs to
// the oldest live waiter before any new arrival can inspect the free slots.
func (m *SlotManager) admitWaiting(now time.Time) {
	changed := false
	for len(m.waiting) != 0 {
		head := m.waiting[0]
		if !now.Before(head.expires) {
			head.leaveReason = queueTimedOut
		} else {
			admission, err := m.admit(head.clientIP, head.duration, now)
			if err != nil {
				break
			}
			head.admission = admission
		}
		m.waiting = slices.Delete(m.waiting, 0, 1)
		close(head.done)
		changed = true
	}
	if changed {
		m.refreshQueue()
	}
}

func (m *SlotManager) leaveQueue(entry *queueEntry, reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := slices.Index(m.waiting, entry); i >= 0 {
		m.waiting = slices.Delete(m.waiting, i, i+1)
		entry.leaveReason = reason
		close(entry.done)
		m.refreshQueue()
		return true
	}
	return false
}

func (m *SlotManager) stopQueue() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queueClosed = true
	for _, entry := range m.waiting {
		entry.leaveReason = EndReasonShutdown
		close(entry.done)
	}
	m.waiting = nil
}

// refreshQueue runs under the slot lock. One pending notification is enough
// because each redraw gets a fresh snapshot.
func (m *SlotManager) refreshQueue() {
	for _, entry := range m.waiting {
		select {
		case entry.refresh <- struct{}{}:
		default:
		}
	}
}

func (m *SlotManager) queueEstimate(entry *queueEntry, now time.Time) (int, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := slices.Index(m.waiting, entry)
	if index < 0 {
		return 0, time.Time{}
	}
	releases := make([]time.Time, 0, len(m.active))
	for i, state := range m.quarantined {
		switch state {
		case slotUsable:
			release := now
			if active := m.active[i]; active != nil && !active.deadline.IsZero() {
				release = active.deadline
			}
			releases = append(releases, release)
		case slotAwaitingCleanup, slotAwaitingMemory:
			releases = append(releases, now.Add(cleanupRetryInterval))
		}
	}
	if len(releases) == 0 {
		return index + 1, time.Time{}
	}
	slices.SortFunc(releases, time.Time.Compare)
	waves := min(time.Duration(index/len(releases)), math.MaxInt64/entry.duration)
	return index + 1, releases[index%len(releases)].Add(waves * entry.duration)
}

func queueScreen(sessions, position, width int, now, estimate time.Time) string {
	sessionText := fmt.Sprintf("all %d sessions are in use.", sessions)
	if sessions == 1 {
		sessionText = "the only session is in use."
	}
	ahead := "nobody"
	switch position {
	case 1:
	case 2:
		ahead = "1 person"
	default:
		ahead = fmt.Sprintf("%d people", position-1)
	}
	estimatedStart := "Estimated start: unknown"
	if !estimate.IsZero() {
		// An estimate, not a promise: cleanup and memory release can take
		// longer than their retry interval, and sessions can end early.
		estimatedStart = "Estimated start: any moment now."
		if remaining := estimate.Sub(now); remaining > 0 {
			seconds := int64((remaining-1)/time.Second) + 1
			estimatedStart = fmt.Sprintf("Estimated start: in about %02d:%02d (around %s UTC).",
				seconds/60, seconds%60, estimate.UTC().Format("15:04"))
		}
	}
	var screen strings.Builder
	screen.WriteString(clearQueueScreen)
	for _, line := range []string{
		"DataTug demo — " + sessionText,
		fmt.Sprintf("You are number %d in the queue (%s ahead of you).", position, ahead),
		estimatedStart,
		"Your session will start here automatically. Press q to leave the queue.",
	} {
		column := 0
		for _, word := range strings.Fields(line) {
			wordWidth := utf8.RuneCountInString(word)
			if column != 0 && column+1+wordWidth > max(40, width) {
				screen.WriteString("\r\n")
				column = 0
			}
			if column != 0 {
				screen.WriteByte(' ')
				column++
			}
			screen.WriteString(word)
			column += wordWidth
		}
		screen.WriteString("\r\n")
	}
	return screen.String()
}

func queueTimeoutMessage(wait time.Duration) string {
	duration := wait.String()
	switch {
	case wait == time.Minute:
		duration = "1 minute"
	case wait%time.Minute == 0:
		duration = fmt.Sprintf("%d minutes", wait/time.Minute)
	}
	return fmt.Sprintf("\r\nSorry, no session became free within %s. Please try again later.\r\n", duration)
}

func (s *Server) tickQueue(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			s.queueWritesMu.Lock()
			for conn := range s.queueWrites {
				_ = conn.Close()
			}
			s.queueWrites = nil
			s.queueWritesMu.Unlock()
			return
		case <-ticks:
			now := time.Now()
			s.queueWritesMu.Lock()
			for conn, deadline := range s.queueWrites {
				if !now.Before(deadline) {
					_ = conn.Close()
					delete(s.queueWrites, conn)
				}
			}
			s.queueWritesMu.Unlock()
			s.slots.mu.Lock()
			s.slots.refreshQueue()
			s.slots.mu.Unlock()
		}
	}
}

// queueOutput uses the same transport-close fallback as the quota notice: a
// TCP write deadline alone cannot interrupt an SSH channel's flow-control wait.
// The shared queue ticker enforces the deadline, including during handoff.
func (s *Server) queueOutput(conn net.Conn, write func() error) error {
	s.queueWritesMu.Lock()
	if s.queueWrites == nil {
		s.queueWritesMu.Unlock()
		_ = conn.Close()
		return net.ErrClosed
	}
	s.queueWrites[conn] = time.Now().Add(transportWriteGrace)
	s.queueWritesMu.Unlock()
	defer func() {
		s.queueWritesMu.Lock()
		delete(s.queueWrites, conn)
		s.queueWritesMu.Unlock()
	}()
	return write()
}

func (s *Server) queueWrite(conn net.Conn, w io.Writer, message string) error {
	return s.queueOutput(conn, func() error {
		_, err := io.WriteString(w, message)
		return err
	})
}

// waitInQueue runs in place of the normal session handler while waiting.
func (s *Server) waitInQueue(ch gossh.Channel, conn *connectionState, entry *queueEntry, log *slog.Logger) slotAdmission {
	timer := time.AfterFunc(time.Until(entry.expires), func() { s.slots.leaveQueue(entry, queueTimedOut) })
	defer timer.Stop()
	for {
		now := time.Now()
		position, estimate := s.slots.queueEstimate(entry, now)
		if position != 0 {
			screen := queueScreen(s.cfg.MaxSessions, position, int(entry.width.Load()), now, estimate)
			if err := s.queueWrite(conn.conn, ch, screen); err != nil {
				s.slots.leaveQueue(entry, queueWriteFailed)
			}
		}

		select {
		case <-entry.done:
			if entry.leaveReason != "" {
				log.Info("queue leave", "reason", entry.leaveReason, "waited_ms", time.Since(entry.joined).Milliseconds())
				if entry.leaveReason == queueTimedOut {
					_ = s.queueWrite(conn.conn, ch, queueTimeoutMessage(s.cfg.MaxQueueWait))
				}
				return slotAdmission{}
			}
			if err := s.queueWrite(conn.conn, ch, clearQueueScreen); err != nil {
				s.slots.Release(entry.admission.slot)
				log.Info("queue leave", "reason", queueWriteFailed, "waited_ms", time.Since(entry.joined).Milliseconds())
				return slotAdmission{}
			}
			log.Info("queue admit", "waited_ms", entry.admission.at.Sub(entry.joined).Milliseconds())
			return entry.admission
		case <-entry.refresh:
		}
	}
}
