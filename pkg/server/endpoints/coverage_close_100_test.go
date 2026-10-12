package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClose100_QueryCapture_And_Semantic(t *testing.T) {
	ctx := context.Background()

	// 1. query_capture.go:200 captureInternal error
	origAuth := authorizeProjectQueryWrite
	authorizeProjectQueryWrite = func(ctx context.Context, projectID, queryID string, operation access.Operations) error {
		return errors.New("auth internal boom")
	}
	defer func() { authorizeProjectQueryWrite = origAuth }()

	pID := fmt.Sprintf("qc_proj_%d", time.Now().UnixNano())
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{pID: dir}) }
	defer func() { storage.NewDatatugStore = origDatatugStore }()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	_, _, err := computeCaptureQuery(ctx, captureQueryRequest{
		Project:           pID,
		Environment:       "e",
		SecurityContextID: api.SecurityContextID(),
		IfNoneMatch:       true,
		Query: capturedQuery{
			ID:      "q1",
			Title:   "Q1",
			Purpose: "test purpose",
			Source:  "s1",
			DTQL:    "from: {name: orders}\n",
		},
	})
	t.Logf("computeCaptureQuery err: %v", err)
	assert.Error(t, err)

	// 2. semantic_columns.go:63 httpDeclaredColumnsHook error
	origDecl := httpDeclaredColumnsHook
	httpDeclaredColumnsHook = func(dir, qID string) (map[string]datatug.EntityFieldRef, error) {
		return nil, errors.New("decl fail")
	}
	defer func() { httpDeclaredColumnsHook = origDecl }()

	origEnt := loadModuleEntitiesHook
	loadModuleEntitiesHook = func(dir string) ([]*datatug.Entity, error) {
		return nil, errors.New("ent fail")
	}
	defer func() { loadModuleEntitiesHook = origEnt }()

	// Trigger semantic_columns with HTTP source ("type": "HTTP")
	projJSON := fmt.Sprintf(`{"id":%q,"access":"shared","environments":[{"id":"e"}]}`, pID)
	_ = os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(projJSON), 0644)
	qDir := filepath.Join(dir, "queries")
	_ = os.MkdirAll(qDir, 0755)
	qJSON := `{"id":"http_q","type":"HTTP"}`
	_ = os.WriteFile(filepath.Join(qDir, "http_q.query.json"), []byte(qJSON), 0644)

	scope := apicontract.Scope{Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID()}
	_, err = computeSemanticColumns(ctx, scope, apicontract.SourceRef{Source: "http_q", Collection: "http_q"})
	assert.Error(t, err)

	// Trigger semantic_columns with SQL source -> loadModuleEntitiesHook error
	catDir := filepath.Join(dir, "environments", "e", "catalogs")
	_ = os.MkdirAll(catDir, 0755)
	catJSON := `{"id":"c1","driver":"sqlite","path":"s1.db"}`
	_ = os.WriteFile(filepath.Join(catDir, "c1.db.json"), []byte(catJSON), 0644)
	chinookData, _ := os.ReadFile(chinookFixturePath)
	_ = os.WriteFile(filepath.Join(dir, "s1.db"), chinookData, 0644)

	_, err = computeSemanticColumns(ctx, scope, apicontract.SourceRef{Source: "c1", Collection: "items"})
	assert.Error(t, err)

	// 3. semantic_applicable.go:52 req.Validate error
	_, err = computeSemanticApplicable(ctx, apicontract.ApplicableRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Values: []apicontract.Fact{{Entity: ""}},
	})
	assert.Error(t, err)

	// semantic_applicable.go:108 unconvertible integer overflow
	_, err = computeSemanticApplicable(ctx, apicontract.ApplicableRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Values: []apicontract.Fact{{
			ID: "f1", Origin: "manual", Entity: "Customer", Field: "ID", Enabled: true,
			Value: apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "99999999999999999999999999999999999999999999999999999999999999999999999999999999"},
		}},
	})
	assert.NoError(t, err)

	// semantic_applicable.go:306 finishCandidate with single catalog (len(eligible) == 1)
	singleStore := mockStoreWithCatalogs{
		catalogs: datatug.DbCatalogs{
			{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "c1"}}, Driver: "sqlite", Path: "s1.db"}},
		},
	}
	candSingle := finishCandidate(ctx, singleStore, dir, "e", &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1"}},
	}, nil, nil, nil, nil, nil)
	assert.Len(t, candSingle.Targets, 1)
	assert.Equal(t, "c1", candSingle.SelectedSource)

	// 4. semantic_related.go hooks and errors
	origRelatedEnt := loadModuleEntitiesHookRelated
	defer func() { loadModuleEntitiesHookRelated = origRelatedEnt }()
	loadModuleEntitiesHookRelated = func(dir string) ([]*datatug.Entity, error) {
		return nil, errors.New("related ent fail")
	}

	_, err = computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Origin: "manual",
			Entity: "customer", Field: "id",
			Value:    apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "1"},
			Physical: &apicontract.PhysicalRef{Source: "c1", Collection: "customers", Column: "id"},
		},
	})
	assert.Error(t, err)
	loadModuleEntitiesHookRelated = origRelatedEnt

	// semanticRelated.go:129 secureExecutorHook !ok
	origExecHook := secureExecutorHook
	secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	_, err = computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Origin: "manual", Entity: "customer", Field: "id",
			Value:    apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "1"},
			Physical: &apicontract.PhysicalRef{Source: "c1", Collection: "Customer", Column: "CustomerId"},
		},
	})
	assert.Error(t, err)

	// semanticRelatedRows.go:266 secureExecutorHook !ok
	_, err = computeSemanticRelatedRows(ctx, apicontract.RelatedRowsRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		LookupID: encodeLookupID("c1", "Customer", "CustomerId"),
		Value:    apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "1"},
	})
	assert.Error(t, err)
	secureExecutorHook = origExecHook

	// relatedProjectDir false for computeSemanticRelatedRows (line 237)
	origRelDir := relatedProjectDir
	relatedProjectDir = func(string) (string, bool) { return "", false }
	_, err = computeSemanticRelatedRows(ctx, apicontract.RelatedRowsRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		LookupID: encodeLookupID("c1", "Customer", "CustomerId"),
		Value:    apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "1"},
	})
	assert.Error(t, err)
	relatedProjectDir = origRelDir

	// semantic_related.go:151 sort by collection when source equal, and line 178 countRelated with limitations
	origRunStruct := runStructuredRelatedHook
	runStructuredRelatedHook = func(e *secureread.Executor, c context.Context, s string, q dal.StructuredQuery, v map[string]any) (secureread.Result, error) {
		return secureread.Result{Limitations: []secureread.Limitation{{Policy: "p"}}}, nil
	}
	defer func() { runStructuredRelatedHook = origRunStruct }()

	loadModuleEntitiesHookRelated = func(dir string) ([]*datatug.Entity, error) {
		return []*datatug.Entity{
			{
				ID: "customer",
				Fields: []*datatug.EntityField{
					{
						ID: "id",
						Mappings: datatug.PhysicalRefs{
							{Source: "c1", Collection: "Invoice", Column: "CustomerId"},
							{Source: "c1", Collection: "Customer", Column: "CustomerId"},
							{Source: "c1", Collection: "InvoiceLine", Column: "InvoiceId"},
						},
					},
				},
			},
		}, nil
	}
	respRel, err := computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Origin: "manual", Entity: "customer", Field: "id",
			Value:    apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "1"},
			Physical: &apicontract.PhysicalRef{Source: "c1", Collection: "Invoice", Column: "CustomerId"},
		},
	})
	assert.NoError(t, err)
	assert.True(t, len(respRel.Related) >= 2)

	// semanticRelatedRowsHandler with valid body (line 202)
	validRowsReq := apicontract.RelatedRowsRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		LookupID: encodeLookupID("c1", "Customer", "CustomerId"),
		Value:    apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "1"},
	}
	bRows, _ := json.Marshal(validRowsReq)
	wRelValid := httptest.NewRecorder()
	rRelValid := httptest.NewRequest(http.MethodPost, "/datatug/semantic/related/rows", bytes.NewReader(bRows))
	semanticRelatedRowsHandler(wRelValid, rRelValid)

	// relatedProjectStoreFor error (line 237)
	origRelStore := relatedProjectStoreFor
	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) { return nil, errors.New("store fail") }
	_, err = computeSemanticRelatedRows(ctx, validRowsReq)
	assert.Error(t, err)
	relatedProjectStoreFor = origRelStore

	// runStructuredRelatedHook DeadlineExceeded and generic error (lines 272, 277)
	runStructuredRelatedHook = func(e *secureread.Executor, c context.Context, s string, q dal.StructuredQuery, v map[string]any) (secureread.Result, error) {
		return secureread.Result{}, context.DeadlineExceeded
	}
	_, err = computeSemanticRelatedRows(ctx, validRowsReq)
	assert.Error(t, err)

	runStructuredRelatedHook = func(e *secureread.Executor, c context.Context, s string, q dal.StructuredQuery, v map[string]any) (secureread.Result, error) {
		return secureread.Result{}, errors.New("other db error")
	}
	_, err = computeSemanticRelatedRows(ctx, validRowsReq)
	assert.Error(t, err)

	// Hook returning success so lines 287-310 run
	runStructuredRelatedHook = func(e *secureread.Executor, c context.Context, s string, q dal.StructuredQuery, v map[string]any) (secureread.Result, error) {
		return secureread.Result{}, nil
	}

	wRel := httptest.NewRecorder()
	rRel := httptest.NewRequest(http.MethodPost, "/datatug/semantic/related/rows", strings.NewReader("{invalid"))
	semanticRelatedRowsHandler(wRel, rRel)
	assert.Equal(t, http.StatusBadRequest, wRel.Code)

	origRecHook := toContractRecordsetHook
	toContractRecordsetHook = func(r secureread.Result) (apicontract.Recordset, error) {
		return apicontract.Recordset{}, errors.New("recordset fail")
	}
	defer func() { toContractRecordsetHook = origRecHook }()

	_, err = computeSemanticRelatedRows(ctx, validRowsReq)
	assert.Error(t, err)
	toContractRecordsetHook = origRecHook

	origValHook := relatedResultValidateHook
	relatedResultValidateHook = func(r *apicontract.Result) error {
		return errors.New("val fail")
	}
	defer func() { relatedResultValidateHook = origValHook }()

	_, err = computeSemanticRelatedRows(ctx, validRowsReq)
	assert.Error(t, err)
	relatedResultValidateHook = origValHook
}

