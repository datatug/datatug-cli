package executionstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func validExecutionRecord(storeID, projectID, executionID string) apicontract.ExecutionRecord {
	return apicontract.ExecutionRecord{
		Ref:      apicontract.ExecutionRef{StoreID: storeID, ProjectID: projectID, ExecutionID: executionID},
		Scope:    apicontract.ExecutionRecordScope{StoreID: projectID, Project: projectID, Environment: "prod"},
		DTQLHash: strings.Repeat("1", 64), Parameters: map[string]apicontract.TypedValueOrSet{}, BindingsApplied: []apicontract.Binding{},
		Principal:         apicontract.ExecutionPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}},
		PolicyFingerprint: strings.Repeat("2", 64), ExecutedAt: "2026-09-13T10:11:12Z",
		Limitations: []apicontract.Limitation{}, Provenance: apicontract.Provenance{
			Source: "sales", Collection: "orders", Mode: apicontract.ProvenanceModeLive,
			ObservedAt: "2026-09-13T10:11:12Z", ExecutionProfile: apicontract.ExecutionProfileProtected,
		}, AuthorizedFields: []apicontract.FieldAccessRef{}, ResultFingerprint: strings.Repeat("3", 64), Measurements: []apicontract.ScalarMeasurement{},
	}
}

func TestResolvePrivateDir(t *testing.T) {
	// 1. Explicit
	res, err := ResolvePrivateDir("/tmp/custom-evidence")
	require.NoError(t, err)
	require.Equal(t, "/tmp/custom-evidence", res)

	// 2. From env
	t.Setenv(DirEnv, "/tmp/env-evidence")
	res, err = ResolvePrivateDir("")
	require.NoError(t, err)
	require.Equal(t, "/tmp/env-evidence", res)
	t.Setenv(DirEnv, "")

	// 3. User home error
	origHome := userHomeDir
	defer func() { userHomeDir = origHome }()
	userHomeDir = func() (string, error) { return "", errors.New("cannot find home") }
	_, err = ResolvePrivateDir("")
	require.ErrorContains(t, err, "cannot find home")

	// 4. Default home
	userHomeDir = func() (string, error) { return "/tmp/home", nil }
	res, err = ResolvePrivateDir("")
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/tmp/home", filepath.FromSlash(DefaultDir)), res)
}

func TestNewManager_OptionsValidation(t *testing.T) {
	origHome := userHomeDir
	defer func() { userHomeDir = origHome }()
	userHomeDir = func() (string, error) { return "", errors.New("fail home") }
	_, err := NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: ""})
	require.Error(t, err)

	userHomeDir = origHome
	priv := t.TempDir()
	_, err = NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: priv, ByteCap: -1})
	require.ErrorContains(t, err, "snapshot byte cap must not be negative")

	_, err = NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: priv, Retention: -1})
	require.ErrorContains(t, err, "snapshot retention must not be negative")

	// Default options (ByteCap 0, Retention 0, Now nil)
	mgr, err := NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: priv})
	require.NoError(t, err)
	require.Equal(t, DefaultByteCap, mgr.options.ByteCap)
	require.Equal(t, DefaultRetention, mgr.options.Retention)
	require.NotNil(t, mgr.options.Now)
	require.NoError(t, mgr.Close())
}

func TestValidatePrivateDir_BareGitAndRoots(t *testing.T) {
	// Test bare git repo detection
	bareDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bareDir, "HEAD"), []byte("ref: refs/heads/main"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(bareDir, "objects"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(bareDir, "refs"), 0o700))

	privInBare := filepath.Join(bareDir, "sub", "priv")
	_, err := NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: privInBare})
	require.ErrorIs(t, err, ErrUnsafePrivateDir)

	// Test dedicated, project, and application roots
	appRoot := t.TempDir()
	dedicatedRoot := t.TempDir()
	projectRoot := t.TempDir()
	roots := incidentstore.RepositoryRoots{
		Application: appRoot,
		Dedicated:   map[string]string{"d": dedicatedRoot},
		Projects:    map[incidents.ProjectRef]string{{StoreID: "p", ProjectID: "p"}: projectRoot},
	}
	privDir := t.TempDir()
	mgr, err := NewManager(roots, nil, Options{PrivateDir: privDir})
	require.NoError(t, err)
	require.NoError(t, mgr.Close())

	// Private dir inside application root
	_, err = NewManager(roots, nil, Options{PrivateDir: filepath.Join(appRoot, "sub")})
	require.ErrorIs(t, err, ErrUnsafePrivateDir)

	// Private dir inside dedicated root
	_, err = NewManager(roots, nil, Options{PrivateDir: filepath.Join(dedicatedRoot, "sub")})
	require.ErrorIs(t, err, ErrUnsafePrivateDir)
}

