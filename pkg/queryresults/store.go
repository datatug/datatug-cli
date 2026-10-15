// Package queryresults keeps the private result files that `datatug query run
// --result-id <id>` writes: <root>/<id>/data.json (the rows) and
// <root>/<id>/meta.json (everything a card needs except the rows).
//
// The root is always ~/.datatug/query-results: there is deliberately no
// environment override, so the shell that ran the query, the person's terminal
// and the daemon all see the same files. Every directory is 0700 and every file
// 0600; a symlinked root or result directory, one owned by another user, and a
// root inside a Git repository are refused. Files are written as O_EXCL
// temporary files, synced and renamed into place, data.json first and
// meta.json last, so a result is complete exactly when meta.json exists and
// data.json has the byte length meta.json records.
//
// Every error this package returns is an *Error carrying a reason code. It
// names no path and no id, so it is safe to show to a model.
package queryresults

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/datatug/datatug-cli/pkg/gitpath"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
)

// Store bounds and lifetimes.
const (
	// DefaultDir is the root below the user's home directory.
	DefaultDir = ".datatug/query-results"
	// Retention is how long a result lives.
	Retention = 7 * 24 * time.Hour
	// MaxResults and MaxStoreBytes bound the store; the oldest results go first.
	MaxResults    = 200
	MaxStoreBytes = 512 << 20
	// IncompleteGrace is how old a directory without meta.json must be before
	// pruning removes it (a write may still be in progress before that).
	IncompleteGrace = time.Hour

	DataFile = "data.json"
	MetaFile = "meta.json"
)

// Reason codes. They are the whole message of an *Error.
const (
	ReasonUnwritable = "results-store-unwritable"
	ReasonInsideGit  = "inside-git"
	ReasonSymlink    = "symlink"
	ReasonOwner      = "owner"
	ReasonIDInUse    = "result-id-in-use"
	ReasonBadID      = "bad-result-id"
	ReasonNotFound   = "not-found"
	ReasonIncomplete = "incomplete"
	ReasonExpired    = "expired"
)

// Error is a store failure: a reason code, never a path or an id.
type Error struct{ Reason string }

func (e *Error) Error() string { return "query results: " + e.Reason }

func fail(reason string) error { return &Error{Reason: reason} }

// ReasonOf is the reason code of err when it is an *Error, else "".
func ReasonOf(err error) string {
	var storeErr *Error
	if errors.As(err, &storeErr) {
		return storeErr.Reason
	}
	return ""
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidID reports whether id follows the result id grammar.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Seams for tests: the home directory, the clock, the owner check and the file
// system calls whose failures cannot be produced on a real file system.
var (
	userHomeDir  = os.UserHomeDir
	now          = time.Now
	ownedByUser  = isOwnedByCurrentUser
	filepathAbs  = filepath.Abs
	osRemove     = os.Remove
	osLstat      = os.Lstat
	osOpen       = os.Open
	evalSymlinks = filepath.EvalSymlinks
	osMkdir      = os.Mkdir
	osMkdirAll   = os.MkdirAll
	osChmod      = os.Chmod
	osCreateTemp = os.CreateTemp
	osRename     = os.Rename
	osRemoveAll  = os.RemoveAll
	osReadDir    = os.ReadDir
	syncFile     = func(f *os.File) error { return f.Sync() }
	syncDir      = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		return d.Sync()
	}
)

// DefaultRoot is ~/.datatug/query-results.
func DefaultRoot() (string, error) {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return "", fail(ReasonUnwritable)
	}
	return filepath.Join(home, filepath.FromSlash(DefaultDir)), nil
}

// Store is one results root.
type Store struct{ root string }

// OpenDefault opens the store at DefaultRoot.
func OpenDefault() (*Store, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	return Open(root)
}

// Open checks root without creating it: it must not lie inside a Git
// repository, must not be a symbolic link, and, when it exists, must be a
// directory owned by the user.
func Open(root string) (*Store, error) {
	abs, err := filepathAbs(root)
	if err != nil {
		return nil, fail(ReasonUnwritable)
	}
	// Both the spelling and the form with symlinked ancestors resolved are
	// checked, for a working tree and for a bare repository.
	for _, candidate := range []string{abs, resolveExisting(abs)} {
		inside, err := gitpath.Inside(candidate, osLstat)
		if err != nil {
			return nil, fail(ReasonUnwritable)
		}
		if inside {
			return nil, fail(ReasonInsideGit)
		}
	}
	resolved, err := sessionstore.ResolvePrivateDirectory(abs)
	if err != nil {
		// The Git cases were answered above, so what is left is a root that is
		// a link or not a directory, or one that cannot be inspected.
		if info, lerr := osLstat(abs); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fail(ReasonSymlink)
		}
		return nil, fail(ReasonUnwritable)
	}
	s := &Store{root: resolved}
	if info, err := osLstat(resolved); err == nil && !ownedByUser(info) {
		return nil, fail(ReasonOwner)
	}
	return s, nil
}