func TestClose100_Compare_And_Sides(t *testing.T) {
	ctx := context.Background()

	pID := fmt.Sprintf("cmp_proj_%d", time.Now().UnixNano())
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	// 1. compare.go:152 appendRun == nil error
	leftRec := apicontract.CompareSideReceipt{
		Execution:   apicontract.ExecutionRef{StoreID: "s1", ProjectID: "p", ExecutionID: "e1"},
		ExecutedAt:  time.Now().UTC().Format(time.RFC3339),
		Limitations: []apicontract.Limitation{},
	}
	rightRec := apicontract.CompareSideReceipt{
		Execution:   apicontract.ExecutionRef{StoreID: "s1", ProjectID: "p", ExecutionID: "e2"},
		ExecutedAt:  time.Now().UTC().Format(time.RFC3339),
		Limitations: []apicontract.Limitation{},
	}
	_, err := computeCompareWith(ctx, apicontract.CompareRequest{
		QueryID:           "q1",
		Key:               []string{"k1"},
		Incident:          &apicontract.IncidentRef{StoreID: "s1", IncidentID: "INC-1"},
		MutationID:        "mut-1",
		SecurityContextID: api.SecurityContextID(),
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s1", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s1", Project: "p", Environment: "e"},
	}, compareDependencies{
		executeSide: func(ctx context.Context, req apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{
				recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "k1", Type: "string"}}, Rows: [][]apicontract.TypedValue{}},
				receipt:   leftRec,
			}, nil
		},
		appendRun: nil,
	})
	t.Logf("err1 nil appendRun: %v", err)
	assert.Error(t, err)

	// 1b. compare.go:156 appendRun error
	_, err = computeCompareWith(ctx, apicontract.CompareRequest{
		QueryID:           "q1",
		Key:               []string{"k1"},
		Incident:          &apicontract.IncidentRef{StoreID: "s1", IncidentID: "INC-1"},
		MutationID:        "mut-1",
		SecurityContextID: api.SecurityContextID(),
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s1", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s1", Project: "p", Environment: "e"},
	}, compareDependencies{
		executeSide: func(ctx context.Context, req apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{
				recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "k1", Type: "string"}}, Rows: [][]apicontract.TypedValue{}},
				receipt:   leftRec,
			}, nil
		},
		appendRun: func(ctx context.Context, req apicontract.CompareRequest, comparison incidents.ComparisonRef) error {
			return errors.New("append run boom")
		},
	})
	t.Logf("err1b appendRun err: %v", err)
	assert.Error(t, err)

	// 2. compare.go:161 compareResultValidateHook error
	origCompVal := compareResultValidateHook
	compareResultValidateHook = func(r *apicontract.CompareResult) error {
		return errors.New("compare val fail")
	}
	defer func() { compareResultValidateHook = origCompVal }()

	sideCall := 0
	_, err = computeCompareWith(ctx, apicontract.CompareRequest{
		QueryID:           "q1",
		Key:               []string{"k1"},
		SecurityContextID: api.SecurityContextID(),
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s1", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s1", Project: "p", Environment: "e"},
	}, compareDependencies{
		executeSide: func(ctx context.Context, req apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
			receipt := leftRec
			if sideCall > 0 {
				receipt = rightRec
			}
			sideCall++
			return compareSideData{
				recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "k1", Type: "string"}}, Rows: [][]apicontract.TypedValue{}},
				receipt:   receipt,
			}, nil
		},
	})
	t.Logf("err2: %v", err)
	assert.Error(t, err)

	// 3. compare_sides.go:98 loadCompareRecordSide error
	pID2 := fmt.Sprintf("cs_proj_%d", time.Now().UnixNano())
	dir2 := t.TempDir()
	filestore.SetProjectPath(pID2, dir2)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID2: dir2}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{pID2: dir2}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	defer func() { _ = api.CloseExecutionEvidence() }()

	// compare_sides.go:61 Normalize error
	_, err = executeCompareLiveSide(ctx, apicontract.CompareRequest{QueryID: "q1", Key: []string{"k1"}, SecurityContextID: api.SecurityContextID()}, apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideScope, StoreID: "s1", Project: pID2, Environment: "e",
	}, nil, []apicontract.BindingOriginEntry{{ParameterID: "p1"}, {ParameterID: "p1"}})
	assert.Error(t, err)

	// compare_sides.go:68 Execution == nil
	origRunOpt := compareComputeRunQueryWithOptionsHook
	defer func() { compareComputeRunQueryWithOptionsHook = origRunOpt }()
	compareComputeRunQueryWithOptionsHook = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	_, err = executeCompareSide(ctx, apicontract.CompareRequest{QueryID: "q1", Key: []string{"k1"}, SecurityContextID: api.SecurityContextID()}, apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideScope, StoreID: "s1", Project: pID2, Environment: "e",
	})
	assert.Error(t, err)

	// compare_sides.go:72 Execution.StoreID unknown
	compareComputeRunQueryWithOptionsHook = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{Execution: &apicontract.ExecutionRef{ProjectID: pID2, StoreID: "unknown_store", ExecutionID: "e1"}}, nil
	}
	_, err = executeCompareSide(ctx, apicontract.CompareRequest{QueryID: "q1", Key: []string{"k1"}, SecurityContextID: api.SecurityContextID()}, apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideScope, StoreID: "s1", Project: pID2, Environment: "e",
	})
	assert.Error(t, err)

	// compare_sides.go:76 store.Execution error (nonexistent execution)
	compareComputeRunQueryWithOptionsHook = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{Execution: &apicontract.ExecutionRef{ProjectID: pID2, StoreID: pID2, ExecutionID: "nonexistent"}}, nil
	}
	_, err = executeCompareSide(ctx, apicontract.CompareRequest{QueryID: "q1", Key: []string{"k1"}, SecurityContextID: api.SecurityContextID()}, apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideScope, StoreID: "s1", Project: pID2, Environment: "e",
	})
	assert.Error(t, err)

	// Put a real execution and snapshot into store
	evStore, err := api.ExecutionEvidenceStoreByID(pID2, pID2)
	require.NoError(t, err)
	recSet := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{{Type: apicontract.ValueTypeString, Str: "1"}}},
	}
	execRef := apicontract.ExecutionRef{StoreID: pID2, ProjectID: pID2, ExecutionID: "e_live_1"}
	execTime := time.Now().UTC()
	snapRef, _, err := evStore.PutSnapshot(ctx, execRef, recSet, execTime)
	require.NoError(t, err)
	isComp := true
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: execRef, Scope: apicontract.ExecutionRecordScope{StoreID: pID2, Project: pID2, Environment: "e"},
		QueryID: "q1", ExecutedAt: execTime.Format(time.RFC3339Nano), SnapshotRef: snapRef, ResultComplete: &isComp,
		Principal:         apicontract.ExecutionPrincipal{ID: "user1"},
		PolicyFingerprint: strings.Repeat("0", 64),
		ResultFingerprint: strings.Repeat("0", 64),
		Provenance:        apicontract.Provenance{Source: "s1", Collection: "c1", QueryID: "q1", Mode: apicontract.ProvenanceModeLive, ObservedAt: execTime.Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected},
	}))

	// compare_sides.go:98 compareSideReceiptValidateHook error
	origReceiptVal := compareSideReceiptValidateHook
	defer func() { compareSideReceiptValidateHook = origReceiptVal }()
	compareComputeRunQueryWithOptionsHook = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{Execution: &execRef, Recordset: recSet}, nil
	}
	compareSideReceiptValidateHook = func(r *apicontract.CompareSideReceipt) error { return errors.New("receipt fail") }
	_, err = executeCompareSide(ctx, apicontract.CompareRequest{QueryID: "q1", Key: []string{"k1"}, SecurityContextID: api.SecurityContextID()}, apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideScope, StoreID: "s1", Project: pID2, Environment: "e",
	})
	assert.Error(t, err)
	compareSideReceiptValidateHook = origReceiptVal

	// compare_sides.go:99 !reproducible
	execRefNoSnap := apicontract.ExecutionRef{StoreID: pID2, ProjectID: pID2, ExecutionID: "e_no_snap"}
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: execRefNoSnap, Scope: apicontract.ExecutionRecordScope{StoreID: pID2, Project: pID2, Environment: "e"},
		QueryID: "q1", ExecutedAt: execTime.Format(time.RFC3339Nano), ResultComplete: &isComp,
		Principal:         apicontract.ExecutionPrincipal{ID: "user1"},
		PolicyFingerprint: strings.Repeat("0", 64),
		ResultFingerprint: strings.Repeat("0", 64),
		Provenance:        apicontract.Provenance{Source: "s1", Collection: "c1", QueryID: "q1", Mode: apicontract.ProvenanceModeLive, ObservedAt: execTime.Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected},
	}))
	compareComputeRunQueryWithOptionsHook = func(ctx context.Context, req apicontract.ExecutionRequest, opts runQueryOptions) (apicontract.Result, error) {
		return apicontract.Result{Execution: &execRefNoSnap, Recordset: recSet}, nil
	}
	_, err = executeCompareSide(ctx, apicontract.CompareRequest{QueryID: "q1", Key: []string{"k1"}, SecurityContextID: api.SecurityContextID()}, apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideScope, StoreID: "s1", Project: pID2, Environment: "e",
	})
	assert.NoError(t, err)

	// compare_sides.go:133 loadCompareRecordSide nonexistent execution
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &apicontract.ExecutionRef{ProjectID: pID2, StoreID: pID2, ExecutionID: "nonexistent"},
	})
	assert.Error(t, err)

	// compare_sides.go:135 loadCompareRecordSide generic execution read error (canceled context)
	ctxCanc, cancel := context.WithCancel(ctx)
	cancel()
	_, err = loadCompareRecordSide(ctxCanc, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &execRef,
	})
	assert.Error(t, err)

	// compare_sides.go:153 compareSecureExecutorHook !ok
	origCompSec := compareSecureExecutorHook
	defer func() { compareSecureExecutorHook = origCompSec }()
	compareSecureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &execRef,
	})
	assert.Error(t, err)
	compareSecureExecutorHook = origCompSec

	// compare_sides.go:158 compareRunSnapshotHook access denied
	origRunSnap := compareRunSnapshotHook
	defer func() { compareRunSnapshotHook = origRunSnap }()
	compareRunSnapshotHook = func(e *secureread.Executor, ctx context.Context, col string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, secureread.ErrAccessDenied
	}
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &execRef,
	})
	assert.Error(t, err)

	// compare_sides.go:160 compareRunSnapshotHook generic error
	compareRunSnapshotHook = func(e *secureread.Executor, ctx context.Context, col string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, errors.New("snap fail")
	}
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &execRef,
	})
	assert.Error(t, err)

	// compare_sides.go:163 filtered.SnapshotRecordset == nil
	compareRunSnapshotHook = func(e *secureread.Executor, ctx context.Context, col string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: nil}, nil
	}
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &execRef,
	})
	assert.Error(t, err)

	// compare_sides.go:174 compareSideReceiptValidateHook error in loadCompareRecordSide
	compareRunSnapshotHook = func(e *secureread.Executor, ctx context.Context, col string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: &recSet}, nil
	}
	compareSideReceiptValidateHook = func(r *apicontract.CompareSideReceipt) error { return errors.New("receipt fail") }
	_, err = loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "q1"}, apicontract.CompareSideSpec{
		Kind:      apicontract.CompareSideRecord,
		Execution: &execRef,
	})
	assert.Error(t, err)
	compareSideReceiptValidateHook = origReceiptVal
	compareRunSnapshotHook = origRunSnap

	// 4. compare_incident.go
	_, scopes, _ := configureIncidentHTTPState(t, pID2)
	scopeInc := scopes[pID2]
	reqInc := apicontract.CompareRequest{
		QueryID: "q1", Key: []string{"k1"}, MutationID: "mut-1", SecurityContextID: api.SecurityContextID(),
		Incident: &apicontract.IncidentRef{StoreID: scopeInc.StoreID, IncidentID: "INC-1"},
		Left:     apicontract.CompareSideSpec{Execution: &apicontract.ExecutionRef{ProjectID: pID2, StoreID: pID2, ExecutionID: "e1"}},
		Right:    apicontract.CompareSideSpec{Execution: &apicontract.ExecutionRef{ProjectID: pID2, StoreID: pID2, ExecutionID: "e2"}},
	}

	// Projection hook error (line 110)
	origProjHook := compareIncidentStoreProjectionHook
	defer func() { compareIncidentStoreProjectionHook = origProjHook }()
	compareIncidentStoreProjectionHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, q *time.Time) (incidents.Incident, error) {
		return incidents.Incident{}, errors.New("proj err")
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)
	compareIncidentStoreProjectionHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, q *time.Time) (incidents.Incident, error) {
		return incidents.Incident{Ref: ref}, nil
	}

	// Events hook error (line 114)
	origEventsHook := compareIncidentStoreEventsHook
	defer func() { compareIncidentStoreEventsHook = origEventsHook }()
	compareIncidentStoreEventsHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
		return nil, errors.New("events err")
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)
	compareIncidentStoreEventsHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
		return nil, nil
	}

	// IncidentView hook error (line 27)
	origIncView := compareIncidentViewHook
	defer func() { compareIncidentViewHook = origIncView }()
	compareIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, errors.New("view err")
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)

	// view.Ref != req.Incident (line 30)
	compareIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{Ref: incidents.IncidentRef{StoreID: scopeInc.StoreID, IncidentID: "INC-DIFF"}}, incidents.ViewPolicy{}, nil
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)

	// events has matching mutationID: ApplyEventView !allowed (line 38)
	compareIncidentStoreEventsHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
		return []incidents.Event{{ID: "mut-1", Type: "other"}}, nil
	}
	compareIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{Ref: *reqInc.Incident}, incidents.ViewPolicy{WithheldEvents: map[string]bool{"mut-1": true}}, nil
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)

	// events has matching mutationID: comparisonFromEvent !ok (line 42)
	compareIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{Ref: *reqInc.Incident}, incidents.ViewPolicy{}, nil
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)
	compareIncidentStoreEventsHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
		return nil, nil
	}
	compareIncidentViewHook = origIncView

	// Actor hook error in preflight and append (lines 48, 69)
	origActorHook := compareSecureIncidentActorHook
	defer func() { compareSecureIncidentActorHook = origActorHook }()
	compareSecureIncidentActorHook = func(via string) (incidents.Actor, error) {
		return incidents.Actor{}, errors.New("actor fail")
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)
	err = appendCompareRun(ctx, reqInc, incidents.ComparisonRef{})
	assert.Error(t, err)
	// Now set a valid actor hook
	compareSecureIncidentActorHook = func(via string) (incidents.Actor, error) {
		return incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: via}, nil
	}

	// Draft hook error in preflight and append (lines 53, 73)
	origDraftHook := compareRunDraftHook
	defer func() { compareRunDraftHook = origDraftHook }()
	compareRunDraftHook = func(ref incidents.IncidentRef, actor incidents.Actor, key []string, comp incidents.ComparisonRef) (incidents.EventDraft, error) {
		return incidents.EventDraft{}, errors.New("draft fail")
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)
	err = appendCompareRun(ctx, reqInc, incidents.ComparisonRef{})
	assert.Error(t, err)
	compareRunDraftHook = origDraftHook

	compRef := incidents.ComparisonRef{
		Left:  incidents.ExecutionRef{StoreID: pID2, ProjectID: pID2, ExecutionID: "e1"},
		Right: incidents.ExecutionRef{StoreID: pID2, ProjectID: pID2, ExecutionID: "e2"},
	}

	// Authorize hook error in preflight and append (lines 57, 88)
	origAuthInc := compareAuthorizeIncidentEventHook
	defer func() { compareAuthorizeIncidentEventHook = origAuthInc }()
	compareAuthorizeIncidentEventHook = func(ctx context.Context, store incidents.APIStore, mutation incidents.Mutation) (uint64, error) {
		return 0, errors.New("auth fail")
	}
	_, err = preflightCompareIncident(ctx, reqInc)
	assert.Error(t, err)
	err = appendCompareRun(ctx, reqInc, compRef)
	assert.Error(t, err)
	compareAuthorizeIncidentEventHook = func(ctx context.Context, store incidents.APIStore, mutation incidents.Mutation) (uint64, error) {
		return 1, nil
	}

	// Append hook error (line 93)
	origAppendHook := compareIncidentStoreAppendHook
	defer func() { compareIncidentStoreAppendHook = origAppendHook }()
	compareIncidentStoreAppendHook = func(s incidents.APIStore, ctx context.Context, m incidents.Mutation) (incidents.AppendResult, error) {
		return incidents.AppendResult{}, errors.New("append fail")
	}
	err = appendCompareRun(ctx, reqInc, compRef)
	assert.Error(t, err)

	// Append hook returns invalid event (line 97)
	compareIncidentStoreAppendHook = func(s incidents.APIStore, ctx context.Context, m incidents.Mutation) (incidents.AppendResult, error) {
		return incidents.AppendResult{Event: incidents.Event{Type: "other"}}, nil
	}
	err = appendCompareRun(ctx, reqInc, compRef)
	assert.Error(t, err)

	// Append hook success
	actorValid, _ := compareSecureIncidentActorHook(incidents.ActorViaAPI)
	draftValid, _ := compareRunDraftHook(*reqInc.Incident, actorValid, reqInc.Key, compRef)
	compareAuthorizeIncidentEventHook = func(ctx context.Context, store incidents.APIStore, mutation incidents.Mutation) (uint64, error) {
		return 1, nil
	}
	compareIncidentStoreAppendHook = func(s incidents.APIStore, ctx context.Context, m incidents.Mutation) (incidents.AppendResult, error) {
		return incidents.AppendResult{Event: incidents.Event{Type: draftValid.Type, Assertion: draftValid.Assertion, Refs: draftValid.Refs, Payload: draftValid.Payload}}, nil
	}
	err = appendCompareRun(ctx, reqInc, compRef)
	assert.NoError(t, err)
	compareAuthorizeIncidentEventHook = origAuthInc
	compareIncidentStoreAppendHook = origAppendHook
}

