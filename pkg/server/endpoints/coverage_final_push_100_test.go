package endpoints

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/personalqueries"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSchemaReader struct {
	descErr error
	listErr error
}

func (m mockSchemaReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return nil, nil
}

func (m mockSchemaReader) DescribeCollection(ctx context.Context, collection *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if m.descErr != nil {
		return nil, m.descErr
	}
	return &dbschema.CollectionDef{
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.String}},
		PrimaryKey: []dal.FieldName{"id"},
	}, nil
}

func (m mockSchemaReader) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (m mockSchemaReader) ListConstraints(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, nil
}

func (m mockSchemaReader) ListReferrers(ctx context.Context, collection *dal.CollectionRef) ([]dbschema.Referrer, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return []dbschema.Referrer{
		{Collection: dal.NewRootCollectionRef("orders", ""), Fields: []dal.FieldName{"cust_id"}},
	}, nil
}

type mockStoreWithCatalogs struct {
	datatug.ProjectStore
	catalogs datatug.DbCatalogs
	err      error
}

func (m mockStoreWithCatalogs) LoadEnvDbCatalogs(ctx context.Context, env string, opts ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	if m.err != nil {
		return nil, m.err
	}
	if len(m.catalogs) > 0 {
		return m.catalogs, nil
	}
	return m.ProjectStore.LoadEnvDbCatalogs(ctx, env, opts...)
}

func TestFinal_SemanticColumns_And_Schema(t *testing.T) {
	ctx := context.Background()

	// 1. query_endpoints.go:198-199 ResolveProjectDir error
	pID := "invalid..proj"
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	_, err := getPersonalQueries(ctx, dto.ProjectRef{StoreID: "s1", ProjectID: pID})
	assert.ErrorIs(t, err, personalqueries.ErrInvalidProjectID)

	// 2. semantic_columns.go:59 httpDeclaredColumns error
	goodPID := "p_col_test"
	goodDir := t.TempDir()
	filestore.SetProjectPath(goodPID, goodDir)
	origStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{goodPID: goodDir}) }
	defer func() { storage.NewDatatugStore = origStore }()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{goodPID: goodDir}, api.Capabilities{})

	// Create project with http source pointing to non-existent query
	projJSON := `{"id":"p_col_test","access":"shared","environments":[{"id":"env1","sources":[{"id":"http1","kind":"http","url":"http://localhost"}]}]}`
	_ = os.WriteFile(filepath.Join(goodDir, "datatug-project.json"), []byte(projJSON), 0644)

	scope := apicontract.Scope{Project: goodPID, Environment: "env1", SecurityContextID: api.SecurityContextID()}
	sourceRef := apicontract.SourceRef{Source: "http1", Collection: "items"}
	_, err = computeSemanticColumns(ctx, scope, sourceRef)
	assert.Error(t, err)

	// 3. semantic_columns.go:72 loadModuleEntities error
	badEntDir := filepath.Join(goodDir, "entities")
	_ = os.MkdirAll(badEntDir, 0755)
	_ = os.WriteFile(filepath.Join(badEntDir, "corrupt.entity.json"), []byte("{invalid json"), 0644)

	// Switch source to a sql source so it attempts loadModuleEntities
	projJSON2 := `{"id":"p_col_test","access":"shared","environments":[{"id":"env1","sources":[{"id":"sql1","kind":"sqlite","url":"sqlite://local.db"}]}]}`
	_ = os.WriteFile(filepath.Join(goodDir, "datatug-project.json"), []byte(projJSON2), 0644)

	sourceRef2 := apicontract.SourceRef{Source: "sql1", Collection: "items"}
	_, err = computeSemanticColumns(ctx, scope, sourceRef2)
	assert.Error(t, err)
	_ = os.Remove(filepath.Join(badEntDir, "corrupt.entity.json"))

	// 4. semantic_schema.go:88 dbcopy.Parse / ref.Open error with postgres
	_, err = resolveSQLSourceURL(ctx, "postgres://localhost/mydb", "items")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "postgres")

	// 5. semantic_schema.go:114 ListReferrers error
	origDalAs := dalAsSchemaReader
	defer func() { dalAsSchemaReader = origDalAs }()

	dalAsSchemaReader = func(dal.DB) (dbschema.SchemaReader, bool) {
		return mockSchemaReader{listErr: errors.New("cannot list referrers")}, true
	}

	dbFile := filepath.Join(t.TempDir(), "test.db")
	_ = os.WriteFile(dbFile, []byte{}, 0644)
	_, err = resolveSQLSourceURL(ctx, "sqlite://"+dbFile, "items")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "list referrers")
}

