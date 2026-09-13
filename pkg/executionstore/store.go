// Package executionstore composes immutable repository-backed execution
// receipts with server-private SQLite snapshot bytes and lifecycle state.
package executionstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	_ "modernc.org/sqlite"
)

const (
	DirEnv           = "DATATUG_EVIDENCE_DIR"
	DefaultDir       = ".datatug/evidence"
	DefaultByteCap   = 2 << 20
	DefaultRetention = 30 * 24 * time.Hour
)

var (
	ErrSnapshotNotFound = errors.New("snapshot not found")
	ErrUnsafePrivateDir = errors.New("private evidence directory must be outside Git and evidence repositories")
)

// Options are trusted server configuration, never request-controlled.
type Options struct {
	PrivateDir string
	ByteCap    int
	Retention  time.Duration
	Now        func() time.Time
}

// ResolvePrivateDir selects the explicitly configured directory, then the
// environment override, then ~/.datatug/evidence.
func ResolvePrivateDir(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv(DirEnv)
	}
	if explicit == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve evidence directory: %w", err)
		}
		explicit = filepath.Join(home, filepath.FromSlash(DefaultDir))
	}
	return filepath.Abs(explicit)
}

// Manager opens one Store per routed evidence store and owns their handles.
type Manager struct {
	roots     incidentstore.RepositoryRoots
	locations map[string]incidents.StoreLocation
	options   Options
	mu        sync.Mutex
	stores    map[string]*Store
}

