package endpoints

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoverage_Executions_ListHandler_And_RequestErrors(t *testing.T) {
	// 1. executionsListHandler with invalid limit (triggering writeContractError on line 32)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/datatug/executions?limit=notanint", nil)
	executionsListHandler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// 2. executionListRequest with invalid incident reference (line 47)
	rBadInc := httptest.NewRequest(http.MethodGet, "/datatug/executions?incident=bad_incident_without_slash_or_format!", nil)
	_, err := executionListRequest(rBadInc)
	assert.Error(t, err)

	// 3. executionListRequest with valid incident reference and valid limit (lines 49, 56)
	rGood := httptest.NewRequest(http.MethodGet, "/datatug/executions?incident=store1/inc-1&limit=50", nil)
	reqGood, err := executionListRequest(rGood)
	require.NoError(t, err)
	assert.NotNil(t, reqGood.Incident)
	assert.Equal(t, 50, *reqGood.Limit)

	// 4. computeExecutionList scope validation error (line 63)
	rReq := httptest.NewRequest(http.MethodGet, "/datatug/executions", nil)
	_, err = computeExecutionList(rReq, apicontract.ExecutionListRequest{Scope: apicontract.Scope{}})
	assert.Error(t, err)

	// 5. computeExecutionList request validation error (line 66)
	validScope := apicontract.Scope{Project: "p", Environment: "e", SecurityContextID: "s"}
	badLimit := -1
	_, err = computeExecutionList(rReq, apicontract.ExecutionListRequest{Scope: validScope, Limit: &badLimit})
	assert.Error(t, err)

	// 6. computeExecutionList store not found (line 70)
	_, err = computeExecutionList(rReq, apicontract.ExecutionListRequest{Scope: validScope})
	assert.Error(t, err)

	// 7. Test with a configured project and store
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidence(t, projectID, projectDir)
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.Source = semanticTestSource
	req.Record, req.Snapshot = true, true
	req.MeasurementProjections = []apicontract.MeasurementProjection{{ID: "rows", Aggregate: apicontract.MeasurementAggregateRowCount}}

	result, err := computeRunQuery(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result.Execution)

	// computeExecutionList with since/until filters and limit
	now := time.Now().UTC()
	sinceStr := now.Add(-1 * time.Hour).Format(time.RFC3339)
	untilStr := now.Add(1 * time.Hour).Format(time.RFC3339)
	limitVal := 1
	listReq := apicontract.ExecutionListRequest{
		Scope: scope,
		Since: sinceStr,
		Until: untilStr,
		Limit: &limitVal,
	}
	resp, err := computeExecutionList(rReq, listReq)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Executions)

	// computeExecutionList filtering out records (line 91)
	listReqMismatch := apicontract.ExecutionListRequest{
		Scope:   scope,
		QueryID: "different_query_id",
	}
	respMismatch, err := computeExecutionList(rReq, listReqMismatch)
	require.NoError(t, err)
	assert.Empty(t, respMismatch.Executions)

	// Truncated flag when len(executions) == limit (line 104)
	_, err = computeRunQuery(context.Background(), req)
	require.NoError(t, err)

	limitOne := 1
	listReqTrunc := apicontract.ExecutionListRequest{
		Scope: scope,
		Limit: &limitOne,
	}
	respTrunc, err := computeExecutionList(rReq, listReqTrunc)
	require.NoError(t, err)
	assert.True(t, respTrunc.Truncated)
}

