package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llm "github.com/strongo/aichat/ai"
)

// Not parallel: tests exec_run_query line 428 when actor is empty
func TestCoverageRemaining100_ExecRunQueryAnonymousActor(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	require.NoError(t, err)
	pathsByID := map[string]string{projectID: projectDir}
	api.ConfigureSecureSession(session, pathsByID, api.Capabilities{
		AllowWrites: true,
		SnapshotPolicies: map[string]api.SnapshotProjectPolicy{
			projectID: {Sources: map[string]api.SnapshotSourcePolicy{semanticTestSource: {Allow: true}}},
		},
	})
	require.NoError(t, api.ConfigureExecutionEvidence(pathsByID, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	defer func() {
		storage.NewDatatugStore = origDatatugStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()
	limit := 5
	req := apicontract.ExecutionRequest{
		StoreID:           api.LocalStoreID,
		Project:           projectID,
		Environment:       semanticTestEnv,
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "customers/customer-invoices",
		Parameters:        map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(apicontract.NewIntegerValue("1"))},
		BindingOrigins:    []apicontract.BindingOriginEntry{{ParameterID: "CustomerId", Origin: apicontract.BindingOriginManual}},
		Mode:              apicontract.ProvenanceModeLive,
		Limit:             &limit,
		Record:            true,
	}
	res, err := computeRunQuery(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, res.Execution)
}

// Not parallel: tests session_context.go limit parse error
func TestCoverageRemaining100_SessionContextLimitInvalid(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	envDir := filepath.Join(project, "environments", "uat")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "uat.env.json"), []byte(`{"id":"uat","dbServers":[]}`), 0600))
	paths := map[string]string{"transport-test": project}

	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origDatatugStore
		_ = api.CloseSessionContext()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()

	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})

	router := httprouter.New()
	sessionContextRoutes("/datatug", guardedRouter{router: router, guard: NewRequestGuard("localhost", 0)}, nil, false, Capabilities{AllowWrites: true})

	// Get capabilities first to fetch databaseId and projectId
	wCap := httptest.NewRecorder()
	rCap := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context/capabilities?storage=local&project=transport-test", nil)
	rCap.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wCap, rCap)
	assert.Equal(t, http.StatusOK, wCap.Code)

	var caps api.SessionContextCapabilities
	require.NoError(t, json.Unmarshal(wCap.Body.Bytes(), &caps))

	baseQ := url.Values{
		"storage":    {"local"},
		"project":    {"transport-test"},
		"databaseId": {caps.DatabaseID},
		"projectId":  {caps.ProjectID},
		"limit":      {"bad_limit"},
	}

	wBadLimit := httptest.NewRecorder()
	rBadLimit := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context/sessions?"+baseQ.Encode(), nil)
	rBadLimit.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wBadLimit, rBadLimit)
	assert.Equal(t, http.StatusBadRequest, wBadLimit.Code)
}

// Not parallel: tests project_query_mutation lines 51 and 66 bad project ref
func TestCoverageRemaining100_ProjectQueryMutationBadRef(t *testing.T) {
	rBad := httptest.NewRequest(http.MethodGet, "/datatug/queries/revision?project=bad/project", nil)
	wBad1 := httptest.NewRecorder()
	getProjectQueryRevisionHandler(wBad1, rBad)
	assert.Equal(t, http.StatusBadRequest, wBad1.Code)

	wBad2 := httptest.NewRecorder()
	projectCapabilitiesHandler(wBad2, rBad)
	assert.Equal(t, http.StatusBadRequest, wBad2.Code)
}

// Not parallel: tests compare_rows seams for authorize, loadCompareRecordSide, and pageCompareCache
func TestCoverageRemaining100_CompareRowsSeams(t *testing.T) {
	ctx := context.Background()

	// 1. authorizeCachedComparison: left succeeds, right fails
	origLoad := compareRowsLoadCompareRecordSideHook
	defer func() { compareRowsLoadCompareRecordSideHook = origLoad }()

	calls := 0
	compareRowsLoadCompareRecordSideHook = func(ctx context.Context, req apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
		calls++
		if calls == 1 {
			return compareSideData{}, nil
		}
		return compareSideData{}, errors.New("right side failed")
	}

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{"p": t.TempDir()}, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	meta := comparecache.Meta{
		PolicyFingerprint: api.SecurePolicyFingerprint(),
		QueryID:           "q1",
		Left:              apicontract.ExecutionRef{ProjectID: "p", StoreID: "local", ExecutionID: "e1"},
		Right:             apicontract.ExecutionRef{ProjectID: "p", StoreID: "local", ExecutionID: "e2"},
	}
	err = authorizeCachedComparison(ctx, meta)
	assert.EqualError(t, err, "right side failed")

	// 2. authorizeCachedComparison: both succeed
	calls = 0
	compareRowsLoadCompareRecordSideHook = func(ctx context.Context, req apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
		return compareSideData{}, nil
	}
	assert.NoError(t, authorizeCachedComparison(ctx, meta))

	// 3. compareRowsHandler with compareRowsAuthorizeHook and compareRowsPageCompareCacheHook
	origAuth := compareRowsAuthorizeHook
	origPage := compareRowsPageCompareCacheHook
	defer func() {
		compareRowsAuthorizeHook = origAuth
		compareRowsPageCompareCacheHook = origPage
	}()

	compareRowsAuthorizeHook = func(context.Context, comparecache.Meta) error { return nil }

	// Page returns error
	compareRowsPageCompareCacheHook = func(context.Context, comparecache.PageRequest) (comparecache.Page, error) {
		return comparecache.Page{}, comparecache.ErrNotFound
	}
	secID := api.SecurityContextID()
	rErr := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?securityContextId="+secID+"&comparisonId=c1&state=matched&limit=10", nil)
	wErr := httptest.NewRecorder()

	origLookup := compareRowsLookupCompareCacheHook
	defer func() { compareRowsLookupCompareCacheHook = origLookup }()
	compareRowsLookupCompareCacheHook = func(context.Context, string) (comparecache.Meta, error) { return meta, nil }

	// Now compareRowsHandler when page returns error
	compareRowsHandler(wErr, rErr)
	assert.Equal(t, http.StatusServiceUnavailable, wErr.Code)

	// Now compareRowsHandler when page succeeds
	compareRowsPageCompareCacheHook = func(context.Context, comparecache.PageRequest) (comparecache.Page, error) {
		return comparecache.Page{ComparisonID: "c1", State: "matched"}, nil
	}
	wOk := httptest.NewRecorder()
	compareRowsHandler(wOk, rErr)
	assert.Equal(t, http.StatusOK, wOk.Code)
}

type mockSessionContextService struct {
	readOpFn       func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Receipt, error)
	readRunFn      func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Run, error)
	readInputFn    func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.InputSnapshot, []sessioncontext.EntryRevision, error)
	executeFn      func(ctx context.Context, p sessioncontext.Principal, c sessioncontext.Command) (sessioncontext.CommandResult, error)
	getSnapshotFn  func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error)
	readEntryRevFn func(ctx context.Context, p sessioncontext.Principal, sessionID, entryID, revisionID string) (sessioncontext.EntryRevision, error)
}

