package policyexceptions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/callercontext"
	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pending(id string) Exception {
	return Exception{ID: id, Kind: Kind, ProjectID: "demo", Source: "chinook", Role: "agent", Table: "Employee",
		Policy: PolicyRef{Name: "agent", Path: "policies/agent.yaml", SHA256: "abc"}, Fields: []string{"EmployeeId"},
		Reason: "why", DurationSeconds: 900, State: StatePending, ProposedAt: t0, PendingExpiresAt: t0.Add(PendingTTL)}
}

func restore[T any](t *testing.T, target *T) {
	original := *target
	t.Cleanup(func() { *target = original })
}

func TestStoreJourney(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	list, _, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, list)
	events, err := s.Audit()
	require.NoError(t, err)
	assert.Empty(t, events)

	first, second := pending("0000000000000001"), pending("0000000000000002")
	second.ProposedAt = t0.Add(time.Second)
	require.NoError(t, s.Propose(second))
	require.NoError(t, s.Propose(first))
	assert.ErrorContains(t, s.Propose(first), "taken")
	require.NoError(t, os.Mkdir(filepath.Join(s.dir(), "sub"), 0o700))

	list, _, err = s.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, first.ID, list[0].ID)

	got, raw, err := s.Get(first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.Reason, got.Reason)
	require.NoError(t, s.Commit(first.ID, raw, func(e *Exception) (Event, error) {
		e.State = StateApplied
		expires := t0.Add(time.Minute)
		e.ExpiresAt = &expires
		return NewEvent("applied", *e, e.Fields, t0), nil
	}))
	assert.ErrorIs(t, s.Commit(first.ID, raw, nil), ErrChanged)
	assert.EqualError(t, s.Commit(first.ID, mustRaw(t, s, first.ID), func(*Exception) (Event, error) { return Event{}, errors.New("refused") }), "refused")

	require.NoError(t, s.Record(NewEvent("used", got, got.Fields, t0)))
	all, _, _ := s.List()
	s.RecordExpiries(all, t0.Add(time.Hour))
	s.RecordExpiries(all, t0.Add(time.Hour)) // a stale list: Get still works, nothing new is recorded twice
	after, _, _ := s.List()
	for _, e := range after {
		assert.True(t, e.ExpiryRecorded, e.ID)
	}
	s.RecordExpiries(after, t0.Add(time.Hour))
	s.RecordExpiries([]Exception{pending("00000000000000ff")}, t0.Add(time.Hour))
	events, err = s.Audit()
	require.NoError(t, err)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Event)
	}
	assert.Equal(t, []string{"proposed", "proposed", "applied", "used", "expired", "expired"}, kinds)
	assert.Equal(t, Kind, events[0].Kind)
}

func mustRaw(t *testing.T, s Store, id string) []byte {
	t.Helper()
	_, raw, err := s.Get(id)
	require.NoError(t, err)
	return raw
}

func TestStoreGetFailures(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	_, _, err := s.Get("../../etc")
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.Get("0000000000000001")
	assert.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, os.MkdirAll(s.dir(), 0o700))
	for id, body := range map[string]string{
		"0000000000000001": `{"id":"0000000000000001","kind":"local-exception","extra":1}`,
		"0000000000000002": `{"id":"0000000000000002","kind":"other"}`,
		"0000000000000003": `{"id":"0000000000000004","kind":"local-exception"}`,
	} {
		require.NoError(t, os.WriteFile(s.file(id), []byte(body), 0o600))
		_, _, err = s.Get(id)
		assert.ErrorContains(t, err, "unreadable", id)
	}
	listed, unreadable, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, listed, "an unreadable file contributes nothing")
	assert.Len(t, unreadable, 3)
	restore(t, &readFile)
	readFile = func(string) ([]byte, error) { return nil, errors.New("disk") }
	_, _, err = s.Get("0000000000000001")
	assert.ErrorContains(t, err, "disk")
	_, err = s.Audit()
	assert.ErrorContains(t, err, "disk")
}

func TestStoreListReadDirFailure(t *testing.T) {
	restore(t, &readDir)
	readDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("denied") }
	_, _, err := Store{ProjectDir: t.TempDir()}.List()
	assert.ErrorContains(t, err, "denied")
}

