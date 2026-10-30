package endpoints

import (
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
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockFailingCommitSession struct{}

func (mockFailingCommitSession) Observer() recordsetcompare.RecordObserver {
	return func(recordsetcompare.ComparedRecord) error { return nil }
}

func (mockFailingCommitSession) Commit(apicontract.CompareResult) error {
	return errors.New("commit failed")
}

func (mockFailingCommitSession) Abort() {}

// Not parallel: replaces apiBeginCompareCache seam
func TestCoverageFinish100_CompareCommitErr(t *testing.T) {
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{"p1": t.TempDir()}, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origBegin := apiBeginCompareCache
	defer func() { apiBeginCompareCache = origBegin }()

	apiBeginCompareCache = func(context.Context, comparecache.BeginRequest) (compareCacheSession, error) {
		return mockFailingCommitSession{}, nil
	}

	leftSet := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("1")}},
	}
	deps := compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			receipt := apicontract.CompareSideReceipt{
				Execution:  apicontract.ExecutionRef{StoreID: "local", ProjectID: "p1", ExecutionID: "exec-1"},
				ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
				RowCount:   1,
			}
			return compareSideData{recordset: leftSet, receipt: receipt}, nil
		},
	}

	exec1 := apicontract.ExecutionRef{StoreID: "local", ProjectID: "p1", ExecutionID: "exec-1"}
	exec2 := apicontract.ExecutionRef{StoreID: "local", ProjectID: "p1", ExecutionID: "exec-2"}
	req := apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "q1",
		Key:               []string{"id"},
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &exec1},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &exec2},
	}
	res, err := computeCompareWith(context.Background(), req, deps)
	assert.NoError(t, err)
	assert.Equal(t, 1, res.Summary.Unchanged)
}

// Not parallel: tests transientRowsCache methods and edge cases
func TestCoverageFinish100_DetectiveTransientRows(t *testing.T) {
	c := &transientRowsCache{items: make(map[transientRowsKey]transientRowsEntry)}
	now := time.Now()

	// 1. Invalid put: empty actor, invalid ref, invalid rows
	badRef := apicontract.ExecutionRef{}
	goodRef := apicontract.ExecutionRef{StoreID: "s1", ProjectID: "p1", ExecutionID: "e1"}
	goodRows := apicontract.Recordset{Columns: []apicontract.Column{{Name: "c1", Type: "string"}}}
	badRows := apicontract.Recordset{Columns: []apicontract.Column{{Name: "", Type: ""}}}

	c.put(badRef, "actor1", goodRows, now)
	assert.Equal(t, 0, len(c.items))

	c.put(goodRef, "", goodRows, now)
	assert.Equal(t, 0, len(c.items))

	c.put(goodRef, "actor1", badRows, now)
	assert.Equal(t, 0, len(c.items))

	// 2. Put with prior item replacement
	c.put(goodRef, "actor1", goodRows, now)
	assert.Equal(t, 1, len(c.items))
	c.put(goodRef, "actor1", goodRows, now)
	assert.Equal(t, 1, len(c.items))

	// 3. Put loop break at line 65: oldestAt.IsZero() when items is empty but bytes check triggers
	c2 := &transientRowsCache{items: make(map[transientRowsKey]transientRowsEntry), bytes: transientRowsTotalBytes}
	c2.put(goodRef, "actor1", goodRows, now)
	assert.Equal(t, 1, len(c2.items))

	// 4. Get with corrupt or invalid rows
	key := transientRowsKey{ref: goodRef, actor: "corrupt_actor"}
	c.items[key] = transientRowsEntry{encoded: []byte("not-json"), at: now}
	_, ok := c.get(goodRef, "corrupt_actor", now)
	assert.False(t, ok)

	c.items[key] = transientRowsEntry{encoded: []byte(`{"columns":[{"name":""}]}`), at: now}
	_, ok = c.get(goodRef, "corrupt_actor", now)
	assert.False(t, ok)

	// Valid get
	gotRows, ok := c.get(goodRef, "actor1", now)
	assert.True(t, ok)
	assert.Equal(t, 1, len(gotRows.Columns))

	// Expired prune
	_, ok = c.get(goodRef, "actor1", now.Add(2*transientRowsTTL))
	assert.False(t, ok)
}

