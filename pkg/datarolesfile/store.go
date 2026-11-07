// Package datarolesfile keeps a local project's data-role classifications at
// <project>/datatug-data-roles.json (dataroles.FileName), beside the Meaning
// Graph labels of meaninglabelsfile. Only confirmed entries take effect.
package datarolesfile

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/gofrs/flock"
)

// Seams for failure tests.
var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
	rename    = os.Rename
	marshal   = func(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
	tryLock   = func(l *flock.Flock, ctx context.Context) (bool, error) {
		return l.TryLockContext(ctx, 20*time.Millisecond)
	}
)

// Store is the file-backed classification store of one project directory.
type Store struct {
	Dir string
}

func (s Store) path() string { return filepath.Join(s.Dir, dataroles.FileName) }

// Load reads and validates the document; a project with no file has an empty
// one.
func (s Store) Load() (dataroles.Document, error) {
	body, err := readFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return dataroles.Document{Format: dataroles.Format, Classifications: []dataroles.Classification{}}, nil
	}
	if err != nil {
		return dataroles.Document{}, err
	}
	var doc dataroles.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return dataroles.Document{}, err
	}
	if doc.Classifications == nil {
		doc.Classifications = []dataroles.Classification{}
	}
	return doc, doc.Validate()
}

// Update loads the document under a lock, applies change and writes the
// result atomically when change succeeds.
func (s Store) Update(ctx context.Context, change func(*dataroles.Document) error) (dataroles.Document, error) {
	lock := flock.New(s.path() + ".lock")
	ok, err := tryLock(lock, ctx)
	if err != nil || !ok {
		return dataroles.Document{}, errors.Join(errors.New("datarolesfile: could not lock the data-roles document"), err)
	}
	defer func() { _ = lock.Unlock() }()
	doc, err := s.Load()
	if err != nil {
		return dataroles.Document{}, err
	}
	if err := change(&doc); err != nil {
		return dataroles.Document{}, err
	}
	if err := doc.Validate(); err != nil {
		return dataroles.Document{}, err
	}
	body, err := marshal(doc)
	if err != nil {
		return dataroles.Document{}, err
	}
	tmp := s.path() + ".tmp"
	if err := writeFile(tmp, append(body, '\n'), 0o644); err != nil {
		return dataroles.Document{}, err
	}
	return doc, rename(tmp, s.path())
}