func TestClose100_ExecutionRecording_And_Executions(t *testing.T) {
	ctx := context.Background()

	// 1. measureRecordset calls aggregateMeasurement (lines 284-285)
	mRes, err := measureRecordset(apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "integer"}},
		Rows:    [][]apicontract.TypedValue{{{Type: apicontract.ValueTypeInteger, Str: "42"}}},
	}, []apicontract.MeasurementProjection{
		{Aggregate: apicontract.MeasurementAggregateCount, Column: "c1"},
	}, nil, false)
	assert.NoError(t, err)
	assert.Len(t, mRes, 1)

	// 2. execution_recording.go rollback on validate error
	dir := t.TempDir()
	pID := fmt.Sprintf("rec_proj_%d", time.Now().UnixNano())
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{Principal: nil}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{pID: dir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	defer func() { _ = api.CloseExecutionEvidence() }()

	origRecVal := executionRecordValidateHook
	executionRecordValidateHook = func(r *apicontract.ExecutionRecord) error {
		return errors.New("rec val fail")
	}
	defer func() { executionRecordValidateHook = origRecVal }()

	res := apicontract.Result{
		Provenance: apicontract.Provenance{
			Source:           "s1",
			QueryID:          "q1",
			Mode:             apicontract.ProvenanceModeLive,
			ExecutionProfile: apicontract.ExecutionProfileProtected,
			ObservedAt:       time.Now().UTC().Format(time.RFC3339),
		},
		Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "c1", Type: "string"}}, Rows: [][]apicontract.TypedValue{}},
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1",
		QueryID: "q1", Snapshot: true,
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	executionRecordValidateHook = origRecVal

	// 3. requireIncidentHook not found error (line 101)
	origReqInc := requireIncidentHook
	defer func() { requireIncidentHook = origReqInc }()
	requireIncidentHook = func(s *executionstore.Store, ctx context.Context, ref apicontract.IncidentRef) error {
		return incidentstore.ErrIncidentNotFound
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1",
		QueryID: "q1", Incident: &apicontract.IncidentRef{StoreID: pID, IncidentID: "INC-1"},
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)

	// 3b. requireIncidentHook generic error (line 103)
	requireIncidentHook = func(s *executionstore.Store, ctx context.Context, ref apicontract.IncidentRef) error {
		return errors.New("incident hook generic fail")
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1",
		Incident: &apicontract.IncidentRef{StoreID: pID, IncidentID: "INC-1"},
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	requireIncidentHook = origReqInc

	// 4. newExecutionIDHook error (line 108)
	origNewID := newExecutionIDHook
	defer func() { newExecutionIDHook = origNewID }()
	newExecutionIDHook = func() (string, error) { return "", errors.New("id fail") }
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1",
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	newExecutionIDHook = origNewID

	// 5. fingerprintRecordsetHook error (line 113)
	origFP := fingerprintRecordsetHook
	defer func() { fingerprintRecordsetHook = origFP }()
	fingerprintRecordsetHook = func(r apicontract.Recordset) (string, error) { return "", errors.New("fp fail") }
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1",
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	fingerprintRecordsetHook = origFP

	// 6. snapshotRecordsetForStorageHook error (line 143)
	origSnapStore := snapshotRecordsetForStorageHook
	defer func() { snapshotRecordsetForStorageHook = origSnapStore }()
	snapshotRecordsetForStorageHook = func(p, s string, r apicontract.Recordset) (apicontract.Recordset, bool, error) {
		return apicontract.Recordset{}, false, errors.New("snap policy err")
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1", Snapshot: true,
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	snapshotRecordsetForStorageHook = origSnapStore

	// 7. putSnapshotHook error (line 148)
	origPutSnap := putSnapshotHook
	defer func() { putSnapshotHook = origPutSnap }()
	origSnapStore7 := snapshotRecordsetForStorageHook
	defer func() { snapshotRecordsetForStorageHook = origSnapStore7 }()
	snapshotRecordsetForStorageHook = func(project, source string, recordset apicontract.Recordset) (apicontract.Recordset, bool, error) {
		return recordset, true, nil
	}
	putSnapshotHook = func(s *executionstore.Store, ctx context.Context, ref apicontract.ExecutionRef, r apicontract.Recordset, at time.Time) (string, bool, error) {
		return "", false, errors.New("put snap err")
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1", Snapshot: true,
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	putSnapshotHook = origPutSnap
	snapshotRecordsetForStorageHook = origSnapStore7

	// 7b. executionRecordValidateHook error with SnapshotRef != "" (line 157)
	origValHook := executionRecordValidateHook
	defer func() { executionRecordValidateHook = origValHook }()
	snapshotRecordsetForStorageHook = func(project, source string, recordset apicontract.Recordset) (apicontract.Recordset, bool, error) {
		return recordset, true, nil
	}
	putSnapshotHook = func(s *executionstore.Store, ctx context.Context, ref apicontract.ExecutionRef, r apicontract.Recordset, at time.Time) (string, bool, error) {
		return "snap-to-rollback-val", true, nil
	}
	executionRecordValidateHook = func(r *apicontract.ExecutionRecord) error {
		return errors.New("validate err with snapshot")
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1", Snapshot: true,
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	executionRecordValidateHook = origValHook

	// 8. putExecutionHook error with SnapshotRef != "" and without (lines 162, 165)
	origPutExec := putExecutionHook
	defer func() { putExecutionHook = origPutExec }()
	putSnapshotHook = func(s *executionstore.Store, ctx context.Context, ref apicontract.ExecutionRef, r apicontract.Recordset, at time.Time) (string, bool, error) {
		return "snap-to-rollback-put", true, nil
	}
	putExecutionHook = func(s *executionstore.Store, ctx context.Context, r apicontract.ExecutionRecord) error {
		return errors.New("put exec err")
	}
	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1", Snapshot: true,
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)

	_, err = recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1", QueryID: "q1", Snapshot: false,
	}, nil, "", res, time.Now().UTC())
	assert.Error(t, err)
	putExecutionHook = origPutExec
	putSnapshotHook = origPutSnap
	snapshotRecordsetForStorageHook = origSnapStore
	reqSeries := apicontract.ExecutionSeriesRequest{
		Scope: apicontract.Scope{StoreID: "s1", Project: pID, Environment: "env1", SecurityContextID: api.SecurityContextID()},
		Partition: apicontract.ExecutionSeriesPartition{
			EvidenceStoreID: "s1", SourceScope: apicontract.ExecutionRecordScope{StoreID: "s1", Project: pID, Environment: "env1"},
			Source: "s1", PolicyFingerprint: api.SecurePolicyFingerprint(),
			Projection: apicontract.MeasurementProjection{Aggregate: apicontract.MeasurementAggregateCount},
		},
	}
	rSeries := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", nil)
	rSeries = rSeries.WithContext(ctx)

	reqSeriesBadStore := reqSeries
	reqSeriesBadStore.StoreID = "bad_store"
	_, err = computeExecutionSeries(rSeries, reqSeriesBadStore)
	assert.Error(t, err)

	origSeriesVal := executionSeriesValidateHook
	executionSeriesValidateHook = func(r *apicontract.ExecutionSeriesResponse) error {
		return errors.New("series val fail")
	}
	defer func() { executionSeriesValidateHook = origSeriesVal }()

	_, err = computeExecutionSeries(rSeries, reqSeries)
	assert.Error(t, err)

	rSnap := httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId=s1&project="+pID+"&environment=env1&id=nonexistent", nil)
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
}

func TestClose100_Incidents_Hooks(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha")
	scope := scopes["alpha"]

	origActor := incidentActorHook
	incidentActorHook = func(via string) (incidents.Actor, error) {
		return incidents.Actor{}, errors.New("actor denied")
	}
	defer func() { incidentActorHook = origActor }()

	reqCreate := incidentCreateFixture(scope, "mut-1", "Inc 1")
	bodyCreate, _ := json.Marshal(reqCreate)
	wCreate := httptest.NewRecorder()
	rCreate := httptest.NewRequest(http.MethodPost, "/datatug/incidents", bytes.NewReader(bodyCreate))
	router.ServeHTTP(wCreate, rCreate)
	assert.Equal(t, http.StatusForbidden, wCreate.Code)

	incidentActorHook = api.SecureIncidentActor
	origResolveProj := incidentResolveProjectHook
	incidentResolveProjectHook = func(projectID, env string) (incidents.ProjectRef, error) {
		return incidents.ProjectRef{}, errors.New("proj resolve fail")
	}
	defer func() { incidentResolveProjectHook = origResolveProj }()

	reqCreateBad := incidentCreateFixture(scope, "mut-2", "Inc 2")
	reqCreateBad.Projects = []incidents.ProjectRef{{ProjectID: "bad_p", Environment: "e"}}
	bodyCreateBadProj, _ := json.Marshal(reqCreateBad)
	wCreateBad := httptest.NewRecorder()
	rCreateBad := httptest.NewRequest(http.MethodPost, "/datatug/incidents", bytes.NewReader(bodyCreateBadProj))
	router.ServeHTTP(wCreateBad, rCreateBad)
	assert.Equal(t, http.StatusNotFound, wCreateBad.Code)

	incidentResolveProjectHook = api.ResolveIncidentProject
	createGood := incidentCreateFixture(scope, "mut-good", "Inc Good")
	bGood, _ := json.Marshal(createGood)
	wGood := httptest.NewRecorder()
	rGood := httptest.NewRequest(http.MethodPost, "/datatug/incidents", bytes.NewReader(bGood))
	router.ServeHTTP(wGood, rGood)
	require.Equal(t, http.StatusCreated, wGood.Code)

	origViewHook := incidentViewHook
	incidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, errors.New("view denied")
	}
	defer func() { incidentViewHook = origViewHook }()

	wSim := httptest.NewRecorder()
	rSim := httptest.NewRequest(http.MethodGet, "/datatug/incidents/INC-1/similar?storeId="+scope.StoreID+"&project="+scope.Project+"&environment="+scope.Environment+"&securityContextId="+api.SecurityContextID(), nil)
	router.ServeHTTP(wSim, rSim)
	assert.Equal(t, http.StatusInternalServerError, wSim.Code)
}