func TestManager_RoutingAndCache(t *testing.T) {
	repositoryRoot := t.TempDir()
	privateRoot := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	locationProject := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	locationDedicated := incidents.StoreLocation{StoreID: "ded", Kind: incidents.StoreLocationDedicatedRepository}

	manager, err := NewManager(
		incidentstore.RepositoryRoots{
			Projects:  map[incidents.ProjectRef]string{project: repositoryRoot},
			Dedicated: map[string]string{"ded": repositoryRoot},
		},
		map[string]incidents.StoreLocation{
			"p":   locationProject,
			"ded": locationDedicated,
		},
		Options{PrivateDir: privateRoot},
	)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	// ProjectStore
	_, err = manager.ProjectStore("unknown")
	require.ErrorContains(t, err, "not configured")

	_, err = manager.ProjectStore("ded")
	require.ErrorContains(t, err, "not configured")

	st, err := manager.ProjectStore("p")
	require.NoError(t, err)
	require.NotNil(t, st)

	// Cached store returns same pointer
	st2, err := manager.Store(locationProject)
	require.NoError(t, err)
	require.Same(t, st, st2)

	// RoutedStore with nil incident
	stRouted, err := manager.RoutedStore("p", nil)
	require.NoError(t, err)
	require.Same(t, st, stRouted)

	// RoutedStore with incident
	_, err = manager.RoutedStore("p", &apicontract.IncidentRef{StoreID: "missing"})
	require.ErrorContains(t, err, "not configured")

	stDed, err := manager.RoutedStore("p", &apicontract.IncidentRef{StoreID: "ded"})
	require.NoError(t, err)
	require.NotNil(t, stDed)

	// StoreByID
	stID, err := manager.StoreByID("p", "")
	require.NoError(t, err)
	require.Same(t, st, stID)

	_, err = manager.StoreByID("p", "missing")
	require.ErrorContains(t, err, "not configured")

	// IncidentStoreByID
	incStore, err := manager.IncidentStoreByID("p", "")
	require.NoError(t, err)
	require.NotNil(t, incStore)

	_, err = manager.IncidentStoreByID("p", "missing")
	require.Error(t, err)

	// Store validation error
	_, err = manager.Store(incidents.StoreLocation{})
	require.Error(t, err)
}

func TestStore_DelegationsAndExecutions(t *testing.T) {
	repositoryRoot := t.TempDir()
	privateRoot := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}

	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"p": location},
		Options{PrivateDir: privateRoot},
	)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	store, err := manager.ProjectStore("p")
	require.NoError(t, err)

	ctx := context.Background()
	ref := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-1"}
	record := validExecutionRecord("p", "p", "exec-1")

	err = store.PutExecution(ctx, record)
	require.NoError(t, err)

	got, err := store.Execution(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "exec-1", got.Ref.ExecutionID)

	records, err := store.Executions(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1)

	bounded, hasMore, err := store.ExecutionsBounded(ctx, 10)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, bounded, 1)

	// RequireIncident for non-existent incident
	incRef := incidents.IncidentRef{StoreID: "p", IncidentID: "inc-missing"}
	err = store.RequireIncident(ctx, incRef)
	require.Error(t, err)
}