// Not parallel: tests withLocalOrigin helper
func TestCoverageFinish100_WithLocalOrigin(t *testing.T) {
	called := false
	next := func(w http.ResponseWriter, r *http.Request, dto apicore.RequestDTO, options verify.RequestOptions, status int, contextProvider apicore.ContextProvider, worker apicore.Worker) {
		called = true
		assert.Empty(t, r.Header.Get("Origin"))
		w.WriteHeader(http.StatusOK)
	}
	handler := withLocalOrigin(next)

	// 1. No Origin header
	called = false
	rNoOrigin := httptest.NewRequest(http.MethodGet, "/test", nil)
	wNoOrigin := httptest.NewRecorder()
	handler(wNoOrigin, rNoOrigin, nil, nil, 0, nil, nil)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, wNoOrigin.Code)

	// 2. Multiple Origin headers -> 403
	called = false
	rMultiOrigin := httptest.NewRequest(http.MethodGet, "/test", nil)
	rMultiOrigin.Header.Add("Origin", "http://datatug.localhost:3000")
	rMultiOrigin.Header.Add("Origin", "http://datatug.localhost:4000")
	wMultiOrigin := httptest.NewRecorder()
	handler(wMultiOrigin, rMultiOrigin, nil, nil, 0, nil, nil)
	assert.False(t, called)
	assert.Equal(t, http.StatusForbidden, wMultiOrigin.Code)

	// 3. Unsupported Origin -> 403
	called = false
	rBadOrigin := httptest.NewRequest(http.MethodGet, "/test", nil)
	rBadOrigin.Header.Set("Origin", "http://malicious.com")
	wBadOrigin := httptest.NewRecorder()
	handler(wBadOrigin, rBadOrigin, nil, nil, 0, nil, nil)
	assert.False(t, called)
	assert.Equal(t, http.StatusForbidden, wBadOrigin.Code)

	// 4. Valid supported Origin -> 200, origin deleted on request passed to next
	called = false
	rGoodOrigin := httptest.NewRequest(http.MethodGet, "/test", nil)
	rGoodOrigin.Header.Set("Origin", "http://datatug.localhost:3000")
	wGoodOrigin := httptest.NewRecorder()
	handler(wGoodOrigin, rGoodOrigin, nil, nil, 0, nil, nil)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, wGoodOrigin.Code)
}