func TestFinal_SemanticApplicable(t *testing.T) {
	ctx := context.Background()

	// 1. semanticApplicableHandler body read error
	rErr := httptest.NewRequest(http.MethodPost, "/datatug/queries/applicable", errReader{})
	wErr := httptest.NewRecorder()
	semanticApplicableHandler(wErr, rErr)
	assert.Equal(t, http.StatusBadRequest, wErr.Code)

	// 2. computeSemanticApplicable validateScope error
	_, err := computeSemanticApplicable(ctx, apicontract.ApplicableRequest{
		Project: "p", Environment: "e", SecurityContextID: "wrong-sec-id",
	})
	assert.Error(t, err)

	// 3. computeSemanticApplicable req.Validate() error
	_, err = computeSemanticApplicable(ctx, apicontract.ApplicableRequest{
		Project: "", Environment: "e", SecurityContextID: api.SecurityContextID(),
	})
	assert.Error(t, err)

	// 4. computeSemanticApplicable applicableProjectStoreFor error
	pID := "app_proj"
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{pID: dir}) }
	defer func() { storage.NewDatatugStore = origDatatugStore }()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origStoreFor := applicableProjectStoreFor
	applicableProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store offline")
	}
	_, err = computeSemanticApplicable(ctx, apicontract.ApplicableRequest{
		Project: pID, Environment: "dev", SecurityContextID: api.SecurityContextID(),
	})
	assert.Error(t, err)
	applicableProjectStoreFor = origStoreFor

	// 5. computeSemanticApplicable loadModuleQueries error
	badQDir := filepath.Join(dir, "queries")
	_ = os.MkdirAll(badQDir, 0755)
	_ = os.WriteFile(filepath.Join(badQDir, "bad.query.json"), []byte("{bad json"), 0644)
	_, err = computeSemanticApplicable(ctx, apicontract.ApplicableRequest{
		Project: pID, Environment: "dev", SecurityContextID: api.SecurityContextID(),
	})
	assert.Error(t, err)
	_ = os.Remove(filepath.Join(badQDir, "bad.query.json"))

	// 6. finishCandidate: multiple eligible targets (CandidateStateNeedsTarget)
	projStore := filestore.NewProjectStore(pID, dir)
	_ = os.WriteFile(filepath.Join(dir, "s1.db"), []byte{}, 0644)
	_ = os.WriteFile(filepath.Join(dir, "s2.db"), []byte{}, 0644)
	multiStore := mockStoreWithCatalogs{
		ProjectStore: projStore,
		catalogs: datatug.DbCatalogs{
			{DbCatalogBase: datatug.DbCatalogBase{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "s1"}},
				Driver: "sqlite", Path: "s1.db",
			}},
			{DbCatalogBase: datatug.DbCatalogBase{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "s2"}},
				Driver: "sqlite", Path: "s2.db",
			}},
		},
	}

	qDef := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1"}},
	}
	cand := finishCandidate(ctx, multiStore, dir, "env1", qDef, nil, nil, nil, nil, map[*datatug.QueryDef]string{qDef: "q1"})
	assert.Equal(t, apicontract.CandidateStateNeedsTarget, cand.State)
	assert.Len(t, cand.Targets, 2)

	// 7. finishCandidate: EligibleTargets error -> eligible == nil
	candErr := finishCandidate(ctx, mockStoreWithCatalogs{ProjectStore: projStore, err: errors.New("catalogs fail")}, dir, "env1", qDef, nil, nil, nil, nil, map[*datatug.QueryDef]string{qDef: "q1"})
	assert.Equal(t, apicontract.CandidateStateSourceUnavailable, candErr.State)
	assert.Empty(t, candErr.Targets)
}

