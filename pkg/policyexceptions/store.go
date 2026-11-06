package policyexceptions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/pkg/callercontext"
	"github.com/gofrs/flock"
)

// DirName is the store's folder, relative to the project directory.
var DirName = filepath.Join(".datatug", "policy-exceptions")

// AuditFile is the audit log's file name inside the store.
const AuditFile = "audit.jsonl"

// ErrNotFound is the failure of a lookup of an exception the store does not hold.
var ErrNotFound = errors.New("policy exception not found")

// ErrChanged is the failure of a commit whose exception changed on disk
// since it was read: the decision is not recorded.
var ErrChanged = errors.New("the policy exception changed while it was being decided; nothing was recorded")

// Event is one audit record. It names scopes and never holds a row value.
type Event struct {
	At        time.Time  `json:"at"`
	Kind      string     `json:"kind"`
	Event     string     `json:"event"` // proposed, applied, narrowed, rejected, revoked, expired, used
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Env       string     `json:"env"`
	Source    string     `json:"source"`
	Policy    string     `json:"policy"`
	Role      string     `json:"role"`
	Table     string     `json:"table"`
	Fields    []string   `json:"fields,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	// Lifetime and BoundSession are the exception's: which harness session,
	// if any, the permission was given to.
	Lifetime     string          `json:"lifetime,omitempty"`
	BoundSession *SessionBinding `json:"session,omitempty"`
	// Caller is the self-reported harness of the process that wrote the
	// event (Store.Caller): attribution only. On a used event it is the
	// session that read under the exception.
	Caller *callercontext.Caller `json:"caller,omitempty"`
}

// NewEvent is an event about e at at.
func NewEvent(kind string, e Exception, fields []string, at time.Time) Event {
	return Event{At: at.UTC(), Kind: Kind, Event: kind, ID: e.ID, ProjectID: e.ProjectID, Env: e.Env, Source: e.Source, Policy: e.Policy.Path, Role: e.Role, Table: e.Table, Fields: fields, ExpiresAt: e.ExpiresAt, Lifetime: e.Lifetime, BoundSession: e.BoundSession}
}

// Seams over the file system, for failure tests.
var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
	rename    = os.Rename
	mkdirAll  = os.MkdirAll
	readDir   = os.ReadDir
	openFile  = os.OpenFile
	tryLock   = func(ctx context.Context, l *flock.Flock) (bool, error) {
		return l.TryLockContext(ctx, 20*time.Millisecond)
	}
	lockTimeout = 5 * time.Second
)

// Store is the exception store of one project directory.
type Store struct {
	ProjectDir string
	// Caller is stamped on every event this store writes that names none.
	Caller *callercontext.Caller
}

func (s Store) dir() string              { return filepath.Join(s.ProjectDir, DirName) }
func (s Store) file(id string) string    { return filepath.Join(s.dir(), id+".json") }
func (s Store) auditPath() string        { return filepath.Join(s.dir(), AuditFile) }
func (s Store) lockPath() string         { return filepath.Join(s.dir(), ".lock") }
func encode(e Exception) ([]byte, error) { return json.MarshalIndent(e, "", "  ") }

// lock takes the store's file lock, waiting at most lockTimeout.
func (s Store) lock() (func(), error) {
	if err := mkdirAll(s.dir(), 0o700); err != nil {
		return nil, fmt.Errorf("policy exceptions: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	l := flock.New(s.lockPath())
	ok, err := tryLock(ctx, l)
	if err != nil || !ok {
		return nil, errors.Join(errors.New("policy exceptions: the store is locked by another command; try again"), err)
	}
	return func() { _ = l.Unlock() }, nil
}

// write stores e atomically (a temporary file renamed over the old one).
func (s Store) write(e Exception) error {
	body, _ := encode(e) // plain values; cannot fail
	tmp := s.file(e.ID) + ".tmp"
	if err := writeFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("policy exceptions: %w", err)
	}
	if err := rename(tmp, s.file(e.ID)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("policy exceptions: %w", err)
	}
	return nil
}

// Propose stores a new pending exception and records the proposed event.
func (s Store) Propose(e Exception) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := readFile(s.file(e.ID)); err == nil {
		return fmt.Errorf("policy exceptions: id %s is taken", e.ID)
	}
	if err := s.write(e); err != nil {
		return err
	}
	if err := s.appendAudit(NewEvent("proposed", e, e.Fields, e.ProposedAt)); err != nil {
		_ = os.Remove(s.file(e.ID))
		return err
	}
	return nil
}

// Get reads one exception and the exact bytes it was read from.
func (s Store) Get(id string) (Exception, []byte, error) {
	if !ValidID(id) {
		return Exception{}, nil, ErrNotFound
	}
	raw, err := readFile(s.file(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Exception{}, nil, ErrNotFound
	}
	if err != nil {
		return Exception{}, nil, fmt.Errorf("policy exceptions: %w", err)
	}
	e, err := decode(raw)
	if err != nil || e.ID != id {
		return Exception{}, nil, fmt.Errorf("policy exception %s is unreadable", id)
	}
	return e, raw, nil
}

func decode(raw []byte) (Exception, error) {
	var e Exception
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return Exception{}, err
	}
	if e.Kind != Kind || !ValidID(e.ID) {
		return Exception{}, errors.New("not a local policy exception")
	}
	if e.Lifetime != "" && e.Lifetime != LifetimeSession && e.Lifetime != LifetimeDeadline {
		return Exception{}, errors.New("unknown lifetime")
	}
	return e, nil
}

// List reads every exception, sorted by proposal time, and names the files
// it could not read: an unreadable file contributes nothing, and the others
// still count. Only a store folder that cannot be listed is an error.
func (s Store) List() (exceptions []Exception, unreadable []string, err error) {
	entries, err := readDir(s.dir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("policy exceptions: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		e, _, err := s.Get(strings.TrimSuffix(name, ".json"))
		if err != nil {
			unreadable = append(unreadable, name)
			continue
		}
		exceptions = append(exceptions, e)
	}
	sort.Slice(exceptions, func(i, j int) bool { return exceptions[i].ProposedAt.Before(exceptions[j].ProposedAt) })
	return exceptions, unreadable, nil
}

// Commit changes one exception under the store's lock, only if its file still
// holds exactly expected. change may refuse by returning an error. The event
// change returns is appended to the audit log first and the new state is
// written only after it: a decision that cannot be audited is never recorded,
// and a write that fails after the event leaves the old state in force.
func (s Store) Commit(id string, expected []byte, change func(*Exception) (Event, error)) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.commitLocked(id, expected, change)
}

func (s Store) commitLocked(id string, expected []byte, change func(*Exception) (Event, error)) error {
	current, raw, err := s.Get(id)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, expected) {
		return ErrChanged
	}
	event, err := change(&current)
	if err != nil {
		return err
	}
	if err := s.appendAudit(event); err != nil {
		return err
	}
	if err := s.write(current); err != nil {
		// The event is already in the log: say it did not take effect.
		failed := event
		failed.Event = event.Event + "-failed"
		_ = s.appendAudit(failed)
		return err
	}
	return nil
}

// EndSession marks every pending or applied session exception bound to
// exactly this harness session as ended, with one ended event each, under
// the store's lock, and returns their ids. It only ever narrows access.
func (s Store) EndSession(binding SessionBinding, now time.Time) ([]string, error) {
	// The lock is taken before the store is listed, so a proposal either
	// lands before (and is ended) or after this whole pass.
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	all, _, err := s.List()
	if err != nil {
		return nil, err
	}
	ended := []string{}
	for _, e := range all {
		if !e.IsSession() || e.BoundSession == nil || *e.BoundSession != binding {
			continue
		}
		_, raw, err := s.Get(e.ID)
		if err == nil {
			err = s.commitLocked(e.ID, raw, func(current *Exception) (Event, error) {
				// Only a live exception is ended; a decided or expired one keeps
				// its state and decision.
				if state := current.Status(now); state != StatePending && state != StateApplied {
					return Event{}, errNotLive
				}
				current.State = StateEnded
				return NewEvent("ended", *current, nil, now), nil
			})
		}
		if errors.Is(err, errNotLive) {
			continue
		}
		if err != nil {
			return ended, err
		}
		ended = append(ended, e.ID)
	}
	return ended, nil
}

// errNotLive skips an exception that is no longer pending or applied.
var errNotLive = errors.New("not pending or applied")

// Record appends one event to the audit log under the store's lock.
func (s Store) Record(event Event) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.appendAudit(event)
}

func (s Store) appendAudit(event Event) error {
	if event.Caller == nil {
		event.Caller = s.Caller
	}
	line, _ := json.Marshal(event) // plain values; cannot fail
	f, err := openFile(s.auditPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("policy exceptions audit: %w", err)
	}
	_, writeErr := f.Write(append(line, '\n'))
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("policy exceptions audit: %w", err)
	}
	return nil
}

// Audit reads the audit log in order. A line that does not decode fails it.
func (s Store) Audit() ([]Event, error) {
	raw, err := readFile(s.auditPath())
	if errors.Is(err, fs.ErrNotExist) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy exceptions audit: %w", err)
	}
	events := []Event{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; scanner.Scan(); n++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("policy exceptions audit: line %d is unreadable", n)
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

// RecordExpiries marks every exception that expired at now and was not yet
// recorded, writing one expired event each, in one pass under the store's
// lock, and returns the first failure. It is best effort: it never changes
// what is applied, because expiry is evaluated against the clock on every
// load.
func (s Store) RecordExpiries(exceptions []Exception, now time.Time) error {
	var due []string
	for _, e := range exceptions {
		if _, failed := expiryFailed.Load(e.ID); !failed && !e.ExpiryRecorded && e.Status(now) == StateExpired {
			due = append(due, e.ID)
		}
	}
	if len(due) == 0 {
		return nil
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	var first error
	for _, id := range due {
		current, raw, err := s.Get(id)
		if err != nil || current.ExpiryRecorded {
			continue
		}
		err = s.commitLocked(id, raw, func(e *Exception) (Event, error) {
			e.ExpiryRecorded = true
			return NewEvent("expired", *e, nil, now), nil
		})
		if err != nil {
			// Not retried by this process: one failure, one warning.
			expiryFailed.Store(id, true)
			if first == nil {
				first = fmt.Errorf("the expiry of policy exception %s could not be recorded: %w", id, err)
			}
		}
	}
	return first
}

// expiryFailed holds the exceptions whose expiry could not be recorded in
// this process; they are not tried again, so a failing store does not fill
// the audit log with repeated events.
var expiryFailed sync.Map