func TestClose100_CompareFacts_Deep(t *testing.T) {
	ctx := context.Background()
	pID := fmt.Sprintf("cf_proj_%d", time.Now().UnixNano())
	router, scopes, paths := configureIncidentHTTPState(t, pID)
	dir := paths[pID]
	filestore.SetProjectPath(pID, dir)
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{pID: dir}) }
	defer func() { storage.NewDatatugStore = origDatatugStore }()

	scope := scopes[pID]

	// 1. request.Incident == nil
	_, err := executeCompareFactsSide(ctx, apicontract.CompareRequest{Incident: nil}, apicontract.CompareSideSpec{})
	assert.Error(t, err)

	// 2. compareFactsIncidentViewHook error
	origView := compareFactsIncidentViewHook
	compareFactsIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, errors.New("view err")
	}
	defer func() { compareFactsIncidentViewHook = origView }()

	reqInc := incidentCreateFixture(scope, "mut-1", "Inc CF")
	b, _ := json.Marshal(reqInc)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/datatug/incidents", bytes.NewReader(b))
	router.ServeHTTP(w, r)

	var createResp apicontract.IncidentResponse
	_ = json.Unmarshal(w.Body.Bytes(), &createResp)
	incRef := createResp.Incident.Ref

	// 2a. compareIncidentState error (line 36)
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{
		Incident: &incRef,
		Left:     apicontract.CompareSideSpec{Kind: apicontract.CompareSideFacts, StoreID: scope.StoreID, Project: "p_a", Environment: scope.Environment},
		Right:    apicontract.CompareSideSpec{Kind: apicontract.CompareSideFacts, StoreID: scope.StoreID, Project: "p_b", Environment: scope.Environment},
	}, apicontract.CompareSideSpec{Kind: apicontract.CompareSideFacts, StoreID: scope.StoreID, Project: scope.Project, Environment: scope.Environment})
	assert.Error(t, err)

	// 2b. visibleFactCohort error (line 44)
	compareFactsIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, nil
	}
	sideSpec := apicontract.CompareSideSpec{Kind: apicontract.CompareSideFacts, StoreID: scope.StoreID, Project: scope.Project, Environment: scope.Environment}
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{
		Incident: &incRef, Left: sideSpec, Right: sideSpec,
	}, sideSpec)
	assert.Error(t, err)

	// 3. proveNativeFactsBindingHook error (line 48)
	validView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				investigation.VisibleFact(investigation.Fact{
					ID: "f1", Enabled: true, Role: investigation.FactRoleAffected,
					Layer:  investigation.FactLayerCanonical,
					Scope:  &investigation.ProjectScope{StoreID: scope.StoreID, ProjectID: scope.Project, Environment: scope.Environment},
					Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("1"),
				}),
			},
		},
	}
	compareFactsIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return validView, incidents.ViewPolicy{}, nil
	}
	origProve := proveNativeFactsBindingHook
	proveNativeFactsBindingHook = func(ctx context.Context, queryID string, side apicontract.CompareSideSpec, entity, field string) (string, error) {
		return "", errors.New("prove fail")
	}
	defer func() { proveNativeFactsBindingHook = origProve }()

	sideSpec = apicontract.CompareSideSpec{Kind: apicontract.CompareSideFacts, StoreID: scope.StoreID, Project: scope.Project, Environment: scope.Environment}
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{
		Incident: &incRef,
		Left:     sideSpec, Right: sideSpec,
	}, sideSpec)
	assert.Error(t, err)

	// 3b. NormalizeTypedValueSet error (line 50)
	invalidCohortView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				investigation.VisibleFact(investigation.Fact{
					ID: "f1", Enabled: true, Role: investigation.FactRoleAffected,
					Layer:  investigation.FactLayerCanonical,
					Scope:  &investigation.ProjectScope{StoreID: scope.StoreID, ProjectID: scope.Project, Environment: scope.Environment},
					Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("1"),
				}),
				investigation.VisibleFact(investigation.Fact{
					ID: "f2", Enabled: true, Role: investigation.FactRoleAffected,
					Layer:  investigation.FactLayerCanonical,
					Scope:  &investigation.ProjectScope{StoreID: scope.StoreID, ProjectID: scope.Project, Environment: scope.Environment},
					Entity: "Customer", Field: "ID", Value: investigation.NewStringValue("not-an-int"),
				}),
			},
		},
	}
	compareFactsIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return invalidCohortView, incidents.ViewPolicy{}, nil
	}
	proveNativeFactsBindingHook = func(ctx context.Context, queryID string, side apicontract.CompareSideSpec, entity, field string) (string, error) {
		return "param1", nil
	}
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{
		Incident: &incRef,
		Left:     sideSpec, Right: sideSpec,
	}, sideSpec)
	assert.Error(t, err)

	// 2c. compareFactsIncidentViewHook error (line 40)
	compareFactsIncidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, errors.New("view err")
	}
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{
		Incident: &incRef, Left: sideSpec, Right: sideSpec,
	}, sideSpec)
	assert.Error(t, err)

	// 4. proveNativeFactsBinding direct branches
	compareFactsIncidentViewHook = origView
	proveNativeFactsBindingHook = proveNativeFactsBinding

	qDir := filepath.Join(dir, "queries")
	_ = os.MkdirAll(qDir, 0755)
	dtqlDef := `{"id":"dtql_q","type":"DTQL","parameters":[{"id":"p1","type":"integer","isMultiValue":true,"meta":{"entity":"Customer","field":"ID"}}]}`
	_ = os.WriteFile(filepath.Join(qDir, "dtql_q.query.json"), []byte(dtqlDef), 0644)
	_ = os.WriteFile(filepath.Join(qDir, "dtql_q.query.yaml"), []byte("from: {name: customers}\nwhere: {field: ID, operator: In, right: {parameter: p1}}\n"), 0644)

	dtqlSingleDef := `{"id":"dtql_single","type":"DTQL","parameters":[{"id":"p2","type":"integer","meta":{"entity":"Customer","field":"ID"}}]}`
	_ = os.WriteFile(filepath.Join(qDir, "dtql_single.query.json"), []byte(dtqlSingleDef), 0644)
	_ = os.WriteFile(filepath.Join(qDir, "dtql_single.query.yaml"), []byte("from: {name: customers}\n"), 0644)

	resolvedStore, errStore := api.ResolveStoreID("", pID)
	t.Logf("resolvedStore: %q, err: %v", resolvedStore, errStore)
	sideP := apicontract.CompareSideSpec{Project: pID, Environment: scope.Environment, StoreID: resolvedStore}

	// 4a. matchingCohortParameter error (line 118)
	_, err = proveNativeFactsBinding(ctx, "dtql_q", sideP, "WrongEntity", "WrongField")
	assert.Error(t, err)

	// 4a1. matchingCohortParameter single value error (line 157)
	_, err = proveNativeFactsBinding(ctx, "dtql_single", sideP, "Customer", "ID")
	assert.Error(t, err)

	// 4b. resolveExecutionSourceHookCompare error (line 126)
	origResolve := resolveExecutionSourceHookCompare
	resolveExecutionSourceHookCompare = func(c context.Context, p datatug.ProjectStore, pd string, r apicontract.ExecutionRequest, q *datatug.QueryDef) (api.ResolvedSource, error) {
		return api.ResolvedSource{}, errors.New("res err")
	}
	defer func() { resolveExecutionSourceHookCompare = origResolve }()
	_, err = proveNativeFactsBinding(ctx, "dtql_q", sideP, "Customer", "ID")
	assert.Error(t, err)

	// 4c. non-sqlite/ingitdb source (line 131)
	resolveExecutionSourceHookCompare = func(c context.Context, p datatug.ProjectStore, pd string, r apicontract.ExecutionRequest, q *datatug.QueryDef) (api.ResolvedSource, error) {
		return api.ResolvedSource{Kind: api.SourceKindHTTP, URL: "http://test.local"}, nil
	}
	_, err = proveNativeFactsBinding(ctx, "dtql_q", sideP, "Customer", "ID")
	assert.Error(t, err)

	// 4d. executionQueryDocumentHookCompare error (line 135)
	resolveExecutionSourceHookCompare = func(c context.Context, p datatug.ProjectStore, pd string, r apicontract.ExecutionRequest, q *datatug.QueryDef) (api.ResolvedSource, error) {
		return api.ResolvedSource{Kind: api.SourceKindSQL, URL: "sqlite://test.db"}, nil
	}
	origDoc := executionQueryDocumentHookCompare
	executionQueryDocumentHookCompare = func(projID, qID string, q *datatug.QueryDef, rev string) (string, error) {
		return "", errors.New("doc err")
	}
	defer func() { executionQueryDocumentHookCompare = origDoc }()
	_, err = proveNativeFactsBinding(ctx, "dtql_q", sideP, "Customer", "ID")
	assert.Error(t, err)

	// 4e. dtql.Deserialize error (line 139)
	executionQueryDocumentHookCompare = func(projID, qID string, q *datatug.QueryDef, rev string) (string, error) {
		return "invalid: [yaml", nil
	}
	_, err = proveNativeFactsBinding(ctx, "dtql_q", sideP, "Customer", "ID")
	assert.Error(t, err)
}

func TestClose100_ExecRunQuery_Deep(t *testing.T) {
	ctx := context.Background()
	pID := fmt.Sprintf("erq_proj_%d", time.Now().UnixNano())
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	fStore, _ := filestore.NewStore("files", map[string]string{pID: dir})
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return fStore, nil }
	defer func() { storage.NewDatatugStore = origDatatugStore }()
	api.ConfigureSecureSession(secureread.Session{Principal: nil}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	projJSON := fmt.Sprintf(`{"id":%q,"access":"shared","environments":[{"id":"e"}]}`, pID)
	_ = os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(projJSON), 0644)
	catDir := filepath.Join(dir, "environments", "e", "catalogs")
	_ = os.MkdirAll(catDir, 0755)
	catJSON := `{"id":"c1","driver":"sqlite","path":"s1.db"}`
	_ = os.WriteFile(filepath.Join(catDir, "c1.db.json"), []byte(catJSON), 0644)

	qDir := filepath.Join(dir, "queries")
	_ = os.MkdirAll(qDir, 0755)
	sqlQ := `{"id":"sql_q","type":"SQL"}`
	_ = os.WriteFile(filepath.Join(qDir, "sql_q.query.json"), []byte(sqlQ), 0644)
	_ = os.WriteFile(filepath.Join(qDir, "sql_q.query.sql"), []byte("SELECT 1;"), 0644)

	reqBase := apicontract.ExecutionRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		QueryID: "sql_q", Mode: apicontract.ProvenanceModeLive,
	}

	// 1. execRunProjectStoreFor error
	origExecProjStore := execRunProjectStoreFor
	execRunProjectStoreFor = func(projectID string) (datatug.ProjectStore, error) {
		return nil, errors.New("store err")
	}
	_, err := computeRunQuery(ctx, reqBase)
	assert.Error(t, err)
	execRunProjectStoreFor = origExecProjStore

	// 2. executionQueryDocumentHook error
	origDocHook := executionQueryDocumentHook
	executionQueryDocumentHook = func(projectID, queryID string, queryDef *datatug.QueryDef, queryRevision string) (string, error) {
		return "", errors.New("doc err")
	}
	_, err = computeRunQuery(ctx, reqBase)
	assert.Error(t, err)
	executionQueryDocumentHook = origDocHook

	// 3. runNativeSQLHook branches: DeadlineExceeded, ErrOpaqueSQLNotGranted, generic error
	origSQLHook := runNativeSQLHook
	runNativeSQLHook = func(e *secureread.Executor, c context.Context, s, t string, a ...dal.QueryArg) (secureread.Result, error) {
		return secureread.Result{}, context.DeadlineExceeded
	}
	_, err = computeRunQuery(ctx, reqBase)
	assert.Error(t, err)

	runNativeSQLHook = func(e *secureread.Executor, c context.Context, s, t string, a ...dal.QueryArg) (secureread.Result, error) {
		return secureread.Result{}, secureread.ErrOpaqueSQLNotGranted
	}
	_, err = computeRunQuery(ctx, reqBase)
	assert.Error(t, err)

	runNativeSQLHook = func(e *secureread.Executor, c context.Context, s, t string, a ...dal.QueryArg) (secureread.Result, error) {
		return secureread.Result{}, secureread.ErrAccessDenied
	}
	_, err = computeRunQuery(ctx, reqBase)
	assert.Error(t, err)

	runNativeSQLHook = origSQLHook

	// 4. recordQueryExecutionHook error
	origRecHook := recordQueryExecutionHook
	recordQueryExecutionHook = func(c context.Context, r apicontract.ExecutionRequest, q *datatug.QueryDef, rev string, res apicontract.Result, s time.Time) (apicontract.ExecutionRef, error) {
		return apicontract.ExecutionRef{}, errors.New("record fail")
	}
	defer func() { recordQueryExecutionHook = origRecHook }()

	reqRecord := reqBase
	reqRecord.Record = true
	_, err = computeRunQuery(ctx, reqRecord)
	assert.Error(t, err)
	recordQueryExecutionHook = origRecHook

	// 5. req.DTQL != "" with resolveSource error
	reqDTQL := apicontract.ExecutionRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Source: "unknown_src", DTQL: "from: {name: t}\n", Mode: apicontract.ProvenanceModeLive,
	}
	_, err = computeRunQuery(ctx, reqDTQL)
	assert.Error(t, err)

	// 6. candidateTargets multiple eligible targets -> TARGET_REQUIRED (line 475)
	catJSON2 := `{"id":"c2","driver":"sqlite","path":"s2.db"}`
	_ = os.WriteFile(filepath.Join(catDir, "c2.db.json"), []byte(catJSON2), 0644)
	_, err = computeRunQuery(ctx, reqBase)
	assert.Error(t, err)
}

func validTestExecutionRecord(pID, execID string) apicontract.ExecutionRecord {
	bTrue := true
	return apicontract.ExecutionRecord{
		Ref:               apicontract.ExecutionRef{StoreID: pID, ProjectID: pID, ExecutionID: execID},
		Scope:             apicontract.ExecutionRecordScope{StoreID: pID, Project: pID, Environment: "e"},
		Principal:         apicontract.ExecutionPrincipal{ID: "test-user"},
		PolicyFingerprint: strings.Repeat("a", 64),
		ResultFingerprint: strings.Repeat("b", 64),
		QueryID:           "q1",
		ExecutedAt:        time.Now().UTC().Format(time.RFC3339),
		ResultComplete:    &bTrue,
		Provenance: apicontract.Provenance{
			Source:           pID,
			QueryID:          "q1",
			Mode:             apicontract.ProvenanceModeLive,
			ObservedAt:       time.Now().UTC().Format(time.RFC3339),
			ExecutionProfile: apicontract.ExecutionProfileProtected,
		},
	}
}