func TestStore_PutSnapshot_ValidationAndErrors(t *testing.T) {
	repositoryRoot := t.TempDir()
	privateRoot := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}

	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"p": location},
		Options{PrivateDir: privateRoot},
	)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	store, err := manager.ProjectStore("p")
	require.NoError(t, err)

	ctx := context.Background()

	// 1. ref.Validate error
	_, _, err = store.PutSnapshot(ctx, apicontract.ExecutionRef{}, apicontract.Recordset{}, time.Now())
	require.Error(t, err)

	// 2. ref.StoreID mismatch
	refMismatch := apicontract.ExecutionRef{StoreID: "other", ProjectID: "p", ExecutionID: "exec-1"}
	_, _, err = store.PutSnapshot(ctx, refMismatch, apicontract.Recordset{}, time.Now())
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	// 3. recordset.Validate error (e.g. duplicate column name or invalid column)
	validRef := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-invalid-rs"}
	invalidRs := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "", Type: "string"}},
	}
	_, _, err = store.PutSnapshot(ctx, validRef, invalidRs, time.Now())
	require.Error(t, err)

	// 4. normaliseRecordset branches:
	// Columns nil, Rows nil
	refNorm1 := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-norm-1"}
	_, stored, err := store.PutSnapshot(ctx, refNorm1, apicontract.Recordset{}, time.Now())
	require.NoError(t, err)
	require.True(t, stored)

	// Columns empty, Rows having nil row
	refNorm2 := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-norm-2"}
	rsWithNilRow := apicontract.Recordset{
		Rows: [][]apicontract.TypedValue{nil, {}},
	}
	_, stored, err = store.PutSnapshot(ctx, refNorm2, rsWithNilRow, time.Now())
	require.NoError(t, err)
	require.True(t, stored)

	// 5. Canceled context on insert
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	refCanceled := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-canceled"}
	_, _, err = store.PutSnapshot(canceledCtx, refCanceled, apicontract.Recordset{}, time.Now())
	require.Error(t, err)
}

func TestStore_SnapshotAndState_Errors(t *testing.T) {
	repositoryRoot := t.TempDir()
	privateRoot := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}

	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"p": location},
		Options{PrivateDir: privateRoot},
	)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	store, err := manager.ProjectStore("p")
	require.NoError(t, err)
	ctx := context.Background()

	// Snapshot with invalid ref
	_, err = store.Snapshot(ctx, apicontract.ExecutionRef{}, "snap-1")
	require.Error(t, err)

	// State with empty ref
	st, err := store.State(ctx, "")
	require.NoError(t, err)
	require.Nil(t, st)

	// State not found
	_, err = store.State(ctx, "nonexistent")
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	// RollbackSnapshot
	err = store.RollbackSnapshot(ctx, "snap-none")
	require.NoError(t, err)

	// ExpireDue with canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	err = store.ExpireDue(canceledCtx)
	require.Error(t, err)

	// Snapshot ExpireDue error
	_, err = store.Snapshot(canceledCtx, apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-1"}, "exec-1")
	require.Error(t, err)

	// State ExpireDue error
	_, err = store.State(canceledCtx, "exec-1")
	require.Error(t, err)

	// Corrupt payload decode error in Snapshot
	_, err = store.db.ExecContext(ctx, `INSERT INTO snapshots(snapshot_ref,store_id,project_id,execution_id,availability,changed_at,expires_at_ns,payload)
		VALUES('corrupt','p','p','exec-corrupt','available','2026-09-13T10:00:00Z',9999999999999999999,'invalid-json')`)
	require.NoError(t, err)

	refCorrupt := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-corrupt"}
	_, err = store.Snapshot(ctx, refCorrupt, "corrupt")
	require.ErrorContains(t, err, "decode snapshot")

	// Invalid recordset validation error in Snapshot
	_, err = store.db.ExecContext(ctx, `INSERT INTO snapshots(snapshot_ref,store_id,project_id,execution_id,availability,changed_at,expires_at_ns,payload)
		VALUES('invalid-rs','p','p','exec-invalid-rs','available','2026-09-13T10:00:00Z',9999999999999999999,'{"columns":[{"name":"","type":"string"}]}')`)
	require.NoError(t, err)
	refInvalidRS := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-invalid-rs"}
	_, err = store.Snapshot(ctx, refInvalidRS, "invalid-rs")
	require.ErrorContains(t, err, "validate stored snapshot")
}