func (m *mockSessionContextService) ReadOperation(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Receipt, error) {
	if m.readOpFn != nil {
		return m.readOpFn(ctx, p, id)
	}
	return sessioncontext.Receipt{}, sessioncontext.ErrNotFound
}

func (m *mockSessionContextService) ReadRun(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Run, error) {
	if m.readRunFn != nil {
		return m.readRunFn(ctx, p, id)
	}
	return sessioncontext.Run{}, nil
}

func (m *mockSessionContextService) ReadInput(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.InputSnapshot, []sessioncontext.EntryRevision, error) {
	if m.readInputFn != nil {
		return m.readInputFn(ctx, p, id)
	}
	return sessioncontext.InputSnapshot{}, nil, nil
}

func (m *mockSessionContextService) Execute(ctx context.Context, p sessioncontext.Principal, c sessioncontext.Command) (sessioncontext.CommandResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, p, c)
	}
	return sessioncontext.CommandResult{}, nil
}

func (m *mockSessionContextService) GetSnapshot(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error) {
	if m.getSnapshotFn != nil {
		return m.getSnapshotFn(ctx, p, id)
	}
	return sessioncontext.Snapshot{}, nil
}

func (m *mockSessionContextService) ReadEntryRevision(ctx context.Context, p sessioncontext.Principal, sessionID, entryID, revisionID string) (sessioncontext.EntryRevision, error) {
	if m.readEntryRevFn != nil {
		return m.readEntryRevFn(ctx, p, sessionID, entryID, revisionID)
	}
	return sessioncontext.EntryRevision{}, nil
}

