package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read error") }

func TestCoverageFinish100_Compare(t *testing.T) {
	handler := compareHandler(Capabilities{})

	// 1. GET method
	rGet := httptest.NewRequest(http.MethodGet, "/datatug/compare", nil)
	wGet := httptest.NewRecorder()
	handler(wGet, rGet)
	assert.Equal(t, http.StatusBadRequest, wGet.Code)

	// 2. Body read error
	rErr := httptest.NewRequest(http.MethodPost, "/datatug/compare", &errReader{})
	wErr := httptest.NewRecorder()
	handler(wErr, rErr)
	assert.Equal(t, http.StatusBadRequest, wErr.Code)

	// 3. Invalid JSON
	rBadJSON := httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader("not-json"))
	wBadJSON := httptest.NewRecorder()
	handler(wBadJSON, rBadJSON)
	assert.Equal(t, http.StatusBadRequest, wBadJSON.Code)

	// 4. req.Validate error
	rInvalidReq := httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader("{}"))
	wInvalidReq := httptest.NewRecorder()
	handler(wInvalidReq, rInvalidReq)
	assert.Equal(t, http.StatusBadRequest, wInvalidReq.Code)

	// 5. compareHandler with computeCompareWith error -> writeCompareError
	wFail := httptest.NewRecorder()
	rFail := httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader(fmt.Sprintf(`{
		"securityContextId": %q,
		"queryId": "nonexistent_query",
		"key": ["id"],
		"left": {"kind":"scope","storeId":"s","project":"p","environment":"e"},
		"right": {"kind":"scope","storeId":"s","project":"p","environment":"e"}
	}`, api.SecurityContextID())))
	compareHandler(Capabilities{AllowWrites: true})(wFail, rFail)
	assert.Equal(t, http.StatusNotFound, wFail.Code)

	// 6. computeCompareWith with req.Left.Kind == CompareSideRecord and no key
	ctx := context.Background()
	_, err := computeCompareWith(ctx, apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "q1",
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "ex1"}},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
	}, compareDependencies{})
	assert.Error(t, err)

	// 7. computeCompareWith with req.Incident != nil and deps.appendRun == nil
	sampleReceipt := apicontract.CompareSideReceipt{
		Execution:  apicontract.ExecutionRef{ProjectID: "p", StoreID: "s", ExecutionID: "exec-1"},
		ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RowCount:   1,
	}
	deps := compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{
				recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "id"}}},
				receipt:   sampleReceipt,
			}, nil
		},
		appendRun: nil,
	}
	_, err = computeCompareWith(ctx, apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "q1",
		Key:               []string{"id"},
		Incident:          &incidents.IncidentRef{StoreID: "s", IncidentID: "inc1"},
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
	}, deps)
	assert.Error(t, err)

	// 8. compareResultValidateHook error
	origCompareVal := compareResultValidateHook
	compareResultValidateHook = func(*apicontract.CompareResult) error {
		return errors.New("comp val fail")
	}
	defer func() { compareResultValidateHook = origCompareVal }()
	depsOK := compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{
				recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "id"}}},
				receipt:   sampleReceipt,
			}, nil
		},
	}
	_, err = computeCompareWith(ctx, apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "q1",
		Key:               []string{"id"},
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
	}, depsOK)
	assert.Error(t, err)
	compareResultValidateHook = origCompareVal
}

