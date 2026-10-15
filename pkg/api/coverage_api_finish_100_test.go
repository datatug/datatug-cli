package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Not parallel: mutates compareCache global state.
func TestCoverageFinish100_CompareCache(t *testing.T) {
	ctx := context.Background()

	// Initial unconfigured state
	require.NoError(t, CloseCompareCache())
	assert.Nil(t, CloseCompareCache())

	sess, err := BeginCompareCache(ctx, comparecache.BeginRequest{})
	assert.NoError(t, err)
	assert.Nil(t, sess)

	_, err = LookupCompareCache(ctx, "comp-1")
	assert.ErrorIs(t, err, comparecache.ErrNotConfigured)

	_, err = PageCompareCache(ctx, comparecache.PageRequest{})
	assert.ErrorIs(t, err, comparecache.ErrNotConfigured)

	// Configure compare cache
	privDir := t.TempDir()
	opts := comparecache.Options{
		PrivateDir: privDir,
		ByteCap:    1024 * 1024,
		TTL:        time.Hour,
	}
	require.NoError(t, ConfigureCompareCache(opts))

	// Reconfigure to cover previous != nil branch
	privDir2 := t.TempDir()
	opts2 := comparecache.Options{
		PrivateDir: privDir2,
		ByteCap:    1024 * 1024,
		TTL:        time.Hour,
	}
	require.NoError(t, ConfigureCompareCache(opts2))

	// Begin session when configured
	sess, err = BeginCompareCache(ctx, comparecache.BeginRequest{
		Left:    apicontract.ExecutionRef{ProjectID: "p", StoreID: "s", ExecutionID: "e1"},
		Right:   apicontract.ExecutionRef{ProjectID: "p", StoreID: "s", ExecutionID: "e2"},
		QueryID: "q1",
	})
	assert.NoError(t, err)
	if sess != nil {
		sess.Abort()
	}

	// Lookup and Page when configured (expected not found / error from empty cache)
	_, _ = LookupCompareCache(ctx, "comp-1")
	_, _ = PageCompareCache(ctx, comparecache.PageRequest{ComparisonID: "comp-1"})

	// Configure error branch (MkdirAll failure)
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))
	badOpts := comparecache.Options{PrivateDir: filepath.Join(tmpFile, "sub")}
	assert.Error(t, ConfigureCompareCache(badOpts))

	// LogCompareCacheSkip
	LogCompareCacheSkip("test-op", nil)
	LogCompareCacheSkip("test-op", errors.New("sample error"))

	// ComparisonCacheID
	id := ComparisonCacheID(
		apicontract.ExecutionRef{ProjectID: "p", StoreID: "s", ExecutionID: "e1"},
		apicontract.ExecutionRef{ProjectID: "p", StoreID: "s", ExecutionID: "e2"},
	)
	assert.NotEmpty(t, id)

	require.NoError(t, CloseCompareCache())
}