func TestClose100_Executions_Deep(t *testing.T) {
	ctx := context.Background()
	pID := fmt.Sprintf("ex_proj_%d", time.Now().UnixNano())
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{pID: dir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	defer func() { _ = api.CloseExecutionEvidence() }()

	store, err := api.ExecutionEvidenceStoreByID(pID, pID)
	require.NoError(t, err)
	require.NotNil(t, store)

	// write valid execution record
	rec1 := validTestExecutionRecord(pID, "exec-1")
	require.NoError(t, store.PutExecution(ctx, rec1))

	// 1. authorizeExecutionRecordContextFn denial and error
	origAuthFn := authorizeExecutionRecordContextFn
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return secureread.ErrSnapshotPolicyUnexpressible
	}
	r := httptest.NewRequest(http.MethodGet, "/datatug/executions/exec-1?storeId="+pID+"&project="+pID+"&environment=e", nil)
	r = r.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-1"}}))

	_, err = computeExecutionShow(r)
	assert.Error(t, err)

	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return errors.New("generic auth boom")
	}
	_, err = computeExecutionShow(r)
	assert.Error(t, err)
	authorizeExecutionRecordContextFn = origAuthFn

	// 2. snapshot execution with runSnapshotExecutionHook errors
	execRefSnap := apicontract.ExecutionRef{StoreID: pID, ProjectID: pID, ExecutionID: "exec-snap"}
	snapRef, stored, snapErr := store.PutSnapshot(ctx, execRefSnap, apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}}, Rows: [][]apicontract.TypedValue{},
	}, time.Now().UTC())
	require.NoError(t, snapErr)
	if stored {
		recSnap := validTestExecutionRecord(pID, "exec-snap")
		recSnap.SnapshotRef = snapRef
		require.NoError(t, store.PutExecution(ctx, recSnap))
	}
	rSnap := httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId="+pID+"&project="+pID+"&environment=e", nil)
	rSnap = rSnap.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-snap"}}))

	origRunSnap := runSnapshotExecutionHook
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, secureread.ErrSnapshotPolicyUnexpressible
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)

	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, errors.New("snap run err")
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)

	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: nil}, nil
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)

	runSnapshotExecutionHook = origRunSnap

	// snapshotReadValidateHook error
	origSnapVal := snapshotReadValidateHook
	snapshotReadValidateHook = func(s *apicontract.SnapshotReadResponse) error {
		return errors.New("snap val fail")
	}
	defer func() { snapshotReadValidateHook = origSnapVal }()

	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
	snapshotReadValidateHook = origSnapVal

	// 3. executionSnapshotLimit error (limit=invalid)
	rSnapBadLimit := httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId="+pID+"&project="+pID+"&environment=e&limit=abc", nil)
	rSnapBadLimit = rSnapBadLimit.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-1"}}))
	_, err = computeExecutionSnapshot(rSnapBadLimit)
	assert.Error(t, err)

	// 4. validateScope error in show and snapshot
	rBadScope := httptest.NewRequest(http.MethodGet, "/datatug/executions/exec-1?storeId=..&project="+pID+"&environment=e", nil)
	rBadScope = rBadScope.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-1"}}))
	_, err = computeExecutionShow(rBadScope)
	assert.Error(t, err)
	_, err = computeExecutionSnapshot(rBadScope)
	assert.Error(t, err)

	// 5. store.Execution error in show
	rNonExistent := httptest.NewRequest(http.MethodGet, "/datatug/executions/exec-nonexistent?storeId="+pID+"&project="+pID+"&environment=e", nil)
	rNonExistent = rNonExistent.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-nonexistent"}}))
	_, err = computeExecutionShow(rNonExistent)
	assert.Error(t, err)

	// 6. snapshot.SnapshotState.Availability != Available
	execRefExpired := apicontract.ExecutionRef{StoreID: pID, ProjectID: pID, ExecutionID: "exec-exp"}
	snapRefExp, storedExp, _ := store.PutSnapshot(ctx, execRefExpired, apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}}, Rows: [][]apicontract.TypedValue{},
	}, time.Now().UTC().Add(-40*24*time.Hour))
	if storedExp {
		recExp := validTestExecutionRecord(pID, "exec-exp")
		recExp.SnapshotRef = snapRefExp
		require.NoError(t, store.PutExecution(ctx, recExp))
	}
	rSnapExp := httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID(), nil)
	rSnapExp = rSnapExp.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-exp"}}))

	origAuthFnExp := authorizeExecutionRecordContextFn
	defer func() { authorizeExecutionRecordContextFn = origAuthFnExp }()
	// denial
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return secureread.ErrSnapshotPolicyUnexpressible
	}
	_, err = computeExecutionSnapshot(rSnapExp)
	assert.Error(t, err)
	// generic error
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return errors.New("auth generic")
	}
	_, err = computeExecutionSnapshot(rSnapExp)
	assert.Error(t, err)
	// success with terminal state
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return nil
	}
	resExp, err := computeExecutionSnapshot(rSnapExp)
	assert.NoError(t, err)
	assert.NotEqual(t, apicontract.SnapshotAvailable, resExp.SnapshotState.Availability)
	authorizeExecutionRecordContextFn = origAuthFnExp

	// 7. computeExecutionList auth branches and executionBrief error
	rListReq := apicontract.ExecutionListRequest{
		Scope: apicontract.Scope{StoreID: pID, Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID()},
	}
	rListHTTP := httptest.NewRequest(http.MethodGet, "/datatug/executions?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID(), nil)
	rListHTTP = rListHTTP.WithContext(ctx)

	// auth denial continue
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return secureread.ErrSnapshotPolicyUnexpressible
	}
	listResp, err := computeExecutionList(rListHTTP, rListReq)
	assert.NoError(t, err)
	assert.Empty(t, listResp.Executions)

	// auth generic error
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return errors.New("list auth boom")
	}
	_, err = computeExecutionList(rListHTTP, rListReq)
	assert.Error(t, err)
	authorizeExecutionRecordContextFn = origAuthFnExp

	// 8. computeExecutionSnapshot row truncation with limit
	origRunSnap8 := runSnapshotExecutionHook
	defer func() { runSnapshotExecutionHook = origRunSnap8 }()
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: &snap}, nil
	}
	snapRefRows, storedRows, snapRowsErr := store.PutSnapshot(ctx, apicontract.ExecutionRef{StoreID: pID, ProjectID: pID, ExecutionID: "exec-trunc"}, apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}},
		Rows: [][]apicontract.TypedValue{
			{apicontract.NewStringValue("r1")},
			{apicontract.NewStringValue("r2")},
			{apicontract.NewStringValue("r3")},
		},
	}, time.Now().UTC())
	require.NoError(t, snapRowsErr)
	if storedRows {
		recTrunc := validTestExecutionRecord(pID, "exec-trunc")
		recTrunc.SnapshotRef = snapRefRows
		require.NoError(t, store.PutExecution(ctx, recTrunc))
	}
	rSnapLimit := httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID()+"&limit=1", nil)
	rSnapLimit = rSnapLimit.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-trunc"}}))
	snapTruncResp, err := computeExecutionSnapshot(rSnapLimit)
	assert.NoError(t, err)
	assert.True(t, snapTruncResp.Truncated)
	if assert.NotNil(t, snapTruncResp.Recordset) {
		assert.Len(t, snapTruncResp.Recordset.Rows, 1)
	}
	runSnapshotExecutionHook = origRunSnap8

	// 9. executionSeriesHandler POST tests
	wSeries := httptest.NewRecorder()
	rSeriesBadBody := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", strings.NewReader("bad json"))
	executionSeriesHandler(wSeries, rSeriesBadBody)
	assert.Equal(t, http.StatusBadRequest, wSeries.Code)

	seriesReq := apicontract.ExecutionSeriesRequest{
		Scope: apicontract.Scope{StoreID: pID, Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID()},
		Partition: apicontract.ExecutionSeriesPartition{
			EvidenceStoreID: pID, SourceScope: apicontract.ExecutionRecordScope{StoreID: pID, Project: pID, Environment: "e"},
			Source: pID, PolicyFingerprint: api.SecurePolicyFingerprint(), QueryID: "q1", QueryRevision: "rev-1",
			BindingsApplied: []apicontract.Binding{},
			Projection:      apicontract.MeasurementProjection{ID: "p1", Aggregate: apicontract.MeasurementAggregateRowCount},
		},
	}
	bodySeries, _ := json.Marshal(seriesReq)
	rSeriesGood := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", bytes.NewReader(bodySeries))
	rSeriesGood = rSeriesGood.WithContext(ctx)
	wSeriesGood := httptest.NewRecorder()
	executionSeriesHandler(wSeriesGood, rSeriesGood)
	assert.Equal(t, http.StatusOK, wSeriesGood.Code)

	// 10. computeExecutionSeriesBounded store not found
	badStoreReq := seriesReq
	badStoreReq.StoreID = "nonexistent_store"
	_, err = computeExecutionSeriesBounded(rSeriesGood, badStoreReq, 10)
	assert.Error(t, err)

	// 11. computeExecutionSeriesBounded: auth denial, auth generic error, partition mismatch, projection missing, sort points
	pCount := apicontract.MeasurementProjection{ID: "p1", Aggregate: apicontract.MeasurementAggregateRowCount}
	recS1 := validTestExecutionRecord(pID, "exec-s1")
	recS1.ExecutedAt = "2026-01-02T10:00:00Z"
	recS1.QueryRevision = "rev-1"
	recS1.RowCount = 10
	recS1.BindingsApplied = []apicontract.Binding{}
	recS1.PolicyFingerprint = api.SecurePolicyFingerprint()
	recS1.Measurements = []apicontract.ScalarMeasurement{{
		Projection:   pCount,
		Completeness: apicontract.MeasurementComplete,
		Value:        func() *apicontract.TypedValue { v := apicontract.NewIntegerValue("10"); return &v }(),
	}}
	require.NoError(t, store.PutExecution(ctx, recS1))

	recS2 := validTestExecutionRecord(pID, "exec-s2")
	recS2.ExecutedAt = "2026-01-01T10:00:00Z"
	recS2.QueryRevision = "rev-1"
	recS2.RowCount = 20
	recS2.BindingsApplied = []apicontract.Binding{}
	recS2.PolicyFingerprint = api.SecurePolicyFingerprint()
	recS2.Measurements = []apicontract.ScalarMeasurement{{
		Projection:   pCount,
		Completeness: apicontract.MeasurementComplete,
		Value:        func() *apicontract.TypedValue { v := apicontract.NewIntegerValue("20"); return &v }(),
	}}
	require.NoError(t, store.PutExecution(ctx, recS2))

	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return nil
	}
	sortSeriesReq := seriesReq
	sortResp, err := computeExecutionSeriesBounded(rSeriesGood, sortSeriesReq, 10)
	assert.NoError(t, err)
	if assert.Len(t, sortResp.Points, 2) {
		assert.Equal(t, "2026-01-01T10:00:00Z", sortResp.Points[0].ExecutedAt)
		assert.Equal(t, "2026-01-02T10:00:00Z", sortResp.Points[1].ExecutedAt)
	}

	// Series: partition projection not found -> Omitted = true
	missingProjReq := seriesReq
	missingProjReq.Partition.Projection = apicontract.MeasurementProjection{ID: "other", Aggregate: apicontract.MeasurementAggregateRowCount}
	missingResp, err := computeExecutionSeriesBounded(rSeriesGood, missingProjReq, 10)
	assert.NoError(t, err)
	assert.True(t, missingResp.Omitted)

	// Series: auth denial -> Omitted = true
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return secureread.ErrSnapshotPolicyUnexpressible
	}
	denialResp, err := computeExecutionSeriesBounded(rSeriesGood, sortSeriesReq, 10)
	assert.NoError(t, err)
	assert.True(t, denialResp.Omitted)

	// Series: auth generic error -> error
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return errors.New("series auth error")
	}
	_, err = computeExecutionSeriesBounded(rSeriesGood, sortSeriesReq, 10)
	assert.Error(t, err)
	authorizeExecutionRecordContextFn = origAuthFnExp

	// Series: executionSeriesValidateHook error
	origSeriesVal := executionSeriesValidateHook
	executionSeriesValidateHook = func(s *apicontract.ExecutionSeriesResponse) error {
		return errors.New("series val fail")
	}
	defer func() { executionSeriesValidateHook = origSeriesVal }()
	_, err = computeExecutionSeriesBounded(rSeriesGood, sortSeriesReq, 10)
	assert.Error(t, err)
	executionSeriesValidateHook = origSeriesVal

	// 12. authorizeExecutionRecordContext without SecureExecutor (line 351)
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	err = authorizeExecutionRecordContext(ctx, apicontract.ExecutionRecord{})
	assert.Error(t, err)

	// 13. authorizeExecutionRecordContext: missing column and rows filtered
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	origRunSnap2 := runSnapshotExecutionHook
	defer func() { runSnapshotExecutionHook = origRunSnap2 }()
	// missing column
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{
			SnapshotRecordset: &apicontract.Recordset{Columns: []apicontract.Column{{Name: "other_col"}}},
		}, nil
	}
	err = authorizeExecutionRecordContext(ctx, apicontract.ExecutionRecord{
		AuthorizedFields: []apicontract.FieldAccessRef{{Column: "c1"}},
	})
	assert.ErrorIs(t, err, secureread.ErrSnapshotPolicyUnexpressible)

	// rows filtered limitation
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{
			SnapshotRecordset: &apicontract.Recordset{Columns: []apicontract.Column{{Name: "c1"}}},
			Limitations:       []secureread.Limitation{{Kind: secureread.LimitationRowsFiltered}},
		}, nil
	}
	err = authorizeExecutionRecordContext(ctx, apicontract.ExecutionRecord{
		AuthorizedFields: []apicontract.FieldAccessRef{{Column: "c1"}},
	})
	assert.ErrorIs(t, err, secureread.ErrSnapshotPolicyUnexpressible)
	// 14. executions.go line 82: executionStoreExecutionsHook error
	origExecsHook := executionStoreExecutionsHook
	defer func() { executionStoreExecutionsHook = origExecsHook }()
	executionStoreExecutionsHook = func(s *executionstore.Store, c context.Context) ([]apicontract.ExecutionRecord, error) {
		return nil, errors.New("execs err")
	}
	rList := httptest.NewRequest(http.MethodGet, "/datatug/executions?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID(), nil)
	rList = rList.WithContext(ctx)
	listReq, _ := executionListRequest(rList)
	_, err = computeExecutionList(rList, listReq)
	assert.Error(t, err)
	executionStoreExecutionsHook = origExecsHook

	// 15. executions.go lines 109 & 399: executionStoreStateHook error
	origStateHook := executionStoreStateHook
	defer func() { executionStoreStateHook = origStateHook }()
	executionStoreStateHook = func(s *executionstore.Store, c context.Context, sRef string) (*apicontract.SnapshotState, error) {
		return nil, errors.New("state fail")
	}
	recWithSnap := validTestExecutionRecord(pID, "rec-state-err")
	recWithSnap.SnapshotRef = "snap-ref-1"
	origExecsHook2 := executionStoreExecutionsHook
	executionStoreExecutionsHook = func(s *executionstore.Store, c context.Context) ([]apicontract.ExecutionRecord, error) {
		return []apicontract.ExecutionRecord{recWithSnap}, nil
	}
	origAuthBrief := authorizeExecutionRecordContextFn
	defer func() { authorizeExecutionRecordContextFn = origAuthBrief }()
	authorizeExecutionRecordContextFn = func(ctx context.Context, record apicontract.ExecutionRecord) error {
		return nil
	}
	_, err = computeExecutionList(rList, listReq)
	assert.Error(t, err)
	authorizeExecutionRecordContextFn = origAuthBrief
	executionStoreExecutionsHook = origExecsHook2
	executionStoreStateHook = origStateHook

	// 16. executions.go line 138: computeExecutionShow executionStoreExecutionHook error
	origExecHook := executionStoreExecutionHook
	defer func() { executionStoreExecutionHook = origExecHook }()
	executionStoreExecutionHook = func(s *executionstore.Store, c context.Context, ref apicontract.ExecutionRef) (apicontract.ExecutionRecord, error) {
		return apicontract.ExecutionRecord{}, errors.New("get exec fail")
	}
	rShow := httptest.NewRequest(http.MethodGet, "/datatug/executions/show?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID(), nil)
	rShow = rShow.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "e1"}}))
	_, err = computeExecutionShow(rShow)
	assert.Error(t, err)

	// 17. executions.go lines 145 & 147: computeExecutionShow authorize error
	executionStoreExecutionHook = func(s *executionstore.Store, c context.Context, ref apicontract.ExecutionRef) (apicontract.ExecutionRecord, error) {
		return recWithSnap, nil
	}
	origAuthFnShow := authorizeExecutionRecordContextFn
	defer func() { authorizeExecutionRecordContextFn = origAuthFnShow }()
	// line 145: policy denial
	authorizeExecutionRecordContextFn = func(c context.Context, record apicontract.ExecutionRecord) error {
		return secureread.ErrAccessDenied
	}
	_, err = computeExecutionShow(rShow)
	assert.Error(t, err)
	// line 147: generic error
	authorizeExecutionRecordContextFn = func(c context.Context, record apicontract.ExecutionRecord) error {
		return errors.New("auth show fail")
	}
	_, err = computeExecutionShow(rShow)
	assert.Error(t, err)
	authorizeExecutionRecordContextFn = origAuthFnShow
	executionStoreExecutionHook = origExecHook

	// 18. executions.go line 180: computeExecutionSnapshot executionStoreSnapshotHook error
	rSnap = httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID(), nil)
	rSnap = rSnap.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-trunc"}}))
	origSnapHook := executionStoreSnapshotHook
	defer func() { executionStoreSnapshotHook = origSnapHook }()
	executionStoreSnapshotHook = func(s *executionstore.Store, c context.Context, ref apicontract.ExecutionRef, sRef string) (apicontract.SnapshotReadResponse, error) {
		return apicontract.SnapshotReadResponse{}, errors.New("snap read err")
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
	executionStoreSnapshotHook = origSnapHook

	// 19. executions.go line 199: computeExecutionSnapshot record.ResultComplete == nil
	// and line 206: secureExecutorExecutionsHook returning false
	origSecExec := secureExecutorExecutionsHook
	defer func() { secureExecutorExecutionsHook = origSecExec }()
	secureExecutorExecutionsHook = func() (*secureread.Executor, bool) { return nil, false }
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)

	// line 351: authorizeExecutionRecordContext secureExecutorExecutionsHook returning false
	err = authorizeExecutionRecordContext(ctx, apicontract.ExecutionRecord{})
	assert.Error(t, err)
	secureExecutorExecutionsHook = origSecExec

	// 20. executions.go lines 211, 213, 216, 225: computeExecutionSnapshot runSnapshotExecutionHook errors
	origRunSnapE := runSnapshotExecutionHook
	defer func() { runSnapshotExecutionHook = origRunSnapE }()
	// line 211: policy denial dal.ErrNotSupported
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, dal.ErrNotSupported
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
	// line 213: generic error
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, errors.New("run snap generic err")
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
	// line 216: filtered.SnapshotRecordset == nil
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: nil}, nil
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
	// line 225: snapshotReadValidateHook error
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: &apicontract.Recordset{}}, nil
	}
	origSnapReadVal := snapshotReadValidateHook
	defer func() { snapshotReadValidateHook = origSnapReadVal }()
	snapshotReadValidateHook = func(s *apicontract.SnapshotReadResponse) error {
		return errors.New("snap val err")
	}
	_, err = computeExecutionSnapshot(rSnap)
	assert.Error(t, err)
	snapshotReadValidateHook = origSnapReadVal
	runSnapshotExecutionHook = origRunSnapE

	// line 216: computeExecutionSnapshot record.ResultComplete == nil
	recNoComplete := validTestExecutionRecord(pID, "exec-no-comp")
	recNoComplete.ResultComplete = nil
	snapRefNoComp, _, errPutSnap := store.PutSnapshot(ctx, apicontract.ExecutionRef{StoreID: pID, ProjectID: pID, ExecutionID: "exec-no-comp"}, apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}},
		Rows: [][]apicontract.TypedValue{
			{apicontract.NewStringValue("r1")},
		},
	}, time.Now().UTC())
	require.NoError(t, errPutSnap)
	recNoComplete.SnapshotRef = snapRefNoComp
	require.NoError(t, store.PutExecution(ctx, recNoComplete))
	runSnapshotExecutionHook = func(e *secureread.Executor, c context.Context, coll string, snap apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: &snap}, nil
	}
	rSnapNoComp := httptest.NewRequest(http.MethodGet, "/datatug/executions/snapshot?storeId="+pID+"&project="+pID+"&environment=e&securityContextId="+api.SecurityContextID(), nil)
	rSnapNoComp = rSnapNoComp.WithContext(context.WithValue(ctx, httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: "exec-no-comp"}}))
	snapNoCompResp, err := computeExecutionSnapshot(rSnapNoComp)
	assert.NoError(t, err)
	assert.True(t, snapNoCompResp.Truncated)
	runSnapshotExecutionHook = origRunSnapE

	// 21. executions.go line 275: computeExecutionSeriesBounded store not found
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	seriesReqFresh := seriesReq
	seriesReqFresh.SecurityContextID = api.SecurityContextID()
	bodySeriesFresh, _ := json.Marshal(seriesReqFresh)
	rSeriesFresh := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", bytes.NewReader(bodySeriesFresh))
	rSeriesFresh = rSeriesFresh.WithContext(ctx)

	origEvStoreHook := executionEvidenceStoreByIDHook
	defer func() { executionEvidenceStoreByIDHook = origEvStoreHook }()
	executionEvidenceStoreByIDHook = func(p, s string) (*executionstore.Store, error) {
		return nil, errors.New("ev store err")
	}
	_, err = computeExecutionSeriesBounded(rSeriesFresh, seriesReqFresh, 10)
	assert.Error(t, err)
	executionEvidenceStoreByIDHook = origEvStoreHook

	// 22. executions.go line 279: computeExecutionSeriesBounded store.ExecutionsBounded error
	origBoundHook := executionStoreExecutionsBoundedHook
	defer func() { executionStoreExecutionsBoundedHook = origBoundHook }()
	executionStoreExecutionsBoundedHook = func(s *executionstore.Store, c context.Context, limit int) ([]apicontract.ExecutionRecord, bool, error) {
		return nil, false, errors.New("bounded err")
	}
	_, err = computeExecutionSeriesBounded(rSeriesFresh, seriesReqFresh, 10)
	assert.Error(t, err)
	executionStoreExecutionsBoundedHook = origBoundHook
}

