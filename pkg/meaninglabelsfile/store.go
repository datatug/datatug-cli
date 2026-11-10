// Package meaninglabelsfile keeps a local project's Meaning Graph label
// document at <project>/datatug-labels.json and its audit trail at
// <project>/.datatug/audit/labels.jsonl. It implements the core
// meaninglabels.Store interface, so a later move to the DALgo project store
// changes only which Store the edge composes.
package meaninglabelsfile

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/gofrs/flock"
)

// FileName is the label document's name in the project root.
const FileName = "datatug-labels.json"

// AuditPath is the audit trail's path relative to the project root.
var AuditPath = filepath.Join(".datatug", "audit", "labels.jsonl")

// Seams for failure tests.
var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
	rename    = os.Rename
	mkdirAll  = os.MkdirAll
	openFile  = os.OpenFile
	marshal   = json.Marshal
	tryLock   = func(l *flock.Flock, ctx context.Context) (bool, error) {
		return l.TryLockContext(ctx, 20*time.Millisecond)
	}
)

// Store is the file-backed label store of one project directory.
type Store struct {
	Dir string
}

var _ meaninglabels.Store = Store{}

func (s Store) path() string { return filepath.Join(s.Dir, FileName) }

// Load reads the document; a project with no file has an empty document with
// revision "".
func (s Store) Load(context.Context) (meaninglabels.Document, error) {
	body, err := readFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return meaninglabels.Document{Format: meaninglabels.Format, Labels: []meaninglabels.Label{}}, nil
	}
	if err != nil {
		return meaninglabels.Document{}, err
	}
	return meaninglabels.Decode(body)
}

// Save writes doc atomically under a lock if the stored revision is still
// ifRevision.
func (s Store) Save(ctx context.Context, doc meaninglabels.Document, ifRevision string) error {
	body, err := meaninglabels.Encode(doc)
	if err != nil {
		return err
	}
	lock := flock.New(s.path() + ".lock")
	ok, err := tryLock(lock, ctx)
	if err != nil || !ok {
		return errors.Join(errors.New("meaninglabelsfile: could not lock the label document"), err)
	}
	defer func() { _ = lock.Unlock() }()
	current, err := s.Load(ctx)
	if err != nil {
		return err
	}
	if err := meaninglabels.CheckRevision(current, ifRevision); err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := writeFile(tmp, body, 0o644); err != nil {
		return err
	}
	return rename(tmp, s.path())
}

// AuditRecord is one line of the local audit trail.
type AuditRecord struct {
	meaninglabels.Event
	// Columns lists revealed columns for meaning.field.revealed_locally; it
	// never holds values.
	Columns []string `json:"columns,omitempty"`
}

// Append adds one record to the project's audit trail.
func (s Store) Append(record AuditRecord) error {
	path := filepath.Join(s.Dir, AuditPath)
	if err := mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := marshal(record)
	if err != nil {
		return err
	}
	f, err := openFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}