func TestFinal_SemanticRelated(t *testing.T) {
	ctx := context.Background()

	// 1. semanticRelatedHandler body read error
	rErr := httptest.NewRequest(http.MethodPost, "/datatug/semantic/related", errReader{})
	wErr := httptest.NewRecorder()
	semanticRelatedHandler(wErr, rErr)
	assert.Equal(t, http.StatusBadRequest, wErr.Code)

	// 2. computeSemanticRelated validateScope error
	_, err := computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: "p", Environment: "e", SecurityContextID: "wrong",
	})
	assert.Error(t, err)

	// 3. computeSemanticRelated req.Fact.Physical == nil
	_, err = computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: "p", Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Entity: "Customer", Field: "ID",
			Value:   apicontract.NewStringValue("123"),
			Origin:  apicontract.FactOriginContext,
			Mapping: apicontract.FactMappingInferred,
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "physical")

	// 4. computeSemanticRelated unknown project
	_, err = computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: "unknown_proj", Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Entity: "Customer", Field: "ID",
			Value:   apicontract.NewStringValue("123"),
			Origin:  apicontract.FactOriginContext,
			Mapping: apicontract.FactMappingInferred,
			Physical: &apicontract.PhysicalRef{
				Source: "s", Collection: "c", Column: "id",
			},
		},
	})
	assert.Error(t, err)

	// 5. computeSemanticRelated relatedProjectStoreFor error
	pID := "rel_proj"
	dir := t.TempDir()
	filestore.SetProjectPath(pID, dir)
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{pID: dir}) }
	defer func() { storage.NewDatatugStore = origDatatugStore }()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origStoreFor := relatedProjectStoreFor
	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store failed")
	}
	_, err = computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Entity: "Customer", Field: "ID",
			Value:   apicontract.NewStringValue("123"),
			Origin:  apicontract.FactOriginContext,
			Mapping: apicontract.FactMappingInferred,
			Physical: &apicontract.PhysicalRef{
				Source: "s", Collection: "c", Column: "id",
			},
		},
	})
	assert.Error(t, err)
	relatedProjectStoreFor = origStoreFor

	// 6. computeSemanticRelated loadModuleEntities error
	badEntDir := filepath.Join(dir, "entities")
	_ = os.MkdirAll(badEntDir, 0755)
	_ = os.WriteFile(filepath.Join(badEntDir, "bad.entity.json"), []byte("{bad json"), 0644)
	_, err = computeSemanticRelated(ctx, apicontract.RelatedRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		Fact: apicontract.Fact{
			ID: "f1", Entity: "Customer", Field: "ID",
			Value:   apicontract.NewStringValue("123"),
			Origin:  apicontract.FactOriginContext,
			Mapping: apicontract.FactMappingInferred,
			Physical: &apicontract.PhysicalRef{
				Source: "s", Collection: "c", Column: "id",
			},
		},
	})
	assert.Error(t, err)
	_ = os.Remove(filepath.Join(badEntDir, "bad.entity.json"))

	// 7. computeSemanticRelatedRows unknown project and relatedProjectStoreFor error
	_, err = computeSemanticRelatedRows(ctx, apicontract.RelatedRowsRequest{
		Project: "nonexistent", Environment: "e", SecurityContextID: api.SecurityContextID(),
		LookupID: "s:c:col", Value: apicontract.NewStringValue("val"),
	})
	assert.Error(t, err)

	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store err")
	}
	_, err = computeSemanticRelatedRows(ctx, apicontract.RelatedRowsRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		LookupID: "s:c:col", Value: apicontract.NewStringValue("val"),
	})
	assert.Error(t, err)
	relatedProjectStoreFor = origStoreFor
}

func TestFinal_Compare_And_CompareIncident(t *testing.T) {
	ctx := context.Background()

	// 1. compare.go:155-157 deps.appendRun == nil with req.Incident != nil
	incRef := incidents.IncidentRef{StoreID: "istore", IncidentID: "INC-1"}
	_, err := computeCompareWith(ctx, apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "q1",
		Key:               []string{"id"},
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
		Incident:          &incRef,
		MutationID:        "mut-1",
	}, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "id"}}}, receipt: apicontract.CompareSideReceipt{}}, nil
		},
		appendRun: nil,
	})
	assert.Error(t, err)

	// 2. compare.go:164 compareResultValidateHook error
	origCompVal := compareResultValidateHook
	compareResultValidateHook = func(*apicontract.CompareResult) error {
		return errors.New("validation failed")
	}
	defer func() { compareResultValidateHook = origCompVal }()

	_, err = computeCompareWith(ctx, apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(),
		QueryID:           "q1",
		Key:               []string{"id"},
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
	}, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "id"}}}, receipt: apicontract.CompareSideReceipt{}}, nil
		},
	})
	assert.Error(t, err)
	compareResultValidateHook = origCompVal

	// 3. compare_facts.go:25 req.Incident == nil
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{Incident: nil}, apicontract.CompareSideSpec{})
	assert.Error(t, err)

	// 4. compare_facts.go:30 compareIncidentState error
	_, err = executeCompareFactsSide(ctx, apicontract.CompareRequest{
		Incident: &incRef,
		Left:     apicontract.CompareSideSpec{StoreID: "bad_store", Project: "bad_p"},
	}, apicontract.CompareSideSpec{})
	assert.Error(t, err)
}