func TestStore_UnsafeDirectoryAndFileErrors(t *testing.T) {
	repositoryRoot := t.TempDir()
	privateRoot := t.TempDir()

	// Make store dir a regular file instead of directory
	require.NoError(t, os.WriteFile(filepath.Join(privateRoot, "file-store"), []byte("not a dir"), 0o600))
	loc := incidents.StoreLocation{StoreID: "file-store", Kind: incidents.StoreLocationDedicatedRepository}
	manager, err := NewManager(
		incidentstore.RepositoryRoots{Dedicated: map[string]string{"file-store": repositoryRoot}},
		map[string]incidents.StoreLocation{"file-store": loc},
		Options{PrivateDir: privateRoot},
	)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	_, err = manager.Store(loc)
	require.ErrorIs(t, err, ErrUnsafePrivateDir)

	// Make snapshots.sqlite a directory instead of a regular file
	dirStore := filepath.Join(privateRoot, "dir-store")
	require.NoError(t, os.Mkdir(dirStore, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dirStore, "snapshots.sqlite"), 0o700))
	locDir := incidents.StoreLocation{StoreID: "dir-store", Kind: incidents.StoreLocationDedicatedRepository}
	managerDir, err := NewManager(
		incidentstore.RepositoryRoots{Dedicated: map[string]string{"dir-store": repositoryRoot}},
		map[string]incidents.StoreLocation{"dir-store": locDir},
		Options{PrivateDir: privateRoot},
	)
	require.NoError(t, err)
	defer func() { _ = managerDir.Close() }()

	_, err = managerDir.Store(locDir)
	require.ErrorIs(t, err, ErrUnsafePrivateDir)
}

func TestLexicalAndResolvedPaths_Branches(t *testing.T) {
	// abs == resolved
	origEval := filepathEvalSymlinks
	defer func() { filepathEvalSymlinks = origEval }()
	filepathEvalSymlinks = func(path string) (string, error) {
		return path, nil
	}
	paths, err := lexicalAndResolvedPaths("/some/path")
	require.NoError(t, err)
	require.Len(t, paths, 1)

	// filepathAbs error
	origAbs := filepathAbs
	defer func() { filepathAbs = origAbs }()
	filepathAbs = func(path string) (string, error) {
		return "", errors.New("abs error")
	}
	_, err = lexicalAndResolvedPaths("/some/path")
	require.ErrorContains(t, err, "abs error")

	// filepathEvalSymlinks error (not os.ErrNotExist)
	filepathAbs = origAbs
	filepathEvalSymlinks = func(path string) (string, error) {
		return "", errors.New("eval error")
	}
	_, err = lexicalAndResolvedPaths("/some/path")
	require.ErrorContains(t, err, "eval error")

	// parent == current in resolveExistingSymlinks
	filepathEvalSymlinks = func(path string) (string, error) {
		return "", os.ErrNotExist
	}
	_, err = lexicalAndResolvedPaths("/")
	require.Error(t, err)
}

func TestPathWithin(t *testing.T) {
	// filepath.Rel error when mixing relative and absolute
	require.False(t, pathWithin("rel/path", "/abs/path"))
	require.True(t, pathWithin("/a/b", "/a/b/c"))
	require.False(t, pathWithin("/a/b", "/a/c"))
}

func TestPathInsideGitRepository_Branches(t *testing.T) {
	// Path is a file
	tmpFile := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))
	inside, err := pathInsideGitRepository(tmpFile)
	require.NoError(t, err)
	require.False(t, inside)

	// Lstat error not ErrNotExist
	origLstat := osLstat
	defer func() { osLstat = origLstat }()
	osLstat = func(name string) (os.FileInfo, error) {
		return nil, errors.New("lstat error")
	}
	_, err = pathInsideGitRepository(tmpFile)
	require.ErrorContains(t, err, "lstat error")

	// Lstat error on .git marker
	osLstat = func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, ".git") {
			return nil, errors.New("git marker error")
		}
		return origLstat(name)
	}
	_, err = pathInsideGitRepository(t.TempDir())
	require.ErrorContains(t, err, "git marker error")

	// Lstat error on bare git marker
	osLstat = func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, "HEAD") {
			return nil, errors.New("head marker error")
		}
		return origLstat(name)
	}
	_, err = pathInsideGitRepository(t.TempDir())
	require.ErrorContains(t, err, "head marker error")
}