// Not parallel: tests project_query_mutation handlers and error mapper
func TestCoverageFinish100_ProjectQueryMutation(t *testing.T) {
	// 1. writeProjectMutationError branches
	tests := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{dto.ErrUnsupportedCapability, http.StatusBadRequest, "UNSUPPORTED_CAPABILITY"},
		{dto.ErrInitializationRequired, http.StatusConflict, "INITIALIZATION_REQUIRED"},
		{api.ErrQueryNotFound, http.StatusNotFound, "QUERY_NOT_FOUND"},
		{os.ErrNotExist, http.StatusNotFound, "QUERY_NOT_FOUND"},
	}
	for _, tc := range tests {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/test", nil)
		writeProjectMutationError(w, r, tc.err)
		assert.Equal(t, tc.wantStatus, w.Code)
		var resp map[string]map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, tc.wantCode, resp["error"]["code"])
	}

	// 2. saveProjectQueryHandler body read error and scope error
	rErrBody := httptest.NewRequest(http.MethodPost, "/datatug/queries/save_query", &errReader{})
	wErrBody := httptest.NewRecorder()
	saveProjectQueryHandler(wErrBody, rErrBody)
	assert.Equal(t, http.StatusBadRequest, wErrBody.Code)

	// Body too large
	rLargeBody := httptest.NewRequest(http.MethodPost, "/datatug/queries/save_query", strings.NewReader(strings.Repeat("a", maxProjectQueryMutationBody+10)))
	wLargeBody := httptest.NewRecorder()
	saveProjectQueryHandler(wLargeBody, rLargeBody)
	assert.Equal(t, http.StatusBadRequest, wLargeBody.Code)

	// Scope error (unauthenticated serving principal)
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	saveReq := dto.SaveQueryRequest{
		ProjectRef: dto.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "p1"},
		Branch:     "main",
		Query: datatug.QueryDefWithFolderPath{
			QueryDef: datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1", Title: "Q1"}},
				Type:        datatug.QueryTypeSQL,
				Text:        "SELECT 1",
			},
		},
	}
	bodyBytes, _ := json.Marshal(saveReq)
	rScopeErr := httptest.NewRequest(http.MethodPost, "/datatug/queries/save_query", strings.NewReader(string(bodyBytes)))
	wScopeErr := httptest.NewRecorder()
	saveProjectQueryHandler(wScopeErr, rScopeErr)
	assert.Equal(t, http.StatusForbidden, wScopeErr.Code)

	// 3. Configure session for read principal
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	dir := t.TempDir()
	pID := "p_branches_err"
	paths := map[string]string{pID: dir}
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	// getProjectQueryRevisionHandler newProjectRef error
	rBadRef := httptest.NewRequest(http.MethodGet, "/datatug/queries/revision", nil)
	wBadRef := httptest.NewRecorder()
	getProjectQueryRevisionHandler(wBadRef, rBadRef)
	assert.Equal(t, http.StatusBadRequest, wBadRef.Code)

	// 4. projectCapabilitiesHandler newProjectRef error
	wCapBad := httptest.NewRecorder()
	projectCapabilitiesHandler(wCapBad, rBadRef)
	assert.Equal(t, http.StatusBadRequest, wCapBad.Code)

	// 5. projectConnectionCatalogHandler url.ParseQuery error
	rBadCatalogQuery := httptest.NewRequest(http.MethodGet, "/datatug/project/catalog?%zz", nil)
	wBadCatalogQuery := httptest.NewRecorder()
	projectConnectionCatalogHandler(wBadCatalogQuery, rBadCatalogQuery)
	assert.Equal(t, http.StatusBadRequest, wBadCatalogQuery.Code)

	// 6. projectBranchesHandler newProjectRef error & LocalProjectBranches error
	// newProjectRef error
	wBranchBad := httptest.NewRecorder()
	projectBranchesHandler(wBranchBad, rBadRef)
	assert.Equal(t, http.StatusBadRequest, wBranchBad.Code)

	// LocalProjectBranches error (unknown store)
	rBranchUnknownStore := httptest.NewRequest(http.MethodGet, "/datatug/project/branches?project="+pID+"&storage=unknown_store", nil)
	wBranchUnknownStore := httptest.NewRecorder()
	projectBranchesHandler(wBranchUnknownStore, rBranchUnknownStore)
	assert.Equal(t, http.StatusBadRequest, wBranchUnknownStore.Code)
}