func NewManager(roots incidentstore.RepositoryRoots, locations map[string]incidents.StoreLocation, options Options) (*Manager, error) {
	privateDir, err := ResolvePrivateDir(options.PrivateDir)
	if err != nil {
		return nil, err
	}
	options.PrivateDir = privateDir
	if err := validatePrivateDir(privateDir, roots); err != nil {
		return nil, err
	}
	if options.ByteCap == 0 {
		options.ByteCap = DefaultByteCap
	}
	if options.ByteCap < 0 {
		return nil, errors.New("snapshot byte cap must not be negative")
	}
	if options.Retention == 0 {
		options.Retention = DefaultRetention
	}
	if options.Retention < 0 {
		return nil, errors.New("snapshot retention must not be negative")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager{roots: roots, locations: locations, options: options, stores: make(map[string]*Store)}, nil
}

func validatePrivateDir(privateDir string, roots incidentstore.RepositoryRoots) error {
	privatePaths, err := lexicalAndResolvedPaths(privateDir)
	if err != nil {
		return fmt.Errorf("validate private evidence directory: %w", err)
	}
	for _, candidate := range privatePaths {
		insideGit, err := pathInsideGitRepository(candidate)
		if err != nil {
			return fmt.Errorf("validate private evidence directory: %w", err)
		}
		if insideGit {
			return ErrUnsafePrivateDir
		}
	}
	repositoryRoots := make([]string, 0, 1+len(roots.Dedicated)+len(roots.Projects))
	repositoryRoots = append(repositoryRoots, roots.Application)
	for _, root := range roots.Dedicated {
		repositoryRoots = append(repositoryRoots, root)
	}
	for _, root := range roots.Projects {
		repositoryRoots = append(repositoryRoots, root)
	}
	for _, repositoryRoot := range repositoryRoots {
		if repositoryRoot == "" {
			continue
		}
		rootPaths, err := lexicalAndResolvedPaths(repositoryRoot)
		if err != nil {
			return fmt.Errorf("validate evidence repository: %w", err)
		}
		for _, candidate := range privatePaths {
			for _, root := range rootPaths {
				if pathWithin(root, candidate) {
					return ErrUnsafePrivateDir
				}
			}
		}
	}
	return nil
}

func lexicalAndResolvedPaths(path string) ([]string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveExistingSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if resolved == abs {
		return []string{abs}, nil
	}
	return []string{abs, resolved}, nil
}

// resolveExistingSymlinks resolves the longest existing prefix without
// creating the configured directory. Non-existent suffixes are appended only
// after their existing ancestor has been resolved.
func resolveExistingSymlinks(path string) (string, error) {
	current := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathInsideGitRepository(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err == nil && !info.IsDir() {
		path = filepath.Dir(path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for current := path; ; current = filepath.Dir(current) {
		gitMarker := filepath.Join(current, ".git")
		if _, err := os.Lstat(gitMarker); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		bare, err := isBareGitRepository(current)
		if err != nil {
			return false, err
		}
		if bare {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
	}
}

func isBareGitRepository(path string) (bool, error) {
	markers := []string{"HEAD", "objects", "refs"}
	for _, marker := range markers {
		if _, err := os.Lstat(filepath.Join(path, marker)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// ProjectStore opens the primary project-repository evidence route.
func (m *Manager) ProjectStore(projectID string) (*Store, error) {
	location, ok := m.locations[projectID]
	if !ok || location.Kind != incidents.StoreLocationProjectRepository {
		return nil, fmt.Errorf("project evidence store %q is not configured", projectID)
	}
	return m.Store(location)
}

// RoutedStore selects an incident-qualified route or the project's primary
// route when incident is absent.
func (m *Manager) RoutedStore(projectID string, incident *apicontract.IncidentRef) (*Store, error) {
	if incident == nil {
		return m.ProjectStore(projectID)
	}
	location, ok := m.locations[incident.StoreID]
	if !ok {
		return nil, fmt.Errorf("incident evidence store %q is not configured", incident.StoreID)
	}
	return m.Store(location)
}

// StoreByID resolves a qualified evidence store for reads where the execution
// reference itself carries the route.
func (m *Manager) StoreByID(projectID, storeID string) (*Store, error) {
	if storeID == "" {
		storeID = projectID
	}
	location, ok := m.locations[storeID]
	if !ok {
		return nil, fmt.Errorf("evidence store %q is not configured", storeID)
	}
	return m.Store(location)
}

// Store opens one configured project, dedicated, or application repository.
func (m *Manager) Store(location incidents.StoreLocation) (*Store, error) {
	if err := location.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.stores[location.StoreID]; existing != nil {
		return existing, nil
	}
	repository, err := incidentstore.OpenLocation(location, m.roots)
	if err != nil {
		return nil, err
	}
	store, err := openStore(location, repository, m.options, m.roots)
	if err != nil {
		_ = repository.Close()
		return nil, err
	}
	m.stores[location.StoreID] = store
	return store, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for id, store := range m.stores {
		errs = append(errs, store.Close())
		delete(m.stores, id)
	}
	return errors.Join(errs...)
}

// Store owns one routed repository and its private snapshot sidecar.
type Store struct {
	location   incidents.StoreLocation
	repository *incidentstore.RepositoryStore
	db         *sql.DB
	byteCap    int
	retention  time.Duration
	now        func() time.Time
}

func openStore(location incidents.StoreLocation, repository *incidentstore.RepositoryStore, options Options, roots incidentstore.RepositoryRoots) (*Store, error) {
	dir := filepath.Join(options.PrivateDir, location.StoreID)
	if err := validatePrivateStoreDir(dir, roots); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create private evidence directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure private evidence directory: %w", err)
	}
	if err := validatePrivateStoreDir(dir, roots); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "snapshots.sqlite")
	if err := ensurePrivateDatabaseFile(path, roots); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open snapshot sidecar: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS snapshots (
		snapshot_ref TEXT PRIMARY KEY,
		store_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		execution_id TEXT NOT NULL UNIQUE,
		availability TEXT NOT NULL,
		changed_at TEXT NOT NULL,
		expires_at_ns INTEGER NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		payload BLOB
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize snapshot sidecar: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure snapshot sidecar: %w", err)
	}
	return &Store{location: location, repository: repository, db: db, byteCap: options.ByteCap, retention: options.Retention, now: options.Now}, nil
}

func validatePrivateStoreDir(dir string, roots incidentstore.RepositoryRoots) error {
	info, err := os.Lstat(dir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnsafePrivateDir
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private evidence store directory: %w", err)
	}
	if err := validatePrivateDir(dir, roots); err != nil {
		return err
	}
	return nil
}

func ensurePrivateDatabaseFile(path string, roots incidentstore.RepositoryRoots) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return ErrUnsafePrivateDir
		}
		return validatePrivateDir(path, roots)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private snapshot database: %w", err)
	}
	if err := validatePrivateDir(path, roots); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ensurePrivateDatabaseFile(path, roots)
	}
	if err != nil {
		return fmt.Errorf("create private snapshot database: %w", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close private snapshot database: %w", closeErr)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify private snapshot database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafePrivateDir
	}
	return validatePrivateDir(path, roots)
}

func (s *Store) Close() error {
	return errors.Join(s.db.Close(), s.repository.Close())
}

func (s *Store) PutExecution(ctx context.Context, record apicontract.ExecutionRecord) error {
	return s.repository.PutExecution(ctx, record)
}

func (s *Store) Execution(ctx context.Context, ref apicontract.ExecutionRef) (apicontract.ExecutionRecord, error) {
	return s.repository.Execution(ctx, ref)
}

func (s *Store) Executions(ctx context.Context) ([]apicontract.ExecutionRecord, error) {
	return s.repository.Executions(ctx)
}

func (s *Store) ExecutionsBounded(ctx context.Context, limit int) ([]apicontract.ExecutionRecord, bool, error) {
	return s.repository.ExecutionsBounded(ctx, limit)
}

// RequireIncident proves that a client-supplied incident association resolves
// in the same routed repository before immutable evidence is written.
func (s *Store) RequireIncident(ctx context.Context, ref incidents.IncidentRef) error {
	_, err := s.repository.Projection(ctx, ref, nil)
	return err
}

// PutSnapshot stores canonical typed-row JSON when it fits the configured
// cap. A refusal returns stored=false and writes no lifecycle row.
func (s *Store) PutSnapshot(ctx context.Context, ref apicontract.ExecutionRef, recordset apicontract.Recordset, executedAt time.Time) (snapshotRef string, stored bool, err error) {
	if err = ref.Validate(); err != nil {
		return "", false, err
	}
	if ref.StoreID != s.location.StoreID {
		return "", false, ErrSnapshotNotFound
	}
	if err = recordset.Validate(); err != nil {
		return "", false, err
	}
	recordset = normalizeRecordset(recordset)
	payload, err := json.Marshal(recordset)
	if err != nil {
		return "", false, fmt.Errorf("encode snapshot: %w", err)
	}
	if len(payload) > s.byteCap {
		return "", false, nil
	}
	snapshotRef = ref.ExecutionID
	changedAt := executedAt.UTC().Format(time.RFC3339Nano)
	expiresAt := executedAt.Add(s.retention).UTC().UnixNano()
	_, err = s.db.ExecContext(ctx, `INSERT INTO snapshots(snapshot_ref,store_id,project_id,execution_id,availability,changed_at,expires_at_ns,payload)
		VALUES(?,?,?,?,?,?,?,?)`, snapshotRef, ref.StoreID, ref.ProjectID, ref.ExecutionID, apicontract.SnapshotAvailable, changedAt, expiresAt, payload)
	if err != nil {
		return "", false, fmt.Errorf("store snapshot: %w", err)
	}
	return snapshotRef, true, nil
}

func normalizeRecordset(recordset apicontract.Recordset) apicontract.Recordset {
	if recordset.Columns == nil {
		recordset.Columns = []apicontract.Column{}
	}
	if recordset.Rows == nil {
		recordset.Rows = [][]apicontract.TypedValue{}
	} else if len(recordset.Columns) == 0 {
		rows := make([][]apicontract.TypedValue, len(recordset.Rows))
		copy(rows, recordset.Rows)
		recordset.Rows = rows
		for i, row := range recordset.Rows {
			if row == nil {
				recordset.Rows[i] = []apicontract.TypedValue{}
			}
		}
	}
	return recordset
}

// RollbackSnapshot removes unpublished bytes after receipt publication fails.
func (s *Store) RollbackSnapshot(ctx context.Context, snapshotRef string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE snapshot_ref=?`, snapshotRef)
	return err
}

func (s *Store) Snapshot(ctx context.Context, ref apicontract.ExecutionRef, snapshotRef string) (apicontract.SnapshotReadResponse, error) {
	if err := ref.Validate(); err != nil {
		return apicontract.SnapshotReadResponse{}, err
	}
	if err := s.ExpireDue(ctx); err != nil {
		return apicontract.SnapshotReadResponse{}, err
	}
	var availability, changedAt, reason string
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT availability,changed_at,reason,payload FROM snapshots
		WHERE snapshot_ref=? AND store_id=? AND project_id=? AND execution_id=?`, snapshotRef, ref.StoreID, ref.ProjectID, ref.ExecutionID).
		Scan(&availability, &changedAt, &reason, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return apicontract.SnapshotReadResponse{}, ErrSnapshotNotFound
	}
	if err != nil {
		return apicontract.SnapshotReadResponse{}, fmt.Errorf("read snapshot: %w", err)
	}
	response := apicontract.SnapshotReadResponse{
		Execution: ref, SnapshotRef: snapshotRef,
		SnapshotState: apicontract.SnapshotState{Availability: availability, ChangedAt: changedAt, Reason: reason},
	}
	if availability == apicontract.SnapshotAvailable {
		var recordset apicontract.Recordset
		if err := apicontract.DecodeStrict(payload, &recordset); err != nil {
			return apicontract.SnapshotReadResponse{}, fmt.Errorf("decode snapshot: %w", err)
		}
		response.Recordset = &recordset
	}
	if err := response.Validate(); err != nil {
		return apicontract.SnapshotReadResponse{}, fmt.Errorf("validate stored snapshot: %w", err)
	}
	return response, nil
}

// State returns the separately stored current lifecycle state.
func (s *Store) State(ctx context.Context, snapshotRef string) (*apicontract.SnapshotState, error) {
	if snapshotRef == "" {
		return nil, nil
	}
	if err := s.ExpireDue(ctx); err != nil {
		return nil, err
	}
	var state apicontract.SnapshotState
	err := s.db.QueryRowContext(ctx, `SELECT availability,changed_at,reason FROM snapshots WHERE snapshot_ref=?`, snapshotRef).
		Scan(&state.Availability, &state.ChangedAt, &state.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSnapshotNotFound
	}
	return &state, err
}

// ExpireDue deletes retained bytes and advances available snapshots to the
// terminal expired state. Receipts and snapshotRef remain untouched.
func (s *Store) ExpireDue(ctx context.Context) error {
	now := s.now().UTC()
	changedAt := now.Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE snapshots SET availability=?,changed_at=?,reason='retention',payload=NULL
		WHERE availability=? AND expires_at_ns<?`, apicontract.SnapshotExpired, changedAt, apicontract.SnapshotAvailable, now.UnixNano())
	if err != nil {
		return fmt.Errorf("expire snapshots: %w", err)
	}
	return nil
}