// Not parallel: tests session_context_query branches via seams
func TestCoverageRemaining100_SessionContextQuerySuite(t *testing.T) {
	origResolve := sessionQueryResolveSessionContext
	origCompute := sessionQueryComputeRunQuery
	origStoreFor := sessionQueryProjectStoreFor
	defer func() {
		sessionQueryResolveSessionContext = origResolve
		sessionQueryComputeRunQuery = origCompute
		sessionQueryProjectStoreFor = origStoreFor
	}()

	mockSvc := &mockSessionContextService{}
	p := sessioncontext.Principal{DatabaseID: "db1", ProjectID: "p1"}
	caps := api.SessionContextCapabilities{
		DatabaseID: "db1",
		ProjectID:  "p1",
		Environments: []api.SessionContextEnvironment{
			{EnvironmentID: "uat", IdentityDomain: "dom1"},
		},
	}

	sessionQueryResolveSessionContext = func(ctx context.Context, storage, project string) (sessionContextService, sessioncontext.Principal, api.SessionContextCapabilities, error) {
		if project == "err_project" {
			return nil, p, caps, errors.New("resolve err")
		}
		return mockSvc, p, caps, nil
	}

	qBase := "storage=local&project=p1&databaseId=db1&projectId=p1"

	// 1. sessionQueryResolveSessionContext error -> line 48
	wErr1 := httptest.NewRecorder()
	rErr1 := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?storage=local&project=err_project&databaseId=db1&projectId=p1", nil)
	sessionContextQueryHandler(wErr1, rErr1)
	assert.Equal(t, http.StatusServiceUnavailable, wErr1.Code)

	// 2. Command body databaseId mismatch -> line 72
	reqMismatched := sessionQueryRequest{
		Command: sessioncontext.Command{
			Action:     "pinInput",
			Kind:       "query",
			DatabaseID: "wrong_db",
			Scope:      &sessioncontext.Scope{EnvironmentID: "uat", IdentityDomain: "dom1"},
		},
		QueryID:  "q1",
		Bindings: []sessionQueryBinding{{EntryID: "e1", ParameterID: "p1"}},
	}
	bodyMis, _ := json.Marshal(reqMismatched)
	wMis := httptest.NewRecorder()
	rMis := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyMis))
	sessionContextQueryHandler(wMis, rMis)
	assert.Equal(t, http.StatusForbidden, wMis.Code)

	validReq := sessionQueryRequest{
		Command: sessioncontext.Command{
			Action:          "pinInput",
			Kind:            "query",
			OpID:            "op1",
			SessionID:       "s1",
			ExpectedVersion: 1,
			RunID:           "run1",
			Scope:           &sessioncontext.Scope{EnvironmentID: "uat", IdentityDomain: "dom1"},
		},
		QueryID:  "q1",
		Bindings: []sessionQueryBinding{{EntryID: "e1", ParameterID: "p1"}},
	}
	validBody, _ := json.Marshal(validReq)

	// 3. ReadOperation lookup succeeds, ReadRun fails -> line 87
	mockSvc.readOpFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Receipt, error) {
		return sessioncontext.Receipt{ResourceID: "run1"}, nil
	}
	mockSvc.readRunFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Run, error) {
		return sessioncontext.Run{}, errors.New("read run fail")
	}
	wRunErr := httptest.NewRecorder()
	rRunErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wRunErr, rRunErr)
	assert.Equal(t, http.StatusServiceUnavailable, wRunErr.Code)

	// 4. ReadOperation lookup succeeds, ReadInput fails -> line 93
	mockSvc.readRunFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Run, error) {
		return sessioncontext.Run{InputSnapshotID: "input1"}, nil
	}
	mockSvc.readInputFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.InputSnapshot, []sessioncontext.EntryRevision, error) {
		return sessioncontext.InputSnapshot{}, nil, errors.New("read input fail")
	}
	wInputErr := httptest.NewRecorder()
	rInputErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wInputErr, rInputErr)
	assert.Equal(t, http.StatusServiceUnavailable, wInputErr.Code)

	// 5. ReadOperation lookup succeeds, Replay Execute fails -> line 102
	mockSvc.readInputFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.InputSnapshot, []sessioncontext.EntryRevision, error) {
		return sessioncontext.InputSnapshot{}, nil, nil
	}
	mockSvc.executeFn = func(ctx context.Context, p sessioncontext.Principal, c sessioncontext.Command) (sessioncontext.CommandResult, error) {
		return sessioncontext.CommandResult{}, errors.New("replay execute fail")
	}
	wReplayErr := httptest.NewRecorder()
	rReplayErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wReplayErr, rReplayErr)
	assert.Equal(t, http.StatusServiceUnavailable, wReplayErr.Code)

	// 6. ReadOperation lookup error other than ErrNotFound -> line 112
	mockSvc.readOpFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Receipt, error) {
		return sessioncontext.Receipt{}, errors.New("read op other err")
	}
	wLookupErr := httptest.NewRecorder()
	rLookupErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wLookupErr, rLookupErr)
	assert.Equal(t, http.StatusServiceUnavailable, wLookupErr.Code)

	// Now ReadOperation returns ErrNotFound
	mockSvc.readOpFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Receipt, error) {
		return sessioncontext.Receipt{}, sessioncontext.ErrNotFound
	}

	// 7. sessionQueryProjectStoreFor error -> line 117
	sessionQueryProjectStoreFor = func(projectID string) (datatug.ProjectStore, error) {
		return nil, errors.New("store for err")
	}
	wStoreErr := httptest.NewRecorder()
	rStoreErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wStoreErr, rStoreErr)
	assert.Equal(t, http.StatusServiceUnavailable, wStoreErr.Code)

	// 8. store not RevisionedQueriesStore -> line 122
	type nonRevisionedStore struct{ datatug.ProjectStore }
	sessionQueryProjectStoreFor = func(projectID string) (datatug.ProjectStore, error) {
		return nonRevisionedStore{}, nil
	}
	wNotRev := httptest.NewRecorder()
	rNotRev := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wNotRev, rNotRev)
	assert.Equal(t, http.StatusServiceUnavailable, wNotRev.Code)

	// Set up revisioned mock store
	projectDir := t.TempDir()
	queriesDir := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "q1.query.json"), []byte(`{"id":"q1","type":"SQL","parameters":[{"id":"p1","type":"string","isRequired":true}]}`), 0600))
	paths := map[string]string{"p1": projectDir}
	fStore, err := filestore.NewStore("files", paths)
	require.NoError(t, err)
	projStore := fStore.GetProjectStore("p1")
	mockRevStore := &mockSessionQueryRevisionedStore{ProjectStore: projStore}
	sessionQueryProjectStoreFor = func(projectID string) (datatug.ProjectStore, error) {
		return mockRevStore, nil
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	// 9. ResolveQueryID error (query not found) -> line 128
	reqBadQuery := validReq
	reqBadQuery.QueryID = "nonexistent_query"
	bodyBadQ, _ := json.Marshal(reqBadQuery)
	wBadQ := httptest.NewRecorder()
	rBadQ := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyBadQ))
	sessionContextQueryHandler(wBadQ, rBadQ)
	assert.Equal(t, http.StatusNotFound, wBadQ.Code)

	// 9b. LoadQueryRevision error -> line 154
	mockRevStore.loadErr = errors.New("load rev fail")
	wLoadRevErr := httptest.NewRecorder()
	rLoadRevErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wLoadRevErr, rLoadRevErr)
	assert.Equal(t, http.StatusNotFound, wLoadRevErr.Code)
	mockRevStore.loadErr = nil

	// 10. GetSnapshot error -> line 144
	mockSvc.getSnapshotFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error) {
		return sessioncontext.Snapshot{}, errors.New("get snapshot fail")
	}
	wSnapErr := httptest.NewRecorder()
	rSnapErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wSnapErr, rSnapErr)
	assert.Equal(t, http.StatusServiceUnavailable, wSnapErr.Code)

	// 11. Snapshot version mismatch -> line 148
	mockSvc.getSnapshotFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error) {
		return sessioncontext.Snapshot{
			Session: sessioncontext.Session{Version: 99},
		}, nil
	}
	wVerErr := httptest.NewRecorder()
	rVerErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wVerErr, rVerErr)
	assert.Equal(t, http.StatusConflict, wVerErr.Code)

	// Snapshot with ContextSet and slots having conflicting entry revision -> line 165
	scopeObj := sessioncontext.Scope{EnvironmentID: "uat", IdentityDomain: "dom1"}
	mockSvc.getSnapshotFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error) {
		return sessioncontext.Snapshot{
			Session: sessioncontext.Session{Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{
				Enabled: true,
				Slots: []sessioncontext.AppliedSlot{
					{
						Scope: scopeObj,
						BagID: "b1",
						Entries: []sessioncontext.EntryRef{
							{EntryID: "e1", RevisionID: "r1"},
							{EntryID: "e1", RevisionID: "r2"}, // duplicate with diff revision
						},
					},
				},
			},
		}, nil
	}
	wDupSlot := httptest.NewRecorder()
	rDupSlot := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(validBody))
	sessionContextQueryHandler(wDupSlot, rDupSlot)
	assert.Equal(t, http.StatusBadRequest, wDupSlot.Code)

	// Conflicting entry revision in Command.Attachments -> line 174
	mockSvc.getSnapshotFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error) {
		return sessioncontext.Snapshot{
			Session: sessioncontext.Session{Version: 1},
		}, nil
	}
	reqDupAttach := validReq
	reqDupAttach.Command.Attachments = []sessioncontext.EntryRef{
		{EntryID: "e1", RevisionID: "r1"},
		{EntryID: "e1", RevisionID: "r2"},
	}
	bodyDupAttach, _ := json.Marshal(reqDupAttach)
	wDupAttach := httptest.NewRecorder()
	rDupAttach := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyDupAttach))
	sessionContextQueryHandler(wDupAttach, rDupAttach)
	assert.Equal(t, http.StatusBadRequest, wDupAttach.Code)

	// Valid attachment with e1 -> r1
	validWithAttach := validReq
	validWithAttach.Command.Attachments = []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}}

	// Binding not in refs / undeclared parameter -> line 186
	reqBadParam := validWithAttach
	reqBadParam.Bindings = []sessionQueryBinding{{EntryID: "e1", ParameterID: "unknown_param"}}
	bodyBadParam, _ := json.Marshal(reqBadParam)
	wBadParam := httptest.NewRecorder()
	rBadParam := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyBadParam))
	sessionContextQueryHandler(wBadParam, rBadParam)
	assert.Equal(t, http.StatusBadRequest, wBadParam.Code)

	// Duplicate binding for same parameter -> line 190
	reqDupParam := validWithAttach
	reqDupParam.Bindings = []sessionQueryBinding{
		{EntryID: "e1", ParameterID: "p1"},
		{EntryID: "e1", ParameterID: "p1"},
	}
	bodyDupParam, _ := json.Marshal(reqDupParam)
	wDupParam := httptest.NewRecorder()
	rDupParam := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyDupParam))
	sessionContextQueryHandler(wDupParam, rDupParam)
	assert.Equal(t, http.StatusBadRequest, wDupParam.Code)

	// ReadEntryRevision error -> line 195
	mockSvc.readEntryRevFn = func(ctx context.Context, p sessioncontext.Principal, sessionID, entryID, revisionID string) (sessioncontext.EntryRevision, error) {
		return sessioncontext.EntryRevision{}, errors.New("read entry rev fail")
	}
	bodyValidAttach, _ := json.Marshal(validWithAttach)
	wRevErr := httptest.NewRecorder()
	rRevErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyValidAttach))
	sessionContextQueryHandler(wRevErr, rRevErr)
	assert.Equal(t, http.StatusServiceUnavailable, wRevErr.Code)

	// Non-scalar revision -> line 199
	mockSvc.readEntryRevFn = func(ctx context.Context, p sessioncontext.Principal, sessionID, entryID, revisionID string) (sessioncontext.EntryRevision, error) {
		return sessioncontext.EntryRevision{
			Value: sessioncontext.ValueEnvelope{Kind: "set"},
		}, nil
	}
	wNonScalar := httptest.NewRecorder()
	rNonScalar := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyValidAttach))
	sessionContextQueryHandler(wNonScalar, rNonScalar)
	assert.Equal(t, http.StatusBadRequest, wNonScalar.Code)

	// First Execute error -> line 208
	scalarVal := apicontract.NewStringValue("val1")
	mockSvc.readEntryRevFn = func(ctx context.Context, p sessioncontext.Principal, sessionID, entryID, revisionID string) (sessioncontext.EntryRevision, error) {
		return sessioncontext.EntryRevision{
			Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &scalarVal},
		}, nil
	}
	mockSvc.executeFn = func(ctx context.Context, p sessioncontext.Principal, c sessioncontext.Command) (sessioncontext.CommandResult, error) {
		return sessioncontext.CommandResult{}, errors.New("first execute fail")
	}
	wExecFail := httptest.NewRecorder()
	rExecFail := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyValidAttach))
	sessionContextQueryHandler(wExecFail, rExecFail)
	assert.Equal(t, http.StatusServiceUnavailable, wExecFail.Code)

	// sessionQueryComputeRunQuery error, completion Execute error -> line 228
	execCount := 0
	mockSvc.executeFn = func(ctx context.Context, p sessioncontext.Principal, c sessioncontext.Command) (sessioncontext.CommandResult, error) {
		execCount++
		if execCount == 1 {
			return sessioncontext.CommandResult{Receipt: sessioncontext.Receipt{OpID: "r1"}}, nil
		}
		return sessioncontext.CommandResult{}, errors.New("completion execute fail")
	}
	sessionQueryComputeRunQuery = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{}, errors.New("source execution fail")
	}
	wCompErr := httptest.NewRecorder()
	rCompErr := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyValidAttach))
	sessionContextQueryHandler(wCompErr, rCompErr)
	assert.Equal(t, http.StatusServiceUnavailable, wCompErr.Code)

	// sessionQueryComputeRunQuery error, completion Execute succeeds -> writeContractError line 232
	execCount = 0
	mockSvc.executeFn = func(ctx context.Context, p sessioncontext.Principal, c sessioncontext.Command) (sessioncontext.CommandResult, error) {
		return sessioncontext.CommandResult{Receipt: sessioncontext.Receipt{OpID: "r1"}}, nil
	}
	sessionQueryComputeRunQuery = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{}, errors.New("source execution fail")
	}
	wExecErrResp := httptest.NewRecorder()
	rExecErrResp := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyValidAttach))
	sessionContextQueryHandler(wExecErrResp, rExecErrResp)
	assert.Equal(t, http.StatusInternalServerError, wExecErrResp.Code)

	// sessionQueryComputeRunQuery succeeds, ReadRun error -> line 237
	sessionQueryComputeRunQuery = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{Execution: &apicontract.ExecutionRef{ExecutionID: "ex1"}}, nil
	}
	mockSvc.readRunFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Run, error) {
		return sessioncontext.Run{}, errors.New("read run fail")
	}
	wReadRunFail := httptest.NewRecorder()
	rReadRunFail := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?"+qBase, bytes.NewReader(bodyValidAttach))
	sessionContextQueryHandler(wReadRunFail, rReadRunFail)
	assert.Equal(t, http.StatusServiceUnavailable, wReadRunFail.Code)
}