// Not parallel: tests compare_rows handlers and helpers
func TestCoverageFinish100_CompareRows(t *testing.T) {
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	dir := t.TempDir()
	pID := "p_compare_rows"
	paths := map[string]string{pID: dir}
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	ctx := context.Background()

	// 1. mapCompareCacheError
	errNotConf := mapCompareCacheError(comparecache.ErrNotConfigured)
	assert.Error(t, errNotConf)

	errNotFound := mapCompareCacheError(comparecache.ErrNotFound)
	assert.Error(t, errNotFound)

	customErr := errors.New("custom error")
	assert.Equal(t, customErr, mapCompareCacheError(customErr))

	// 2. authorizeCachedComparison branches
	// empty fingerprint
	metaEmptyFP := comparecache.Meta{}
	assert.Error(t, authorizeCachedComparison(ctx, metaEmptyFP))

	// wrong fingerprint
	metaWrongFP := comparecache.Meta{PolicyFingerprint: "wrong_fp"}
	assert.Error(t, authorizeCachedComparison(ctx, metaWrongFP))

	// left execution error
	currentFP := api.SecurePolicyFingerprint()
	metaLeftErr := comparecache.Meta{
		PolicyFingerprint: currentFP,
		QueryID:           "q1",
		Left:              apicontract.ExecutionRef{StoreID: "unknown_store", ProjectID: pID, ExecutionID: "e1"},
		Right:             apicontract.ExecutionRef{StoreID: "unknown_store", ProjectID: pID, ExecutionID: "e2"},
	}
	assert.Error(t, authorizeCachedComparison(ctx, metaLeftErr))

	// 3. compareRowsHandler error branches
	// non-GET method
	rPost := httptest.NewRequest(http.MethodPost, "/datatug/compare/rows", nil)
	wPost := httptest.NewRecorder()
	compareRowsHandler(wPost, rPost)
	assert.Equal(t, http.StatusBadRequest, wPost.Code)

	// invalid securityContextId
	rBadSec := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?securityContextId=bad", nil)
	wBadSec := httptest.NewRecorder()
	compareRowsHandler(wBadSec, rBadSec)
	assert.Equal(t, http.StatusConflict, wBadSec.Code)

	secID := api.SecurityContextID()

	// empty comparisonId
	rNoComp := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?securityContextId="+secID, nil)
	wNoComp := httptest.NewRecorder()
	compareRowsHandler(wNoComp, rNoComp)
	assert.Equal(t, http.StatusBadRequest, wNoComp.Code)

	// invalid state
	rBadState := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?securityContextId="+secID+"&comparisonId=c1&state=invalid", nil)
	wBadState := httptest.NewRecorder()
	compareRowsHandler(wBadState, rBadState)
	assert.Equal(t, http.StatusBadRequest, wBadState.Code)

	// invalid limit (not integer, < 1, > 500)
	for _, badLimit := range []string{"abc", "0", "501"} {
		rBadLimit := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?securityContextId="+secID+"&comparisonId=c1&state=matched&limit="+badLimit, nil)
		wBadLimit := httptest.NewRecorder()
		compareRowsHandler(wBadLimit, rBadLimit)
		assert.Equal(t, http.StatusBadRequest, wBadLimit.Code)
	}

	// LookupCompareCache error
	rLookupErr := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?securityContextId="+secID+"&comparisonId=c1&state=matched&limit=10", nil)
	wLookupErr := httptest.NewRecorder()
	compareRowsHandler(wLookupErr, rLookupErr)
	assert.Equal(t, http.StatusServiceUnavailable, wLookupErr.Code)
}