func TestCoverageFinish100_CompareKey(t *testing.T) {
	ctx := context.Background()

	// 1. resolveCompareKey: left and right not equal
	dir := t.TempDir()
	pID := "comp_key_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	origStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{pID: dir}) }
	defer func() { storage.NewDatatugStore = origStore }()

	qDir := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(qDir, 0755))

	qDef := datatug.QueryDef{
		ID: "q1",
		Recordsets: []datatug.RecordsetDefinition{
			{
				PrimaryKey: &datatug.UniqueKey{Columns: []string{"id"}},
				Columns: datatug.RecordsetColumnDefs{
					{Name: "id", Meta: &datatug.EntityFieldRef{Entity: "E", Field: "F"}},
				},
			},
		},
	}
	data, _ := json.Marshal(qDef)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "q1.query.json"), data, 0644))

	// mappedCompareKey with no recordsets
	qDefNoRec := datatug.QueryDef{ID: "q_no_rec"}
	dataNoRec, _ := json.Marshal(qDefNoRec)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "q_no_rec.query.json"), dataNoRec, 0644))

	_, err := mappedCompareKey(ctx, "q_no_rec", apicontract.CompareSideSpec{Project: pID})
	assert.Error(t, err)

	// mappedCompareKey with column missing Meta
	qDefNoMeta := datatug.QueryDef{
		ID: "q_no_meta",
		Recordsets: []datatug.RecordsetDefinition{
			{
				PrimaryKey: &datatug.UniqueKey{Columns: []string{"id"}},
				Columns:    datatug.RecordsetColumnDefs{{Name: "id"}},
			},
		},
	}
	dataNoMeta, _ := json.Marshal(qDefNoMeta)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "q_no_meta.query.json"), dataNoMeta, 0644))
	_, err = mappedCompareKey(ctx, "q_no_meta", apicontract.CompareSideSpec{Project: pID})
	assert.Error(t, err)

	// findRecordsetColumn returns nil
	col := findRecordsetColumn(datatug.RecordsetColumnDefs{{Name: "a"}}, "b")
	assert.Nil(t, col)

	// resolveCompareKey right side error (line 33)
	_, err = resolveCompareKey(ctx, apicontract.CompareRequest{
		QueryID: "q1",
		Left:    apicontract.CompareSideSpec{Project: pID},
		Right:   apicontract.CompareSideSpec{Project: "unk_proj"},
	})
	assert.Error(t, err)

	// resolveCompareKey keys not equal (line 36)
	dir2 := t.TempDir()
	pID2 := "comp_key_proj_2"
	filestore.SetProjectPath(pID2, dir2)
	pathsByID2 := map[string]string{pID: dir, pID2: dir2}
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", pathsByID2) }
	api.ConfigureSecureSession(secureread.Session{}, pathsByID2, api.Capabilities{})

	qDir2 := filepath.Join(dir2, "queries")
	require.NoError(t, os.MkdirAll(qDir2, 0755))
	qDefDiff := datatug.QueryDef{
		ID: "q1",
		Recordsets: []datatug.RecordsetDefinition{
			{
				PrimaryKey: &datatug.UniqueKey{Columns: []string{"id2"}},
				Columns: datatug.RecordsetColumnDefs{
					{Name: "id2", Meta: &datatug.EntityFieldRef{Entity: "E2", Field: "F2"}},
				},
			},
		},
	}
	dataDiff, _ := json.Marshal(qDefDiff)
	require.NoError(t, os.WriteFile(filepath.Join(qDir2, "q1.query.json"), dataDiff, 0644))

	_, err = resolveCompareKey(ctx, apicontract.CompareRequest{
		QueryID: "q1",
		Left:    apicontract.CompareSideSpec{Project: pID},
		Right:   apicontract.CompareSideSpec{Project: pID2},
	})
	assert.Error(t, err)
}

func TestCoverageFinish100_CompareFacts(t *testing.T) {
	// 1. countNativeInBindings cases
	// comparisonMentionsCohortBinding returns false
	fRef := dal.NewFieldRef("", "other_field")
	pRef := dal.NewParam("other_param")
	cmpDiff := dal.NewComparison(fRef, dal.Equal, pRef)
	cnt, safe := countNativeInBindings(cmpDiff, "my_field", "my_param")
	assert.Equal(t, 0, cnt)
	assert.True(t, safe)

	// countNativeInBindings default case
	cnt, safe = countNativeInBindings(dummyCondition{}, "my_field", "my_param")
	assert.Equal(t, 0, cnt)
	assert.False(t, safe)

	// comparisonMentionsCohortBinding with non-matching types
	dummyCmp := dal.NewComparison(dal.Constant{Value: 1}, dal.Equal, dal.Constant{Value: 2})
	assert.False(t, comparisonMentionsCohortBinding(dummyCmp, "f", "p"))

	// 2. visibleFactCohort with > 10000 values
	hugeFacts := make([]investigation.FactView, apicontract.CompareMaximumLimit+5)
	for i := range hugeFacts {
		val := investigation.NewIntegerValue(fmt.Sprint(i))
		hugeFacts[i] = investigation.FactView{
			ID:      fmt.Sprint(i),
			Enabled: true,
			Layer:   investigation.FactLayerCanonical,
			Role:    investigation.FactRoleAffected,
			Scope:   &investigation.ProjectScope{StoreID: "s", ProjectID: "p", Environment: "e"},
			Entity:  "E", Field: "F",
			Value: investigation.VisibleValue(val),
		}
	}
	hugeView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{Facts: hugeFacts},
	}
	_, _, _, _, err := visibleFactCohort(hugeView, apicontract.CompareSideSpec{StoreID: "s", Project: "p", Environment: "e"})
	assert.Error(t, err)
}

