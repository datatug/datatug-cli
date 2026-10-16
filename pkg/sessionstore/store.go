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

	"context"
	"github.com/datatug/datatug-cli/pkg/privateadmission"
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
	if err == nil {
		absolute, err = privatePath(absolute)
	}
	return absolute, err
}

// privatePath rejects Git repositories and resolves existing symlink ancestors
// before checking so a symlink cannot redirect private context into a checkout.
func privatePath(path string) (string, error) {
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for current := resolved; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return "", errors.New("session context must be stored outside Git repositories")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return "", sessioncontext.ErrInvalid
	}
	relative, _ := filepath.Rel(ancestor, path)
	return filepath.Join(resolved, relative), nil
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
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return sessioncontext.ErrInvalid
	}
	return os.Chmod(path, 0600)
}

func openSecuredSQLite(database string) (*sql.DB, error) {
	if err := secureFile(database); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", database)
}

func (m *Manager) Repository(project, projectDir string) (*sessioncontext.DALRepository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resolved, err := filepath.Abs(projectDir)
	if err == nil {
		resolved, err = filepath.EvalSymlinks(resolved)
	}
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
	if _, err = privatePath(dir); err == nil {
		if err = os.MkdirAll(dir, 0700); err == nil {
			err = os.Chmod(dir, 0700)
		}
	}
	if err != nil {
		return nil, err
	}
	database := filepath.Join(dir, "sessions.sqlite")
	db, err := openSecuredSQLite(database)
	if err != nil {
		return nil, err
	}
	for _, statement := range append([]string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL"}, sessioncontext.SQLiteSchemaStatements()...) {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	_ = db.Close()
	quoted, _ := json.Marshal(database)
	manifest := "database:\n  id: " + id + "\n  schema_mode: strict\nstorage:\n  engine: sqlite\n  path: " + string(quoted) + "\nschemas:\n" + sessioncontext.ManifestCollectionsYAML()
	manifestPath := filepath.Join(dir, "session-context.ovdb.yaml")
	var mounted *core.Database
	if err = secureFile(manifestPath); err == nil {
		if err = os.WriteFile(manifestPath, []byte(manifest), 0600); err == nil {
			mounted, err = mount.File(manifestPath)
		}
	}
	if err != nil {
		return nil, err
	}
	m.mounts[id] = mounted
	return sessioncontext.NewDALRepository(mounted, nil)
}

// NativeRepository wraps ALL session mutations in the same native project
// admission as Detective. View stays unlocked so native finalization can check
// its fence under admission without trying to reacquire it. Never call Transact
// while already holding native admission (lock order: admission, then SQLite).
func (m *Manager) NativeRepository(project, projectDir, privateBase string) (*NativeRepository, error) {
	path, err := privateadmission.ProjectPath(projectDir, privateBase)
	if err != nil {
		return nil, err
	}
	raw, err := m.Repository(project, projectDir)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	mounted := m.mounts[raw.DatabaseID()]
	m.mu.Unlock()
	native, err := sessioncontext.NewNativePinRepository(mounted, nil, "local", "", project)
	if err != nil {
		return nil, err
	}
	return &NativeRepository{DALRepository: native, admissionPath: path}, nil
}

type NativeRepository struct {
	*sessioncontext.DALRepository
	admissionPath string
}

func (r *NativeRepository) Transact(ctx context.Context, fn func(sessioncontext.Transaction) error) error {
	return privateadmission.With(ctx, r.admissionPath, func() error { return r.DALRepository.Transact(ctx, fn) })
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