// Not parallel: tests detective error mapping, stores, and validation
func TestCoverageFinish100_DetectiveSuite(t *testing.T) {
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	dir := t.TempDir()
	pID := "p1"
	paths := map[string]string{pID: dir}
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origDatatugStore
	}()

	ctx := context.Background()
	secID := api.SecurityContextID()

	// 1. detectiveError mapping
	assert.Nil(t, detectiveError(nil))
	assert.Equal(t, apicontract.ErrCodeRevisionConflict, detectiveError(detective.ErrConflict).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeNotFound, detectiveError(detective.ErrNotFound).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeInvalidRequest, detectiveError(detective.ErrInvalid).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeInvalidRequest, detectiveError(detective.ErrCapacity).(*contractError).Code)
	assert.Equal(t, "OUTCOME_UNCERTAIN", string(detectiveError(detective.ErrOutcomeUncertain).(*contractError).Code))
	assert.Equal(t, apicontract.ErrCodeInvalidRequest, detectiveError(context.DeadlineExceeded).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeInvalidRequest, detectiveError(errors.New("exceeded row limit")).(*contractError).Code)
	assert.Equal(t, codeInternal, detectiveError(errors.New("other internal error")).(*contractError).Code)

	// 2. aiContractError mapping
	assert.Nil(t, aiContractError(nil))
	assert.Equal(t, apicontract.ErrCodeAccessDenied, aiContractError(ai.ErrUnauthorized).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeRevisionConflict, aiContractError(ai.ErrStale).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeInvalidRequest, aiContractError(ai.ErrInvalid).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeInvalidRequest, aiContractError(ai.ErrUnsupported).(*contractError).Code)
	assert.Equal(t, apicontract.ErrCodeNotFound, aiContractError(detective.ErrNotFound).(*contractError).Code)

	// 3. detectiveIncidentActionValid
	assert.False(t, detectiveIncidentActionValid(detectiveIncidentAction{IfMatch: -1}))
	assert.False(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "bad ID"}))
	assert.False(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "unknown"}))
	assert.False(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "create", CaseID: ""}))
	assert.False(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "link", CaseID: "c1", IncidentRef: ""}))
	assert.False(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "start-case", Case: nil}))
	assert.True(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "create", CaseID: "c1", Title: "Title"}))
	assert.True(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "link", CaseID: "c1", IncidentRef: "inc-1"}))
	assert.True(t, detectiveIncidentActionValid(detectiveIncidentAction{OperationID: "op1", Type: "start-case", IncidentRef: "inc-1", Case: &detective.Case{ID: "c1", Title: "T"}}))

	// 4. detectiveStore validation
	_, _, err = detectiveStore("", secID)
	assert.Error(t, err)

	_, _, err = detectiveStore(pID, "")
	assert.Error(t, err)

	_, _, err = detectiveStore("p_not_found", secID)
	assert.Error(t, err)

	// 5. detectiveEnvironments error
	_, err = detectiveEnvironments(ctx, "unconfigured_proj")
	assert.Error(t, err)

	// 6. detectiveIncidentEnvironment error
	_, err = detectiveIncidentEnvironment(ctx, pID, "")
	assert.Error(t, err)
	_, err = detectiveIncidentEnvironment(ctx, "unconfigured_proj", "uat")
	assert.Error(t, err)

	// 7. recordedDetectiveInputs invalid value
	_, err = recordedDetectiveInputs(apicontract.ExecutionRecord{
		Parameters: map[string]apicontract.TypedValueOrSet{
			"bad": {Scalar: &apicontract.TypedValue{Type: ""}},
		},
	})
	assert.Error(t, err)

	// 8. detectiveExecutionMetadata error branches
	metaFunc := detectiveExecutionMetadata(pID)
	_, _, err = metaFunc(ctx, nil)
	assert.ErrorIs(t, err, detective.ErrInvalid)

	_, _, err = metaFunc(ctx, &apicontract.ExecutionRef{ProjectID: "other_project"})
	assert.ErrorIs(t, err, detective.ErrInvalid)

	_, _, err = metaFunc(ctx, &apicontract.ExecutionRef{ProjectID: pID, StoreID: "unknown_store", ExecutionID: "ex1"})
	assert.Error(t, err)

	// 9. detectiveQueryRevisionLookup error branches
	revLookup := detectiveQueryRevisionLookup("unserved_project")
	_, err = revLookup(ctx, "q1")
	assert.Error(t, err)

	// 10. detectiveQueryRunner error branches
	runner := detectiveQueryRunner(pID, secID)
	_, err = runner(ctx, "q_unadmitted", "env1", nil)
	assert.Error(t, err)

	// 11. loadDetectiveScenarioQueryProof invalid refs
	_, _, err = loadDetectiveScenarioQueryProof(ctx, pID, "actor1", nil)
	assert.Error(t, err)

	_, _, err = loadDetectiveScenarioQueryProof(ctx, pID, "actor1", &apicontract.ExecutionRef{})
	assert.Error(t, err)

	_, _, err = loadDetectiveScenarioQueryProof(ctx, pID, "actor1", &apicontract.ExecutionRef{ProjectID: "other"})
	assert.Error(t, err)

	// 12. detectiveCaseEventsHandler invalid cursor
	rEventsBad := httptest.NewRequest(http.MethodGet, "/datatug/detective/case/events?project="+pID+"&securityContextId="+secID+"&before=invalid", nil)
	wEventsBad := httptest.NewRecorder()
	detectiveCaseEventsHandler(wEventsBad, rEventsBad)
	assert.Equal(t, http.StatusBadRequest, wEventsBad.Code)

	// 13. detectiveIncidentView project mismatch
	scope := incidents.ProjectRef{StoreID: "local", ProjectID: pID, Environment: "uat"}
	_, err = detectiveIncidentView(rEventsBad, nil, incidents.Incident{}, scope)
	assert.Error(t, err)

	// 14. detectiveCaseFacts validation & conversion
	badFacts := map[string]detective.InputValue{"bad": {Scalar: &apicontract.TypedValue{Type: ""}}}
	_, err = detectiveCaseFacts(badFacts, scope)
	assert.Error(t, err)

	valScalar := apicontract.NewStringValue("val")
	val1 := apicontract.NewStringValue("val1")
	valSet, err := apicontract.NewTypedValueSet(val1)
	require.NoError(t, err)
	startVal := apicontract.NewIntegerValue("1")
	endVal := apicontract.NewIntegerValue("10")

	goodFacts := map[string]detective.InputValue{
		"f_scalar": {Scalar: &valScalar},
		"f_set":    {Set: &valSet},
		"f_range":  {Range: &detective.ValueRange{Type: "range", Start: startVal, End: endVal}},
	}
	canonCtx, err := detectiveCaseFacts(goodFacts, scope)
	assert.NoError(t, err)
	assert.NotEmpty(t, canonCtx.Facts)

	// Capacity error (> 200 facts)
	bigFacts := make(map[string]detective.InputValue)
	for i := 0; i < 205; i++ {
		bigFacts[fmt.Sprintf("k%d", i)] = detective.InputValue{Scalar: &valScalar}
	}
	_, err = detectiveCaseFacts(bigFacts, scope)
	assert.ErrorIs(t, err, detective.ErrCapacity)

	// 15. detectiveContextFromIncident
	vEmpty := incidents.IncidentView{}
	cEmpty, err := detectiveContextFromIncident(vEmpty, scope)
	assert.NoError(t, err)
	assert.Empty(t, cEmpty)

	// Convert canonCtx back
	vGood := incidents.IncidentView{CanonicalContext: investigation.VisibleContext(canonCtx)}
	cGood, err := detectiveContextFromIncident(vGood, scope)
	assert.NoError(t, err)
	assert.NotEmpty(t, cGood)
}