func TestValidatePrivateDir_Errors(t *testing.T) {
	// lexicalAndResolvedPaths error on privateDir
	origAbs := filepathAbs
	defer func() { filepathAbs = origAbs }()
	filepathAbs = func(path string) (string, error) {
		return "", errors.New("abs error")
	}
	err := validatePrivateDir("/some/path", incidentstore.RepositoryRoots{})
	require.ErrorContains(t, err, "abs error")

	// pathInsideGitRepository error
	filepathAbs = origAbs
	origLstat := osLstat
	defer func() { osLstat = origLstat }()
	osLstat = func(name string) (os.FileInfo, error) {
		return nil, errors.New("lstat error")
	}
	err = validatePrivateDir("/some/path", incidentstore.RepositoryRoots{})
	require.ErrorContains(t, err, "lstat error")

	// lexicalAndResolvedPaths error on repositoryRoot
	osLstat = origLstat
	roots := incidentstore.RepositoryRoots{Application: "/app/root"}
	filepathAbs = func(path string) (string, error) {
		if path == "/app/root" {
			return "", errors.New("abs app error")
		}
		return origAbs(path)
	}
	err = validatePrivateDir("/some/path", roots)
	require.ErrorContains(t, err, "abs app error")
}

func TestOpenStore_SeamErrors(t *testing.T) {
	privDir := t.TempDir()
	repoDir := t.TempDir()
	loc := incidents.StoreLocation{StoreID: "s1", Kind: incidents.StoreLocationDedicatedRepository}
	roots := incidentstore.RepositoryRoots{Dedicated: map[string]string{"s1": repoDir}}
	repo, err := incidentstore.OpenLocation(loc, roots)
	require.NoError(t, err)
	defer func() { _ = repo.Close() }()

	opts := Options{PrivateDir: privDir}

	// 1. osMkdirAll error
	origMkdir := osMkdirAll
	defer func() { osMkdirAll = origMkdir }()
	osMkdirAll = func(path string, perm os.FileMode) error {
		return errors.New("mkdir fail")
	}
	_, err = openStore(loc, repo, opts, roots)
	require.ErrorContains(t, err, "mkdir fail")
	osMkdirAll = origMkdir

	// 2. osChmod dir error
	origChmod := osChmod
	defer func() { osChmod = origChmod }()
	osChmod = func(path string, perm os.FileMode) error {
		return errors.New("chmod dir fail")
	}
	_, err = openStore(loc, repo, opts, roots)
	require.ErrorContains(t, err, "chmod dir fail")
	osChmod = origChmod

	// 3. sqlOpen error
	origSqlOpen := sqlOpen
	defer func() { sqlOpen = origSqlOpen }()
	sqlOpen = func(driverName, dataSourceName string) (*sql.DB, error) {
		return nil, errors.New("sql open fail")
	}
	_, err = openStore(loc, repo, opts, roots)
	require.ErrorContains(t, err, "sql open fail")
	sqlOpen = origSqlOpen

	// 4. dbExec error
	origDbExec := dbExec
	defer func() { dbExec = origDbExec }()
	dbExec = func(db *sql.DB, query string, args ...any) (sql.Result, error) {
		return nil, errors.New("db exec fail")
	}
	_, err = openStore(loc, repo, opts, roots)
	require.ErrorContains(t, err, "db exec fail")
	dbExec = origDbExec

	// 5. osChmod file error
	osChmod = func(path string, perm os.FileMode) error {
		if strings.HasSuffix(path, "snapshots.sqlite") {
			return errors.New("chmod db fail")
		}
		return origChmod(path, perm)
	}
	_, err = openStore(loc, repo, opts, roots)
	require.ErrorContains(t, err, "chmod db fail")
	osChmod = origChmod

	// 6. osOpenFile error in ensurePrivateDatabaseFile
	origOpenFile := osOpenFile
	defer func() { osOpenFile = origOpenFile }()
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("open file fail")
	}
	_, err = openStore(loc, repo, Options{PrivateDir: t.TempDir()}, roots)
	require.ErrorContains(t, err, "open file fail")
	osOpenFile = origOpenFile

	// 7. fileClose error in ensurePrivateDatabaseFile
	origFileClose := fileClose
	defer func() { fileClose = origFileClose }()
	fileClose = func(f *os.File) error {
		_ = f.Close()
		return errors.New("close file fail")
	}
	_, err = openStore(loc, repo, Options{PrivateDir: t.TempDir()}, roots)
	require.ErrorContains(t, err, "close file fail")
	fileClose = origFileClose

	// 8. osLstat verification error after close
	origLstat := osLstat
	defer func() { osLstat = origLstat }()
	callCount := 0
	osLstat = func(name string) (os.FileInfo, error) {
		callCount++
		if callCount == 3 { // third lstat call is after file close
			return nil, errors.New("verify lstat fail")
		}
		return origLstat(name)
	}
	_, err = openStore(loc, repo, Options{PrivateDir: t.TempDir()}, roots)
	require.ErrorContains(t, err, "verify lstat fail")
	osLstat = origLstat

	// 9. osLstat in ensurePrivateDatabaseFile returns error other than ErrNotExist
	osLstat = func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, "snapshots.sqlite") {
			return nil, errors.New("inspect db fail")
		}
		return origLstat(name)
	}
	_, err = openStore(loc, repo, Options{PrivateDir: t.TempDir()}, roots)
	require.ErrorContains(t, err, "inspect db fail")
	osLstat = origLstat

	// 10. osLstat in validatePrivateStoreDir returns error other than ErrNotExist
	osLstat = func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, "s1") {
			return nil, errors.New("inspect dir fail")
		}
		return origLstat(name)
	}
	_, err = openStore(loc, repo, Options{PrivateDir: t.TempDir()}, roots)
	require.ErrorContains(t, err, "inspect dir fail")
	osLstat = origLstat

	// 11. jsonMarshal error in PutSnapshot
	st, err := openStore(loc, repo, Options{PrivateDir: t.TempDir()}, roots)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	origJsonMarshal := jsonMarshal
	defer func() { jsonMarshal = origJsonMarshal }()
	jsonMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("json fail")
	}
	_, _, err = st.PutSnapshot(context.Background(), apicontract.ExecutionRef{StoreID: "s1", ProjectID: "p", ExecutionID: "exec-1"}, apicontract.Recordset{}, time.Now())
	require.ErrorContains(t, err, "json fail")
	jsonMarshal = origJsonMarshal
}