func TestStoreLockFailures(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	t.Run("mkdir", func(t *testing.T) {
		restore(t, &mkdirAll)
		mkdirAll = func(string, os.FileMode) error { return errors.New("read-only") }
		assert.ErrorContains(t, s.Propose(pending("0000000000000001")), "read-only")
		assert.ErrorContains(t, s.Record(Event{}), "read-only")
		assert.ErrorContains(t, s.Commit("0000000000000001", nil, nil), "read-only")
	})
	t.Run("busy", func(t *testing.T) {
		restore(t, &tryLock)
		tryLock = func(context.Context, *flock.Flock) (bool, error) { return false, nil }
		assert.ErrorContains(t, s.Propose(pending("0000000000000001")), "locked")
	})
	t.Run("real lock times out", func(t *testing.T) {
		restore(t, &lockTimeout)
		lockTimeout = 50 * time.Millisecond
		require.NoError(t, os.MkdirAll(s.dir(), 0o700))
		held := flock.New(s.lockPath())
		require.NoError(t, held.Lock())
		defer func() { _ = held.Unlock() }()
		assert.ErrorContains(t, s.Record(Event{}), "locked")
	})
}

func TestStoreWriteFailures(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		s := Store{ProjectDir: t.TempDir()}
		restore(t, &writeFile)
		writeFile = func(string, []byte, os.FileMode) error { return errors.New("full") }
		assert.ErrorContains(t, s.Propose(pending("0000000000000001")), "full")
	})
	t.Run("rename", func(t *testing.T) {
		s := Store{ProjectDir: t.TempDir()}
		restore(t, &rename)
		rename = func(string, string) error { return errors.New("cross-device") }
		assert.ErrorContains(t, s.Propose(pending("0000000000000001")), "cross-device")
		_, err := os.Stat(s.file("0000000000000001") + ".tmp")
		assert.True(t, os.IsNotExist(err))
	})
	t.Run("audit on propose", func(t *testing.T) {
		s := Store{ProjectDir: t.TempDir()}
		restore(t, &openFile)
		openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("no audit") }
		assert.ErrorContains(t, s.Propose(pending("0000000000000001")), "no audit")
		_, _, err := s.Get("0000000000000001")
		assert.ErrorIs(t, err, ErrNotFound, "a proposal that cannot be audited is taken back")
	})
	t.Run("audit write", func(t *testing.T) {
		s := Store{ProjectDir: t.TempDir()}
		restore(t, &openFile)
		openFile = func(name string, _ int, _ os.FileMode) (*os.File, error) {
			require.NoError(t, os.WriteFile(name, nil, 0o600))
			return os.Open(name) // read-only: the write fails
		}
		assert.ErrorContains(t, s.Record(Event{}), "audit")
	})
}

func TestStoreCommitFailures(t *testing.T) {
	setup := func(t *testing.T) (Store, []byte) {
		s := Store{ProjectDir: t.TempDir()}
		require.NoError(t, s.Propose(pending("0000000000000001")))
		return s, mustRaw(t, s, "0000000000000001")
	}
	change := func(e *Exception) (Event, error) {
		e.State = StateRejected
		return NewEvent("rejected", *e, nil, t0), nil
	}
	t.Run("missing", func(t *testing.T) {
		s := Store{ProjectDir: t.TempDir()}
		assert.ErrorIs(t, s.Commit("0000000000000001", nil, change), ErrNotFound)
	})
	t.Run("write", func(t *testing.T) {
		s, raw := setup(t)
		restore(t, &writeFile)
		writeFile = func(string, []byte, os.FileMode) error { return errors.New("full") }
		assert.ErrorContains(t, s.Commit("0000000000000001", raw, change), "full")
	})
	t.Run("an event that cannot be audited records nothing", func(t *testing.T) {
		s, raw := setup(t)
		restore(t, &openFile)
		openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("no audit") }
		assert.ErrorContains(t, s.Commit("0000000000000001", raw, change), "no audit")
		assert.Equal(t, raw, mustRaw(t, s, "0000000000000001"))
	})
	t.Run("a write after the event keeps the old state", func(t *testing.T) {
		s, raw := setup(t)
		restore(t, &rename)
		rename = func(string, string) error { return errors.New("cross-device") }
		assert.ErrorContains(t, s.Commit("0000000000000001", raw, change), "cross-device")
		assert.Equal(t, raw, mustRaw(t, s, "0000000000000001"))
		events, err := s.Audit()
		require.NoError(t, err)
		assert.Equal(t, "rejected-failed", events[len(events)-1].Event, "the log says the decision did not take effect")
	})
}

