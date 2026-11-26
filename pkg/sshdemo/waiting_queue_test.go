//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func queueRequest(t *testing.T, slots *SlotManager, cfg Config, ip string, now time.Time) (slotAdmission, *queueEntry) {
	t.Helper()
	admission, entry, err := slots.admitOrQueue(ip, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	return admission, entry
}

func TestQueueReleaseRacingArrival(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSessions = 1
	now := time.Now()
	for range 25 {
		slots := NewSlotManager(1, 4, DefaultUIDBase, IsolationProcess)
		active, _ := queueRequest(t, slots, cfg, "active", now)
		_, first := queueRequest(t, slots, cfg, "first", now)
		_, second := queueRequest(t, slots, cfg, "second", now)
		start := make(chan struct{})
		released := make(chan struct{})
		go func() {
			<-start
			slots.Release(active.slot)
			close(released)
		}()
		close(start)
		direct, newcomer := queueRequest(t, slots, cfg, "newcomer", now)
		<-released
		if !direct.at.IsZero() || newcomer == nil {
			t.Fatal("new arrival took the slot ahead of existing waiters")
		}
		<-first.done
		if first.admission.slot.ClientIP != "first" || first.admission.slot.Index != active.slot.Index ||
			first.admission.deadline.Sub(first.admission.at) != cfg.MaxSessionDuration {
			t.Fatalf("admission %+v", first.admission)
		}
		slots.Release(active.slot)
		select {
		case <-second.done:
			t.Fatal("stale release freed the new reservation")
		default:
		}
		slots.Release(first.admission.slot)
		<-second.done
		if second.admission.slot.ClientIP != "second" {
			t.Fatalf("second admission %+v", second.admission)
		}
		slots.Release(second.admission.slot)
		<-newcomer.done
		if newcomer.admission.slot.ClientIP != "newcomer" {
			t.Fatalf("last admission %+v", newcomer.admission)
		}
	}
}

func TestQueueEstimateAndPendingSlots(t *testing.T) {
	cfg := DefaultConfig()
	now := time.Now()
	slots := NewSlotManager(5, 8, DefaultUIDBase, IsolationProcess)
	cfg.MaxSessionDuration = 2 * time.Minute
	active, _ := queueRequest(t, slots, cfg, "slow", now)
	cfg.MaxSessionDuration = time.Minute
	_, _ = queueRequest(t, slots, cfg, "fast", now)
	slots.setSlotState(2, slotAwaitingMemory)
	slots.setSlotState(3, slotAwaitingCleanup)
	slots.quarantine(4)
	cfg.MaxSessionDuration = time.Hour
	var waiters []*queueEntry
	for i, delay := range []time.Duration{30 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, time.Hour + 30*time.Second} {
		_, entry := queueRequest(t, slots, cfg, "queued", now)
		waiters = append(waiters, entry)
		pos, estimate := slots.queueEstimate(entry, now)
		if pos != i+1 || !estimate.Equal(now.Add(delay)) {
			t.Fatalf("position %d estimate %s, want %s", pos, estimate.Sub(now), delay)
		}
	}
	slots.setSlotState(2, slotUsable)
	<-waiters[0].done
	if waiters[0].admission.slot.Index != 2 {
		t.Fatalf("memory recheck handed out slot %+v", waiters[0].admission.slot)
	}
	if pos, _ := slots.queueEstimate(waiters[0], now); pos != 0 {
		t.Fatalf("admitted waiter still has queue position %d", pos)
	}
	for i := range 5 {
		slots.quarantine(i)
	}
	slots.Release(active.slot)
	if pos, estimate := slots.queueEstimate(waiters[1], now); pos != 1 || !estimate.IsZero() {
		t.Fatalf("position %d estimate %v, want 1 and unknown", pos, estimate)
	}
}