func TestOpenStore_ExistingRegularDatabaseFile(t *testing.T) {
	privDir := t.TempDir()
	repoDir := t.TempDir()
	loc := incidents.StoreLocation{StoreID: "s1", Kind: incidents.StoreLocationDedicatedRepository}
	roots := incidentstore.RepositoryRoots{Dedicated: map[string]string{"s1": repoDir}}
	repo, err := incidentstore.OpenLocation(loc, roots)
	require.NoError(t, err)
	defer func() { _ = repo.Close() }()

	// First open creates snapshots.sqlite
	st1, err := openStore(loc, repo, Options{PrivateDir: privDir}, roots)
	require.NoError(t, err)
	require.NoError(t, st1.Close())

	// Second open sees existing snapshots.sqlite (covers line 386 and validatePrivateDir on existing file)
	st2, err := openStore(loc, repo, Options{PrivateDir: privDir}, roots)
	require.NoError(t, err)
	require.NoError(t, st2.Close())
}

func TestStore_SnapshotNotFoundAndExistingState(t *testing.T) {
	privDir := t.TempDir()
	repoDir := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	loc := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	roots := incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repoDir}}
	mgr, err := NewManager(roots, map[string]incidents.StoreLocation{"p": loc}, Options{PrivateDir: privDir})
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	st, err := mgr.ProjectStore("p")
	require.NoError(t, err)

	ctx := context.Background()
	ref := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-1"}

	// Snapshot not found (line 510)
	_, err = st.Snapshot(ctx, ref, "missing-snap")
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	// Put snapshot then read State on existing (line 546)
	_, stored, err := st.PutSnapshot(ctx, ref, apicontract.Recordset{}, time.Now())
	require.NoError(t, err)
	require.True(t, stored)

	state, err := st.State(ctx, "exec-1")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, apicontract.SnapshotAvailable, state.Availability)

	// OpenLocation error in Store (line 289)
	locMissing := incidents.StoreLocation{StoreID: "p2", Kind: incidents.StoreLocationProjectRepository, Project: &incidents.ProjectRef{StoreID: "p2", ProjectID: "p2"}}
	_, err = mgr.Store(locMissing)
	require.Error(t, err)
}

type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

