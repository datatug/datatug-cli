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
	"time"

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
	Source    string     `json:"source"`
	Policy    string     `json:"policy"`
	Role      string     `json:"role"`
	Table     string     `json:"table"`
	Fields    []string   `json:"fields,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	ScopeHash string     `json:"scopeHash,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

// NewEvent is an event about e at at.
func NewEvent(kind string, e Exception, fields []string, at time.Time) Event {
	return Event{At: at.UTC(), Kind: Kind, Event: kind, ID: e.ID, ProjectID: e.ProjectID, Source: e.Source, Policy: e.Policy.Path, Role: e.Role, Table: e.Table, Fields: fields, ExpiresAt: e.ExpiresAt}
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
	return e, nil
}

// List reads every exception, sorted by proposal time. One unreadable file
// fails the whole list: a caller applying exceptions then applies none.
func (s Store) List() ([]Exception, error) {
	entries, err := readDir(s.dir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy exceptions: %w", err)
	}
	var out []Exception
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		e, _, err := s.Get(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, fmt.Errorf("policy exceptions: %s is unreadable", name)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProposedAt.Before(out[j].ProposedAt) })
	return out, nil
}

// Commit changes one exception under the store's lock, only if its file still
// holds exactly expected, and records the event change returns. change may
// refuse the commit by returning an error, and nothing is written then. When the event cannot be
// recorded the old file is put back and the commit fails.
func (s Store) Commit(id string, expected []byte, change func(*Exception) (Event, error)) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
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
	if err := s.write(current); err != nil {
		return err
	}
	if err := s.appendAudit(event); err != nil {
		if restoreErr := writeFile(s.file(id), raw, 0o600); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	return nil
}

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
// recorded, writing one expired event each. It is best effort: a failure
// leaves the exception to be recorded next time and never changes what is
// applied, because expiry is evaluated against the clock on every load.
func (s Store) RecordExpiries(exceptions []Exception, now time.Time) {
	for _, e := range exceptions {
		if e.ExpiryRecorded || e.Status(now) != StateExpired {
			continue
		}
		current, raw, err := s.Get(e.ID)
		if err != nil || current.ExpiryRecorded {
			continue
		}
		_ = s.Commit(e.ID, raw, func(current *Exception) (Event, error) {
			current.ExpiryRecorded = true
			return NewEvent("expired", *current, nil, now), nil
		})
	}
}