type dummyCondition struct{}

func (dummyCondition) Operator() dal.Operator { return dal.Equal }
func (dummyCondition) String() string         { return "dummy" }

func TestCoverageFinish100_CompareSides(t *testing.T) {
	ctx := context.Background()

	// 1. executeCompareSide with CompareSideRecord
	_, err := executeCompareSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord})
	assert.Error(t, err)

	// 2. compareSideReceiptValidateHook error in executeCompareLiveSide
	origHook := compareSideReceiptValidateHook
	compareSideReceiptValidateHook = func(*apicontract.CompareSideReceipt) error {
		return errors.New("receipt invalid")
	}
	defer func() { compareSideReceiptValidateHook = origHook }()

	// Test receipt error via loadCompareRecordSide
	dir := t.TempDir()
	pID := "comp_sides_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	// loadCompareRecordSide execution is nil
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord})
	assert.Error(t, err)

	// loadCompareRecordSide store not found
	refBadStore := apicontract.ExecutionRef{ProjectID: "bad_proj", StoreID: "bad_store", ExecutionID: "exec-1"}
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &refBadStore})
	assert.Error(t, err)
}

func TestCoverageFinish100_CompareIncident(t *testing.T) {
	ctx := context.Background()

	// compareRunDraftMarshalJSON error
	origMarshal := compareRunDraftMarshalJSON
	compareRunDraftMarshalJSON = func(any) ([]byte, error) {
		return nil, errors.New("json error")
	}
	defer func() { compareRunDraftMarshalJSON = origMarshal }()

	incRef := incidents.IncidentRef{StoreID: "s", IncidentID: "inc-1"}
	actor := incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}
	_, err := compareRunDraft(incRef, actor, []string{"id"}, incidents.ComparisonRef{})
	assert.Error(t, err)

	// compareProjectID with side.Execution != nil
	execRef := apicontract.ExecutionRef{ProjectID: "proj_from_exec"}
	sideWithExec := apicontract.CompareSideSpec{Execution: &execRef}
	assert.Equal(t, "proj_from_exec", compareProjectID(sideWithExec))

	// compareIncidentState errors
	// nil incident
	_, _, _, err = compareIncidentState(ctx, apicontract.CompareRequest{})
	assert.Error(t, err)

	// cannot resolve project
	_, _, _, err = compareIncidentState(ctx, apicontract.CompareRequest{Incident: &incRef, Left: apicontract.CompareSideSpec{}, Right: apicontract.CompareSideSpec{}})
	assert.Error(t, err)

	// project mismatch
	_, _, _, err = compareIncidentState(ctx, apicontract.CompareRequest{
		Incident: &incRef,
		Left:     apicontract.CompareSideSpec{Project: "p1"},
		Right:    apicontract.CompareSideSpec{Project: "p2"},
	})
	assert.Error(t, err)

	// incident store not found
	_, _, _, err = compareIncidentState(ctx, apicontract.CompareRequest{
		Incident: &incRef,
		Left:     apicontract.CompareSideSpec{Project: "p1"},
		Right:    apicontract.CompareSideSpec{Project: "p1"},
	})
	assert.Error(t, err)

	// preflightCompareIncident with compareIncidentState error
	_, err = preflightCompareIncident(ctx, apicontract.CompareRequest{})
	assert.Error(t, err)

	// appendCompareRun with compareIncidentState error
	err = appendCompareRun(ctx, apicontract.CompareRequest{}, incidents.ComparisonRef{})
	assert.Error(t, err)
}

