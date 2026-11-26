//go:build linux

package sshdemo

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// forwardRequests holds the validated PTY request until a slot is reserved.
// Wish gets a local replay of the request before the shell, so it can keep
// doing PTY allocation, mode setup and resizing for the active session.
func (f *filteringNewChannel) forwardRequests(ch *activityChannel, raw <-chan *gossh.Request, filtered chan<- *gossh.Request) {
	defer close(filtered)
	defer close(f.meta.chanDone)
	var filter sessionRequestFilter
	var pty []byte
	var entry *queueEntry
	var handoff <-chan slotAdmission
	started := false

	for {
		select {
		case admission := <-handoff:
			if admission.at.IsZero() {
				return
			}
			close(f.state.admitted)
			f.startShell(filtered, pty, admission)
			started = true
			handoff = nil
			pty, entry = nil, nil
		case req, ok := <-raw:
			if !ok {
				if handoff != nil {
					f.abandonHandoff(entry, handoff)
				}
				return
			}
			req = filter.filterSessionRequest(req)
			if started {
				filtered <- req
				continue
			}
			switch req.Type {
			case "pty-req":
				accept := pty == nil
				if accept {
					pty = req.Payload
				}
				_ = req.Reply(accept, nil)
			case "window-change":
				if pty != nil {
					copy(pty[ptyWindowOffset(pty):], req.Payload[:16])
					if entry != nil {
						entry.width.Store(int32(binary.BigEndian.Uint32(req.Payload)))
					}
				}
				_ = req.Reply(pty != nil, nil)
			case "env":
				if handoff == nil {
					filtered <- localRequest(req.Type, req.Payload)
				}
				_ = req.Reply(handoff == nil, nil)
			case "shell":
				if handoff != nil {
					_ = req.Reply(false, nil)
					continue
				}
				if pty == nil {
					filtered <- req
					started = true
					continue
				}
				_ = req.Reply(true, nil)
				admission, waiter, log := f.requestSlot(ch, pty)
				if waiter != nil {
					entry = waiter
					ch.queued = true
					handoff = f.runQueue(ch, waiter, log)
				} else if !admission.at.IsZero() {
					f.startShell(filtered, pty, admission)
					started = true
					pty = nil
				} else {
					return
				}
			default:
				_ = req.Reply(false, nil)
			}
		}
	}
}

func (f *filteringNewChannel) abandonHandoff(entry *queueEntry, handoff <-chan slotAdmission) {
	f.server.slots.leaveQueue(entry, EndReasonClientDrop)
	if admission := <-handoff; !admission.at.IsZero() {
		f.server.slots.Release(admission.slot)
	}
	_ = f.state.conn.Close()
}

func ptyWindowOffset(payload []byte) int {
	return 4 + int(binary.BigEndian.Uint32(payload))
}

// localRequest is internal to Wish. The original request already got its
// ordered wire reply, so the replay must not send a second response.
func localRequest(typ string, payload []byte) *gossh.Request {
	return &gossh.Request{Type: typ, Payload: payload}
}

func (f *filteringNewChannel) requestSlot(ch gossh.Channel, pty []byte) (slotAdmission, *queueEntry, *slog.Logger) {
	s := f.server
	window := pty[ptyWindowOffset(pty):]
	log := s.logger.With("client_ip", safeLogValue(f.clientIP), "ssh_user", f.sshUser,
		"pty_width", binary.BigEndian.Uint32(window), "pty_height", binary.BigEndian.Uint32(window[4:]))
	id, err := generateSessionID()
	if err != nil {
		log.Error("generate session id failed", "err", err)
		f.exitChannel(ch, "")
		return slotAdmission{}, nil, nil
	}
	admission, entry, err := s.slots.admitOrQueue(f.clientIP, s.cfg, time.Now())
	if err != nil {
		log.Info("session end", "session_id", "", "duration_ms", 0, "end_reason", EndReasonFull,
			"exit_status", 1, "quarantined_slots", s.slots.quarantinedCount(), "max_sessions", s.cfg.MaxSessions)
		f.exitChannel(ch, demoFullMessage)
		return slotAdmission{}, nil, nil
	}
	f.meta.sessionID = id
	if entry != nil {
		entry.width.Store(int32(binary.BigEndian.Uint32(window)))
		log = log.With("session_id", id)
		log.Info("queue enqueue", "position", entry.position, "queue_length", entry.position)
	}
	return admission, entry, log
}

func (f *filteringNewChannel) startShell(filtered chan<- *gossh.Request, pty []byte, admission slotAdmission) {
	f.meta.admission = admission
	filtered <- localRequest("pty-req", pty)
	filtered <- localRequest("shell", nil)
}

func (f *filteringNewChannel) runQueue(ch *activityChannel, entry *queueEntry, log *slog.Logger) <-chan slotAdmission {
	close(f.state.queued)
	handoff := make(chan slotAdmission, 1)
	go f.readQueueInput(ch, entry)
	go func() {
		admission := f.server.waitInQueue(ch, f.state, entry, log)
		if admission.at.IsZero() {
			_ = f.server.queueOutput(f.state.conn, func() error { return sendQueueExit(ch) })
			_ = f.state.conn.Close()
		}
		handoff <- admission
	}()
	return handoff
}

func (f *filteringNewChannel) readQueueInput(ch *activityChannel, entry *queueEntry) {
	var buf [256]byte
	var activePTY *os.File
	done := entry.done
	for {
		n, err := ch.Channel.Read(buf[:])
		if done != nil {
			switch {
			case bytes.ContainsAny(buf[:n], "q\x03\x04"):
				if f.server.slots.leaveQueue(entry, queueQuit) {
					return
				}
			case err != nil:
				f.server.slots.leaveQueue(entry, EndReasonClientDrop)
			}
		}
		select {
		case <-done:
			if entry.admission.at.IsZero() {
				return
			}
			select {
			case <-f.state.shellStarted:
				activePTY = f.meta.pty.Master
				done = nil
			case <-f.meta.chanDone:
				return
			}
		default:
		}

		if activePTY != nil && n != 0 {
			ch.signalActivity()
			if _, err := activePTY.Write(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// exitChannel is used before Wish has a shell to send an exit status.
func (f *filteringNewChannel) exitChannel(ch gossh.Channel, message string) {
	defer func() { _ = f.state.conn.Close() }()
	withTransportDeadline(f.state.conn, func() {
		if message != "" {
			_, _ = io.WriteString(ch, message)
		}
		_ = sendQueueExit(ch)
	})
}

func sendQueueExit(ch gossh.Channel) error {
	_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{1}))
	return ch.Close()
}