func TestCoverage_Executions_Show_And_Snapshot(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidence(t, projectID, projectDir)
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.Source = semanticTestSource
	req.Record, req.Snapshot = true, true

	result, err := computeRunQuery(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result.Execution)

	// 1. executionShowHandler
	router := httprouter.New()
	router.HandlerFunc(http.MethodGet, "/datatug/executions/:id", executionShowHandler)
	router.HandlerFunc(http.MethodGet, "/datatug/executions/:id/snapshot", executionSnapshotHandler)

	query := url.Values{"project": {projectID}, "environment": {scope.Environment}, "securityContextId": {scope.SecurityContextID}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/datatug/executions/"+result.Execution.ExecutionID+"?"+query.Encode(), nil)
	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	// 2. computeExecutionShow environment mismatch (line 133)
	queryEnvMismatch := url.Values{"project": {projectID}, "environment": {"other_env"}, "securityContextId": {scope.SecurityContextID}}
	rEnvMismatch := httptest.NewRequest(http.MethodGet, "/datatug/executions/"+result.Execution.ExecutionID+"?"+queryEnvMismatch.Encode(), nil)
	paramsCtx := context.WithValue(rEnvMismatch.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: result.Execution.ExecutionID}})
	_, err = computeExecutionShow(rEnvMismatch.WithContext(paramsCtx))
	assert.Error(t, err)

	// 3. executionReadScope invalid id (line 320)
	rBadID := httptest.NewRequest(http.MethodGet, "/datatug/executions/invalid/slash/id?"+query.Encode(), nil)
	badParamsCtx := context.WithValue(rBadID.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "invalid/slash/id"}})
	_, _, _, err = executionReadScope(rBadID.WithContext(badParamsCtx))
	assert.Error(t, err)

	// 4. executionReadScope store not found (line 324)
	queryBadStore := url.Values{"project": {"unknown_proj"}, "environment": {scope.Environment}, "securityContextId": {scope.SecurityContextID}}
	rBadStore := httptest.NewRequest(http.MethodGet, "/datatug/executions/show-test-1?"+queryBadStore.Encode(), nil)
	badStoreCtx := context.WithValue(rBadStore.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "show-test-1"}})
	_, _, _, err = executionReadScope(rBadStore.WithContext(badStoreCtx))
	assert.Error(t, err)

	// 5. executionReadError cases (lines 330-333)
	errSnap := executionReadError(executionstore.ErrSnapshotNotFound)
	assert.Equal(t, apicontract.ErrCodeNotFound, errSnap.(*contractError).Code)

	errInc := executionReadError(incidentstore.ErrExecutionNotFound)
	assert.Equal(t, apicontract.ErrCodeNotFound, errInc.(*contractError).Code)

	errOther := executionReadError(errors.New("other"))
	assert.Equal(t, codeInternal, errOther.(*contractError).Code)

	// 6. executionSnapshotLimit (lines 227-231)
	rLimBad := httptest.NewRequest(http.MethodGet, "/?limit=abc", nil)
	_, err = executionSnapshotLimit(rLimBad)
	assert.Error(t, err)

	rLimZero := httptest.NewRequest(http.MethodGet, "/?limit=0", nil)
	_, err = executionSnapshotLimit(rLimZero)
	assert.Error(t, err)

	rLimOver := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/?limit=%d", maxResultLimit+1), nil)
	_, err = executionSnapshotLimit(rLimOver)
	assert.Error(t, err)

	rLimEmpty := httptest.NewRequest(http.MethodGet, "/", nil)
	limPtr, err := executionSnapshotLimit(rLimEmpty)
	assert.NoError(t, err)
	assert.Nil(t, limPtr)

	// 7. computeExecutionSnapshot snapshot not found / no snapshotRef (line 168)
	rNoSnap := httptest.NewRequest(http.MethodGet, "/datatug/executions/show-test-1/snapshot?"+query.Encode(), nil)
	rNoSnapCtx := context.WithValue(rNoSnap.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "show-test-1"}})
	_, err = computeExecutionSnapshot(rNoSnap.WithContext(rNoSnapCtx))
	assert.Error(t, err)
}

func TestCoverage_Executions_SeriesHandler(t *testing.T) {
	// 1. executionSeriesHandler body read error (MaxBytesReader) (lines 237-243)
	hugeBody := strings.Repeat("A", maxExecutionSeriesBodyBytes+100)
	rHuge := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", strings.NewReader(hugeBody))
	wHuge := httptest.NewRecorder()
	executionSeriesHandler(wHuge, rHuge)
	assert.Equal(t, http.StatusBadRequest, wHuge.Code)

	// 2. executionSeriesHandler invalid JSON (line 247)
	rBadJSON := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", strings.NewReader("bad json"))
	wBadJSON := httptest.NewRecorder()
	executionSeriesHandler(wBadJSON, rBadJSON)
	assert.Equal(t, http.StatusBadRequest, wBadJSON.Code)

	// 3. computeExecutionSeriesBounded store not found (line 267)
	rPost := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", nil)
	seriesReq := apicontract.ExecutionSeriesRequest{
		Scope: apicontract.Scope{Project: "unknown_proj", Environment: "env", SecurityContextID: "sec"},
		Partition: apicontract.ExecutionSeriesPartition{
			EvidenceStoreID:   "unknown_proj",
			SourceScope:       apicontract.ExecutionRecordScope{StoreID: "unknown_proj", Project: "unknown_proj", Environment: "env"},
			Source:            "src",
			PolicyFingerprint: "fp",
		},
	}
	_, err := computeExecutionSeries(rPost, seriesReq)
	assert.Error(t, err)

	// 4. isCurrentPolicyDenial helper (lines 308-309)
	assert.True(t, isCurrentPolicyDenial(dal.ErrNotSupported))
	assert.True(t, isCurrentPolicyDenial(secureread.ErrSnapshotPolicyUnexpressible))
	assert.True(t, isCurrentPolicyDenial(secureread.ErrAccessDenied))
	assert.True(t, isCurrentPolicyDenial(errors.New("access denied: forbidden")))
	assert.False(t, isCurrentPolicyDenial(errors.New("random err")))
}