func TestCoverageFinish100_ExecutionRecording(t *testing.T) {
	ctx := context.Background()

	// 1. normalize helpers with nil
	assert.Empty(t, normalizeParameters(nil))
	assert.Empty(t, normalizeBindings(nil))
	assert.Empty(t, normalizeMeasurements(nil))

	// 2. recordQueryExecution measureRecordsetHook error
	origMeas := measureRecordsetHook
	measureRecordsetHook = func(apicontract.Recordset, []apicontract.MeasurementProjection, []apicontract.Limitation, bool) ([]apicontract.ScalarMeasurement, error) {
		return nil, errors.New("measure fail")
	}
	defer func() { measureRecordsetHook = origMeas }()

	_, err := recordQueryExecution(ctx, apicontract.ExecutionRequest{}, nil, "", apicontract.Result{}, time.Now())
	assert.Error(t, err)
	measureRecordsetHook = origMeas

	// 3. recordRelatedRowsExecution relatedRowsMarshalJSON error
	origRelMarshal := relatedRowsMarshalJSON
	relatedRowsMarshalJSON = func(any) ([]byte, error) {
		return nil, errors.New("marshal fail")
	}
	defer func() { relatedRowsMarshalJSON = origRelMarshal }()
	_, err = recordRelatedRowsExecution(ctx, apicontract.RelatedRowsRequest{}, apicontract.Result{}, time.Now())
	assert.Error(t, err)
	relatedRowsMarshalJSON = origRelMarshal

	// 4. persistExecution source store mismatch
	dir := t.TempDir()
	pID := "rec_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	_, err = persistExecution(ctx, executionInput{
		Project:       pID,
		SourceStoreID: "wrong_store",
	}, apicontract.Result{}, time.Now())
	assert.Error(t, err)

	// 5. persistExecution newExecutionIDHook error
	origExecID := newExecutionIDHook
	newExecutionIDHook = func() (string, error) {
		return "", errors.New("id fail")
	}
	defer func() { newExecutionIDHook = origExecID }()
	_, err = persistExecution(ctx, executionInput{Project: pID}, apicontract.Result{}, time.Now())
	assert.Error(t, err)
	newExecutionIDHook = origExecID

	// 6. persistExecution fingerprintRecordsetHook error
	origFP := fingerprintRecordsetHook
	fingerprintRecordsetHook = func(apicontract.Recordset) (string, error) {
		return "", errors.New("fp fail")
	}
	defer func() { fingerprintRecordsetHook = origFP }()
	_, err = persistExecution(ctx, executionInput{Project: pID}, apicontract.Result{}, time.Now())
	assert.Error(t, err)
	fingerprintRecordsetHook = origFP

	// 7. persistExecution executionRecordValidateHook error
	origRecVal := executionRecordValidateHook
	executionRecordValidateHook = func(*apicontract.ExecutionRecord) error {
		return errors.New("rec val fail")
	}
	defer func() { executionRecordValidateHook = origRecVal }()
	_, err = persistExecution(ctx, executionInput{Project: pID}, apicontract.Result{}, time.Now())
	assert.Error(t, err)
	executionRecordValidateHook = origRecVal

	// 8. snapshotRecordsetValidateHook error
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{
		SnapshotPolicies: map[string]api.SnapshotProjectPolicy{
			pID: {
				Sources: map[string]api.SnapshotSourcePolicy{
					"src": {Allow: true},
				},
			},
		},
	})
	origSnapVal := snapshotRecordsetValidateHook
	snapshotRecordsetValidateHook = func(*apicontract.Recordset) error {
		return errors.New("snap val fail")
	}
	defer func() { snapshotRecordsetValidateHook = origSnapVal }()
	_, _, err = snapshotRecordsetForStorage(pID, "src", apicontract.Recordset{})
	assert.Error(t, err)
	snapshotRecordsetValidateHook = origSnapVal

	// 9. newExecutionID with failing randReadHook
	origRand := randReadHook
	randReadHook = func([]byte) (int, error) { return 0, errors.New("rand fail") }
	defer func() { randReadHook = origRand }()
	_, err = newExecutionID()
	assert.Error(t, err)
	randReadHook = origRand
}