func TestQueueSkipsExpiredHeadOnRelease(t *testing.T) {
	slots := NewSlotManager(1, 3, DefaultUIDBase, IsolationProcess)
	cfg := DefaultConfig()
	now := time.Now()
	active, _ := queueRequest(t, slots, cfg, "active", now)
	_, expired := queueRequest(t, slots, cfg, "expired", now.Add(-2*cfg.MaxQueueWait))
	_, ready := queueRequest(t, slots, cfg, "ready", now)
	slots.Release(active.slot)
	<-expired.done
	<-ready.done
	if expired.leaveReason != queueTimedOut || ready.admission.slot.ClientIP != "ready" {
		t.Fatalf("expired reason %q, admitted %+v", expired.leaveReason, ready.admission)
	}
	slots.stopQueue()
	if _, _, err := slots.admitOrQueue("late", cfg, now); err != ErrGlobalLimit {
		t.Fatalf("closed queue returned %v", err)
	}
}

func TestQueueScreen(t *testing.T) {
	now := time.Date(2026, 1, 1, 13, 54, 19, 0, time.UTC)
	for _, tc := range []struct {
		position int
		ahead    string
	}{
		{1, "nobody ahead of you"},
		{2, "1 person ahead of you"},
		{3, "2 people ahead of you"},
	} {
		for _, width := range []int{40, 65, 80, MaxPTYWidth} {
			screen := queueScreen(6, tc.position, width, now, now.Add(12*time.Minute+41*time.Second))
			if !strings.HasPrefix(screen, clearQueueScreen) {
				t.Fatal("screen was not cleared")
			}
			for _, line := range strings.Split(strings.TrimPrefix(screen, clearQueueScreen), "\r\n") {
				if utf8.RuneCountInString(line) > width {
					t.Fatalf("screen at width %d overflowed: %q", width, line)
				}
			}
			flat := strings.ReplaceAll(screen, "\r\n", " ")
			for _, want := range []string{"all 6 sessions", tc.ahead, "in about 12:41 (around 14:07 UTC).", "Press q to leave the queue."} {
				if !strings.Contains(flat, want) {
					t.Fatalf("screen missing %q: %q", want, screen)
				}
			}
		}
	}
	if screen := queueScreen(1, 1, 80, now, time.Time{}); !strings.Contains(screen, "the only session") || !strings.Contains(screen, "Estimated start: unknown") {
		t.Fatal(screen)
	}
	if screen := queueScreen(1, 1, 80, now, now); !strings.Contains(screen, "any moment now") {
		t.Fatal(screen)
	}
	if got := queueTimeoutMessage(30 * time.Minute); strings.TrimSpace(got) != "Sorry, no session became free within 30 minutes. Please try again later." {
		t.Fatal(got)
	}
	if got := queueTimeoutMessage(time.Minute); !strings.Contains(got, "within 1 minute.") {
		t.Fatal(got)
	}
}

type queueVisitor struct {
	client *gossh.Client
	sess   *gossh.Session
	input  io.WriteCloser
	lines  <-chan string
}

func newQueueVisitor(t *testing.T, client *gossh.Client, width int) queueVisitor {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err := sess.RequestPty("xterm", 24, width, gossh.TerminalModes{gossh.ICANON: 0}); err != nil {
		t.Fatal(err)
	}
	in, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-t.Context().Done():
				return
			}
		}
	}()
	return queueVisitor{client: client, sess: sess, input: in, lines: lines}
}

func (v queueVisitor) see(t *testing.T, want string) {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	var seen []string
	for {
		select {
		case line, ok := <-v.lines:
			if !ok {
				t.Fatalf("closed before %q, saw %q", want, seen)
			}
			if strings.Contains(line, want) {
				return
			}
			seen = append(seen, line)
		case <-timer.C:
			t.Fatalf("timed out waiting for %q, saw %q", want, seen)
		}
	}
}

func (v queueVisitor) waitClosed(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = v.client.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("queue visitor connection stayed open")
	}
}