func TestFinal_ExecutionRecording(t *testing.T) {
	ctx := context.Background()

	// ratToTerminatingDecimal with zero -> decimal = "0"
	val, ok := finiteDecimal(big.NewRat(0, 1))
	assert.True(t, ok)
	assert.Equal(t, "0", val)

	// execution_recording.go:106 principalID == "" -> "local-owner"
	dir := t.TempDir()
	pID := fmt.Sprintf("rec_proj_%d", time.Now().UnixNano())
	filestore.SetProjectPath(pID, dir)
	api.ConfigureSecureSession(secureread.Session{Principal: nil}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{pID: dir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	defer func() { _ = api.CloseExecutionEvidence() }()

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
	ref, err := recordQueryExecution(ctx, apicontract.ExecutionRequest{
		Project: pID, Environment: "env1", Source: "s1",
		QueryID: "q1",
	}, nil, "", res, time.Now().UTC())
	assert.NoError(t, err)
	assert.NotEmpty(t, ref.ExecutionID)
}

func TestFinal_Executions_And_Incidents(t *testing.T) {
	// 1. executions.go:71 computeExecutionList req.Validate() error
	_, err := computeExecutionList(httptest.NewRequest(http.MethodGet, "/?project=p&environment=e", nil), apicontract.ExecutionListRequest{
		Scope: apicontract.Scope{Project: "p", Environment: "e", SecurityContextID: api.SecurityContextID()},
		Limit: func() *int { i := -5; return &i }(),
	})
	assert.Error(t, err)

	// 2. executions.go:75 ExecutionEvidenceStoreByID error
	_, err = computeExecutionList(httptest.NewRequest(http.MethodGet, "/?project=p&environment=e", nil), apicontract.ExecutionListRequest{
		Scope: apicontract.Scope{StoreID: "no_such_store", Project: "p", Environment: "e", SecurityContextID: api.SecurityContextID()},
	})
	assert.Error(t, err)

	// 3. executions.go:247 executionSeriesHandler body read error
	rErr := httptest.NewRequest(http.MethodPost, "/datatug/executions/series", errReader{})
	wErr := httptest.NewRecorder()
	executionSeriesHandler(wErr, rErr)
	assert.Equal(t, http.StatusBadRequest, wErr.Code)

	// 4. incidents.go:83 readIncidentBody body read error
	rErrInc := httptest.NewRequest(http.MethodPost, "/datatug/incidents", errReader{})
	wErrInc := httptest.NewRecorder()
	incidentCreateHandler(wErrInc, rErrInc)
	assert.Equal(t, http.StatusBadRequest, wErrInc.Code)

	// 5. incidents.go:55 validateIncidentScope validateScope error
	_, _, err = validateIncidentScope(apicontract.IncidentScope{
		Project: "p", Environment: "e", SecurityContextID: "wrong_id",
	})
	assert.Error(t, err)

	// 6. incidents.go:58 validateIncidentScope scope.Validate error
	_, _, err = validateIncidentScope(apicontract.IncidentScope{
		Project: "", Environment: "e", SecurityContextID: api.SecurityContextID(),
	})
	assert.Error(t, err)

	// 7. incidents.go:62 ResolveIncidentProject error
	_, _, err = validateIncidentScope(apicontract.IncidentScope{
		Project: "nonexistent_proj", Environment: "e", SecurityContextID: api.SecurityContextID(),
	})
	assert.Error(t, err)

	// 8. incidents.go:66 incidentStoreByID error
	dir := t.TempDir()
	pID := "inc_proj"
	filestore.SetProjectPath(pID, dir)
	projJSON := `{"id":"inc_proj","access":"shared","environments":[{"id":"e","sources":[]}]}`
	_ = os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(projJSON), 0644)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	_, _, err = validateIncidentScope(apicontract.IncidentScope{
		Project: pID, Environment: "e", StoreID: "bad_store", SecurityContextID: api.SecurityContextID(),
	})
	assert.Error(t, err)

	// 9. incidents.go:203 incidentShowHandler with invalid at time format
	rShow := httptest.NewRequest(http.MethodGet, "/datatug/incidents/INC-1?project="+pID+"&environment=e&storeId=s&at=not-a-time", nil)
	wShow := httptest.NewRecorder()
	incidentShowHandler(wShow, rShow)
	assert.Equal(t, http.StatusBadRequest, wShow.Code)

	// 10. incidents.go:241 incidentAppendHandler body read error
	rAppErr := httptest.NewRequest(http.MethodPost, "/datatug/incidents/INC-1", errReader{})
	wAppErr := httptest.NewRecorder()
	incidentAppendHandler(wAppErr, rAppErr)
	assert.Equal(t, http.StatusBadRequest, wAppErr.Code)

	// 11. incidents.go:343 incidentMergeHandler body read error
	rMergeErr := httptest.NewRequest(http.MethodPost, "/datatug/incidents/INC-1/merge", errReader{})
	wMergeErr := httptest.NewRecorder()
	incidentMergeHandler(wMergeErr, rMergeErr)
	assert.Equal(t, http.StatusBadRequest, wMergeErr.Code)
}

func TestFinal_ExecRunQuery_Branches(t *testing.T) {
	ctx := context.Background()

	// 1. exec_run_query.go:234 unsupported query type
	dir := t.TempDir()
	pID := fmt.Sprintf("run_proj_%d", time.Now().UnixNano())
	filestore.SetProjectPath(pID, dir)
	fStore, err := filestore.NewStore("files", map[string]string{pID: dir})
	require.NoError(t, err)
	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return fStore, nil }
	defer func() { storage.NewDatatugStore = origDatatugStore }()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{pID: dir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	projJSON := fmt.Sprintf(`{"id":%q,"access":"shared","environments":[{"id":"e"}]}`, pID)
	_ = os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(projJSON), 0644)
	catDir := filepath.Join(dir, "environments", "e", "catalogs")
	_ = os.MkdirAll(catDir, 0755)
	catJSON := `{"id":"c1","driver":"sqlite","path":"s1.db"}`
	_ = os.WriteFile(filepath.Join(catDir, "c1.db.json"), []byte(catJSON), 0644)
	qDir := filepath.Join(dir, "queries")
	_ = os.MkdirAll(qDir, 0755)
	qJSON := `{"id":"graphql_query","type":"graphql"}`
	_ = os.WriteFile(filepath.Join(qDir, "graphql_query.query.json"), []byte(qJSON), 0644)

	req := apicontract.ExecutionRequest{
		Project: pID, Environment: "e", SecurityContextID: api.SecurityContextID(),
		QueryID: "graphql_query",
		Mode:    apicontract.ProvenanceModeLive,
	}
	_, err = computeRunQuery(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "which exec/run_query does not support yet")

	// 2. validateDefaultBindingOrigins branches
	qWithDef := &datatug.QueryDef{
		ID: "q_def",
		Parameters: datatug.Parameters{
			{ID: "p_nodef", Type: "string"},
			{ID: "p_withdef", Type: "string", DefaultValue: "default_val"},
		},
	}
	// parameter has no default
	err = validateDefaultBindingOrigins(apicontract.ExecutionRequest{
		BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "p_nodef", Origin: apicontract.BindingOriginDefault}},
	}, qWithDef)
	assert.Error(t, err)

	// parameter actual is not scalar
	err = validateDefaultBindingOrigins(apicontract.ExecutionRequest{
		BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "p_withdef", Origin: apicontract.BindingOriginDefault}},
		Parameters: map[string]apicontract.TypedValueOrSet{
			"p_withdef": {Set: &apicontract.TypedValueSet{}},
		},
	}, qWithDef)
	assert.Error(t, err)

	// parameter actual does not match
	err = validateDefaultBindingOrigins(apicontract.ExecutionRequest{
		BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "p_withdef", Origin: apicontract.BindingOriginDefault}},
		Parameters: map[string]apicontract.TypedValueOrSet{
			"p_withdef": {Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "wrong_val"}},
		},
	}, qWithDef)
	assert.Error(t, err)

	// 3. bindingsApplied origin == BindingOriginDefault -> BindingOriginEvidenceServerDefault
	bindings := bindingsApplied(apicontract.ExecutionRequest{
		Parameters: map[string]apicontract.TypedValueOrSet{
			"p1": {Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "v1"}},
		},
		BindingOrigins: []apicontract.BindingOriginEntry{
			{ParameterID: "p1", Origin: apicontract.BindingOriginDefault},
		},
	}, nil)
	require.Len(t, bindings, 1)
	assert.Equal(t, apicontract.BindingOriginEvidenceServerDefault, bindings[0].OriginEvidence)
}