func TestCoverageFinish100_Executions(t *testing.T) {
	ctx := context.Background()
	rReq := httptest.NewRequest(http.MethodGet, "/datatug/executions", nil)

	// 1. computeExecutionList executionListValidateHook error
	origListVal := executionListValidateHook
	executionListValidateHook = func(*apicontract.ExecutionListResponse) error {
		return errors.New("list val fail")
	}
	defer func() { executionListValidateHook = origListVal }()

	dir := t.TempDir()
	pID := "exec_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	configureExecutionEvidence(t, pID, dir)

	scope := apicontract.Scope{StoreID: pID, Project: pID, Environment: "env", SecurityContextID: api.SecurityContextID()}
	_, err := computeExecutionList(rReq, apicontract.ExecutionListRequest{Scope: scope})
	assert.Error(t, err)
	executionListValidateHook = origListVal

	// 2. computeExecutionSeriesBounded executionSeriesValidateHook error
	origSeriesVal := executionSeriesValidateHook
	executionSeriesValidateHook = func(*apicontract.ExecutionSeriesResponse) error {
		return errors.New("series val fail")
	}
	defer func() { executionSeriesValidateHook = origSeriesVal }()

	seriesReq := apicontract.ExecutionSeriesRequest{
		Scope: scope,
		Partition: apicontract.ExecutionSeriesPartition{
			EvidenceStoreID:   pID,
			SourceScope:       apicontract.ExecutionRecordScope{StoreID: pID, Project: pID, Environment: "env"},
			Source:            "src",
			PolicyFingerprint: "fp",
		},
	}
	_, err = computeExecutionSeriesBounded(rReq, seriesReq, 10)
	assert.Error(t, err)
	executionSeriesValidateHook = origSeriesVal

	// 3. computeExecutionSnapshot snapshotReadValidateHook error
	origSnapVal := snapshotReadValidateHook
	snapshotReadValidateHook = func(*apicontract.SnapshotReadResponse) error {
		return errors.New("snap read val fail")
	}
	defer func() { snapshotReadValidateHook = origSnapVal }()
	uSnap := fmt.Sprintf("/datatug/executions/snapshot/exec-1?project=%s&environment=env&securityContextId=%s&storeId=%s", pID, api.SecurityContextID(), pID)
	rSnap := httptest.NewRequest(http.MethodGet, uSnap, nil)
	paramsSnapCtx := context.WithValue(rSnap.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-1"}})
	_, err = computeExecutionSnapshot(rSnap.WithContext(paramsSnapCtx))
	assert.Error(t, err)
	snapshotReadValidateHook = origSnapVal

	// 4. authorizeExecutionRecordContextFn error paths
	origAuth := authorizeExecutionRecordContextFn
	authorizeExecutionRecordContextFn = func(context.Context, apicontract.ExecutionRecord) error {
		return errors.New("auth fail")
	}
	defer func() { authorizeExecutionRecordContextFn = origAuth }()

	err = authorizeExecutionRecord(rReq, apicontract.ExecutionRecord{})
	assert.Error(t, err)
	authorizeExecutionRecordContextFn = origAuth

	// 5. authorizeExecutionRecordContext when api.SecureExecutor() !ok
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	err = authorizeExecutionRecordContext(ctx, apicontract.ExecutionRecord{})
	assert.Error(t, err)
}

func TestCoverageFinish100_ExecRunQuery(t *testing.T) {
	ctx := context.Background()

	// 1. runQueryHandler error cases
	// Method != POST
	rGet := httptest.NewRequest(http.MethodGet, "/datatug/exec/run_query", nil)
	wGet := httptest.NewRecorder()
	runQueryHandler(wGet, rGet)
	assert.Equal(t, http.StatusBadRequest, wGet.Code)

	// Body read error
	rErr := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", &errReader{})
	wErr := httptest.NewRecorder()
	runQueryHandler(wErr, rErr)
	assert.Equal(t, http.StatusBadRequest, wErr.Code)

	// Decode error
	rBad := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", strings.NewReader("bad-json"))
	wBad := httptest.NewRecorder()
	runQueryHandler(wBad, rBad)
	assert.Equal(t, http.StatusBadRequest, wBad.Code)

	// 2. computeRunQueryWithOptions: unknown project
	dir := t.TempDir()
	pID := "run_q_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	_, err := computeRunQuery(ctx, apicontract.ExecutionRequest{Project: "unk_proj", Environment: "env", SecurityContextID: api.SecurityContextID(), QueryID: "q1"})
	assert.Error(t, err)

	// 3. computeRunQueryWithOptions: execRunProjectStoreFor error
	origStore := execRunProjectStoreFor
	execRunProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store err")
	}
	defer func() { execRunProjectStoreFor = origStore }()

	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{Project: pID, Environment: "env", SecurityContextID: api.SecurityContextID(), QueryID: "q1"})
	assert.Error(t, err)
	execRunProjectStoreFor = origStore

	// 3b. computeRunQueryWithOptions: api.SecureExecutor() !ok
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{Project: pID, Environment: "env", SecurityContextID: api.SecurityContextID(), QueryID: "q1"})
	assert.Error(t, err)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{pID: dir}, api.Capabilities{})

	// 4. declaredValueType cases
	for _, tc := range []struct {
		input    string
		expected apicontract.ValueType
		ok       bool
	}{
		{"number", apicontract.ValueTypeNumber, true},
		{"boolean", apicontract.ValueTypeBoolean, true},
		{"bit", apicontract.ValueTypeBoolean, true},
		{"date", apicontract.ValueTypeDate, true},
		{"datetime", apicontract.ValueTypeDatetime, true},
		{"decimal", apicontract.ValueTypeDecimal, true},
		{"unsupported", "", false},
	} {
		vt, ok := declaredValueType(tc.input)
		assert.Equal(t, tc.ok, ok)
		assert.Equal(t, tc.expected, vt)
	}

	// 5. sqlQueryArgs
	qDef := &datatug.QueryDef{
		Parameters: []datatug.ParameterDef{{ID: "p1"}, {ID: "p2"}},
	}
	args := sqlQueryArgs(qDef, map[string]any{"p1": "val1"})
	assert.Len(t, args, 1)
	assert.Equal(t, "p1", args[0].Name)

	// 6. nativeParameterValue error branches
	_, err = nativeParameterValue(apicontract.TypedValueOrSet{})
	assert.Error(t, err)

	badSet := apicontract.TypedValueOrSet{
		Set: &apicontract.TypedValueSet{
			Values: []apicontract.TypedValue{{Type: apicontract.ValueTypeInteger, Str: "not-an-int"}},
		},
	}
	_, err = nativeParameterValue(badSet)
	assert.Error(t, err)

	// 7. runHTTPQuery missing required parameters
	qHTTP := &datatug.QueryDef{
		ID:         "http_q",
		Parameters: []datatug.ParameterDef{{ID: "req_param", IsRequired: true}},
	}
	_, err = runHTTPQuery(ctx, nil, "https://example.com", qHTTP, map[string]any{})
	assert.Error(t, err)

	// 8. sourceUnavailableWithSnapshots with non-HTTP queryDef
	ce := sourceUnavailableWithSnapshots(dir, &datatug.QueryDef{Type: datatug.QueryTypeSQL}, "msg")
	assert.Nil(t, ce.Details)
}