func TestServerQueueHandoverAndResize(t *testing.T) {
	h := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-winch"
		c.MaxSessions = 1
		c.MaxSessionsPerIP = 4
		c.ShellStartTimeout = 700 * time.Millisecond
		c.IdleTimeout = 2 * time.Second
	})
	active := newQueueVisitor(t, dialSSH(t, h.addr, "first"), 80)
	active.see(t, "READY:80x24")
	waiting := newQueueVisitor(t, dialSSH(t, h.addr, "second"), 40)
	waiting.see(t, "You are number 1 in the queue")
	waiting.see(t, "Press q to leave the")
	if err := waiting.sess.WindowChange(30, 100); err != nil {
		t.Fatal(err)
	}
	// A fresh screen after resize also proves the original startup timeout
	// elapsed without disconnecting the queued visitor.
	waiting.see(t, "Your session will start here automatically. Press q")
	if entries, err := os.ReadDir(h.sessionsDir); err != nil || len(entries) != 1 || h.srv.slots.ActiveCount() != 1 {
		t.Fatalf("queue allocated session resources: entries=%v err=%v", entries, err)
	}
	_ = active.client.Close()
	waiting.see(t, clearQueueScreen+"READY:100x30")
	if err := waiting.sess.WindowChange(31, 101); err != nil {
		t.Fatal(err)
	}
	waiting.see(t, "RESIZED:101x31")
	waitLogContains(t, h.logs, "\"msg\":\"queue admit\"", time.Second)
}

func TestServerQueueLeavesAndMovesUp(t *testing.T) {
	for _, tc := range []struct {
		name, value, reason string
	}{
		{"q", "q", queueQuit},
		{"ctrl-c", "\x03", queueQuit},
		{"ctrl-d", "\x04", queueQuit},
		{"disconnect", "", EndReasonClientDrop},
		{"max wait", "", queueTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startTestServer(t, func(c *Config) {
				c.SaveBranch = "wait-forever"
				c.MaxSessions = 1
				c.MaxSessionsPerIP = 4
				if tc.reason == queueTimedOut {
					c.MaxQueueWait = 400 * time.Millisecond
				}
			})
			active := newQueueVisitor(t, dialSSH(t, h.addr, "active"), 80)
			active.see(t, "READY:")
			first := newQueueVisitor(t, dialSSH(t, h.addr, "first"), 80)
			first.see(t, "nobody ahead of you")
			if tc.reason == queueTimedOut {
				time.Sleep(150 * time.Millisecond)
			}
			second := newQueueVisitor(t, dialSSH(t, h.addr, "second"), 80)
			second.see(t, "1 person ahead of you")
			switch tc.reason {
			case queueQuit:
				if _, err := io.WriteString(first.input, "ignore this input"); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(first.input, tc.value); err != nil {
					t.Fatal(err)
				}
			case EndReasonClientDrop:
				_ = first.client.Close()
			case queueTimedOut:
				first.see(t, "Sorry, no session became free within 400ms. Please try again later.")
			}
			first.waitClosed(t)
			second.see(t, "nobody ahead of you")
			waitLogContains(t, h.logs, fmt.Sprintf("\"reason\":%q", tc.reason), time.Second)
			if strings.Contains(h.logs.String(), "ignore this input") {
				t.Fatal("queued input leaked to logs")
			}
			_ = active.client.Close()
			// Leave a larger margin for admission than the intentional 150 ms
			// separation between timer expiries in the timeout case.
			if tc.reason != queueTimedOut {
				second.see(t, "READY:")
				if _, err := io.WriteString(second.input, "q"); err != nil {
					t.Fatal(err)
				}
				second.waitClosed(t)
			}
		})
	}
}

func TestServerQueueConnectionCaps(t *testing.T) {
	h := startTestServerWithPeer(t, net.ParseIP("192.0.2.8"), func(c *Config) {
		c.ProxyProtocol = true
		c.ProxyProtocolTrust = []string{"192.0.2.8/32"}
		c.SaveBranch = "wait-forever"
		c.MaxSessions = 1
		c.MaxQueue = 2
		c.MaxSessionsPerIP = 2
	})
	connect := func(octet int, rejected bool) *gossh.Client {
		t.Helper()
		conn, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		ip := &net.TCPAddr{IP: net.IPv4(203, 0, 113, byte(octet)), Port: 12345}
		if _, err := conn.Write(EncodeProxyV2(ip, conn.RemoteAddr())); err != nil {
			t.Fatal(err)
		}
		var banner string
		server, chans, reqs, err := gossh.NewClientConn(conn, h.addr, &gossh.ClientConfig{
			User: "visitor", HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			BannerCallback: func(msg string) error { banner = msg; return nil },
		})
		if rejected {
			if err == nil || !strings.Contains(banner, "The DataTug demo is full") {
				t.Fatalf("connection accepted or missing full banner: %q, %v", banner, err)
			}
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		client := gossh.NewClient(server, chans, reqs)
		t.Cleanup(func() { _ = client.Close() })
		return client
	}

	newQueueVisitor(t, connect(10, false), 80).see(t, "READY:")
	newQueueVisitor(t, connect(10, false), 80).see(t, "number 1 in the queue")
	connect(10, true) // One active and one queued connection fill the per-IP cap.
	newQueueVisitor(t, connect(11, false), 80).see(t, "number 2 in the queue")
	connect(12, true) // One active and two queued connections fill the global cap.
}

func TestQueueBlockedTransport(t *testing.T) {
	h := startTestServer(t, nil)
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan error, 1)
	go func() { done <- h.srv.queueWrite(server, server, "waiting") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked writer unexpectedly completed")
		}
	case <-time.After(3 * time.Second):
		_ = server.Close()
		t.Fatal("shared ticker did not close the blocked transport")
	}
}