// Not parallel: tests detective_ai_snapshot.go edge cases and error branches
func TestCoverageRemaining100_DetectiveAISnapshotSuite(t *testing.T) {
	ctx := context.Background()

	// Setup valid project with a query
	project := t.TempDir()
	queriesDir := filepath.Join(project, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0700))
	envDir := filepath.Join(project, "environments", "qa")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "qa.env.json"), []byte(`{"id":"qa"}`), 0600))

	// Query with empty title (line 62) and multi-value parameter (line 67)
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "multi.query.json"), []byte(`{"id":"multi","type":"SQL","parameters":[{"id":"p_multi","type":"string","isMultiValue":true}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "multi.query.sql"), []byte("SELECT 1"), 0600))

	paths := map[string]string{"p": project}
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origDatatugStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)

	// 1. ProjectDir not found -> line 38 (session not configured so ProjectDir returns false while ProjectStoreFor succeeds)
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	_, err = detectiveAISnapshotFromState(ctx, "p", detective.State{})
	assert.ErrorIs(t, err, detective.ErrNotFound)

	// Now configure with "p"
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})

	// 1b. Capacity error -> line 45
	stateCap := detective.State{
		Runs: make([]detective.CheckRun, 201),
	}
	_, err = detectiveAISnapshotFromState(ctx, "p", stateCap)
	assert.ErrorIs(t, err, detective.ErrCapacity)

	// 2. Full state with Validators, Scenarios, Runs -> lines 77, 80, 86
	fullState := detective.State{
		Validators: []detective.PublishedValidator{{ID: "v1", Title: "V1", Version: 1}},
		Scenarios:  []detective.Scenario{{ID: "sc1", Title: "SC1", Version: 1, Inputs: []detective.Input{{Name: "i1"}}}},
		Runs:       []detective.CheckRun{{ID: "r1", CheckID: "chk1", StartedAt: time.Now()}},
	}
	snap, err := detectiveAISnapshotFromState(ctx, "p", fullState)
	assert.NoError(t, err)
	assert.NotEmpty(t, snap.CatalogRevision)
	assert.Len(t, snap.Validators, 1)
	assert.Len(t, snap.Scenarios, 1)
	assert.Len(t, snap.Runs, 1)
	assert.Len(t, snap.Queries, 1)
	assert.Equal(t, "multi", snap.Queries[0].Title)               // line 62
	assert.Equal(t, "set", snap.Queries[0].Inputs[0].Cardinality) // line 67

	// 3. aiSnapshotJSONMarshal error -> line 90
	origMarshal := aiSnapshotJSONMarshal
	defer func() { aiSnapshotJSONMarshal = origMarshal }()
	aiSnapshotJSONMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal fail") }
	_, err = detectiveAISnapshotFromState(ctx, "p", fullState)
	assert.EqualError(t, err, "marshal fail")
}

// Not parallel: tests detective_ai.go edge cases and ports
func TestCoverageRemaining100_DetectiveAISuite(t *testing.T) {
	ctx := context.Background()

	// 1. Call default newDetectiveAIProvider -> line 20
	_, _ = newDetectiveAIProvider("gpt-4o", "", "key")

	project, private := t.TempDir(), t.TempDir()
	paths := map[string]string{"p": project}
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origDatatugStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()

	profile := dtconfig.AIProfile{Model: "gpt-4o"}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})

	secID := api.SecurityContextID()
	scope := localAIScope("p", "alice")
	store, _, err := detectiveStore("p", secID)
	require.NoError(t, err)

	ports := newLocalAIPorts(store, scope, "p", secID)

	// 2. ports.AuthorizeSources store error -> line 186
	portsBadProj := newLocalAIPorts(store, scope, "wrong_proj", secID)
	assert.Error(t, portsBadProj.AuthorizeSources(ctx, scope, nil))

	// 3. ports.Current scope mismatch -> line 192, store error -> line 196
	_, err = ports.Current(ctx, localAIScope("p", "wrong_actor"))
	assert.ErrorIs(t, err, ai.ErrUnauthorized)
	_, err = portsBadProj.Current(ctx, localAIScope("wrong_proj", "alice"))
	assert.ErrorIs(t, err, ai.ErrUnauthorized)

	// 4. ports.Authorize scope mismatch or !AllowWrites -> line 202
	assert.ErrorIs(t, ports.Authorize(ctx, localAIScope("p", "wrong_actor"), ai.Proposal{}), ai.ErrUnauthorized)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: false, EvidencePrivateDir: private, DetectiveAIProfile: &profile})
	assert.ErrorIs(t, ports.Authorize(ctx, scope, ai.Proposal{Scope: scope}), ai.ErrUnauthorized)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})

	// 5. ports.PriorReceipt scope mismatch -> line 208
	_, _, err = ports.PriorReceipt(ctx, localAIScope("p", "wrong_actor"), "op1")
	assert.ErrorIs(t, err, ai.ErrUnauthorized)

	// 6. ports.Dispatch branches -> lines 217, 220, 223, 225
	cmdCheck := ai.Command{Proposal: ai.Proposal{Draft: ai.Draft{Kind: ai.KindCheck}}}
	_, _ = ports.Dispatch(ctx, cmdCheck) // line 217

	cmdQuery := ai.Command{Proposal: ai.Proposal{Draft: ai.Draft{Kind: ai.KindQuery}}}
	_, _ = ports.Dispatch(ctx, cmdQuery) // line 220

	cmdRun := ai.Command{Proposal: ai.Proposal{Draft: ai.Draft{Kind: ai.KindRun}}}
	_, _ = ports.Dispatch(ctx, cmdRun) // line 223

	cmdUnknown := ai.Command{Proposal: ai.Proposal{Draft: ai.Draft{Kind: "unsupported"}}}
	_, err = ports.Dispatch(ctx, cmdUnknown)
	assert.ErrorIs(t, err, ai.ErrUnsupported) // line 225

	// 7. GET detectiveAIProposalsHandler -> line 59
	wGet := httptest.NewRecorder()
	rGet := httptest.NewRequest(http.MethodGet, "/datatug/detective/ai_proposals?project=p&securityContextId="+api.SecurityContextID(), nil)
	detectiveAIProposalsHandler(wGet, rGet)
	assert.Equal(t, http.StatusOK, wGet.Code)

	// GET with bad project -> line 56
	wGetBad := httptest.NewRecorder()
	rGetBad := httptest.NewRequest(http.MethodGet, "/datatug/detective/ai_proposals?project=wrong&securityContextId="+api.SecurityContextID(), nil)
	detectiveAIProposalsHandler(wGetBad, rGetBad)
	assert.Equal(t, http.StatusNotFound, wGetBad.Code)

	// 8. POST detectiveAIProposalsHandler error branches
	// !AllowWrites -> line 64
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: false, EvidencePrivateDir: private, DetectiveAIProfile: &profile})
	wNoWrite := httptest.NewRecorder()
	rNoWrite := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", nil)
	detectiveAIProposalsHandler(wNoWrite, rNoWrite)
	assert.Equal(t, http.StatusForbidden, wNoWrite.Code)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})

	// readIncidentBody error -> line 69
	wBadBody := httptest.NewRecorder()
	rBadBody := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", strings.NewReader("bad json"))
	detectiveAIProposalsHandler(wBadBody, rBadBody)
	assert.Equal(t, http.StatusBadRequest, wBadBody.Code)

	// invalid request (empty operationId) -> line 73
	reqInvalid := aiGenerateRequest{Project: "p", OperationID: "", Text: "hello"}
	bInv, _ := json.Marshal(reqInvalid)
	wInv := httptest.NewRecorder()
	rInv := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bInv))
	detectiveAIProposalsHandler(wInv, rInv)
	assert.Equal(t, http.StatusBadRequest, wInv.Code)

	// invalid expected count -> line 78
	reqBadCount := aiGenerateRequest{Project: "p", OperationID: "op1", Text: "hello", ExpectedCounts: map[string]int{"invalid param name!": 5}}
	bCount, _ := json.Marshal(reqBadCount)
	wCount := httptest.NewRecorder()
	rCount := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bCount))
	detectiveAIProposalsHandler(wCount, rCount)
	assert.Equal(t, http.StatusBadRequest, wCount.Code)

	// detectiveStore error -> line 84
	reqStoreErr := aiGenerateRequest{Project: "p", SecurityContextID: "wrong_sec", OperationID: "op1", Text: "hello"}
	bStore, _ := json.Marshal(reqStoreErr)
	wStore := httptest.NewRecorder()
	rStore := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bStore))
	detectiveAIProposalsHandler(wStore, rStore)
	assert.Equal(t, http.StatusConflict, wStore.Code)

	// CaseID not found -> line 89
	reqCaseErr := aiGenerateRequest{Project: "p", SecurityContextID: api.SecurityContextID(), OperationID: "op1", CaseID: "nonexistent-case", Text: "hello"}
	bCase, _ := json.Marshal(reqCaseErr)
	wCase := httptest.NewRecorder()
	rCase := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bCase))
	detectiveAIProposalsHandler(wCase, rCase)
	assert.Equal(t, http.StatusNotFound, wCase.Code)

	// profile == nil -> line 106
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: nil})
	reqValid := aiGenerateRequest{Project: "p", SecurityContextID: api.SecurityContextID(), OperationID: "op1", Text: "hello"}
	bVal, _ := json.Marshal(reqValid)
	wNoProf := httptest.NewRecorder()
	rNoProf := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bVal))
	detectiveAIProposalsHandler(wNoProf, rNoProf)
	assert.Equal(t, http.StatusBadRequest, wNoProf.Code)

	// APIKeyEnv credential missing -> line 113
	profKey := dtconfig.AIProfile{Model: "gpt-4o", APIKeyEnv: "NONEXISTENT_ENV_KEY_12345"}
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profKey})
	bValNoKey, _ := json.Marshal(aiGenerateRequest{Project: "p", SecurityContextID: api.SecurityContextID(), OperationID: "op1", Text: "hello"})
	wNoKey := httptest.NewRecorder()
	rNoKey := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bValNoKey))
	detectiveAIProposalsHandler(wNoKey, rNoKey)
	assert.Equal(t, http.StatusBadRequest, wNoKey.Code)

	// newDetectiveAIProvider error -> line 119
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})
	origProv := newDetectiveAIProvider
	defer func() { newDetectiveAIProvider = origProv }()
	newDetectiveAIProvider = func(string, string, string) (llm.LLMProvider, error) {
		return nil, errors.New("provider create fail")
	}
	bValProv, _ := json.Marshal(aiGenerateRequest{Project: "p", SecurityContextID: api.SecurityContextID(), OperationID: "op1", Text: "hello"})
	wProvErr := httptest.NewRecorder()
	rProvErr := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bValProv))
	detectiveAIProposalsHandler(wProvErr, rProvErr)
	assert.Equal(t, http.StatusBadRequest, wProvErr.Code)

	// 9. detectiveAIReviewHandler error branches
	// !AllowWrites -> line 231
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: false, EvidencePrivateDir: private})
	wRevNoW := httptest.NewRecorder()
	rRevNoW := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_review?project=p", nil)
	detectiveAIReviewHandler(wRevNoW, rRevNoW)
	assert.Equal(t, http.StatusForbidden, wRevNoW.Code)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})

	// readIncidentBody error -> line 236
	wRevBadB := httptest.NewRecorder()
	rRevBadB := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_review?project=p", strings.NewReader("bad"))
	detectiveAIReviewHandler(wRevBadB, rRevBadB)
	assert.Equal(t, http.StatusBadRequest, wRevBadB.Code)

	// invalid request -> line 240
	revInvalid := aiReviewRequest{Project: "p", Type: "unknown"}
	bRevInv, _ := json.Marshal(revInvalid)
	wRevInv := httptest.NewRecorder()
	rRevInv := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_review?project=p", bytes.NewReader(bRevInv))
	detectiveAIReviewHandler(wRevInv, rRevInv)
	assert.Equal(t, http.StatusBadRequest, wRevInv.Code)

	// detectiveStore error -> line 245
	revStoreErr := aiReviewRequest{Project: "p", SecurityContextID: "wrong_sec", Type: "draft", Draft: &ai.ReviewedDraftRequest{}}
	bRevStore, _ := json.Marshal(revStoreErr)
	wRevStore := httptest.NewRecorder()
	rRevStore := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_review?project=p", bytes.NewReader(bRevStore))
	detectiveAIReviewHandler(wRevStore, rRevStore)
	assert.Equal(t, http.StatusConflict, wRevStore.Code)

	// Draft != nil path -> line 252
	revDraft := aiReviewRequest{Project: "p", SecurityContextID: api.SecurityContextID(), Type: "draft", Draft: &ai.ReviewedDraftRequest{ProposalID: "p1", Digest: "dig"}}
	bRevDraft, _ := json.Marshal(revDraft)
	wRevDraft := httptest.NewRecorder()
	rRevDraft := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_review?project=p", bytes.NewReader(bRevDraft))
	detectiveAIReviewHandler(wRevDraft, rRevDraft)
	assert.NotEqual(t, http.StatusInternalServerError, wRevDraft.Code)

	// 10. detectiveAIConfirmHandler error branches
	// !AllowWrites -> line 262
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: false, EvidencePrivateDir: private})
	wConfNoW := httptest.NewRecorder()
	rConfNoW := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_confirm?project=p", nil)
	detectiveAIConfirmHandler(wConfNoW, rConfNoW)
	assert.Equal(t, http.StatusForbidden, wConfNoW.Code)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})

	// readIncidentBody error -> line 267
	wConfBadB := httptest.NewRecorder()
	rConfBadB := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_confirm?project=p", strings.NewReader("bad"))
	detectiveAIConfirmHandler(wConfBadB, rConfBadB)
	assert.Equal(t, http.StatusBadRequest, wConfBadB.Code)

	// URL and body project differ -> line 271
	confDiff := aiConfirmRequest{Project: "other_proj"}
	bConfDiff, _ := json.Marshal(confDiff)
	wConfDiff := httptest.NewRecorder()
	rConfDiff := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_confirm?project=p", bytes.NewReader(bConfDiff))
	detectiveAIConfirmHandler(wConfDiff, rConfDiff)
	assert.Equal(t, http.StatusBadRequest, wConfDiff.Code)

	// detectiveStore error -> line 276
	confStoreErr := aiConfirmRequest{Project: "p", SecurityContextID: "wrong_sec"}
	bConfStore, _ := json.Marshal(confStoreErr)
	wConfStore := httptest.NewRecorder()
	rConfStore := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_confirm?project=p", bytes.NewReader(bConfStore))
	detectiveAIConfirmHandler(wConfStore, rConfStore)
	assert.Equal(t, http.StatusConflict, wConfStore.Code)
}

// Not parallel: tests detective_incidents.go branches
func TestCoverageRemaining100_DetectiveIncidentsSuite(t *testing.T) {
	ctx := context.Background()

	// 1. incidentHasProject project mismatch -> line 77
	item := incidents.Incident{
		Projects: []incidents.ProjectRef{{StoreID: "local", ProjectID: "p1", Environment: "uat"}},
	}
	assert.False(t, incidentHasProject(item, incidents.ProjectRef{StoreID: "local", ProjectID: "other_p", Environment: "uat"}))

	// Setup session and evidence store
	project, private, dedicated := t.TempDir(), t.TempDir(), t.TempDir()
	envDir := filepath.Join(project, "environments", "qa")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "qa.env.json"), []byte(`{"id":"qa"}`), 0600))
	paths := map[string]string{"p": project}
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origDatatugStore
		_ = api.CloseExecutionEvidence()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	require.NoError(t, api.ConfigureExecutionEvidence(paths, []incidentstore.ConfiguredStore{{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository, Repository: dedicated}}, executionstore.Options{PrivateDir: private}))

	secID := api.SecurityContextID()
	scope := incidents.ProjectRef{StoreID: "local", ProjectID: "p", Environment: "qa"}

	// 2. detectiveIncidentView store.Events error -> line 86
	rReq := httptest.NewRequest(http.MethodGet, "/test", nil)
	incItem := incidents.Incident{
		Ref:      incidents.IncidentRef{StoreID: "ops", IncidentID: "inc1"},
		Projects: []incidents.ProjectRef{scope},
	}
	incStore, err := api.IncidentStoreByID("p", "ops")
	require.NoError(t, err)
	errEvStore := &customEventsStore{APIStore: incStore, err: errors.New("events fail")}
	_, err = detectiveIncidentView(rReq, errEvStore, incItem, scope)
	assert.Error(t, err)

	// 3. detectiveIncidentsHandler error branches
	// GetCase error -> line 106
	wNoCase := httptest.NewRecorder()
	rNoCase := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=nonexistent&securityContextId="+secID, nil)
	detectiveIncidentsHandler(wNoCase, rNoCase)
	assert.Equal(t, http.StatusNotFound, wNoCase.Code)

	// Case with > 20 IncidentRefs -> line 111
	localStore, _, err := detectiveStore("p", secID)
	require.NoError(t, err)

	putCaseWithRefs := func(id, title string, refs []string) {
		_, err := localStore.SaveCase(ctx, detective.Case{ID: id, Title: title}, "alice", "create-case")
		require.NoError(t, err)
		matches, _ := filepath.Glob(filepath.Join(private, "detective", "*", "cases", id+".json"))
		require.NotEmpty(t, matches)
		data, err := os.ReadFile(matches[0])
		require.NoError(t, err)
		var cObj map[string]any
		require.NoError(t, json.Unmarshal(data, &cObj))
		cObj["incidentRefs"] = refs
		newData, err := json.Marshal(cObj)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(matches[0], newData, 0600))
	}

	manyRefs := make([]string, 25)
	for i := range manyRefs {
		manyRefs[i] = fmt.Sprintf("p/qa/inc-%d", i)
	}
	putCaseWithRefs("case-many", "Many", manyRefs)

	wMany := httptest.NewRecorder()
	rMany := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=case-many&securityContextId="+api.SecurityContextID(), nil)
	detectiveIncidentsHandler(wMany, rMany)
	assert.Equal(t, http.StatusBadRequest, wMany.Code)

	// Case with invalid incident ref text -> line 117
	putCaseWithRefs("case-bad-ref", "Bad", []string{"invalid ref!"})
	wBadRef := httptest.NewRecorder()
	rBadRef := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=case-bad-ref&securityContextId="+api.SecurityContextID(), nil)
	detectiveIncidentsHandler(wBadRef, rBadRef)
	assert.Equal(t, http.StatusForbidden, wBadRef.Code)

	// Case with unknown incident store -> line 122
	putCaseWithRefs("case-unk-store", "Unk", []string{"unknown_store/inc1"})
	wUnkStore := httptest.NewRecorder()
	rUnkStore := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=case-unk-store&securityContextId="+api.SecurityContextID(), nil)
	detectiveIncidentsHandler(wUnkStore, rUnkStore)
	assert.Equal(t, http.StatusForbidden, wUnkStore.Code)

	// Case with projection load error -> line 127
	putCaseWithRefs("case-proj-err", "Proj", []string{"ops/INC-MISSING"})
	wProjErr := httptest.NewRecorder()
	rProjErr := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=case-proj-err&securityContextId="+api.SecurityContextID(), nil)
	detectiveIncidentsHandler(wProjErr, rProjErr)
	assert.Equal(t, http.StatusNotFound, wProjErr.Code)

	// 4. detectiveIncidentActionHandler error branches
	// !AllowWrites -> line 156
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: false, EvidencePrivateDir: private})
	wNoW := httptest.NewRecorder()
	rNoW := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", nil)
	detectiveIncidentActionHandler(wNoW, rNoW)
	assert.Equal(t, http.StatusForbidden, wNoW.Code)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})

	// readIncidentBody error -> line 161
	wBadB := httptest.NewRecorder()
	rBadB := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", strings.NewReader("bad"))
	detectiveIncidentActionHandler(wBadB, rBadB)
	assert.Equal(t, http.StatusBadRequest, wBadB.Code)

	// !detectiveIncidentActionValid -> line 165
	actInvalid := detectiveIncidentAction{OperationID: "bad id!"}
	bActInv, _ := json.Marshal(actInvalid)
	wActInv := httptest.NewRecorder()
	rActInv := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bActInv))
	detectiveIncidentActionHandler(wActInv, rActInv)
	assert.Equal(t, http.StatusBadRequest, wActInv.Code)

	// detectiveStore error -> line 170
	actStoreErr := detectiveIncidentAction{OperationID: "op1", Type: "create", CaseID: "c1", Title: "T", Project: "wrong", SecurityContextID: api.SecurityContextID()}
	bActStore, _ := json.Marshal(actStoreErr)
	wActStore := httptest.NewRecorder()
	rActStore := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bActStore))
	detectiveIncidentActionHandler(wActStore, rActStore)
	assert.Equal(t, http.StatusNotFound, wActStore.Code)

	// detectiveIncidentEnvironment error -> line 175
	actEnvErr := detectiveIncidentAction{OperationID: "op1", Type: "create", CaseID: "c1", Title: "T", Project: "p", SecurityContextID: api.SecurityContextID(), Environment: "bad_env"}
	bActEnv, _ := json.Marshal(actEnvErr)
	wActEnv := httptest.NewRecorder()
	rActEnv := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bActEnv))
	detectiveIncidentActionHandler(wActEnv, rActEnv)
	assert.Equal(t, http.StatusForbidden, wActEnv.Code)

	// Type != "create": ParseIncidentRef error -> line 185
	actBadRef := detectiveIncidentAction{OperationID: "op1", Type: "link", CaseID: "c1", Project: "p", SecurityContextID: api.SecurityContextID(), Environment: "qa", IncidentRef: "bad ref!"}
	bBadRef, _ := json.Marshal(actBadRef)
	wBadRef2 := httptest.NewRecorder()
	rBadRef2 := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bBadRef))
	detectiveIncidentActionHandler(wBadRef2, rBadRef2)
	assert.Equal(t, http.StatusBadRequest, wBadRef2.Code)

	// Type != "create": IncidentStoreByID error -> line 192
	actUnkStore := detectiveIncidentAction{OperationID: "op1", Type: "link", CaseID: "c1", Project: "p", SecurityContextID: api.SecurityContextID(), Environment: "qa", IncidentRef: "unknown_store/inc1"}
	bUnkStore, _ := json.Marshal(actUnkStore)
	wUnkStore2 := httptest.NewRecorder()
	rUnkStore2 := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bUnkStore))
	detectiveIncidentActionHandler(wUnkStore2, rUnkStore2)
	assert.Equal(t, http.StatusForbidden, wUnkStore2.Code)

	// 5. detectiveContextFromIncident: default range condition and missing start/end -> lines 374, 378
	valStart := apicontract.NewIntegerValue("1")
	badCondView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				{ID: "case-f1-start", Entity: "case", Field: "f1", Value: investigation.VisibleValue(valStart), Condition: "unknown_condition", Enabled: true, Scope: &scope},
				{ID: "case-f1-end", Entity: "case", Field: "f1", Value: investigation.VisibleValue(valStart), Condition: "unknown_condition2", Enabled: true, Scope: &scope},
			},
		},
	}
	_, err = detectiveContextFromIncident(badCondView, scope)
	assert.ErrorIs(t, err, detective.ErrInvalid) // line 374

	// Start or end missing
	badStartView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				{ID: "case-f1-start", Entity: "case", Field: "f1", Value: investigation.ValueView{}, Condition: investigation.FactConditionGreaterThanOrEqual, Enabled: true, Scope: &scope},
				{ID: "case-f1-end", Entity: "case", Field: "f1", Value: investigation.VisibleValue(valStart), Condition: investigation.FactConditionLessThanOrEqual, Enabled: true, Scope: &scope},
			},
		},
	}
	// Note: detectiveContextFromIncident checks fact.Value.Value == nil in first loop and returns ErrInvalid
	_, err = detectiveContextFromIncident(badStartView, scope)
	assert.ErrorIs(t, err, detective.ErrInvalid)
}

type customEventsStore struct {
	incidents.APIStore
	err error
}

func (c *customEventsStore) Events(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.APIStore.Events(ctx, ref, after)
}

// Not parallel: tests detective.go remaining branches
func TestCoverageRemaining100_DetectiveSuite(t *testing.T) {
	ctx := context.Background()

	// 1. detectiveResolvePrivateDir error -> line 78
	origResolve := detectiveResolvePrivateDir
	defer func() { detectiveResolvePrivateDir = origResolve }()
	detectiveResolvePrivateDir = func(string) (string, error) { return "", errors.New("private dir fail") }

	project := t.TempDir()
	paths := map[string]string{"p": project}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	secID := api.SecurityContextID()
	_, _, err = detectiveStore("p", secID)
	assert.EqualError(t, err, "private dir fail")

	// Restore resolve
	detectiveResolvePrivateDir = origResolve

	// 2. detectiveEnvironments LoadEnvironments error -> line 107
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return &errLoadingEnvironmentsStore{}, nil
	}
	defer func() { storage.NewDatatugStore = origDatatugStore }()

	_, err = detectiveEnvironments(ctx, "p")
	assert.EqualError(t, err, "load env fail")

	// 3. detectiveActionHandler error branches
	// readIncidentBody error -> line 129
	wBadB := httptest.NewRecorder()
	rBadB := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", strings.NewReader("bad"))
	detectiveActionHandler(wBadB, rBadB)
	assert.Equal(t, http.StatusBadRequest, wBadB.Code)

	// invalid operationId -> line 134
	wBadSlug := httptest.NewRecorder()
	rBadSlug := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", strings.NewReader(`{"operationId":"bad slug!"}`))
	detectiveActionHandler(wBadSlug, rBadSlug)
	assert.Equal(t, http.StatusBadRequest, wBadSlug.Code)

	// Reset store to filestore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// run-check invalid environment -> line 161
	bodyBadEnv := struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{
		OperationID: "op1",
		ActionRequest: detective.ActionRequest{
			Type:              "run-check",
			Project:           "p",
			SecurityContextID: secID,
			Environment:       "",
		},
	}
	bBadEnv, _ := json.Marshal(bodyBadEnv)
	wBadEnv := httptest.NewRecorder()
	rBadEnv := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", bytes.NewReader(bBadEnv))
	detectiveActionHandler(wBadEnv, rBadEnv)
	assert.Equal(t, http.StatusBadRequest, wBadEnv.Code)

	// run-query-validators invalid environment -> line 165
	bodyBadEnv.Type = "run-query-validators"
	bBadEnvQ, _ := json.Marshal(bodyBadEnv)
	wBadEnvQ := httptest.NewRecorder()
	rBadEnvQ := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", bytes.NewReader(bBadEnvQ))
	detectiveActionHandler(wBadEnvQ, rBadEnvQ)
	assert.Equal(t, http.StatusBadRequest, wBadEnvQ.Code)

	// run-query-validators resolveErr != nil -> line 171
	bodyBadEnv.Environment = "uat"
	bodyBadEnv.QueryID = "nonexistent_query"
	bBadQID, _ := json.Marshal(bodyBadEnv)
	wBadQID := httptest.NewRecorder()
	rBadQID := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", bytes.NewReader(bBadQID))
	detectiveActionHandler(wBadQID, rBadQID)
	assert.Equal(t, http.StatusBadRequest, wBadQID.Code)

	// save-query-validators resolveErr != nil -> line 154
	bodySaveVal := struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{
		OperationID: "op1",
		ActionRequest: detective.ActionRequest{
			Type:              "save-query-validators",
			Project:           "p",
			SecurityContextID: secID,
			QueryValidators:   &detective.QueryValidators{QueryID: "nonexistent_query"},
		},
	}
	bSaveVal, _ := json.Marshal(bodySaveVal)
	wSaveVal := httptest.NewRecorder()
	rSaveVal := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", bytes.NewReader(bSaveVal))
	detectiveActionHandler(wSaveVal, rSaveVal)
	assert.Equal(t, http.StatusBadRequest, wSaveVal.Code)

	// record-scenario-step invalid execution -> line 183
	bodyRecStep := struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{
		OperationID: "op1",
		ActionRequest: detective.ActionRequest{
			Type:              "record-scenario-step",
			Project:           "p",
			SecurityContextID: secID,
			Execution:         &apicontract.ExecutionRef{ProjectID: "p", StoreID: "local", ExecutionID: "ex1"},
			StepStatus:        "failed", // not completed
		},
	}
	bRecStep, _ := json.Marshal(bodyRecStep)
	wRecStep := httptest.NewRecorder()
	rRecStep := httptest.NewRequest(http.MethodPost, "/datatug/detective/action", bytes.NewReader(bRecStep))
	detectiveActionHandler(wRecStep, rRecStep)
	assert.Equal(t, http.StatusBadRequest, wRecStep.Code)

	// 4. detectiveQueryRunner branches -> lines 324, 335, 337, 339
	runner := detectiveQueryRunner("p", secID)
	// Query runner not admitted -> line 324 SOURCE_UNAVAILABLE
	_, err = runner(ctx, "q1", "uat", nil)
	assert.Error(t, err)

	origAdmit := detectiveAdmittedQueryRevision
	origCompute := detectiveComputeRunQueryWithOptions
	defer func() {
		detectiveAdmittedQueryRevision = origAdmit
		detectiveComputeRunQueryWithOptions = origCompute
	}()
	detectiveAdmittedQueryRevision = func(context.Context, string) (string, bool) {
		return "rev1", true
	}
	// Contract error -> line 335
	detectiveComputeRunQueryWithOptions = func(context.Context, apicontract.ExecutionRequest, runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{}, &contractError{Code: "CUSTOM_ERR"}
	}
	_, err = runner(ctx, "q1", "uat", map[string]apicontract.TypedValueOrSet{
		"p1": apicontract.ScalarValue(apicontract.NewStringValue("v1")),
	})
	assert.Error(t, err)

	// Internal error -> line 337
	detectiveComputeRunQueryWithOptions = func(context.Context, apicontract.ExecutionRequest, runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{}, errors.New("internal fail")
	}
	_, err = runner(ctx, "q1", "uat", nil)
	assert.Error(t, err)

	// Success -> line 339
	detectiveComputeRunQueryWithOptions = func(context.Context, apicontract.ExecutionRequest, runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	_, err = runner(ctx, "q1", "uat", nil)
	assert.NoError(t, err)

	// 5. detectiveQueryRevisionLookup branches -> lines 350, 354, 358, 362
	lookup := detectiveQueryRevisionLookup("p")
	_, err = lookup(ctx, "nonexistent_query")
	assert.Error(t, err) // line 350

	lookupWrongProj := detectiveQueryRevisionLookup("wrong_proj")
	_, err = lookupWrongProj(ctx, "q1")
	assert.Error(t, err) // line 345

	// 6. detectiveExecutionMetadata branches -> lines 374, 378
	metaFunc := detectiveExecutionMetadata("p")
	_, _, err = metaFunc(ctx, &apicontract.ExecutionRef{ProjectID: "p", StoreID: "unknown_store", ExecutionID: "ex1"})
	assert.Error(t, err) // line 374
}

type errLoadingEnvironmentsStore struct {
	storage.Store
}

func (e *errLoadingEnvironmentsStore) GetProjectStore(string) datatug.ProjectStore {
	return &errLoadingProjectStore{}
}

type errLoadingProjectStore struct {
	datatug.ProjectStore
}

func (e *errLoadingProjectStore) LoadEnvironments(context.Context, ...datatug.StoreOption) (datatug.Environments, error) {
	return nil, errors.New("load env fail")
}

type mockSessionQueryRevisionedStore struct {
	datatug.ProjectStore
	loadErr error
}

func (m *mockSessionQueryRevisionedStore) LoadQueryRevision(ctx context.Context, id string, options ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return &datatug.StoredQuery{
		Query: datatug.QueryDefWithFolderPath{
			QueryDef: datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}},
				Type:        "SQL",
				Text:        "SELECT 1",
				Parameters:  []datatug.ParameterDef{{ID: "p1", Type: "string", IsRequired: true}},
			},
		},
		Revision: "rev1",
	}, nil
}

func (m *mockSessionQueryRevisionedStore) PutQuery(ctx context.Context, query *datatug.QueryDefWithFolderPath, condition datatug.QueryWriteCondition) (*datatug.StoredQuery, error) {
	return nil, nil
}

func (m *mockSessionQueryRevisionedStore) DeleteQueryRevision(ctx context.Context, id string, expected datatug.QueryRevision) error {
	return nil
}