func TestCoverageFinish100_Incidents(t *testing.T) {
	// 1. incidentSearchDispatch id != search
	rBadDispatch := httptest.NewRequest(http.MethodGet, "/datatug/incidents/other", nil)
	paramsCtx := context.WithValue(rBadDispatch.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "other"}})
	wBadDispatch := httptest.NewRecorder()
	incidentSearchDispatch(wBadDispatch, rBadDispatch.WithContext(paramsCtx))
	assert.Equal(t, http.StatusNotFound, wBadDispatch.Code)

	// 2. incidentRefFromPath invalid id
	rBadRef := httptest.NewRequest(http.MethodGet, "/datatug/incidents/bad/id", nil)
	badParamsCtx := context.WithValue(rBadRef.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "bad/id"}})
	_, err := incidentRefFromPath(rBadRef.WithContext(badParamsCtx), "store1")
	assert.Error(t, err)

	// 3. readIncidentBody error
	wBodyErr := httptest.NewRecorder()
	rBodyErr := httptest.NewRequest(http.MethodPost, "/datatug/incidents", strings.NewReader("bad-json"))
	err = readIncidentBody(wBodyErr, rBodyErr, &apicontract.IncidentCreateRequest{})
	assert.Error(t, err)

	// 4. incidentStoreError with ErrIncidentNotFound
	errInc := incidentStoreError(incidentstore.ErrIncidentNotFound)
	assert.Equal(t, apicontract.ErrCodeNotFound, errInc.(*contractError).Code)

	// 5. validation hooks in incidents.go
	router, scopes, _ := configureIncidentHTTPState(t, "alpha")
	// 5a. create an incident
	recCreate := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "mut-success", "title"), http.StatusCreated)
	var created apicontract.IncidentResponse
	require.NoError(t, json.Unmarshal(recCreate.Body.Bytes(), &created))

	// 5b. now set hooks to fail
	origCreateVal := incidentResponseValidateHook
	incidentResponseValidateHook = func(*apicontract.IncidentResponse) error {
		return errors.New("resp val fail")
	}
	defer func() { incidentResponseValidateHook = origCreateVal }()

	origListVal := incidentListResponseValidateHook
	incidentListResponseValidateHook = func(*apicontract.IncidentListResponse) error {
		return errors.New("list val fail")
	}
	defer func() { incidentListResponseValidateHook = origListVal }()

	origSimVal := incidentSimilarResponseValidateHook
	incidentSimilarResponseValidateHook = func(*apicontract.IncidentSimilarResponse) error { return errors.New("sim val fail") }
	defer func() { incidentSimilarResponseValidateHook = origSimVal }()

	// create fails with hook
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "mut-fail", "title"), http.StatusInternalServerError)

	// list fails with hook
	listURL := fmt.Sprintf("/datatug/incidents?project=alpha&environment=prod&securityContextId=%s&storeId=ops", scopes["alpha"].SecurityContextID)
	reqList := httptest.NewRequest(http.MethodGet, listURL, nil)
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, reqList)
	assert.Equal(t, http.StatusInternalServerError, recList.Code)

	// similar fails with hook
	simURL := fmt.Sprintf("/datatug/incidents/%s/similar?project=alpha&environment=prod&securityContextId=%s&storeId=ops", created.Incident.Ref.IncidentID, scopes["alpha"].SecurityContextID)
	reqSim := httptest.NewRequest(http.MethodGet, simURL, nil)
	recSim := httptest.NewRecorder()
	router.ServeHTTP(recSim, reqSim)
	assert.Equal(t, http.StatusInternalServerError, recSim.Code)

	// similar not found (line 449)
	simNotFoundURL := fmt.Sprintf("/datatug/incidents/nonexistent_id/similar?project=alpha&environment=prod&securityContextId=%s&storeId=ops", scopes["alpha"].SecurityContextID)
	reqSimNF := httptest.NewRequest(http.MethodGet, simNotFoundURL, nil)
	recSimNF := httptest.NewRecorder()
	router.ServeHTTP(recSimNF, reqSimNF)
	assert.Equal(t, http.StatusNotFound, recSimNF.Code)
}