// Not parallel: mutates secure session and project paths.
func TestCoverageFinish100_ProjectConnectionCatalog(t *testing.T) {
	ctx := context.Background()

	// 1. Unauthenticated
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, err := LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p1"})
	assert.Error(t, err)

	// Configure valid session
	dir := t.TempDir()
	pID := "p_conn_test"
	paths := map[string]string{pID: dir}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	// 2. Bad store ID
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: "bad", ProjectID: pID})
	assert.Error(t, err)

	// 3. Unknown project
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "unknown_p"})
	assert.Error(t, err)

	// 4. Missing connections/demo-db.json
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)

	// 5. Non-regular file / directory
	connDir := filepath.Join(dir, "connections")
	require.NoError(t, os.MkdirAll(connDir, 0o755))
	connFile := filepath.Join(connDir, "demo-db.json")
	require.NoError(t, os.MkdirAll(connFile, 0o755))
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)
	require.NoError(t, os.RemoveAll(connFile))

	// 6. Too large file (> 256KB)
	bigData := make([]byte, (256<<10)+10)
	require.NoError(t, os.WriteFile(connFile, bigData, 0o600))
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)

	// 7. Invalid JSON
	require.NoError(t, os.WriteFile(connFile, []byte("{invalid json"), 0o600))
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)

	// 8. Invalid URL in source or browserFixture
	badCatalogJSON := `{
		"format": "v1",
		"connections": [{
			"id": "c1",
			"dataset": "ds",
			"storage": "sqlite",
			"readiness": "ready",
			"source": "ftp://bad-scheme.com/db"
		}]
	}`
	require.NoError(t, os.WriteFile(connFile, []byte(badCatalogJSON), 0o600))
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)

	// 9. Valid catalog
	validCatalog := LocalConnectionCatalog{
		Format: "datatug-demo-connections/v1",
		Connections: []LocalCatalogConnection{
			{
				EditionConnection: datatug.EditionConnection{
					ID:        "chinook-sqlite",
					Dataset:   "chinook",
					Storage:   "sqlite",
					Readiness: "public-api",
				},
				Source: "https://demodb.dev/ovdb/v1/databases/chinook",
				BrowserFixture: struct {
					URL   string `json:"url,omitempty"`
					Bytes int64  `json:"bytes,omitempty"`
				}{
					URL:   "https://chinook.demodb.dev/data/chinook.sqlite",
					Bytes: 1007616,
				},
			},
		},
	}
	catalogBytes, err := json.Marshal(validCatalog)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(connFile, catalogBytes, 0o600))

	cat, err := LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.NoError(t, err)
	assert.NotNil(t, cat)

	// 10. StoreID mismatch when explicit is empty (ResolveStoreID returns local, but ref.StoreID != LocalStoreID)
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: "", ProjectID: pID})
	assert.ErrorIs(t, err, ErrUnknownStoreID)

	// 11. servedProjectDir error
	origServed := servedProjectDirCatalog
	servedProjectDirCatalog = func(string) (string, error) { return "", errors.New("served error") }
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.Error(t, err)
	servedProjectDirCatalog = origServed

	// 12. ioReadAll error
	origReadAll := ioReadAllCatalog
	ioReadAllCatalog = func(r io.Reader) ([]byte, error) { return nil, errors.New("read error") }
	_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)
	ioReadAllCatalog = origReadAll

	// 13. URL validation variations: empty source (continue), parse error, bad scheme, empty host, userinfo, query, fragment
	for _, badURL := range []string{
		"://bad-parse",
		"ftp://example.com",
		"https:///empty-host",
		"https://user:pass@example.com",
		"https://example.com?query=val",
		"https://example.com#fragment",
	} {
		badCat := validCatalog
		badCat.Connections[0].Source = badURL
		b, _ := json.Marshal(badCat)
		require.NoError(t, os.WriteFile(connFile, b, 0o600))
		_, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
		assert.ErrorIs(t, err, errConnectionCatalogUnavailable)
	}

	// Empty source covers `if source == "" { continue }`
	emptySourceCat := validCatalog
	emptySourceCat.Connections[0].Source = ""
	emptySourceCat.Connections[0].BrowserFixture.URL = ""
	b, _ := json.Marshal(emptySourceCat)
	require.NoError(t, os.WriteFile(connFile, b, 0o600))
	cat, err = LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.NoError(t, err)
	assert.NotNil(t, cat)

	// Test readLocalConnectionCatalog directly on missing root dir
	_, err = readLocalConnectionCatalog(filepath.Join(dir, "missing_root_dir"))
	assert.ErrorIs(t, err, errConnectionCatalogUnavailable)
}