func TestCoverage_ExecutionRecording_Measurements(t *testing.T) {
	// finiteDecimal edge cases (lines 362-364)
	dec, ok := finiteDecimal(big.NewRat(0, 1))
	assert.True(t, ok)
	assert.Equal(t, "0", dec)

	// Non-finite decimal (e.g. 1/3)
	_, ok = finiteDecimal(big.NewRat(1, 3))
	assert.False(t, ok)

	// numericValue edge cases (lines 373-376)
	numVal := apicontract.TypedValue{Type: apicontract.ValueTypeNumber, Num: 12.5}
	rat, ok := numericValue(numVal)
	assert.True(t, ok)
	assert.NotNil(t, rat)

	strVal := apicontract.NewStringValue("not_num")
	_, ok = numericValue(strVal)
	assert.False(t, ok)

	// aggregateMeasurement cases
	projCount := apicontract.MeasurementProjection{ID: "cnt", Aggregate: apicontract.MeasurementAggregateCount, Column: "id"}
	rowsWithNull := [][]apicontract.TypedValue{
		{apicontract.NewIntegerValue("1")},
		{apicontract.NewNullValue()},
		{apicontract.NewIntegerValue("3")},
	}
	measCount := aggregateMeasurement(projCount, 0, rowsWithNull)
	assert.Equal(t, apicontract.MeasurementComplete, measCount.Completeness)
	assert.Equal(t, "2", measCount.Value.Str)

	// Empty rows for non-count
	projSum := apicontract.MeasurementProjection{ID: "sum", Aggregate: apicontract.MeasurementAggregateSum, Column: "id"}
	measEmpty := aggregateMeasurement(projSum, 0, nil)
	assert.Equal(t, apicontract.MeasurementUnavailable, measEmpty.Completeness)
	assert.Equal(t, apicontract.MeasurementReasonNoRows, measEmpty.Reason)

	// Non-numeric row for sum
	measNonNum := aggregateMeasurement(projSum, 0, [][]apicontract.TypedValue{{apicontract.NewStringValue("abc")}})
	assert.Equal(t, apicontract.MeasurementUnavailable, measNonNum.Completeness)
	assert.Equal(t, apicontract.MeasurementReasonNonNumeric, measNonNum.Reason)

	// First aggregate
	projFirst := apicontract.MeasurementProjection{ID: "first", Aggregate: apicontract.MeasurementAggregateFirst, Column: "id"}
	measFirst := aggregateMeasurement(projFirst, 0, [][]apicontract.TypedValue{{apicontract.NewIntegerValue("42")}})
	assert.Equal(t, apicontract.MeasurementComplete, measFirst.Completeness)
	assert.Equal(t, "42", measFirst.Value.Str)

	// Min and Max aggregates
	numRows := [][]apicontract.TypedValue{
		{apicontract.NewIntegerValue("10")},
		{apicontract.NewIntegerValue("5")},
		{apicontract.NewIntegerValue("20")},
	}
	projMin := apicontract.MeasurementProjection{ID: "min", Aggregate: apicontract.MeasurementAggregateMin, Column: "id"}
	measMin := aggregateMeasurement(projMin, 0, numRows)
	assert.Equal(t, apicontract.MeasurementComplete, measMin.Completeness)
	assert.Equal(t, "5", measMin.Value.Str)

	projMax := apicontract.MeasurementProjection{ID: "max", Aggregate: apicontract.MeasurementAggregateMax, Column: "id"}
	measMax := aggregateMeasurement(projMax, 0, numRows)
	assert.Equal(t, apicontract.MeasurementComplete, measMax.Completeness)
	assert.Equal(t, "20", measMax.Value.Str)

	// Avg aggregate producing repeating decimal (finiteDecimal fails)
	numRowsThird := [][]apicontract.TypedValue{
		{apicontract.NewIntegerValue("1")},
		{apicontract.NewIntegerValue("2")},
		{apicontract.NewIntegerValue("4")}, // sum = 7, avg = 7/3
	}
	projAvg := apicontract.MeasurementProjection{ID: "avg", Aggregate: apicontract.MeasurementAggregateAvg, Column: "id"}
	measAvg := aggregateMeasurement(projAvg, 0, numRowsThird)
	assert.Equal(t, apicontract.MeasurementUnavailable, measAvg.Completeness)
	assert.Equal(t, apicontract.MeasurementReasonSourceRefused, measAvg.Reason)

	// measureRecordset with truncated = true and limitations > 0
	rs := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}},
		Rows:    numRows,
	}
	projs := []apicontract.MeasurementProjection{projMin}
	mTrunc, err := measureRecordset(rs, projs, nil, true)
	require.NoError(t, err)
	assert.Equal(t, apicontract.MeasurementReasonTruncated, mTrunc[0].Reason)

	mLim, err := measureRecordset(rs, projs, []apicontract.Limitation{{Policy: "pol", RowsFiltered: true}}, false)
	require.NoError(t, err)
	assert.Equal(t, apicontract.MeasurementReasonPolicyLimited, mLim[0].Reason)

	// Unknown column in projection
	projMissing := apicontract.MeasurementProjection{ID: "m", Aggregate: apicontract.MeasurementAggregateSum, Column: "missing_col"}
	mMissing, err := measureRecordset(rs, []apicontract.MeasurementProjection{projMissing}, nil, false)
	require.NoError(t, err)
	assert.Equal(t, apicontract.MeasurementReasonSourceRefused, mMissing[0].Reason)
}