func TestClose100_ExecRunQuery_More(t *testing.T) {
	ctx := context.Background()
	pID := fmt.Sprintf("erq_proj_%d", time.Now().UnixNano())
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	pathsByID := map[string]string{pID: dir}
	origDatatugStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origDatatugStore }()
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", pathsByID) }
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	origResolveSource := resolveSourceHook
	defer func() { resolveSourceHook = origResolveSource }()
	resolveSourceHook = func(ctx context.Context, projStore datatug.ProjectStore, projDir, env, src string) (api.ResolvedSource, error) {
		return api.ResolvedSource{ID: src, Label: src, Collection: ""}, nil
	}

	// 1. typedParametersToVariables mismatched error (line 500)
	_, _, mismatched := typedParametersToVariables(map[string]apicontract.TypedValueOrSet{
		"bad": {Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "not_an_int"}},
	}, nil)
	assert.Contains(t, mismatched, "bad")

	// 2. eligibleTargetsHook error (line 460)
	origEligible := eligibleTargetsHook
	defer func() { eligibleTargetsHook = origEligible }()
	eligibleTargetsHook = func(ctx context.Context, projStore datatug.ProjectStore, projDir, env string, q *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return nil, errors.New("eligible targets boom")
	}
	_, err := resolveExecutionSource(ctx, nil, dir, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "q1",
	}, &datatug.QueryDef{})
	assert.Error(t, err)

	// 3. eligibleTargetsHook len(eligible) == 0 (line 471)
	eligibleTargetsHook = func(ctx context.Context, projStore datatug.ProjectStore, projDir, env string, q *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return []api.ResolvedSource{}, nil
	}
	_, err = resolveExecutionSource(ctx, nil, dir, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "q1", Source: "",
	}, &datatug.QueryDef{})
	assert.Error(t, err)

	// 4. eligibleTargetsHook match source (line 464)
	eligibleTargetsHook = func(ctx context.Context, projStore datatug.ProjectStore, projDir, env string, q *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return []api.ResolvedSource{{ID: "src1", Label: "Source 1"}}, nil
	}
	src, err := resolveExecutionSource(ctx, nil, dir, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "q1", Source: "src1",
	}, &datatug.QueryDef{})
	assert.NoError(t, err)
	assert.Equal(t, "src1", src.ID)

	// 5. runDTQLHook dbcopy.ErrSourceFileMissing (line 254) and dalgo2http.ErrSnapshotMiss (line 309)
	origRunDTQL := runDTQLHook
	defer func() { runDTQLHook = origRunDTQL }()

	runDTQLHook = func(e *secureread.Executor, c context.Context, sURL string, text []byte, v map[string]any) (secureread.Result, error) {
		return secureread.Result{}, dbcopy.ErrSourceFileMissing
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "src1", DTQL: "SELECT 1",
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)

	runDTQLHook = func(e *secureread.Executor, c context.Context, sURL string, text []byte, v map[string]any) (secureread.Result, error) {
		return secureread.Result{}, dalgo2http.ErrSnapshotMiss
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "src1", DTQL: "SELECT 1",
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)

	// 6. toContractRecordsetHook error (line 357)
	origToRec := toContractRecordsetHook
	defer func() { toContractRecordsetHook = origToRec }()
	toContractRecordsetHook = func(r secureread.Result) (apicontract.Recordset, error) {
		return apicontract.Recordset{}, errors.New("contract recordset error")
	}
	runDTQLHook = func(e *secureread.Executor, c context.Context, sURL string, text []byte, v map[string]any) (secureread.Result, error) {
		return secureread.Result{Columns: []string{"c1"}, Rows: []secureread.Row{{Data: map[string]any{"c1": "v1"}}}}, nil
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "src1", DTQL: "SELECT 1",
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)
	toContractRecordsetHook = origToRec

	// 7. runQueryResponseValidateHook error (line 400)
	origRespVal := runQueryResponseValidateHook
	defer func() { runQueryResponseValidateHook = origRespVal }()
	runQueryResponseValidateHook = func(r *apicontract.Result) error {
		return errors.New("run query resp val fail")
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "src1", DTQL: "SELECT 1",
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)
	runQueryResponseValidateHook = origRespVal

	// 8. secureExecutorHook returning false (line 106)
	origSec := secureExecutorHook
	defer func() { secureExecutorHook = origSec }()
	secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "src1", DTQL: "SELECT 1",
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)
	secureExecutorHook = origSec

	// 9. execRunProjectStoreFor wrappers for lines 135, 139, 147
	queriesDir := filepath.Join(dir, "queries", "customers")
	_ = os.MkdirAll(queriesDir, 0755)
	_ = os.WriteFile(filepath.Join(queriesDir, "customer-invoices.query.json"), []byte(`{"id":"customer-invoices","type":"DTQL"}`), 0644)
	_ = os.WriteFile(filepath.Join(queriesDir, "customer-invoices.query.dtql"), []byte("SELECT 1"), 0644)

	realStore, _ := api.ProjectStoreFor(pID)
	origProjStore := execRunProjectStoreFor
	defer func() { execRunProjectStoreFor = origProjStore }()

	// line 135: not a RevisionedQueriesStore
	execRunProjectStoreFor = func(p string) (datatug.ProjectStore, error) {
		return dummyNonRevisionedStore{ProjectStore: realStore}, nil
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "customers/customer-invoices", Record: true,
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)

	// line 139: LoadQueryRevision error
	execRunProjectStoreFor = func(p string) (datatug.ProjectStore, error) {
		rev, _ := realStore.(datatug.RevisionedQueriesStore)
		return dummyRevErrStore{ProjectStore: realStore, RevisionedQueriesStore: rev}, nil
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "customers/customer-invoices", Record: true,
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)

	// line 147: LoadQuery error (Record: false)
	execRunProjectStoreFor = func(p string) (datatug.ProjectStore, error) {
		return dummyLoadQueryErrStore{ProjectStore: realStore}, nil
	}
	_, err = computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "customers/customer-invoices", Record: false,
		SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive,
	})
	assert.Error(t, err)
	execRunProjectStoreFor = origProjStore

	// 10. lines 333, 336, 339: collection fallbacks
	eligibleTargetsHook = func(ctx context.Context, projStore datatug.ProjectStore, projDir, env string, q *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return []api.ResolvedSource{{ID: "src1", Label: "Source 1"}}, nil
	}

	runDTQLHook = func(e *secureread.Executor, c context.Context, sURL string, text []byte, v map[string]any) (secureread.Result, error) {
		return secureread.Result{
			Columns:    []string{"c1"},
			Rows:       []secureread.Row{{Data: map[string]any{"c1": "v1"}}},
			Collection: "",
		}, nil
	}
	// line 339: req.DTQL has queryDef == nil, resolved.Collection == "" -> collection = req.Source
	res339, err := computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "src1", DTQL: "SELECT 1", SecurityContextID: api.SecurityContextID(),
		Mode: apicontract.ProvenanceModeLive,
	})
	assert.NoError(t, err)
	assert.Equal(t, "src1", res339.Provenance.Collection)

	// line 333: queryDef.Capture != nil -> collection = queryDef.Capture.Collection
	execRunProjectStoreFor = func(p string) (datatug.ProjectStore, error) {
		return dummyQueryDefStore{
			ProjectStore: realStore,
			qDef: &datatug.QueryDef{
				ID: "customer-invoices", Type: datatug.QueryTypeDTQL, Text: "SELECT 1",
				Capture: &datatug.QueryCapture{Collection: "capture_coll"},
			},
		}, nil
	}
	res333, err := computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "customers/customer-invoices", Source: "src1", SecurityContextID: api.SecurityContextID(),
		Mode: apicontract.ProvenanceModeLive,
	})
	assert.NoError(t, err)
	assert.Equal(t, "capture_coll", res333.Provenance.Collection)

	// line 336: queryDef.Capture == nil, resolved.Collection != ""
	eligibleTargetsHook = func(ctx context.Context, projStore datatug.ProjectStore, projDir, env string, q *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return []api.ResolvedSource{{ID: "src1", Label: "Source 1", Collection: "resolved_coll"}}, nil
	}
	execRunProjectStoreFor = func(p string) (datatug.ProjectStore, error) {
		return dummyQueryDefStore{
			ProjectStore: realStore,
			qDef: &datatug.QueryDef{
				ID: "customer-invoices", Type: datatug.QueryTypeDTQL, Text: "SELECT 1",
			},
		}, nil
	}
	res336, err := computeRunQuery(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", QueryID: "customers/customer-invoices", Source: "src1", SecurityContextID: api.SecurityContextID(),
		Mode: apicontract.ProvenanceModeLive,
	})
	assert.NoError(t, err)
	assert.Equal(t, "resolved_coll", res336.Provenance.Collection)

	execRunProjectStoreFor = origProjStore
	runDTQLHook = origRunDTQL
	eligibleTargetsHook = origEligible
}