func TestCoverageFinish100_QueryCapture_And_Validate(t *testing.T) {
	ctx := context.Background()

	// 1. query_capture.go:200 - authorizeProjectQueryWrite non-access-denied error
	origAuthWrite := authorizeProjectQueryWrite
	authorizeProjectQueryWrite = func(context.Context, string, string, access.Operations) error {
		return errors.New("internal disk full")
	}
	defer func() { authorizeProjectQueryWrite = origAuthWrite }()

	dir := t.TempDir()
	pID := "cap_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	req := captureQueryRequest{
		Project: pID, Environment: "env", SecurityContextID: api.SecurityContextID(),
		IfNoneMatch: true,
		Query:       capturedQuery{ID: "c1", Title: "Orders", Source: "s1", DTQL: "from: {name: orders}\n"},
	}
	_, _, err := computeCaptureQuery(ctx, req)
	assert.Error(t, err)
	authorizeProjectQueryWrite = origAuthWrite

	// 2. query_capture_validate.go:215 - dtqlCollectionHook returns ""
	origCollHook := dtqlCollectionHook
	dtqlCollectionHook = func(dal.StructuredQuery) string {
		return ""
	}
	defer func() { dtqlCollectionHook = origCollHook }()

	_, err = validateCaptureDTQL("from: {name: orders}\n", nil)
	assert.Error(t, err)
	dtqlCollectionHook = origCollHook
}

func TestCoverageFinish100_QueryEndpoints(t *testing.T) {
	ctx := context.Background()

	// 1. getAllQueries unknown project (line 135)
	_, err := getAllQueries(ctx, dto.ProjectRef{StoreID: "s", ProjectID: "unk_proj"})
	assert.Error(t, err)

	// 2. getAllQueries loadModuleQueriesHook error (line 139)
	dir := t.TempDir()
	pID := "personal_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origLoad := loadModuleQueriesHook
	loadModuleQueriesHook = func(string) ([]*datatug.QueryDef, map[*datatug.QueryDef]string, error) {
		return nil, nil, errors.New("load queries fail")
	}
	defer func() { loadModuleQueriesHook = origLoad }()

	_, err = getAllQueries(ctx, dto.ProjectRef{StoreID: "s", ProjectID: pID})
	assert.Error(t, err)

	// 3. getPersonalQueries invalid project IDSegment (line 197)
	_, err = getPersonalQueries(ctx, dto.ProjectRef{StoreID: "s", ProjectID: "bad/project/id"})
	assert.Error(t, err)

	// 4. getPersonalQueries loadModuleQueriesHook error (line 201)
	_, err = getPersonalQueries(ctx, dto.ProjectRef{StoreID: "s", ProjectID: pID})
	assert.Error(t, err)
	loadModuleQueriesHook = origLoad
}