// Not parallel: tests ResolveCatalogSourceURL and seams.
func TestCoverageFinish100_SourceResolverAndSeams(t *testing.T) {
	// ResolveCatalogSourceURL
	cat := datatug.DbCatalog{
		Driver: "sqlite3",
		Path:   "test.db",
	}
	u, err := ResolveCatalogSourceURL(cat, t.TempDir())
	assert.NoError(t, err)
	assert.NotEmpty(t, u)

	// Seams default functions
	tmpFile, err := os.CreateTemp(t.TempDir(), "seam-test-*")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	n, err := fileWrite(tmpFile, []byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)

	assert.NoError(t, fileSync(tmpFile))
	assert.NoError(t, fileChmod(tmpFile, 0o600))
	assert.NoError(t, fileClose(tmpFile))

	// saveQueryPayloadDigestSeam
	req := dto.SaveQueryRequest{
		OperationID: "op-1",
		ProjectRef:  dto.ProjectRef{StoreID: "local", ProjectID: "p1"},
	}
	digest, err := saveQueryPayloadDigestSeam(req)
	assert.NoError(t, err)
	assert.NotEmpty(t, digest)
}

// Not parallel: mutates legacyStoreResolvesFolders.
func TestCoverageFinish100_SaveLegacyQueryRequireFolderSupport(t *testing.T) {
	origFolders := legacyStoreResolvesFolders
	legacyStoreResolvesFolders = false
	defer func() { legacyStoreResolvesFolders = origFolders }()

	ctx := context.Background()
	dir := t.TempDir()
	pID := "p_leg_folder"
	paths := map[string]string{pID: dir}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	query := &datatug.QueryDefWithFolderPath{
		FolderPath: "my/folder",
		ID:         "q1",
	}
	_, err = saveLegacyQuery(ctx, LocalStoreID, pID, query, legacyUpdate)
	assert.Error(t, err)
}