func TestQueuePTYAllocationFailureReleasesSlot(t *testing.T) {
	orig := newWishServer
	t.Cleanup(func() { newWishServer = orig })
	newWishServer = func(opts ...ssh.Option) (*ssh.Server, error) {
		server, err := orig(opts...)
		if err == nil {
			server.PtyHandler = func(ssh.Context, ssh.Session, ssh.Pty) (func() error, error) {
				return func() error { return nil }, os.ErrPermission
			}
		}
		return server, err
	}
	h := startTestServer(t, nil)
	v := newQueueVisitor(t, dialSSH(t, h.addr, "visitor"), 80)
	v.see(t, "This demo needs an interactive terminal")
	v.waitClosed(t)
	if h.srv.slots.ActiveCount() != 0 {
		t.Fatal("PTY allocation failure leaked a slot")
	}
}

type queueTestChannel struct {
	gossh.Channel
	input  io.Reader
	output io.Writer
}

func (ch queueTestChannel) Read(p []byte) (int, error)  { return ch.input.Read(p) }
func (ch queueTestChannel) Write(p []byte) (int, error) { return ch.output.Write(p) }

type queueWriteError struct{}

func (queueWriteError) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestQueueWriteFailureBeforeAndAfterReservation(t *testing.T) {
	for _, admit := range []bool{false, true} {
		t.Run(fmt.Sprint(admit), func(t *testing.T) {
			cfg := DefaultConfig()
			now := time.Now()
			slots := NewSlotManager(1, 3, DefaultUIDBase, IsolationProcess)
			active, _ := queueRequest(t, slots, cfg, "active", now)
			_, entry := queueRequest(t, slots, cfg, "queued", now)
			if admit {
				slots.Release(active.slot)
			}
			server, client := net.Pipe()
			defer func() { _ = server.Close(); _ = client.Close() }()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			s := &Server{cfg: cfg, slots: slots, queueWrites: make(map[net.Conn]time.Time)}
			result := s.waitInQueue(queueTestChannel{output: queueWriteError{}}, &connectionState{conn: server}, entry, log)
			if !result.at.IsZero() {
				t.Fatal("failed write allowed handoff")
			}
			if admit && slots.ActiveCount() != 0 {
				t.Fatal("failed handoff leaked a reservation")
			}
			if !admit && entry.leaveReason != queueWriteFailed {
				t.Fatalf("queue leave reason %q", entry.leaveReason)
			}
		})
	}
}

func TestQueueOutputAfterTickerStopped(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	s := &Server{queueWrites: map[net.Conn]time.Time{server: time.Now().Add(time.Hour)}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.tickQueue(ctx, nil)
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("ticker shutdown left the writer open: %v", err)
	}
	if err := s.queueWrite(server, server, "closed"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("stopped queue returned %v", err)
	}
}

func TestQueueDisconnectAfterReservation(t *testing.T) {
	cfg := DefaultConfig()
	now := time.Now()
	slots := NewSlotManager(1, 2, DefaultUIDBase, IsolationProcess)
	active, _ := queueRequest(t, slots, cfg, "active", now)
	_, entry := queueRequest(t, slots, cfg, "queued", now)
	slots.Release(active.slot)
	handoff := make(chan slotAdmission, 1)
	handoff <- entry.admission
	s, c := net.Pipe()
	defer func() { _ = c.Close() }()
	f := &filteringNewChannel{server: &Server{slots: slots}, state: &connectionState{conn: s}}
	f.abandonHandoff(entry, handoff)
	if slots.ActiveCount() != 0 {
		t.Fatal("disconnected visitor kept its reservation")
	}
}