// resolveExisting resolves the symbolic links of the longest existing
// ancestor of path and appends the rest; path itself when that fails.
func resolveExisting(path string) string {
	ancestor, rest := path, ""
	for {
		if _, err := osLstat(ancestor); err == nil || filepath.Dir(ancestor) == ancestor {
			break
		}
		rest = filepath.Join(filepath.Base(ancestor), rest)
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := evalSymlinks(ancestor)
	if err != nil {
		return path
	}
	return filepath.Join(resolved, rest)
}

// Root is the absolute root of the store.
func (s *Store) Root() string { return s.root }

// ensure creates the root (and its parent) as 0700 directories when missing,
// and keeps an existing root at 0700.
func (s *Store) ensure() error {
	if err := osMkdirAll(filepath.Dir(s.root), 0o700); err != nil {
		return fail(ReasonUnwritable)
	}
	if err := osMkdir(s.root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fail(ReasonUnwritable)
	}
	info, err := osLstat(s.root)
	switch {
	case err != nil:
		return fail(ReasonUnwritable)
	case info.Mode()&os.ModeSymlink != 0:
		return fail(ReasonSymlink)
	case !info.IsDir():
		return fail(ReasonUnwritable)
	case !ownedByUser(info):
		return fail(ReasonOwner)
	}
	if info.Mode().Perm() != 0o700 {
		if err := osChmod(s.root, 0o700); err != nil {
			return fail(ReasonUnwritable)
		}
	}
	return nil
}

// Check proves the store can be written: it creates the root when missing,
// then creates and removes one private temporary file in it.
func (s *Store) Check() error {
	if err := s.ensure(); err != nil {
		return err
	}
	f, err := osCreateTemp(s.root, ".check-*")
	if err != nil {
		return fail(ReasonUnwritable)
	}
	name := f.Name()
	_ = f.Close()
	if err := osRemove(name); err != nil {
		return fail(ReasonUnwritable)
	}
	return nil
}

// Result is a result directory being written.
type Result struct {
	dir string
}

// Create makes <root>/<id> with mkdir, so it fails when the id is in use: a
// result is never overwritten, and nothing is written through a path that
// already existed.
func (s *Store) Create(id string) (*Result, error) {
	if !ValidID(id) {
		return nil, fail(ReasonBadID)
	}
	if err := s.Check(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.root, id)
	if err := osMkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fail(ReasonIDInUse)
		}
		return nil, fail(ReasonUnwritable)
	}
	if err := osChmod(dir, 0o700); err != nil {
		_ = osRemoveAll(dir)
		return nil, fail(ReasonUnwritable)
	}
	return &Result{dir: dir}, nil
}

// Write writes data.json, then meta.json; each is an O_EXCL 0600 temporary
// file, synced and renamed into place.
func (r *Result) Write(data, meta []byte) error {
	if err := writeAtomic(r.dir, DataFile, data); err != nil {
		return err
	}
	return writeAtomic(r.dir, MetaFile, meta)
}

// Discard removes a result that will not be completed.
func (r *Result) Discard() { _ = osRemoveAll(r.dir) }

func writeAtomic(dir, name string, content []byte) error {
	// os.CreateTemp opens with O_CREATE|O_EXCL and mode 0600.
	f, err := osCreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return fail(ReasonUnwritable)
	}
	tmp := f.Name()
	_, werr := f.Write(content)
	serr := syncFile(f)
	cerr := f.Close()
	if errors.Join(werr, serr, cerr) != nil {
		_ = osRemove(tmp)
		return fail(ReasonUnwritable)
	}
	if err := osRename(tmp, filepath.Join(dir, name)); err != nil {
		_ = osRemove(tmp)
		return fail(ReasonUnwritable)
	}
	if err := syncDir(dir); err != nil {
		return fail(ReasonUnwritable)
	}
	return nil
}

// Locator is where a complete result's files are.
type Locator struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Root    string `json:"root"`
	Data    string `json:"data"`
	Meta    string `json:"meta"`
}