func TestOpenStore_SecondValidateError(t *testing.T) {
	privDir := t.TempDir()
	repoDir := t.TempDir()
	loc := incidents.StoreLocation{StoreID: "s1", Kind: incidents.StoreLocationDedicatedRepository}
	roots := incidentstore.RepositoryRoots{Dedicated: map[string]string{"s1": repoDir}}
	repo, err := incidentstore.OpenLocation(loc, roots)
	require.NoError(t, err)
	defer func() { _ = repo.Close() }()

	origLstat := osLstat
	defer func() { osLstat = origLstat }()
	origChmod := osChmod
	defer func() { osChmod = origChmod }()

	chmodDone := false
	osChmod = func(path string, perm os.FileMode) error {
		chmodDone = true
		return origChmod(path, perm)
	}
	osLstat = func(name string) (os.FileInfo, error) {
		if chmodDone && strings.HasSuffix(name, "s1") {
			chmodDone = false
			return fakeFileInfo{mode: os.ModeSymlink}, nil
		}
		return origLstat(name)
	}
	_, err = openStore(loc, repo, Options{PrivateDir: privDir}, roots)
	require.ErrorIs(t, err, ErrUnsafePrivateDir)
}

func TestEnsurePrivateDatabaseFile_Branches(t *testing.T) {
	roots := incidentstore.RepositoryRoots{}
	gitDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(gitDir, ".git"), 0o700))

	// 1. Path inside git repo (line 406)
	err := ensurePrivateDatabaseFile(filepath.Join(gitDir, "snapshots.sqlite"), roots)
	require.ErrorIs(t, err, ErrUnsafePrivateDir)

	// 2. osOpenFile returns os.ErrExist on first try, then succeeds on retry (line 410)
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "snapshots.sqlite")
	origOpenFile := osOpenFile
	defer func() { osOpenFile = origOpenFile }()
	first := true
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if first {
			first = false
			// create the file so retry finds it
			f, err := origOpenFile(name, flag, perm)
			if err == nil {
				_ = f.Close()
			}
			return nil, os.ErrExist
		}
		return origOpenFile(name, flag, perm)
	}
	err = ensurePrivateDatabaseFile(path, roots)
	require.NoError(t, err)
	osOpenFile = origOpenFile

	// 3. Lstat error after close (line 420)
	tmpDir2 := t.TempDir()
	path2 := filepath.Join(tmpDir2, "snapshots.sqlite")
	origLstat := osLstat
	defer func() { osLstat = origLstat }()
	origFileClose := fileClose
	defer func() { fileClose = origFileClose }()

	afterClose := false
	fileClose = func(f *os.File) error {
		afterClose = true
		return f.Close()
	}
	osLstat = func(name string) (os.FileInfo, error) {
		if afterClose && strings.HasSuffix(name, "snapshots.sqlite") {
			afterClose = false
			return nil, errors.New("lstat after close fail")
		}
		return origLstat(name)
	}
	err = ensurePrivateDatabaseFile(path2, roots)
	require.ErrorContains(t, err, "lstat after close fail")

	// 4. Mode not regular after close (line 423)
	tmpDir3 := t.TempDir()
	path3 := filepath.Join(tmpDir3, "snapshots.sqlite")
	afterClose = false
	fileClose = func(f *os.File) error {
		afterClose = true
		return f.Close()
	}
	osLstat = func(name string) (os.FileInfo, error) {
		if afterClose && strings.HasSuffix(name, "snapshots.sqlite") {
			afterClose = false
			return fakeFileInfo{mode: os.ModeDir}, nil
		}
		return origLstat(name)
	}
	err = ensurePrivateDatabaseFile(path3, roots)
	require.ErrorIs(t, err, ErrUnsafePrivateDir)
}

func TestStore_SnapshotQueryRowError(t *testing.T) {
	privDir := t.TempDir()
	repoDir := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	loc := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	roots := incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repoDir}}
	mgr, err := NewManager(roots, map[string]incidents.StoreLocation{"p": loc}, Options{PrivateDir: privDir})
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	st, err := mgr.ProjectStore("p")
	require.NoError(t, err)

	origQueryRow := queryRowContext
	defer func() { queryRowContext = origQueryRow }()

	// Mock queryRowContext to return a row that fails Scan with error other than ErrNoRows (line 527)
	queryRowContext = func(db *sql.DB, ctx context.Context, query string, args ...any) *sql.Row {
		return db.QueryRowContext(ctx, `SELECT 1 FROM invalid_table_that_does_not_exist`)
	}
	ref := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-1"}
	_, err = st.Snapshot(context.Background(), ref, "exec-1")
	require.ErrorContains(t, err, "read snapshot")
}