func TestQueueInputStopsDuringAndAfterHandoff(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			meta := &sessionMeta{chanDone: make(chan struct{}), pty: testPTY(t)}
			state := &connectionState{shellStarted: make(chan struct{})}
			if started {
				close(state.shellStarted)
				_ = meta.pty.Master.Close()
			} else {
				close(meta.chanDone)
			}
			f := &filteringNewChannel{state: state, meta: meta}
			entry := &queueEntry{done: make(chan struct{}), admission: slotAdmission{at: time.Now()}}
			close(entry.done)
			f.readQueueInput(&activityChannel{Channel: queueTestChannel{input: strings.NewReader("x")}}, entry)
		})
	}
}

func TestQueueEstimateWithAlreadyFreeDeadline(t *testing.T) {
	now := time.Now()
	cfg := DefaultConfig()
	slots := NewSlotManager(1, 2, DefaultUIDBase, IsolationProcess)
	if _, err := slots.Acquire("active"); err != nil {
		t.Fatal(err)
	}
	_, entry := queueRequest(t, slots, cfg, "queued", now)
	if _, estimate := slots.queueEstimate(entry, now); !estimate.Equal(now) {
		t.Fatalf("estimate %s, want %s", estimate, now)
	}
}

func TestServerQueueFullAndRepeatedShell(t *testing.T) {
	h := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
		c.MaxSessions = 1
		c.MaxQueue = 1
		c.MaxSessionsPerIP = 3
		c.MaxConnections = 3
	})
	newQueueVisitor(t, dialSSH(t, h.addr, "active"), 80).see(t, "READY:")
	queued := newQueueVisitor(t, dialSSH(t, h.addr, "queued"), 80)
	queued.see(t, "number 1 in the queue")
	if ok, err := queued.sess.SendRequest("shell", true, nil); err != nil || ok {
		t.Fatalf("repeated shell request returned %t, %v", ok, err)
	}
	newQueueVisitor(t, dialSSH(t, h.addr, "full"), 80).see(t, "The DataTug demo is full")
}

func TestQueuePassesOverWaitersAtTheirSlotLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSessions = 2
	now := time.Now()
	slots := NewSlotManager(2, 1, DefaultUIDBase, IsolationProcess)
	first, _ := queueRequest(t, slots, cfg, "203.0.113.1", now)
	// The same IP already holds its one slot, so it waits although slot 1 is free.
	if admission, entry := queueRequest(t, slots, cfg, "203.0.113.1", now); entry == nil || !admission.at.IsZero() {
		t.Fatalf("second session from one IP: admission %+v, entry %v; want it queued", admission, entry)
	}
	// A new IP behind it is the oldest eligible visitor and takes slot 1 now.
	if admission, entry := queueRequest(t, slots, cfg, "203.0.113.2", now); entry != nil || admission.slot.Index != 1 {
		t.Fatalf("eligible arrival: admission %+v, entry %v; want slot 1", admission, entry)
	}
	_, third := queueRequest(t, slots, cfg, "203.0.113.3", now)
	if third == nil {
		t.Fatal("third IP should wait: no slot is free")
	}
	// Releasing the first IP's slot admits its waiting session, ahead of the third IP.
	slots.Release(first.slot)
	if got := len(slots.waiting); got != 1 || slots.waiting[0] != third {
		t.Fatalf("waiting after release = %d entries, want only the third IP", got)
	}
}

func TestQueueFullRemovesTheArrival(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSessions, cfg.MaxQueue = 1, 0
	slots := NewSlotManager(1, 2, DefaultUIDBase, IsolationProcess)
	queueRequest(t, slots, cfg, "203.0.113.1", time.Now())
	if _, _, err := slots.admitOrQueue("203.0.113.2", cfg, time.Now()); err != ErrGlobalLimit {
		t.Fatalf("arrival with the queue disabled = %v, want ErrGlobalLimit", err)
	}
	if len(slots.waiting) != 0 {
		t.Fatalf("rejected arrival left %d waiting entries", len(slots.waiting))
	}
}