// metaHeader is the part of meta.json the store itself reads.
type metaHeader struct {
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	DataBytes int64     `json:"dataBytes"`
}

// resultDir checks the id and its directory: present, a real directory (not a
// link), owned by the user.
func (s *Store) resultDir(id string) (string, os.FileInfo, error) {
	if !ValidID(id) {
		return "", nil, fail(ReasonBadID)
	}
	dir := filepath.Join(s.root, id)
	info, err := osLstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", nil, fail(ReasonNotFound)
	case err != nil:
		return "", nil, fail(ReasonUnwritable)
	case info.Mode()&os.ModeSymlink != 0:
		return "", nil, fail(ReasonSymlink)
	case !info.IsDir():
		return "", nil, fail(ReasonNotFound)
	case !ownedByUser(info):
		return "", nil, fail(ReasonOwner)
	}
	return dir, info, nil
}

// readHeader reads meta.json of dir: a regular file of at most MaxMetaBytes.
func readHeader(dir string) (metaHeader, error) {
	var header metaHeader
	path := filepath.Join(dir, MetaFile)
	info, err := osLstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxMetaBytes {
		return header, fail(ReasonIncomplete)
	}
	content, err := readFile(path, MaxMetaBytes)
	if err != nil || json.Unmarshal(content, &header) != nil || header.ExpiresAt.IsZero() {
		return header, fail(ReasonIncomplete)
	}
	return header, nil
}

func readFile(path string, limit int64) ([]byte, error) {
	f, err := osOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// Locate answers where a complete, unexpired result's files are.
func (s *Store) Locate(id string) (Locator, error) {
	dir, _, err := s.resultDir(id)
	if err != nil {
		return Locator{}, err
	}
	header, err := readHeader(dir)
	if err != nil {
		return Locator{}, err
	}
	if !now().Before(header.ExpiresAt) {
		return Locator{}, fail(ReasonExpired)
	}
	data := filepath.Join(dir, DataFile)
	info, err := osLstat(data)
	if err != nil || !info.Mode().IsRegular() || info.Size() != header.DataBytes {
		return Locator{}, fail(ReasonIncomplete)
	}
	return Locator{Version: Version, ID: id, Root: s.root, Data: data, Meta: filepath.Join(dir, MetaFile)}, nil
}

// Remove deletes one result directory; a link is never followed.
func (s *Store) Remove(id string) error {
	dir, _, err := s.resultDir(id)
	if err != nil {
		return err
	}
	if err := osRemoveAll(dir); err != nil {
		return fail(ReasonUnwritable)
	}
	return nil
}

type pruneCandidate struct {
	dir       string
	createdAt time.Time
	bytes     int64
}

// Prune removes expired results, directories without meta.json older than
// IncompleteGrace, and then the oldest results while more than MaxResults or
// MaxStoreBytes remain. With all it removes every result. Only directories
// whose name follows the id grammar are touched, after the symlink and owner
// checks; a link is never followed. It answers how many were removed.
func (s *Store) Prune(all bool) (int, error) {
	entries, err := osReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fail(ReasonUnwritable)
	}
	removed := 0
	remove := func(dir string) {
		if osRemoveAll(dir) == nil {
			removed++
		}
	}
	current := now()
	var kept []pruneCandidate
	var total int64
	for _, entry := range entries {
		dir, info, err := s.resultDir(entry.Name())
		if err != nil {
			continue
		}
		if all {
			remove(dir)
			continue
		}
		header, err := readHeader(dir)
		if err != nil {
			if current.Sub(info.ModTime()) > IncompleteGrace {
				remove(dir)
			}
			continue
		}
		if !current.Before(header.ExpiresAt) {
			remove(dir)
			continue
		}
		candidate := pruneCandidate{dir: dir, createdAt: header.CreatedAt, bytes: dirBytes(dir)}
		total += candidate.bytes
		kept = append(kept, candidate)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].createdAt.Before(kept[j].createdAt) })
	for len(kept) > 0 && (len(kept) > MaxResults || total > MaxStoreBytes) {
		remove(kept[0].dir)
		total -= kept[0].bytes
		kept = kept[1:]
	}
	return removed, nil
}

// dirBytes is the size of the two result files of dir.
func dirBytes(dir string) int64 {
	var total int64
	for _, name := range []string{DataFile, MetaFile} {
		if info, err := osLstat(filepath.Join(dir, name)); err == nil {
			total += info.Size()
		}
	}
	return total
}