// Not parallel: mutates contextManager and secure session.
func TestCoverageFinish100_SessionContext(t *testing.T) {
	ctx := context.Background()

	// CloseSessionContext when nil
	assert.NoError(t, CloseSessionContext())

	// ResolveSessionContext - unauthenticated
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, _, _, err := ResolveSessionContext(ctx, LocalStoreID, "p1")
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// Bad store
	_, _, _, err = ResolveSessionContext(ctx, "bad_store", "p1")
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// Valid session setup
	dir := t.TempDir()
	pID := "p_sess_test"
	paths := map[string]string{pID: dir}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	privDir := t.TempDir()
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true, EvidencePrivateDir: privDir})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	defer func() { _ = CloseSessionContext() }()

	// ProjectDir not found
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, "nonexistent_proj")
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// ProjectStoreFor error (store unavailable)
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store unavailable")
	}
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrUnavailable)

	// Setup valid project in filestore
	filestore.SetProjectPath(pID, dir)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(fmt.Sprintf(`{"id":%q,"title":"Test Project"}`, pID)), 0o600))

	// Setup environment via filestore
	projStore := filestore.NewProjectStore(pID, dir)
	env := &datatug.Environment{
		DbServers: datatug.EnvDbServers{
			{
				ServerRef: datatug.ServerRef{Driver: "sqlite3"},
				Catalogs:  []string{"chinook"},
			},
		},
	}
	env.ID = "local"
	require.NoError(t, projStore.SaveEnvironment(ctx, env))
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: "chinook.db", DbModel: "chinook"}}
	catalog.ID = "chinook"
	require.NoError(t, projStore.SaveEnvDbCatalog(ctx, "local", serverID, catalog.ID, catalog))

	// Setup valid connections/demo-db.json for session sample subjects
	connDir := filepath.Join(dir, "connections")
	require.NoError(t, os.MkdirAll(connDir, 0o755))
	connJSON := `{
		"format": "datatug-demo-connections/v1",
		"connections": [{
			"id": "chinook-sqlite",
			"dataset": "chinook",
			"storage": "sqlite",
			"readiness": "public-api",
			"source": "https://demodb.dev/ovdb/v1/databases/chinook",
			"fixtureSha256": "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15",
			"browserFixture": {
				"url": "https://chinook.demodb.dev/data/chinook.sqlite",
				"bytes": 1007616
			}
		}]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(connDir, "demo-db.json"), []byte(connJSON), 0o600))

	// ResolveSessionContext - success path
	svc, principal, caps, err := ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.NoError(t, err)
	assert.NotNil(t, svc)
	assert.NotEmpty(t, principal.Actor)
	assert.NotEmpty(t, caps.Actions)
	assert.NotEmpty(t, caps.Subjects)
	require.Len(t, caps.Environments[0].QuerySources, 1)
	assert.Equal(t, "public-sqlite", caps.Environments[0].QuerySources[0].Kind)
	assert.Equal(t, "chinook-sqlite", caps.Environments[0].QuerySources[0].ConnectionID)
	assert.Equal(t, "chinook", caps.Environments[0].QuerySources[0].DatabaseID)

	// Exercise access check callback of session service via ListSessions
	// 1. Wrong actor
	badActorPrincipal := principal
	badActorPrincipal.Actor = "wrong_actor"
	_, err = svc.ListSessions(ctx, badActorPrincipal, "", 10)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// 2. Wrong databaseID
	badDBPrincipal := principal
	badDBPrincipal.DatabaseID = "wrong_db"
	_, err = svc.ListSessions(ctx, badDBPrincipal, "", 10)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// 3. Wrong projectID
	badProjPrincipal := principal
	badProjPrincipal.ProjectID = "wrong_proj"
	_, err = svc.ListSessions(ctx, badProjPrincipal, "", 10)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// 4. Unadmitted scope via Execute
	unadmittedScope := sessioncontext.Scope{EnvironmentID: "remote", IdentityDomain: "other"}
	cmd := sessioncontext.Command{
		OpID:      "op1",
		Action:    "createSession",
		SessionID: "s1",
		Scope:     &unadmittedScope,
	}
	_, err = svc.Execute(ctx, principal, cmd)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)

	// 5. Valid call
	_, err = svc.ListSessions(ctx, principal, "", 10)
	assert.NoError(t, err)

	// 6. Access check callback when ProjectDir is unmapped
	ConfigureSecureSession(session, map[string]string{}, Capabilities{})
	_, err = svc.ListSessions(ctx, principal, "", 10)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)
	ConfigureSecureSession(session, paths, Capabilities{})

	// CloseSessionContext when non-nil
	assert.NoError(t, CloseSessionContext())

	// Actor empty
	anonSession := secureread.Session{Principal: &access.Principal{ID: ""}}
	ConfigureSecureSession(anonSession, paths, Capabilities{})
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: false, EvidencePrivateDir: privDir})

	// filepathEvalSymlinks error
	origEval := filepathEvalSymlinks
	filepathEvalSymlinks = func(string) (string, error) { return "", errors.New("eval error") }
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrUnavailable)
	filepathEvalSymlinks = origEval

	// sessionstoreNew error
	_ = CloseSessionContext()
	origStoreNew := sessionstoreNew
	sessionstoreNew = func(string) (*sessionstore.Manager, error) { return nil, errors.New("new err") }
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrUnavailable)
	sessionstoreNew = origStoreNew

	// LoadEnvironments error
	badStore := func(string) (storage.Store, error) {
		fs, err := filestore.NewStore("files", paths)
		if err != nil {
			return nil, err
		}
		return &badEnvStore{Store: fs}, nil
	}
	storage.NewDatatugStore = badStore
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrUnavailable)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// Unserved project (covers line 64: ProjectDir(project) !ok)
	origProjDir := sessionContextProjectDir
	sessionContextProjectDir = func(string) (string, bool) { return "", false }
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrForbidden)
	sessionContextProjectDir = origProjDir

	// Nil env and non-sqlite/nil server driver in environments
	mixedStore := func(string) (storage.Store, error) {
		fs, err := filestore.NewStore("files", paths)
		if err != nil {
			return nil, err
		}
		return &mockMixedEnvStore{Store: fs}, nil
	}
	storage.NewDatatugStore = mixedStore
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.NoError(t, err)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// Manager.Repository error (EvidencePrivateDir set to projectDir)
	_ = CloseSessionContext()
	ConfigureSecureSession(session, paths, Capabilities{EvidencePrivateDir: dir})
	_, _, _, err = ResolveSessionContext(ctx, LocalStoreID, pID)
	assert.ErrorIs(t, err, sessioncontext.ErrUnavailable)
	_ = CloseSessionContext()
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: false, EvidencePrivateDir: privDir})

	// Coverage for localSessionSampleSubjects edge branches
	// 1. Missing file -> returns nil
	assert.Nil(t, localSessionSampleSubjects(t.TempDir(), nil))

	// 2. Mismatched fields in connection
	badDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(badDir, "connections"), 0o755))
	for _, badField := range []string{
		`"storage": "postgres"`,
		`"readiness": "draft"`,
		`"source": "https://other.dev"`,
		`"fixtureSha256": "bad"`,
		`"browserFixture": {"url": "https://other", "bytes": 1007616}`,
		`"browserFixture": {"url": "https://chinook.demodb.dev/data/chinook.sqlite", "bytes": 1}`,
	} {
		raw := fmt.Sprintf(`{
			"format": "datatug-demo-connections/v1",
			"connections": [{
				"id": "chinook-sqlite",
				"dataset": "chinook",
				"storage": "sqlite",
				"readiness": "public-api",
				"source": "https://demodb.dev/ovdb/v1/databases/chinook",
				"fixtureSha256": "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15",
				"browserFixture": {
					"url": "https://chinook.demodb.dev/data/chinook.sqlite",
					"bytes": 1007616
				},
				%s
			}]
		}`, badField)
		require.NoError(t, os.WriteFile(filepath.Join(badDir, "connections", "demo-db.json"), []byte(raw), 0o600))
		assert.Nil(t, localSessionSampleSubjects(badDir, nil))
	}

	// 3. Count != 1 (count == 0)
	zeroConnJSON := `{
		"format": "datatug-demo-connections/v1",
		"connections": [
			{"id": "other-db", "dataset": "chinook", "storage": "sqlite", "readiness": "public-api", "source": "https://demodb.dev/ovdb/v1/databases/chinook", "fixtureSha256": "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15", "browserFixture": {"url": "https://chinook.demodb.dev/data/chinook.sqlite", "bytes": 1007616}}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "connections", "demo-db.json"), []byte(zeroConnJSON), 0o600))
	assert.Nil(t, localSessionSampleSubjects(badDir, nil))
}