func TestAuditReadEdges(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	require.NoError(t, os.MkdirAll(s.dir(), 0o700))
	require.NoError(t, os.WriteFile(s.auditPath(), []byte("\n{\"event\":\"used\"}\n\n"), 0o600))
	events, err := s.Audit()
	require.NoError(t, err)
	assert.Len(t, events, 1)
	require.NoError(t, os.WriteFile(s.auditPath(), []byte("{\"event\":\"used\"}\nnot json\n"), 0o600))
	_, err = s.Audit()
	assert.ErrorContains(t, err, "line 2")
	require.NoError(t, os.WriteFile(s.auditPath(), []byte(strings.Repeat("a", 1<<21)), 0o600))
	_, err = s.Audit()
	assert.Error(t, err)
}

func TestRecordExpiriesSkipsUnreadable(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	gone := pending("0000000000000009")
	s.RecordExpiries([]Exception{gone}, t0.Add(time.Hour))
	events, err := s.Audit()
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestRecordExpiriesLockFailure(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	require.NoError(t, s.Propose(pending("0000000000000001")))
	restore(t, &tryLock)
	tryLock = func(context.Context, *flock.Flock) (bool, error) { return false, nil }
	s.RecordExpiries([]Exception{pending("0000000000000001")}, t0.Add(time.Hour))
	got, _, err := s.Get("0000000000000001")
	require.NoError(t, err)
	assert.False(t, got.ExpiryRecorded)
}

func TestStoreStampsTheCaller(t *testing.T) {
	caller := &callercontext.Caller{Harness: "claude-code"}
	s := Store{ProjectDir: t.TempDir(), Caller: caller}
	e := pending("0000000000000001")
	e.Caller = caller
	require.NoError(t, s.Propose(e))
	other := &callercontext.Caller{Harness: "other"}
	event := NewEvent("used", e, nil, t0)
	event.Caller = other
	require.NoError(t, s.Record(event))
	events, err := s.Audit()
	require.NoError(t, err)
	assert.Equal(t, caller, events[0].Caller)
	assert.Equal(t, other, events[1].Caller)
	assert.Contains(t, e.ProposedBy(), "claude-code")
	assert.Equal(t, "Proposed by: not reported", pending("0000000000000002").ProposedBy())
}

func TestEndSession(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	bound := SessionBinding{Harness: "claude-code", Session: "s-1"}
	mk := func(id string, lifetime string, binding *SessionBinding, state State) {
		e := pending(id)
		e.Lifetime, e.BoundSession = lifetime, binding
		require.NoError(t, s.Propose(e))
		if state != StatePending {
			_, raw, _ := s.Get(id)
			require.NoError(t, s.Commit(id, raw, func(c *Exception) (Event, error) {
				c.State = state
				expires := t0.Add(time.Hour)
				c.ExpiresAt = &expires
				return NewEvent(string(state), *c, nil, t0), nil
			}))
		}
	}
	other := SessionBinding{Harness: "claude-code", Session: "s-2"}
	mk("0000000000000001", LifetimeSession, &bound, StatePending)
	mk("0000000000000002", LifetimeSession, &bound, StateApplied)
	mk("0000000000000003", LifetimeSession, &other, StateApplied)
	mk("0000000000000004", LifetimeDeadline, nil, StateApplied)
	mk("0000000000000005", LifetimeSession, &bound, StateRejected)
	ended, err := s.EndSession(bound, t0)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"0000000000000001", "0000000000000002"}, ended)
	got, _, _ := s.Get("0000000000000002")
	assert.Equal(t, StateEnded, got.Status(t0))
	assert.False(t, got.Active(t0))
	again, err := s.EndSession(bound, t0)
	require.NoError(t, err)
	assert.Empty(t, again)

	mk("0000000000000006", LifetimeSession, &bound, StatePending)
	restore(t, &rename)
	rename = func(string, string) error { return errors.New("cross-device") }
	_, err = s.EndSession(bound, t0)
	assert.ErrorContains(t, err, "cross-device")
	rename = os.Rename
	restore(t, &readDir)
	readDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("denied") }
	_, err = s.EndSession(bound, t0)
	assert.ErrorContains(t, err, "denied")
	readDir = os.ReadDir
	restore(t, &tryLock)
	tryLock = func(context.Context, *flock.Flock) (bool, error) { return false, nil }
	_, err = s.EndSession(bound, t0)
	assert.ErrorContains(t, err, "locked")
}