// Not parallel: tests session context handlers and subpaths
func TestCoverageFinish100_SessionContextSuite(t *testing.T) {
	// 1. writeSessionContextError
	wErr := httptest.NewRecorder()
	rErr := httptest.NewRequest(http.MethodGet, "/test", nil)
	writeSessionContextError(wErr, rErr, sessioncontext.ErrReadOnly)
	assert.Equal(t, http.StatusConflict, wErr.Code)

	// 2. sessionContextPageLimit
	limit, err := sessionContextPageLimit(url.Values{})
	assert.NoError(t, err)
	assert.Equal(t, 100, limit)

	limit, err = sessionContextPageLimit(url.Values{"limit": []string{"50"}})
	assert.NoError(t, err)
	assert.Equal(t, 50, limit)

	for _, bad := range []string{"0", "101", "bad"} {
		_, err = sessionContextPageLimit(url.Values{"limit": []string{bad}})
		assert.ErrorIs(t, err, sessioncontext.ErrInvalid)
	}

	// 3. Router and Handler integration
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
	}

	// Query parse error -> 400
	wParseErr := httptest.NewRecorder()
	rParseErr := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context/sessions?%zz", nil)
	rParseErr.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wParseErr, rParseErr)
	assert.Equal(t, http.StatusBadRequest, wParseErr.Code)

	// DatabaseID mismatch -> 403
	badDbQ := url.Values{"storage": {"local"}, "project": {"transport-test"}, "databaseId": {"wrong"}, "projectId": {caps.ProjectID}}
	wBadDb := httptest.NewRecorder()
	rBadDb := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context/sessions?"+badDbQ.Encode(), nil)
	rBadDb.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wBadDb, rBadDb)
	assert.Equal(t, http.StatusForbidden, wBadDb.Code)

	// Subpaths verification
	subpaths := []string{
		"/sessions?" + baseQ.Encode() + "&limit=50",
		"/sessions/s1?" + baseQ.Encode(),
		"/sessions/s1/runs?" + baseQ.Encode(),
		"/sessions/s1/messages?" + baseQ.Encode(),
		"/sessions/s1/entries/e1/revisions?" + baseQ.Encode(),
		"/sessions/s1/entries/e1/revisions/r1?" + baseQ.Encode(),
		"/runs/r1?" + baseQ.Encode(),
		"/inputs/i1?" + baseQ.Encode(),
		"/operations/o1?" + baseQ.Encode(),
	}
	for _, sub := range subpaths {
		wSub := httptest.NewRecorder()
		rSub := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context"+sub, nil)
		rSub.Header.Set("Origin", "http://datatug.localhost:4200")
		router.ServeHTTP(wSub, rSub)
		// We expect either 200, 404, or another structured response (never a 500 router panic)
		assert.NotEqual(t, http.StatusInternalServerError, wSub.Code)
	}

	// 4. sessionContextQueryHandler error branches
	// Parse error -> 400
	wQueryParse := httptest.NewRecorder()
	rQueryParse := httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context/query?%zz", nil)
	rQueryParse.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wQueryParse, rQueryParse)
	assert.Equal(t, http.StatusBadRequest, wQueryParse.Code)

	// Bad key -> 400
	wQueryBadKey := httptest.NewRecorder()
	rQueryBadKey := httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context/query?badkey=1", nil)
	rQueryBadKey.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wQueryBadKey, rQueryBadKey)
	assert.Equal(t, http.StatusBadRequest, wQueryBadKey.Code)

	// DatabaseID mismatch -> 403
	wQueryBadDb := httptest.NewRecorder()
	rQueryBadDb := httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context/query?"+badDbQ.Encode(), nil)
	rQueryBadDb.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wQueryBadDb, rQueryBadDb)
	assert.Equal(t, http.StatusForbidden, wQueryBadDb.Code)

	// Bad body -> 400
	wQueryBadBody := httptest.NewRecorder()
	rQueryBadBody := httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context/query?"+baseQ.Encode(), strings.NewReader("bad-json"))
	rQueryBadBody.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wQueryBadBody, rQueryBadBody)
	assert.Equal(t, http.StatusBadRequest, wQueryBadBody.Code)

	// Unadmitted scope -> 403
	unadmittedCmd := sessionQueryRequest{
		Command: sessioncontext.Command{
			Action: "pinInput",
			Kind:   "query",
			Scope:  &sessioncontext.Scope{EnvironmentID: "unadmitted_env", IdentityDomain: "domain"},
		},
		QueryID:  "q1",
		Bindings: []sessionQueryBinding{{EntryID: "e1", ParameterID: "p1"}},
	}
	bodyUnadmitted, _ := json.Marshal(unadmittedCmd)
	wQueryUnadmitted := httptest.NewRecorder()
	rQueryUnadmitted := httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context/query?"+baseQ.Encode(), strings.NewReader(string(bodyUnadmitted)))
	rQueryUnadmitted.Header.Set("Origin", "http://datatug.localhost:4200")
	router.ServeHTTP(wQueryUnadmitted, rQueryUnadmitted)
	assert.Equal(t, http.StatusForbidden, wQueryUnadmitted.Code)
}