type dummyNonRevisionedStore struct {
	datatug.ProjectStore
}

type dummyRevErrStore struct {
	datatug.ProjectStore
	datatug.RevisionedQueriesStore
}

func (s dummyRevErrStore) LoadQueryRevision(ctx context.Context, queryID string, options ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	return nil, errors.New("cannot load query revision")
}

type dummyLoadQueryErrStore struct {
	datatug.ProjectStore
}

func (s dummyLoadQueryErrStore) LoadQuery(ctx context.Context, queryID string, options ...datatug.StoreOption) (*datatug.QueryDef, error) {
	return nil, errors.New("cannot load query")
}

type dummyQueryDefStore struct {
	datatug.ProjectStore
	qDef *datatug.QueryDef
}

func (s dummyQueryDefStore) LoadQuery(ctx context.Context, queryID string, options ...datatug.StoreOption) (*datatug.QueryDef, error) {
	return s.qDef, nil
}

type customIncidentStore struct {
	incidents.APIStore
	listFn       func(ctx context.Context, query incidents.CandidateListQuery) ([]incidents.Incident, error)
	eventsFn     func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error)
	projectionFn func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error)
	watchFn      func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error)
	mergeFn      func(ctx context.Context, mutation incidents.MergeMutation) (incidents.MergeResult, error)
	appendFn     func(ctx context.Context, mutation incidents.Mutation) (incidents.AppendResult, error)
	createFn     func(ctx context.Context, mutation incidents.CreateMutation) (incidents.CreateResult, error)
}

func (c *customIncidentStore) List(ctx context.Context, query incidents.CandidateListQuery) ([]incidents.Incident, error) {
	if c.listFn != nil {
		return c.listFn(ctx, query)
	}
	return c.APIStore.List(ctx, query)
}

func (c *customIncidentStore) Events(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
	if c.eventsFn != nil {
		return c.eventsFn(ctx, ref, after)
	}
	return c.APIStore.Events(ctx, ref, after)
}

func (c *customIncidentStore) Projection(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
	if c.projectionFn != nil {
		return c.projectionFn(ctx, ref, at)
	}
	return c.APIStore.Projection(ctx, ref, at)
}

func (c *customIncidentStore) Watch(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
	if c.watchFn != nil {
		return c.watchFn(ctx, query)
	}
	return c.APIStore.Watch(ctx, query)
}

func (c *customIncidentStore) Merge(ctx context.Context, mutation incidents.MergeMutation) (incidents.MergeResult, error) {
	if c.mergeFn != nil {
		return c.mergeFn(ctx, mutation)
	}
	return c.APIStore.Merge(ctx, mutation)
}

func (c *customIncidentStore) Append(ctx context.Context, mutation incidents.Mutation) (incidents.AppendResult, error) {
	if c.appendFn != nil {
		return c.appendFn(ctx, mutation)
	}
	return c.APIStore.Append(ctx, mutation)
}

func (c *customIncidentStore) Create(ctx context.Context, mutation incidents.CreateMutation) (incidents.CreateResult, error) {
	if c.createFn != nil {
		return c.createFn(ctx, mutation)
	}
	return c.APIStore.Create(ctx, mutation)
}

type errResponseWriter struct {
	http.ResponseWriter
	failCount int
	writeCall int
}

func (e *errResponseWriter) Write(p []byte) (int, error) {
	e.writeCall++
	if e.failCount == 0 || e.writeCall == e.failCount {
		return 0, errors.New("write error")
	}
	return e.ResponseWriter.Write(p)
}

func (e *errResponseWriter) Flush() {}

