package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/julienschmidt/httprouter"
)

func configureExecutionEvidence(t *testing.T, projectID, projectDir string) {
	t.Helper()
	configureExecutionEvidenceWithOptions(t, projectID, projectDir, executionstore.Options{PrivateDir: t.TempDir()})
}

func configureExecutionEvidenceWithOptions(t *testing.T, projectID, projectDir string, options executionstore.Options) {
	t.Helper()
	if err := api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, nil, options); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.CloseExecutionEvidence() })
}

func TestRecordedRunQueryListShowAndSnapshotJourney(t *testing.T) {
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
	if err != nil {
		t.Fatalf("computeRunQuery: %v", err)
	}
	if result.Execution == nil {
		t.Fatal("recorded result has no execution ref")
	}
	evidenceStore, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	storedRecord, err := evidenceStore.Execution(context.Background(), *result.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if err = authorizeExecutionRecord(httptest.NewRequest(http.MethodGet, "/", nil), storedRecord); err != nil {
		t.Fatalf("record authorization: %v (fields=%+v)", err, storedRecord.AuthorizedFields)
	}
	receipts, err := filepath.Glob(filepath.Join(projectDir, "executions", "*", "*", result.Execution.ExecutionID+".json"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("execution receipt paths = %v, err=%v", receipts, err)
	}

	query := url.Values{"project": {projectID}, "environment": {req.Environment}, "securityContextId": {scope.SecurityContextID}}
	router := httprouter.New()
	router.HandlerFunc(http.MethodGet, "/datatug/executions", executionsListHandler)
	router.HandlerFunc(http.MethodGet, "/datatug/executions/:id", executionShowHandler)
	router.HandlerFunc(http.MethodGet, "/datatug/executions/:id/snapshot", executionSnapshotHandler)

	list := performExecutionGET(t, router, "/datatug/executions?"+query.Encode())
	var listResponse apicontract.ExecutionListResponse
	if err := apicontract.DecodeStrict(list.Body.Bytes(), &listResponse); err != nil {
		t.Fatal(err)
	}
	if len(listResponse.Executions) != 1 || listResponse.Executions[0].Ref != *result.Execution {
		t.Fatalf("list = %+v, want recorded execution", listResponse)
	}

	show := performExecutionGET(t, router, "/datatug/executions/"+result.Execution.ExecutionID+"?"+query.Encode())
	var record apicontract.ExecutionRecord
	if err := apicontract.DecodeStrict(show.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Ref != *result.Execution || record.SnapshotRef == "" || record.Measurements[0].Value.Str != "1" {
		t.Fatalf("record = %+v", record)
	}
	seriesRequest := apicontract.ExecutionSeriesRequest{
		Scope: apicontract.Scope{StoreID: projectID, Project: projectID, Environment: req.Environment, SecurityContextID: scope.SecurityContextID},
		Partition: apicontract.ExecutionSeriesPartition{
			EvidenceStoreID: projectID, SourceScope: record.Scope, Source: record.Provenance.Source,
			PolicyFingerprint: record.PolicyFingerprint, DTQLHash: record.DTQLHash,
			BindingsApplied: record.BindingsApplied, Projection: record.Measurements[0].Projection,
		},
	}
	seriesHTTP := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", nil)
	series, err := computeExecutionSeries(seriesHTTP, seriesRequest)
	if err != nil {
		t.Fatalf("computeExecutionSeries: %v", err)
	}
	if len(series.Points) != 1 || series.Points[0].Execution != *result.Execution || series.Points[0].Value == nil || series.Points[0].Value.Str != "1" || series.Omitted {
		t.Fatalf("series = %+v", series)
	}
	bounded, err := computeExecutionSeriesBounded(seriesHTTP, seriesRequest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.Points) != 0 || !bounded.Omitted {
		t.Fatalf("bounded series = %+v, want truthful omission", bounded)
	}

	snapshot := performExecutionGET(t, router, "/datatug/executions/"+result.Execution.ExecutionID+"/snapshot?"+query.Encode())
	var snapshotResponse apicontract.SnapshotReadResponse
	if err := apicontract.DecodeStrict(snapshot.Body.Bytes(), &snapshotResponse); err != nil {
		t.Fatal(err)
	}
	if snapshotResponse.Recordset == nil || len(snapshotResponse.Recordset.Rows) != len(result.Recordset.Rows) {
		t.Fatalf("snapshot rows = %+v", snapshotResponse.Recordset)
	}
	store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	storedSnapshot, err := store.Snapshot(context.Background(), *result.Execution, record.SnapshotRef)
	if err != nil {
		t.Fatal(err)
	}
	if storedSnapshot.Recordset == nil || !reflect.DeepEqual(*storedSnapshot.Recordset, result.Recordset) {
		t.Fatalf("stored snapshot = %+v, want the already-masked result %+v", storedSnapshot.Recordset, result.Recordset)
	}
	records, err := store.Executions(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("snapshot read created an execution: count=%d err=%v", len(records), err)
	}

	// The persisted grant audit is immutable, but a show response must not
	// disclose its qualified incident/grant references until current grant
	// visibility has been proved. This server does not yet have that resolver,
	// so the read surface conservatively omits them.
	grantRecord := record
	grantRecord.Ref.ExecutionID = "grant-redaction"
	grantRecord.SnapshotRef = ""
	incident := apicontract.IncidentRef{StoreID: projectID, IncidentID: "incident-1"}
	grantRecord.Incident = &incident
	grantRecord.GrantUses = []apicontract.GrantRef{{Incident: incident, GrantID: "grant-1", ApprovalMutationID: "approve-1"}}
	if err := store.PutExecution(context.Background(), grantRecord); err != nil {
		t.Fatalf("PutExecution grant record: %v", err)
	}
	grantShow := performExecutionGET(t, router, "/datatug/executions/"+grantRecord.Ref.ExecutionID+"?"+query.Encode())
	var redacted apicontract.ExecutionRecord
	if err := apicontract.DecodeStrict(grantShow.Body.Bytes(), &redacted); err != nil {
		t.Fatal(err)
	}
	if redacted.GrantUses != nil {
		t.Fatalf("grantUses disclosed without current grant visibility: %+v", redacted.GrantUses)
	}
}

func TestExpiredSnapshotStillRechecksCurrentCollectionAccess(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidenceWithOptions(t, projectID, projectDir, executionstore.Options{
		PrivateDir: t.TempDir(),
		Now:        func() time.Time { return time.Now().Add(31 * 24 * time.Hour) },
	})
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.Source, req.Record, req.Snapshot = semanticTestSource, true, true
	result, err := computeRunQuery(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution == nil {
		t.Fatal("recorded result has no execution")
	}

	// Rotate to a principal with no matching policy role. If lifecycle state
	// were returned before policy replay this request would incorrectly reveal
	// the explicit expired state with HTTP 200.
	deniedScope := configureSemanticSession(t, projectDir, projectID, "mallory", nil)
	query := url.Values{"project": {projectID}, "environment": {req.Environment}, "securityContextId": {deniedScope.SecurityContextID}}
	router := httprouter.New()
	router.HandlerFunc(http.MethodGet, "/datatug/executions/:id/snapshot", executionSnapshotHandler)
	request := httptest.NewRequest(http.MethodGet, "/datatug/executions/"+result.Execution.ExecutionID+"/snapshot?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expired revoked snapshot = %d %s, want non-disclosing denial", recorder.Code, recorder.Body.String())
	}
}

func TestExecutionSnapshotDropsColumnsHiddenByCurrentPolicy(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidence(t, projectID, projectDir)
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.Source, req.Record, req.Snapshot = semanticTestSource, true, true
	result, err := computeRunQuery(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution == nil || columnIndex(result.Recordset.Columns, "Email") < 0 {
		t.Fatalf("recorded result = %+v", result)
	}
	if result.Provenance.Collection != "Customer" {
		t.Fatalf("recorded collection = %q, want queried collection Customer", result.Provenance.Collection)
	}

	policyDir := t.TempDir()
	mustWriteFile(t, filepath.Join(policyDir, "support.yaml"), `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: support-without-email
default: deny
scopes:
  - path: /Customer
    rules:
      - id: visible-customer-fields
        effect: allow
        operations: [query]
        fields: [CustomerId, FirstName, LastName]
`)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "support", PoliciesDir: policyDir})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{})
	evidenceStore, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := evidenceStore.Execution(context.Background(), *result.Execution)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := evidenceStore.Snapshot(context.Background(), record.Ref, record.SnapshotRef)
	if err != nil {
		t.Fatal(err)
	}
	executor, _ := api.SecureExecutor()
	if _, err := executor.RunSnapshot(context.Background(), record.Provenance.Collection, *stored.Recordset); err != nil {
		t.Fatalf("direct current-policy replay for columns %+v: %v", stored.Recordset.Columns, err)
	}
	currentQuery := url.Values{"project": {projectID}, "environment": {req.Environment}, "securityContextId": {api.SecurityContextID()}}
	router := httprouter.New()
	router.HandlerFunc(http.MethodGet, "/datatug/executions/:id/snapshot", executionSnapshotHandler)

	snapshot := performExecutionGET(t, router, "/datatug/executions/"+result.Execution.ExecutionID+"/snapshot?"+currentQuery.Encode())
	var response apicontract.SnapshotReadResponse
	if err := apicontract.DecodeStrict(snapshot.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Recordset == nil || columnIndex(response.Recordset.Columns, "Email") >= 0 {
		t.Fatalf("current-policy snapshot retained Email: %+v", response.Recordset)
	}
	if len(response.Recordset.Rows) != len(result.Recordset.Rows) {
		t.Fatalf("current-policy snapshot rows = %d, want %d", len(response.Recordset.Rows), len(result.Recordset.Rows))
	}
}

func TestSnapshotRetentionRequiresExplicitSourcePolicyAndMasksBeforeStorage(t *testing.T) {
	newRequest := func(t *testing.T, caps api.Capabilities) (apicontract.ExecutionRequest, string) {
		t.Helper()
		var req apicontract.ExecutionRequest
		decodeRequestFixture(t, "execution_request_adhoc.json", &req)
		projectDir, projectID := writeRunQueryTestProject(t)
		scope := configureSemanticSessionWithCapabilities(t, projectDir, projectID, "alice", []string{"admin"}, caps)
		configureExecutionEvidence(t, projectID, projectDir)
		req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
		req.Source, req.Record, req.Snapshot = semanticTestSource, true, true
		return req, projectID
	}

	t.Run("missing policy refuses bytes but retains receipt", func(t *testing.T) {
		req, projectID := newRequest(t, api.Capabilities{})
		result, err := computeRunQuery(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Execution(context.Background(), *result.Execution)
		if err != nil {
			t.Fatal(err)
		}
		if record.SnapshotRef != "" {
			t.Fatalf("snapshot retained without explicit source policy: %+v", record)
		}
	})

	t.Run("masked columns never reach snapshot storage", func(t *testing.T) {
		caps := api.Capabilities{SnapshotPolicies: map[string]api.SnapshotProjectPolicy{}}
		// The project id is generated inside newRequest, so configure the policy
		// immediately after setup and preserve the same serving session identity.
		req, projectID := newRequest(t, caps)
		projectDir, _ := api.ProjectDir(projectID)
		scope := configureSemanticSessionWithCapabilities(t, projectDir, projectID, "alice", []string{"admin"}, api.Capabilities{
			SnapshotPolicies: map[string]api.SnapshotProjectPolicy{
				projectID: {Sources: map[string]api.SnapshotSourcePolicy{semanticTestSource: {Allow: true, MaskedColumns: []string{"Email"}}}},
			},
		})
		req.SecurityContextID = scope.SecurityContextID
		result, err := computeRunQuery(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if columnIndex(result.Recordset.Columns, "Email") < 0 {
			t.Fatal("live result was unexpectedly masked")
		}
		store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Execution(context.Background(), *result.Execution)
		if err != nil || record.SnapshotRef == "" {
			t.Fatalf("record = %+v, err=%v", record, err)
		}
		snapshot, err := store.Snapshot(context.Background(), record.Ref, record.SnapshotRef)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Recordset == nil || columnIndex(snapshot.Recordset.Columns, "Email") >= 0 {
			t.Fatalf("masked Email reached stored snapshot: %+v", snapshot.Recordset)
		}
	})
}

func columnIndex(columns []apicontract.Column, name string) int {
	for i, column := range columns {
		if column.Name == name {
			return i
		}
	}
	return -1
}

func TestExecutionSeriesRejectsOversizedBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", strings.NewReader(strings.Repeat("x", maxExecutionSeriesBodyBytes+1)))
	recorder := httptest.NewRecorder()
	executionSeriesHandler(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized series request = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestExecutionSnapshotLimitValidation(t *testing.T) {
	for _, target := range []string{"?limit=0", "?limit=501", "?limit=bad"} {
		req := httptest.NewRequest(http.MethodGet, "/datatug/executions/id/snapshot"+target, nil)
		if _, err := executionSnapshotLimit(req); err == nil {
			t.Fatalf("executionSnapshotLimit(%q) succeeded", target)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/datatug/executions/id/snapshot?limit=7", nil)
	limit, err := executionSnapshotLimit(req)
	if err != nil || limit == nil || *limit != 7 {
		t.Fatalf("executionSnapshotLimit(valid) = %v, %v", limit, err)
	}
}

func TestRelatedRowsRecordingReturnsQualifiedExecution(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidence(t, projectID, projectDir)
	req := newRelatedRowsRequest(scope, encodeLookupID(semanticTestSource, "Invoice", "CustomerId"), apicontract.NewIntegerValue("5"), nil)
	req.Record = true
	result, err := computeSemanticRelatedRows(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution == nil || result.Execution.StoreID != projectID || result.Execution.ProjectID != projectID {
		t.Fatalf("execution = %+v", result.Execution)
	}
	store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Executions(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
	wantValue := apicontract.ScalarValue(req.Value)
	if !reflect.DeepEqual(records[0].Parameters["value"], wantValue) || len(records[0].BindingsApplied) != 1 || !reflect.DeepEqual(records[0].BindingsApplied[0].Value, wantValue) {
		t.Fatalf("related-row value provenance was not retained: %+v", records[0])
	}

	req.Value = apicontract.NewIntegerValue("6")
	if _, err := computeSemanticRelatedRows(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	records, err = store.Executions(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
	if records[0].DTQLHash == records[1].DTQLHash {
		t.Fatal("related-row execution identity ignored the requested value")
	}
}

func TestExecutionMetadataAuthorizationRejectsProjectedAndRowScopedEvidence(t *testing.T) {
	const policy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: metadata-restricted
default: deny
scopes:
  - path: /orders
    rules:
      - id: own-visible-fields
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: { field: ownerID }
          right: { param: currentUser }
        fields: [id, ownerID]
`
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "policy.yaml"), policy)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", PoliciesDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{"p": t.TempDir()}, api.Capabilities{})
	record := apicontract.ExecutionRecord{
		Provenance: apicontract.Provenance{Collection: "orders"},
		AuthorizedFields: []apicontract.FieldAccessRef{
			{Column: "id"},
			{Column: "secret"},
		},
	}
	err = authorizeExecutionRecord(httptest.NewRequest(http.MethodGet, "/", nil), record)
	if !isCurrentPolicyDenial(err) {
		t.Fatalf("authorizeExecutionRecord = %v, want fail-closed denial", err)
	}

	// Even when every recorded field remains projected, an empty metadata
	// probe cannot prove which historical rows satisfy a current row rule.
	record.AuthorizedFields = []apicontract.FieldAccessRef{{Column: "id"}, {Column: "ownerID"}}
	err = authorizeExecutionRecord(httptest.NewRequest(http.MethodGet, "/", nil), record)
	if !isCurrentPolicyDenial(err) {
		t.Fatalf("row-scoped authorizeExecutionRecord = %v, want fail-closed denial", err)
	}
}

func TestExecutionMetadataAuthorizationFailsClosedForChangedBindingScope(t *testing.T) {
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{"p": t.TempDir()}, api.Capabilities{})
	value := apicontract.ScalarValue(apicontract.NewStringValue("sensitive-binding"))
	record := apicontract.ExecutionRecord{
		Provenance:        apicontract.Provenance{Collection: "orders"},
		AuthorizedFields:  []apicontract.FieldAccessRef{{Column: "id"}},
		PolicyFingerprint: strings.Repeat("a", 64),
		Parameters:        map[string]apicontract.TypedValueOrSet{"customer": value},
		BindingsApplied: []apicontract.Binding{{
			ParameterID: "customer", Value: value, Origin: apicontract.BindingOriginManual,
			OriginEvidence: apicontract.BindingOriginEvidenceClientReported,
		}},
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := authorizeExecutionRecord(request, record); !isCurrentPolicyDenial(err) {
		t.Fatalf("changed-scope metadata authorization = %v, want fail-closed denial", err)
	}
	record.PolicyFingerprint = api.SecurePolicyFingerprint()
	if err := authorizeExecutionRecord(request, record); err != nil {
		t.Fatalf("same-scope metadata authorization = %v", err)
	}
}

func TestAggregateMeasurementDoesNotRoundExactDecimals(t *testing.T) {
	projection := apicontract.MeasurementProjection{ID: "sum", Column: "amount", Aggregate: apicontract.MeasurementAggregateSum}
	measurement := aggregateMeasurement(projection, 0, [][]apicontract.TypedValue{
		{apicontract.NewDecimalValue("1234567890.123456789")},
		{apicontract.NewDecimalValue("0.000000000000000001")},
	})
	if measurement.Value == nil || measurement.Value.Str != "1234567890.123456789000000001" {
		t.Fatalf("sum = %+v value=%+v, want exact finite decimal", measurement, measurement.Value)
	}

	projection = apicontract.MeasurementProjection{ID: "avg", Column: "amount", Aggregate: apicontract.MeasurementAggregateAvg}
	measurement = aggregateMeasurement(projection, 0, [][]apicontract.TypedValue{
		{apicontract.NewIntegerValue("0")},
		{apicontract.NewIntegerValue("1")},
		{apicontract.NewIntegerValue("1")},
	})
	if measurement.Completeness != apicontract.MeasurementUnavailable || measurement.Value != nil {
		t.Fatalf("non-terminating average = %+v, want unavailable rather than rounded", measurement)
	}
}

func TestFiniteDecimalPreservesIntegerZeroes(t *testing.T) {
	for _, value := range []string{"0", "10", "100", "-200"} {
		exact, ok := new(big.Rat).SetString(value)
		if !ok {
			t.Fatalf("parse %q", value)
		}
		got, finite := finiteDecimal(exact)
		if !finite || got != value {
			t.Fatalf("finiteDecimal(%s) = %q, %v", value, got, finite)
		}
	}
}

func TestRecordingRejectsUnresolvedIncidentAssociation(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidence(t, projectID, projectDir)
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.Source, req.Record = semanticTestSource, true
	req.Incident = &apicontract.IncidentRef{StoreID: projectID, IncidentID: "missing-incident"}
	_, err := computeRunQuery(context.Background(), req)
	var contractErr *contractError
	if !errors.As(err, &contractErr) || contractErr.Code != apicontract.ErrCodeInvalidRequest || contractErr.Field != "incident" {
		t.Fatalf("computeRunQuery unresolved incident = %v, want incident INVALID_REQUEST", err)
	}
	store, storeErr := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	records, storeErr := store.Executions(context.Background())
	if storeErr != nil || len(records) != 0 {
		t.Fatalf("unresolved incident persisted %d records, err=%v", len(records), storeErr)
	}
}

func performExecutionGET(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		var body any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		t.Fatalf("GET %s = %d: %+v", target, recorder.Code, body)
	}
	return recorder
}