func TestCoverageFinish100_SemanticColumns_And_Schema(t *testing.T) {
	ctx := context.Background()

	// 1. semantic_columns.go:45 - semanticProjectStoreFor error
	dir := t.TempDir()
	pID := "sem_col_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origStore := semanticProjectStoreFor
	semanticProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("sem store err")
	}
	defer func() { semanticProjectStoreFor = origStore }()

	scope := apicontract.Scope{Project: pID, Environment: "env", SecurityContextID: api.SecurityContextID()}
	_, err := computeSemanticColumns(ctx, scope, apicontract.SourceRef{Source: "s", Collection: "c"})
	assert.Error(t, err)
	semanticProjectStoreFor = origStore

	// 2. semantic_schema.go - dalAsSchemaReader !ok (line 92)
	sqliteDBPath := filepath.Join(dir, "db.sqlite")
	require.NoError(t, os.WriteFile(sqliteDBPath, []byte(""), 0644))
	sqliteURL := "sqlite://" + sqliteDBPath

	origAsReader := dalAsSchemaReader
	dalAsSchemaReader = func(dal.DB) (dbschema.SchemaReader, bool) {
		return nil, false
	}
	defer func() { dalAsSchemaReader = origAsReader }()
	_, err = resolveSQLSourceURL(ctx, "src", sqliteURL, "coll")
	assert.Error(t, err)
	dalAsSchemaReader = origAsReader
}

func TestCoverageFinish100_SemanticApplicable_And_Related(t *testing.T) {
	ctx := context.Background()

	// 1. semanticApplicableHandler method != POST
	rGet := httptest.NewRequest(http.MethodGet, "/datatug/queries/applicable", nil)
	wGet := httptest.NewRecorder()
	semanticApplicableHandler(wGet, rGet)
	assert.Equal(t, http.StatusBadRequest, wGet.Code)

	// 2. distinctFactValues with duplicate values (lines 155-156)
	factsWithDupes := []apicontract.Fact{
		{ID: "f1", Value: apicontract.NewStringValue("dup")},
		{ID: "f2", Value: apicontract.NewStringValue("dup")},
		{ID: "f3", Value: apicontract.NewStringValue("uniq")},
	}
	cnt := distinctFactValues(factsWithDupes)
	assert.Equal(t, 2, cnt)

	// 3. mapFactOrigin cases
	assert.Equal(t, apicontract.BindingOriginSelection, mapFactOrigin(apicontract.FactOriginSelection))
	assert.Equal(t, apicontract.BindingOriginContext, mapFactOrigin(apicontract.FactOriginContext))
	assert.Equal(t, apicontract.BindingOriginManual, mapFactOrigin("custom"))

	// 4. finishCandidate branches
	dir := t.TempDir()
	pID := "cand_proj"
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	projStore := filestore.NewProjectStore(pID, dir)

	q := &datatug.QueryDef{ID: "bare_q"}
	// len(eligible) == 0 -> SourceUnavailable
	cand := finishCandidate(ctx, projStore, dir, "", q, nil, nil, nil, nil, map[*datatug.QueryDef]string{})
	assert.Equal(t, apicontract.CandidateStateSourceUnavailable, cand.State)
	assert.Equal(t, "bare_q", cand.QueryID)
	assert.Empty(t, cand.Bindings)
	assert.Empty(t, cand.Chain)
	assert.Empty(t, cand.Missing)
	assert.Empty(t, cand.Ambiguous)
	assert.Empty(t, cand.Targets)

	// 5. semanticRelatedHandler method != POST
	rRelGet := httptest.NewRequest(http.MethodGet, "/datatug/semantic/related", nil)
	wRelGet := httptest.NewRecorder()
	semanticRelatedHandler(wRelGet, rRelGet)
	assert.Equal(t, http.StatusBadRequest, wRelGet.Code)

	// 6. semanticRelatedRowsHandler method != POST
	rRowsGet := httptest.NewRequest(http.MethodGet, "/datatug/semantic/related/rows", nil)
	wRowsGet := httptest.NewRecorder()
	semanticRelatedRowsHandler(wRowsGet, rRowsGet)
	assert.Equal(t, http.StatusBadRequest, wRowsGet.Code)

	// 7. computeSemanticRelatedRows relatedResultValidateHook error
	origVal := relatedResultValidateHook
	relatedResultValidateHook = func(*apicontract.Result) error {
		return errors.New("related result val fail")
	}
	defer func() { relatedResultValidateHook = origVal }()
}
