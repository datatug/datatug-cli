// Package sessionstore opens a private OVDB SQLite authority for working sessions.
package sessionstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/openvaultdb/openvaultdb-go/pkg/core"
	"github.com/openvaultdb/openvaultdb-go/pkg/mount"
	_ "modernc.org/sqlite"
)

type Manager struct {
	mu     sync.Mutex
	mounts map[string]*core.Database
	root   string
}

func New(root string) (*Manager, error) {
	absolute, err := ResolvePrivateDirectory(root)
	if err != nil {
		return nil, err
	}
	return &Manager{root: absolute, mounts: map[string]*core.Database{}}, nil
}

// ResolvePrivateDirectory is shared by private working context and local display
// caches. It never creates files, accepts repository storage, or exposes paths
// through a remote transport.
func ResolvePrivateDirectory(root string) (string, error) {
	if root == "" {
		return "", sessioncontext.ErrInvalid
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err = privatePath(absolute); err != nil {
		return "", err
	}
	absolute, err = resolvedPrivatePath(absolute)
	if err != nil {
		return "", err
	}
	return absolute, nil
}

func resolvedPrivatePath(path string) (string, error) {
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", sessioncontext.ErrInvalid
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(ancestor, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, relative), nil
}

// privatePath rejects Git repositories and resolves existing symlink ancestors
// before checking so a symlink cannot redirect private context into a checkout.
func privatePath(path string) error {
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return sessioncontext.ErrUnavailable
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return err
	}
	for current := resolved; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return errors.New("session context must be stored outside Git repositories")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return sessioncontext.ErrInvalid
	}
	return nil
}

func secureFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return sessioncontext.ErrInvalid
	}
	return os.Chmod(path, 0600)
}

func (m *Manager) Repository(project, projectDir string) (*sessioncontext.DALRepository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	absolute, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if m.root == resolved || strings.HasPrefix(m.root, resolved+string(filepath.Separator)) {
		return nil, sessioncontext.ErrInvalid
	}
	digest := sha256.Sum256([]byte(project + "\x00" + resolved))
	id := "sessions-" + hex.EncodeToString(digest[:16])
	if existing := m.mounts[id]; existing != nil {
		return sessioncontext.NewDALRepository(existing, nil)
	}
	dir := filepath.Join(m.root, id)
	if err = privatePath(dir); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	if err = privatePath(dir); err != nil {
		return nil, err
	}
	database := filepath.Join(dir, "sessions.sqlite")
	if err = secureFile(database); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		return nil, err
	}
	for _, statement := range append([]string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL"}, sessioncontext.SQLiteSchemaStatements()...) {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err = db.Close(); err != nil {
		return nil, err
	}
	quoted, _ := json.Marshal(database)
	manifest := "database:\n  id: " + id + "\n  schema_mode: strict\nstorage:\n  engine: sqlite\n  path: " + string(quoted) + "\nschemas:\n" + sessioncontext.ManifestCollectionsYAML()
	manifestPath := filepath.Join(dir, "session-context.ovdb.yaml")
	if err = secureFile(manifestPath); err != nil {
		return nil, err
	}
	if err = os.WriteFile(manifestPath, []byte(manifest), 0600); err != nil {
		return nil, err
	}
	mounted, err := mount.File(manifestPath)
	if err != nil {
		return nil, err
	}
	m.mounts[id] = mounted
	return sessioncontext.NewDALRepository(mounted, nil)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for id, mounted := range m.mounts {
		errs = append(errs, mounted.Close())
		delete(m.mounts, id)
	}
	return errors.Join(errs...)
}