type badEnvStore struct {
	storage.Store
}

func (s *badEnvStore) GetProjectStore(id string) datatug.ProjectStore {
	ps := s.Store.GetProjectStore(id)
	return &badEnvProjStore{ProjectStore: ps}
}

type badEnvProjStore struct {
	datatug.ProjectStore
}

func (s *badEnvProjStore) LoadEnvironments(ctx context.Context, options ...datatug.StoreOption) (datatug.Environments, error) {
	return nil, errors.New("load env err")
}

type mockMixedEnvStore struct {
	storage.Store
}

func (s *mockMixedEnvStore) GetProjectStore(id string) datatug.ProjectStore {
	ps := s.Store.GetProjectStore(id)
	return &mockMixedEnvProjStore{ProjectStore: ps}
}

type mockMixedEnvProjStore struct {
	datatug.ProjectStore
}

func (s *mockMixedEnvProjStore) LoadEnvironments(ctx context.Context, options ...datatug.StoreOption) (datatug.Environments, error) {
	return datatug.Environments{
		nil,
		&datatug.Environment{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "e1", Title: "E1"}},
			DbServers: datatug.EnvDbServers{
				nil,
				&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "postgres"}},
				&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite"}, Catalogs: []string{"chinook"}},
			},
		},
	}, nil
}