func TestClose100_Incidents_Deep(t *testing.T) {
	router, scopes, _, _ := configureIncidentHTTPStateAndRoot(t, "alpha")
	scope := scopes["alpha"]

	baseStore, err := incidentStoreByID("alpha", "ops")
	require.NoError(t, err)

	origIncidentStoreByID := incidentStoreByID
	origIncidentActorHook := incidentActorHook
	origIncidentResolveProjectHook := incidentResolveProjectHook
	origIncidentViewHook := incidentViewHook
	origIncidentStreamItemMarshalHook := incidentStreamItemMarshalHook
	origIncidentApplyEventViewHook := incidentApplyEventViewHook
	origIncidentApplyStreamItemViewHook := incidentApplyStreamItemViewHook

	defer func() {
		incidentStoreByID = origIncidentStoreByID
		incidentActorHook = origIncidentActorHook
		incidentResolveProjectHook = origIncidentResolveProjectHook
		incidentViewHook = origIncidentViewHook
		incidentStreamItemMarshalHook = origIncidentStreamItemMarshalHook
		incidentApplyEventViewHook = origIncidentApplyEventViewHook
		incidentApplyStreamItemViewHook = origIncidentApplyStreamItemViewHook
	}()

	// First create an incident successfully to have a known valid incident
	createdRec := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scope, "seed-mut-1", "Seed Incident"), http.StatusCreated)
	var createdResp apicontract.IncidentResponse
	require.NoError(t, json.Unmarshal(createdRec.Body.Bytes(), &createdResp))
	incRef := createdResp.Incident.Ref

	// --- 1. incidentCreateHandler ---
	// validateScope error (lines 103-105)
	badScopeReq := incidentCreateFixture(scope, "mut-bad-scope", "title")
	badScopeReq.SecurityContextID = "bad-sec"
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", badScopeReq, http.StatusConflict)

	// ValidateResolvedProject error (line 109)
	badProjReq := incidentCreateFixture(scope, "mut-bad-title", "")
	badProjReq.Title = ""
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", badProjReq, http.StatusBadRequest)

	// Projects resolve error / mismatch (lines 114-117)
	projErrReq := incidentCreateFixture(scope, "mut-proj-err", "title")
	projErrReq.Projects = []incidents.ProjectRef{{StoreID: "local", ProjectID: "unserved_proj", Environment: "prod"}}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", projErrReq, http.StatusNotFound)

	// store.Events error in create (lines 141-143)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			eventsFn: func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
				return nil, errors.New("events error in create")
			},
		}, nil
	}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scope, "mut-ev-err", "title"), http.StatusInternalServerError)
	incidentStoreByID = origIncidentStoreByID

	// --- 2. incidentListHandler ---
	// Status parsing loop (lines 160-161)
	listURL := fmt.Sprintf("/datatug/incidents?storeId=%s&project=%s&environment=%s&securityContextId=%s&status=open&status=investigating",
		scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, listURL, nil, http.StatusOK)

	// req.ListQuery error (lines 168-169)
	invalidStatusURL := fmt.Sprintf("/datatug/incidents?storeId=%s&project=%s&environment=%s&securityContextId=%s&status=bogus_status",
		scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, invalidStatusURL, nil, http.StatusBadRequest)

	// Candidates loop: List returns error with non-empty candidates (line 177)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			listFn: func(ctx context.Context, query incidents.CandidateListQuery) ([]incidents.Incident, error) {
				return []incidents.Incident{{Ref: incRef}}, errors.New("list err with candidates")
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, listURL, nil, http.StatusInternalServerError)

	// Candidates loop: Events error (line 182)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			eventsFn: func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
				return nil, errors.New("events err in list")
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, listURL, nil, http.StatusInternalServerError)
	incidentStoreByID = origIncidentStoreByID

	// --- 3. incidentShowHandler ---
	// at invalid parse error (lines 208-209)
	showBadAtURL := fmt.Sprintf("/datatug/incidents/%s?storeId=%s&project=%s&environment=%s&securityContextId=%s&at=bad-time",
		incRef.IncidentID, scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, showBadAtURL, nil, http.StatusBadRequest)

	// at valid: visible before and after at (lines 210, 221-227)
	time.Sleep(10 * time.Millisecond)
	appendSeedReq := apicontract.IncidentAppendRequest{
		IncidentScope: scope,
		MutationID:    "seed-append-for-at",
		Incident:      incRef,
		Event:         apicontract.IncidentEventInput{At: time.Now().UTC(), Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionObservation}, Payload: json.RawMessage(`{"body":"seed note"}`)},
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), appendSeedReq, http.StatusOK)
	realEvents, err := baseStore.Events(context.Background(), incRef, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(realEvents), 2)
	atTime := realEvents[0].VisibleAt
	showAtURL := fmt.Sprintf("/datatug/incidents/%s?storeId=%s&project=%s&environment=%s&securityContextId=%s&at=%s",
		incRef.IncidentID, scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID, atTime.Format(time.RFC3339Nano))
	performIncidentRequest(t, router, http.MethodGet, showAtURL, nil, http.StatusOK)

	// !incidentHasProject (lines 231-232)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			projectionFn: func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
				return incidents.Incident{Ref: ref, Projects: []incidents.ProjectRef{{ProjectID: "other"}}}, nil
			},
		}, nil
	}
	showURL := fmt.Sprintf("/datatug/incidents/%s?storeId=%s&project=%s&environment=%s&securityContextId=%s",
		incRef.IncidentID, scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, showURL, nil, http.StatusNotFound)
	incidentStoreByID = origIncidentStoreByID

	// --- 4. incidentAppendHandler & authorizeIncidentEvent ---
	// pathErr != nil in incidentAppendHandler (lines 257-258)
	reqObj := apicontract.IncidentAppendRequest{
		IncidentScope: scope,
		MutationID:    "append-mut-1",
		Incident:      incRef,
		Event:         apicontract.IncidentEventInput{At: time.Now().UTC(), Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionObservation}, Payload: json.RawMessage(`{"body":"text"}`)},
	}
	// pathErr != nil in incidentAppendHandler (lines 257-258)
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/%20/events", reqObj, http.StatusBadRequest)

	// pathRef != req.Incident (lines 260-261)
	reqMismatch := reqObj
	reqMismatch.Incident = incidents.IncidentRef{StoreID: scope.StoreID, IncidentID: "INC-9999"}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqMismatch, http.StatusBadRequest)

	// incidentActorHook error (lines 271-272)
	incidentActorHook = func(via string) (incidents.Actor, error) {
		return incidents.Actor{}, errors.New("actor forbidden")
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqObj, http.StatusForbidden)
	incidentActorHook = origIncidentActorHook

	// !visible after ApplyEventView (lines 305-306)
	applyCallCount := 0
	incidentApplyEventViewHook = func(event incidents.Event, policy incidents.ViewPolicy) (incidents.Event, bool, error) {
		applyCallCount++
		if applyCallCount == 2 {
			return event, false, nil
		}
		return origIncidentApplyEventViewHook(event, policy)
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqObj, http.StatusForbidden)
	incidentApplyEventViewHook = origIncidentApplyEventViewHook

	// authorizeIncidentEvent: store.Projection error (lines 317-318)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		projCount := 0
		return &customIncidentStore{
			APIStore: baseStore,
			projectionFn: func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
				projCount++
				if projCount == 2 {
					return incidents.Incident{}, errors.New("proj err in auth")
				}
				return baseStore.Projection(ctx, ref, at)
			},
		}, nil
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqObj, http.StatusInternalServerError)

	// authorizeIncidentEvent: store.Events error (lines 321-322)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			eventsFn: func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
				return nil, errors.New("events err in auth")
			},
		}, nil
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqObj, http.StatusInternalServerError)

	// authorizeIncidentEvent: incidentViewHook error (lines 333-334)
	incidentStoreByID = origIncidentStoreByID
	incidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, errors.New("view err in auth")
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqObj, http.StatusInternalServerError)
	incidentViewHook = origIncidentViewHook

	// authorizeIncidentEvent: incidentApplyEventViewHook error (lines 337-338)
	incidentApplyEventViewHook = func(event incidents.Event, policy incidents.ViewPolicy) (incidents.Event, bool, error) {
		return incidents.Event{}, false, errors.New("apply event view err in auth")
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/events", incRef.IncidentID), reqObj, http.StatusBadRequest)
	incidentApplyEventViewHook = origIncidentApplyEventViewHook

	// --- 5. incidentMergeHandler ---
	// req.Validate error (lines 354-355)
	badMergeReq := apicontract.IncidentMergeRequest{IncidentScope: scope, MutationID: "", Source: incRef, Into: incRef}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/merge", incRef.IncidentID), badMergeReq, http.StatusBadRequest)

	createdRec2 := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scope, "seed-mut-2", "Seed Incident 2"), http.StatusCreated)
	var createdResp2 apicontract.IncidentResponse
	require.NoError(t, json.Unmarshal(createdRec2.Body.Bytes(), &createdResp2))
	incRef2 := createdResp2.Incident.Ref

	// pathErr != nil (lines 359-360)
	goodMergeReq := apicontract.IncidentMergeRequest{IncidentScope: scope, MutationID: "merge-1", Source: incRef, Into: incRef2}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/%20/merge", goodMergeReq, http.StatusBadRequest)

	// pathRef != req.Source (lines 362-363)
	mismatchMergeReq := goodMergeReq
	mismatchMergeReq.Source = incidents.IncidentRef{StoreID: scope.StoreID, IncidentID: "INC-9999"}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/merge", incRef.IncidentID), mismatchMergeReq, http.StatusBadRequest)

	// store.Projection error in merge (lines 368-369)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			projectionFn: func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
				return incidents.Incident{}, errors.New("projection err in merge")
			},
		}, nil
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/merge", incRef.IncidentID), goodMergeReq, http.StatusInternalServerError)

	// incidentActorHook error in merge (lines 378-379)
	incidentStoreByID = origIncidentStoreByID
	incidentActorHook = func(via string) (incidents.Actor, error) {
		return incidents.Actor{}, errors.New("actor err in merge")
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/merge", incRef.IncidentID), goodMergeReq, http.StatusForbidden)
	incidentActorHook = origIncidentActorHook

	// sourceErr != nil (lines 389-390)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			eventsFn: func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
				return nil, errors.New("source events err")
			},
		}, nil
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/merge", incRef.IncidentID), goodMergeReq, http.StatusInternalServerError)

	// intoErr != nil (lines 391-392)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		evCall := 0
		return &customIncidentStore{
			APIStore: baseStore,
			eventsFn: func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
				evCall++
				if evCall == 2 {
					return nil, errors.New("into events err")
				}
				return baseStore.Events(ctx, ref, after)
			},
		}, nil
	}
	performIncidentJSON(t, router, http.MethodPost, fmt.Sprintf("/datatug/incidents/%s/merge", incRef.IncidentID), goodMergeReq, http.StatusInternalServerError)
	incidentStoreByID = origIncidentStoreByID

	// --- 6. incidentSearchHandler ---
	// POST body read/decode error (lines 410-412)
	performIncidentRequest(t, router, http.MethodPost, "/datatug/incidents/search", []byte("{invalid-json"), http.StatusBadRequest)

	// --- 7. incidentViews ---
	// store.List error in incidentViews (lines 467-468)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			listFn: func(ctx context.Context, query incidents.CandidateListQuery) ([]incidents.Incident, error) {
				return nil, errors.New("list err in incidentViews")
			},
		}, nil
	}
	searchURL := fmt.Sprintf("/datatug/incidents/search?storeId=%s&project=%s&environment=%s&securityContextId=%s&text=query",
		scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, searchURL, nil, http.StatusInternalServerError)

	// store.Events error in incidentViews (lines 473-474)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			listFn: func(ctx context.Context, query incidents.CandidateListQuery) ([]incidents.Incident, error) {
				return []incidents.Incident{{Ref: incRef}}, nil
			},
			eventsFn: func(ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
				return nil, errors.New("events err in incidentViews")
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, searchURL, nil, http.StatusInternalServerError)
	incidentStoreByID = origIncidentStoreByID

	// --- 8. incidentEventsHandler & nextVisibleIncidentStreamItem ---
	// id invalid in path (lines 493-494)
	eventsBadIDURL := fmt.Sprintf("/datatug/incidents/%%20/events?storeId=%s&project=%s&environment=%s&securityContextId=%s",
		scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, eventsBadIDURL, nil, http.StatusBadRequest)

	// req.Validate() error (lines 504-505)
	eventsBadReqURL := fmt.Sprintf("/datatug/incidents/%s/events?storeId=%s&project=%s&environment=%s&securityContextId=%s&since=bad%%20cursor",
		incRef.IncidentID, scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, eventsBadReqURL, nil, http.StatusBadRequest)

	// store.Projection error (lines 510-511)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			projectionFn: func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
				return incidents.Incident{}, errors.New("proj err in events")
			},
		}, nil
	}
	eventsURL := fmt.Sprintf("/datatug/incidents/%s/events?storeId=%s&project=%s&environment=%s&securityContextId=%s",
		incRef.IncidentID, scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusInternalServerError)

	// !follow, store does not implement SnapshotWatcher (lines 521-522)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{APIStore: baseStore}, nil
	}
	eventsNoFollowURL := fmt.Sprintf("/datatug/incidents/%s/events?storeId=%s&project=%s&environment=%s&securityContextId=%s&follow=false",
		incRef.IncidentID, scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, eventsNoFollowURL, nil, http.StatusInternalServerError)

	// follow, ref != nil, store.Watch error (lines 528-529)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return nil, errors.New("watch err")
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusInternalServerError)

	// follow, ref == nil, store does not implement ProjectWatcher (lines 532-533)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{APIStore: baseStore}, nil
	}
	eventsProjURL := fmt.Sprintf("/datatug/incidents/events?storeId=%s&project=%s&environment=%s&securityContextId=%s",
		scope.StoreID, scope.Project, scope.Environment, scope.SecurityContextID)
	performIncidentRequest(t, router, http.MethodGet, eventsProjURL, nil, http.StatusInternalServerError)

	// firstErr != nil && incidentStreamDone (lines 543-546)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return &scriptedEventStream{nextErr: io.EOF}, nil
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusOK)

	// incidentStreamItemMarshalHook error on first item (lines 552-554)
	streamItem1 := incidents.StreamItem{
		Cursor: "c1",
		Event:  incidents.Event{Incident: incRef, ID: "ev1", Type: "note"},
	}
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return &scriptedEventStream{items: []incidents.StreamItem{streamItem1}, nextErr: io.EOF}, nil
			},
		}, nil
	}
	incidentStreamItemMarshalHook = func(v any) ([]byte, error) {
		return nil, errors.New("marshal error on first line")
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusInternalServerError)
	incidentStreamItemMarshalHook = origIncidentStreamItemMarshalHook

	// w.Write error on first line (lines 559-560)
	eventsReq := httptest.NewRequest(http.MethodGet, eventsURL, nil)
	eventsReq = eventsReq.WithContext(context.WithValue(eventsReq.Context(), httprouter.ParamsKey, httprouter.Params{{Key: "id", Value: incRef.IncidentID}}))
	errW1 := &errResponseWriter{ResponseWriter: httptest.NewRecorder(), failCount: 1}
	incidentEventsHandler(errW1, eventsReq)

	// Marshal error on second line (line 574)
	streamItem2 := incidents.StreamItem{
		Cursor: "c2",
		Event:  incidents.Event{Incident: incRef, ID: "ev2", Type: "note"},
	}
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return &scriptedEventStream{items: []incidents.StreamItem{streamItem1, streamItem2}, nextErr: io.EOF}, nil
			},
		}, nil
	}
	marshalCount := 0
	incidentStreamItemMarshalHook = func(v any) ([]byte, error) {
		marshalCount++
		if marshalCount > 1 {
			return nil, errors.New("marshal error on second item")
		}
		return json.Marshal(v)
	}
	func() {
		defer func() {
			_ = recover()
		}()
		rec := httptest.NewRecorder()
		incidentEventsHandler(rec, eventsReq)
	}()
	incidentStreamItemMarshalHook = origIncidentStreamItemMarshalHook

	// w.Write error on second line (lines 577-578)
	errW2 := &errResponseWriter{ResponseWriter: httptest.NewRecorder(), failCount: 2}
	incidentEventsHandler(errW2, eventsReq)

	// --- 9. nextVisibleIncidentStreamItem ---
	// store.Projection error in nextVisibleIncidentStreamItem (lines 593-594)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		firstProj := true
		return &customIncidentStore{
			APIStore: baseStore,
			projectionFn: func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
				if firstProj {
					firstProj = false
					return baseStore.Projection(ctx, ref, at)
				}
				return incidents.Incident{}, errors.New("stream projection err")
			},
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return &scriptedEventStream{items: []incidents.StreamItem{streamItem1}, nextErr: io.EOF}, nil
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusInternalServerError)

	// !incidentHasProject continue (line 596)
	unrelatedRef := incidents.IncidentRef{StoreID: scope.StoreID, IncidentID: "INC-OTHER"}
	itemNotProject := incidents.StreamItem{Cursor: "c0", Event: incidents.Event{Incident: unrelatedRef, ID: "e0"}}
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			projectionFn: func(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (incidents.Incident, error) {
				if ref == unrelatedRef {
					return incidents.Incident{Ref: ref, Projects: []incidents.ProjectRef{{ProjectID: "different"}}}, nil
				}
				return baseStore.Projection(ctx, ref, at)
			},
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return &scriptedEventStream{items: []incidents.StreamItem{itemNotProject, streamItem1}, nextErr: io.EOF}, nil
			},
		}, nil
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusOK)

	// incidentViewHook error in stream (lines 604-605)
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) {
		return &customIncidentStore{
			APIStore: baseStore,
			watchFn: func(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
				return &scriptedEventStream{items: []incidents.StreamItem{streamItem1}, nextErr: io.EOF}, nil
			},
		}, nil
	}
	incidentViewHook = func(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, errors.New("view err in stream")
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusInternalServerError)
	incidentViewHook = origIncidentViewHook

	// incidentApplyStreamItemViewHook error in stream (lines 608-609)
	incidentApplyStreamItemViewHook = func(item incidents.StreamItem, policy incidents.ViewPolicy) (incidents.StreamItem, bool, error) {
		return incidents.StreamItem{}, false, errors.New("apply view err in stream")
	}
	performIncidentRequest(t, router, http.MethodGet, eventsURL, nil, http.StatusInternalServerError)
	incidentApplyStreamItemViewHook = origIncidentApplyStreamItemViewHook
}