func TestUnknownLifetimeIsUnreadable(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	e := pending("0000000000000001")
	e.Lifetime = "forever"
	require.NoError(t, s.Propose(e))
	_, _, err := s.Get(e.ID)
	assert.ErrorContains(t, err, "unreadable")
	listed, unreadable, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, listed)
	assert.Equal(t, []string{"0000000000000001.json"}, unreadable)
}

func TestEndSessionSkipsWhatIsNoLongerLive(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	bound := SessionBinding{Harness: "claude-code", Session: "s-1"}
	e := pending("0000000000000001")
	e.Lifetime, e.BoundSession = LifetimeSession, &bound
	require.NoError(t, s.Propose(e))
	ended, err := s.EndSession(bound, t0.Add(time.Hour)) // past the pending wait
	require.NoError(t, err)
	assert.Empty(t, ended)
	got, _, _ := s.Get(e.ID)
	assert.Equal(t, StatePending, got.State, "an expired proposal keeps its state")
}

// EndSession holds the lock for its whole pass: every proposal of the
// session is either ended, or was stored after the pass.
func TestEndSessionRacingProposals(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	bound := SessionBinding{Harness: "claude-code", Session: "s-1"}
	seed := pending("00000000000000ff")
	seed.Lifetime, seed.BoundSession = LifetimeSession, &bound
	require.NoError(t, s.Propose(seed))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := pending(fmt.Sprintf("%016x", i+1))
			e.Lifetime, e.BoundSession = LifetimeSession, &bound
			assert.NoError(t, s.Propose(e))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.EndSession(bound, t0)
		assert.NoError(t, err)
	}()
	wg.Wait()
	events, err := s.Audit()
	require.NoError(t, err)
	lastEnded := -1
	proposedAt := map[string]int{}
	for i, e := range events {
		switch e.Event {
		case "ended":
			lastEnded = i
		case "proposed":
			proposedAt[e.ID] = i
		}
	}
	require.GreaterOrEqual(t, lastEnded, 0, "the seeded exception was ended")
	all, _, err := s.List()
	require.NoError(t, err)
	for _, e := range all {
		if e.State != StateEnded {
			assert.Greater(t, proposedAt[e.ID], lastEnded, "%s was stored before the pass and not ended", e.ID)
		}
	}
}

func TestRecordExpiriesReportsOnceAndDoesNotRetry(t *testing.T) {
	s := Store{ProjectDir: t.TempDir()}
	e := pending("00000000000000a1")
	require.NoError(t, s.Propose(e))
	restore(t, &rename)
	rename = func(string, string) error { return errors.New("cross-device") }
	err := s.RecordExpiries([]Exception{e}, t0.Add(time.Hour))
	assert.ErrorContains(t, err, "could not be recorded")
	assert.NoError(t, s.RecordExpiries([]Exception{e}, t0.Add(time.Hour)), "not tried again by this process")
	events, _ := s.Audit()
	kinds := []string{}
	for _, ev := range events {
		kinds = append(kinds, ev.Event)
	}
	assert.Equal(t, []string{"proposed", "expired", "expired-failed"}, kinds)
	restore(t, &tryLock)
	tryLock = func(context.Context, *flock.Flock) (bool, error) { return false, nil }
	other := pending("00000000000000a2")
	assert.Error(t, Store{ProjectDir: t.TempDir()}.RecordExpiries([]Exception{other}, t0.Add(time.Hour)))
}
