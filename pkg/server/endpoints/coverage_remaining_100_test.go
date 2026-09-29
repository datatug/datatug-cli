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
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Part 1: Contract Results, Scope, Error, Decode, Errors

func TestRemaining100_ContractResult(t *testing.T) {
	// row with unconvertible type should return error from toContractRecordset
	res := secureread.Result{
		Columns: []string{"bad_col"},
		Rows: []secureread.Row{
			{Data: map[string]any{"bad_col": struct{}{}}},
		},
	}
	_, err := toContractRecordset(res)
	assert.Error(t, err)
}

func TestRemaining100_ContractScope(t *testing.T) {
	// Environment empty
	err := validateScope(apicontract.Scope{Project: "proj", Environment: "", SecurityContextID: "sec"})
	assert.Error(t, err)
	assert.Equal(t, "environment", err.(*contractError).Field)

	// SecurityContextID empty
	err = validateScope(apicontract.Scope{Project: "proj", Environment: "env", SecurityContextID: ""})
	assert.Error(t, err)
	assert.Equal(t, "securityContextId", err.(*contractError).Field)

	// Scope.Validate() fails (StoreID contains slash)
	err = validateScope(apicontract.Scope{Project: "proj", Environment: "env", SecurityContextID: "sec", StoreID: "invalid/store"})
	assert.Error(t, err)

	// boundLimit > maxResultLimit
	clamped := boundLimit(maxResultLimit + 100)
	assert.Equal(t, maxResultLimit, clamped)
}

func TestRemaining100_ContractError(t *testing.T) {
	// contractError.Error() when Field is empty
	ce := &contractError{Code: "TEST_ERR", Message: "something broke"}
	assert.Equal(t, "TEST_ERR: something broke", ce.Error())

	// newRequestID when randRead fails
	origRandRead := randRead
	randRead = func(b []byte) (int, error) { return 0, errors.New("rand failed") }
	defer func() { randRead = origRandRead }()
	reqID := newRequestID()
	assert.Equal(t, "requestid-unavailable", reqID)

	// requestValidationError with Field == "value"
	ve := apicontract.ValidationError{Field: "value", Message: "invalid value type"}
	ceVe := requestValidationError(&ve)
	assert.Equal(t, apicontract.ErrCodeTypeMismatch, ceVe.Code)

	// httpStatusFor with codeRevisionConflict
	status := httpStatusFor(codeRevisionConflict)
	assert.Equal(t, http.StatusConflict, status)

	// httpStatusFor with unknown custom code (fallback to 500)
	status = httpStatusFor(apicontract.ErrorCode("UNKNOWN_CUSTOM"))
	assert.Equal(t, http.StatusInternalServerError, status)

	// envelope() when RequestID is empty
	ceEmpty := &contractError{Code: "ERR", Message: "msg"}
	env := ceEmpty.envelope()
	assert.NotEmpty(t, env.Error.RequestID)
}

func TestRemaining100_ContractErrors_FailEncoders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// writeContractResponse with failWriter
	fw1 := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	writeContractResponse(fw1, req, nil, map[string]string{"foo": "bar"})

	// writeContractResponseStatus with failWriter
	fw2 := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	writeContractResponseStatus(fw2, req, http.StatusOK, map[string]string{"foo": "bar"})

	// writeContractError with secureread.ErrAccessDenied
	rec3 := httptest.NewRecorder()
	writeContractError(rec3, req, fmt.Errorf("denied: %w", secureread.ErrAccessDenied))
	assert.Equal(t, http.StatusForbidden, rec3.Code)

	// writeContractError with failWriter
	fw4 := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	writeContractError(fw4, req, errors.New("boom"))
}

type testDecodeStruct struct {
	IgnoredField   string `json:"-"`
	unexported     string
	ExportedNoTag  int
	ExportedNormal string `json:"normal"`
}

func TestRemaining100_ContractDecode(t *testing.T) {
	// struct with json:"-", unexported field, exported field with no tag
	fields, ok := jsonObjectFields(reflect.TypeOf(testDecodeStruct{}))
	assert.True(t, ok)
	assert.NotNil(t, fields)

	// foldRune with unicode rune > r: Cyrillic capital A '\u0410'
	folded := foldJSONName("\u0410")
	assert.NotEmpty(t, folded)

	// walkJSONValue with malformed object key (e.g. {"a": 1, invalid})
	dec := json.NewDecoder(strings.NewReader(`{"a": 1, invalid}`))
	_ = walkJSONValue(dec, reflect.TypeOf(map[string]int{}))

	// walkJSONValue with non-string key (e.g. {true: 1})
	dec2 := json.NewDecoder(strings.NewReader(`{true: 1}`))
	err := walkJSONValue(dec2, reflect.TypeOf(map[string]int{}))
	assert.Error(t, err)

	// walkJSONValue with delimiter neither '{' nor '['
	dec3 := json.NewDecoder(strings.NewReader(`[]`))
	_, err = dec3.Token() // consume '['
	require.NoError(t, err)
	err = walkJSONValue(dec3, reflect.TypeOf(0))
	assert.NoError(t, err)
}

// Part 2: Endpoints Handlers

func TestRemaining100_ProjectEndpoints(t *testing.T) {
	// getProjectSummary with invalid storeId
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/summary?id=p&storeId=invalid-store-id", nil)
	getProjectSummary(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// getProjectFull with invalid storeId
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/full?id=p&storeId=invalid-store-id", nil)
	getProjectFull(w2, r2)
	assert.Equal(t, http.StatusBadRequest, w2.Code)
}

func TestRemaining100_CatalogTables(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	_ = configureSemanticSession(t, projectDir, projectID, "admin", []string{"admin"})

	// newProjectRef error: missing project
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/catalog/tables", nil)
	getCatalogTablesHandler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// ref.Validate error: invalid project id
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/catalog/tables?project=invalid+project+id", nil)
	getCatalogTablesHandler(w2, r2)
	assert.Equal(t, http.StatusBadRequest, w2.Code)

	// unknown project (via catalogProjectDir returning false)
	origCatalogProjectDir := catalogProjectDir
	catalogProjectDir = func(id string) (string, bool) { return "", false }
	defer func() { catalogProjectDir = origCatalogProjectDir }()
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest(http.MethodGet, "/catalog/tables?project="+projectID, nil)
	getCatalogTablesHandler(w3, r3)
	assert.Equal(t, http.StatusNotFound, w3.Code)
}

func TestRemaining100_BoardEndpoints(t *testing.T) {
	withFakeHandle(t, func() {
		// getBoard
		w1 := httptest.NewRecorder()
		r1 := httptest.NewRequest(http.MethodGet, "/board?project=p&id=b1", nil)
		getBoard(w1, r1)

		// createBoard
		w2 := httptest.NewRecorder()
		r2 := httptest.NewRequest(http.MethodPost, "/board?project=p", strings.NewReader(`{}`))
		createBoard(w2, r2)

		// saveBoard
		w3 := httptest.NewRecorder()
		r3 := httptest.NewRequest(http.MethodPut, "/board?project=p&id=b1", strings.NewReader(`{}`))
		saveBoard(w3, r3)
	})
}

func TestRemaining100_DBServersEndpoints(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	_ = configureSemanticSession(t, projectDir, projectID, "admin", []string{"admin"})

	// addDbServer with withFakeHandle
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/dbserver?project="+projectID+"&dbserver=s1", strings.NewReader(`{}`))
		addDbServer(w, r)
	})

	// getDbServerSummary with getContextFromRequest error
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/dbserver?project="+projectID+"&dbserver=s1", nil)
	getDbServerSummary(w1, r1)

	// deleteDbServer with deleteDbServerFunc returning error
	savedDel := deleteDbServerFunc
	defer func() { deleteDbServerFunc = savedDel }()
	deleteDbServerFunc = func(ctx context.Context, ref dto.ProjectRef, dbServer datatug.ServerRef) error {
		return errors.New("delete failed")
	}
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return context.Background(), nil
	}
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodDelete, "/dbserver?project="+projectID+"&dbserver=s1", nil)
	deleteDbServer(w2, r2)
	assert.Equal(t, http.StatusInternalServerError, w2.Code)
}

func TestRemaining100_BoardAndEntityAndFolderEndpoints(t *testing.T) {
	withFakeHandle(t, func() {
		// Board
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/board?id=b1", nil)
		getBoard(w, r)

		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodPost, "/board?id=b1", strings.NewReader(`{}`))
		createBoard(w, r)

		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodPut, "/board?id=b1", strings.NewReader(`{}`))
		saveBoard(w, r)

		// Entity
		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodGet, "/entity?id=e1", nil)
		getEntity(w, r)

		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodPut, "/entity?id=e1", strings.NewReader(`{}`))
		saveEntity(w, r)

		// Folder
		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodPost, "/folder", strings.NewReader(`{}`))
		createFolder(w, r)

		// Recordsets
		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodGet, "/recordset/def?id=r1", nil)
		getRecordsetDefinition(w, r)

		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodGet, "/recordset/data?id=r1", nil)
		assert.Panics(t, func() {
			getRecordsetData(w, r)
		})
	})

	// getEntities without invalid project query
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/entities", nil)
	getEntities(w, r)
}

func TestRemaining100_RecordsetsEndpoints(t *testing.T) {
	// getRecordsetsSummary with getContextFromRequest error
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/recordsets", nil)
	getRecordsetsSummary(w1, r1)

	// getRecordsetsSummary without error
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return context.Background(), nil
	}
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/recordsets", nil)
	getRecordsetsSummary(w2, r2)
}

func TestRemaining100_LegacyQueryWriteResponse(t *testing.T) {
	fw := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	lrw := &legacyQueryWriteResponse{ResponseWriter: fw, route: "/test", requestID: "req-1"}
	lrw.replace(http.StatusBadRequest, "BAD_REQ", "failed")
}

// Part 3: Query Endpoints and Semantic Schemas / Columns

func TestRemaining100_QueryEndpoints(t *testing.T) {
	// getQueriesHandler with getContextFromRequest error
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/queries?project=p", nil)
	getQueriesHandler(w1, r1)

	// getQueriesHandler with newProjectRef error
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return context.Background(), nil
	}
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/queries", nil)
	getQueriesHandler(w2, r2)
	assert.Equal(t, http.StatusBadRequest, w2.Code)

	// getAllQueries with ref.Validate error
	_, err := getAllQueries(context.Background(), dto.ProjectRef{})
	assert.Error(t, err)

	// getAllQueries with unknown project
	_, err = getAllQueries(context.Background(), dto.ProjectRef{ProjectID: "unknown-proj-123"})
	assert.Error(t, err)

	// getAllQueries with loadModuleQueries error
	tempDir := t.TempDir()
	queriesDir := filepath.Join(tempDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "bad.query.json"), []byte("invalid json"), 0644))
	filestore.SetProjectPath("test-bad-queries", tempDir)
	_, err = getAllQueries(context.Background(), dto.ProjectRef{ProjectID: "test-bad-queries"})
	assert.Error(t, err)

	// getPersonalQueries with ref.Validate error
	_, err = getPersonalQueries(context.Background(), dto.ProjectRef{})
	assert.Error(t, err)

	// getPersonalQueries with invalid project id (traversal)
	_, err = getPersonalQueries(context.Background(), dto.ProjectRef{ProjectID: "../invalid"})
	assert.Error(t, err)

	// buildQueriesFolderTree with nested path
	q1 := &datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1"}}}
	canonicalIDs := map[*datatug.QueryDef]string{
		q1: "nested/subfolder/q1",
	}
	tree := buildQueriesFolderTree([]*datatug.QueryDef{q1}, canonicalIDs, "root")
	assert.NotNil(t, tree)
	assert.NotEmpty(t, tree.Folders)
}

func TestRemaining100_SemanticColumns(t *testing.T) {
	ctx := context.Background()
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "admin", []string{"admin"})

	// unknown project
	badScope := scope
	badScope.Project = "unknown-project-xyz"
	_, err := computeSemanticColumns(ctx, badScope, apicontract.SourceRef{Source: "chinook", Collection: "Customer"})
	assert.Error(t, err)

	// loadModuleEntities error: corrupt an entity file
	badProjDir := t.TempDir()
	badEntDir := filepath.Join(badProjDir, "entities", "bad")
	require.NoError(t, os.MkdirAll(badEntDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(badEntDir, "bad.entity.json"), []byte("corrupt"), 0644))
	_, err = loadModuleEntities(badProjDir)
	assert.Error(t, err)

	// resolution with regex error in NamePattern (line 80 res.Err != nil)
	badRegexDir := filepath.Join(projectDir, "entities", "BadRegex")
	require.NoError(t, os.MkdirAll(badRegexDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(badRegexDir, "BadRegex.entity.json"), []byte(`{
		"id": "BadRegex",
		"fields": [{"id": "F1", "namePatterns": [{"type": "regexp", "value": "[unclosed-regex"}]}]
	}`), 0644))
	respCols, err := computeSemanticColumns(ctx, scope, apicontract.SourceRef{Source: "chinook", Collection: "Customer"})
	assert.NoError(t, err)
	assert.NotNil(t, respCols)
}

func TestRemaining100_SemanticSchema(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	_ = configureSemanticSession(t, projectDir, projectID, "admin", []string{"admin"})
	projStore, err := api.ProjectStoreFor(projectID)
	require.NoError(t, err)

	// recordset source without recordset file (missing support-notes.recordset.json)
	tempDir := t.TempDir()
	_, err = resolveSource(context.Background(), projStore, tempDir, "local", "support-notes", "support-notes")
	assert.Error(t, err)

	// foreign key with non-empty refTable.Name() in resolveRecordsetSource
	rsPath := filepath.Join(tempDir, "test.recordset.json")
	rsJSON := `{
		"columns": [{"name": "c1", "type": "string"}],
		"primaryKey": {"name": "pk", "columns": ["c1"]},
		"foreignKeys": [{"name": "fk1", "columns": ["c1"], "refTable": {"name": "parent", "schema": "s", "catalog": "c"}}]
	}`
	require.NoError(t, os.WriteFile(rsPath, []byte(rsJSON), 0644))
	resSrc, err := resolveRecordsetSource("", rsPath, "")
	assert.NoError(t, err)
	assert.Len(t, resSrc.Columns, 1)

	// resolveHTTPSource with 0 recordsets
	httpDef := datatug.QueryDef{
		Type:       datatug.QueryTypeHTTP,
		Recordsets: []datatug.RecordsetDefinition{},
	}
	data, _ := json.Marshal(httpDef)
	httpDir := filepath.Join(tempDir, "queries", "http")
	require.NoError(t, os.MkdirAll(httpDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(httpDir, "myhttp.query.json"), data, 0644))
	src, err := resolveHTTPSource(tempDir, "myhttp", "")
	assert.NoError(t, err)
	assert.Empty(t, src.Columns)
}

func TestRemaining100_SemanticProject(t *testing.T) {
	tempDir := t.TempDir()

	// entity file with empty ID sets ID from filename
	entitiesDir := filepath.Join(tempDir, "entities")
	require.NoError(t, os.MkdirAll(entitiesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(entitiesDir, "auto_ent.entity.json"), []byte(`{"title":"Auto"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(entitiesDir, "zebra_ent.entity.json"), []byte(`{"id":"zebra"}`), 0644))
	ents, err := loadModuleEntities(tempDir)
	assert.NoError(t, err)
	assert.Len(t, ents, 2)
	assert.Equal(t, "auto_ent", ents[0].ID)
	assert.Equal(t, "zebra", ents[1].ID)

	// query file with empty ID sets ID from filename
	queriesDir := filepath.Join(tempDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "auto_q.query.json"), []byte(`{"type":"DTQL"}`), 0644))
	queries, canonicalIDs, err := loadModuleQueries(tempDir)
	assert.NoError(t, err)
	assert.Len(t, queries, 1)
	assert.Equal(t, "auto_q", queries[0].ID)
	assert.Equal(t, "auto_q", canonicalIDs[queries[0]])

	// walkJSONFiles with unreadable file
	unreadableFile := filepath.Join(queriesDir, "unreadable.query.json")
	require.NoError(t, os.WriteFile(unreadableFile, []byte(`{}`), 0000))
	defer os.Chmod(unreadableFile, 0644)
	err = walkJSONFiles(queriesDir, ".query.json", func(path string, data []byte) error {
		return nil
	})
	assert.Error(t, err)

	// loadModuleQueries with semanticFilepathRel error
	origRel := semanticFilepathRel
	semanticFilepathRel = func(basepath, targpath string) (string, error) {
		return "", errors.New("rel failed")
	}
	defer func() { semanticFilepathRel = origRel }()
	_, _, err = loadModuleQueries(tempDir)
	assert.Error(t, err)
}

func TestRemaining100_ContractDecode_TokenSeam(t *testing.T) {
	origToken := jsonDecoderToken
	jsonDecoderToken = func(dec *json.Decoder) (json.Token, error) {
		return 12345, nil // non-string token inside '{'
	}
	defer func() { jsonDecoderToken = origToken }()
	var dummy struct{ A string }
	err := decodeContractBody([]byte(`{"a": "b"}`), &dummy)
	assert.Error(t, err)
}

func TestRemaining100_QueryCaptureStore(t *testing.T) {
	origStore := projectStoreFor
	projectStoreFor = func(projectID string) (datatug.ProjectStore, error) {
		return nil, errors.New("store failed")
	}
	defer func() { projectStoreFor = origStore }()

	_, err := captureStoreFor("proj")
	assert.Error(t, err)
}

func TestRemaining100_QueryEndpoints_Errors(t *testing.T) {
	ctx := context.Background()

	// getAllQueries with unknown project
	_, err := getAllQueries(ctx, dto.ProjectRef{ProjectID: "unknown_proj_12345"})
	assert.Error(t, err)

	// getAllQueries with loadModuleQueries error
	tempDir := t.TempDir()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{"corrupt_proj": tempDir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	qDir := filepath.Join(tempDir, "queries")
	require.NoError(t, os.MkdirAll(qDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "bad.query.json"), []byte(`{corrupt`), 0644))
	_, err = getAllQueries(ctx, dto.ProjectRef{ProjectID: "corrupt_proj"})
	assert.Error(t, err)

	// getPersonalQueries with invalid project ID (e.g. traversal path)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{"bad/../proj": tempDir}, api.Capabilities{})
	_, err = getPersonalQueries(ctx, dto.ProjectRef{ProjectID: "bad/../proj"})
	assert.Error(t, err)

	// getPersonalQueries with loadModuleQueries error
	persRoot := t.TempDir()
	t.Setenv("DATATUG_PERSONAL_DIR", persRoot)
	persProjDir := filepath.Join(persRoot, "valid_proj")
	persQDir := filepath.Join(persProjDir, "queries")
	require.NoError(t, os.MkdirAll(persQDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(persQDir, "bad.query.json"), []byte(`{corrupt`), 0644))
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{"valid_proj": tempDir}, api.Capabilities{})
	_, err = getPersonalQueries(ctx, dto.ProjectRef{ProjectID: "valid_proj"})
	assert.Error(t, err)
}

func TestRemaining100_SemanticColumns_Errors(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{"sem_proj": tempDir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	scope := apicontract.Scope{Project: "sem_proj", Environment: "env", SecurityContextID: "sec"}

	// ProjectStoreFor error
	origStore := semanticProjectStoreFor
	semanticProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store error")
	}
	_, err := computeSemanticColumns(ctx, scope, apicontract.SourceRef{Source: "src", Collection: "coll"})
	assert.Error(t, err)
	semanticProjectStoreFor = origStore

	// loadModuleEntities error
	entDir := filepath.Join(tempDir, "entities")
	require.NoError(t, os.MkdirAll(entDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(entDir, "bad.entity.json"), []byte(`{bad`), 0644))
	_, err = computeSemanticColumns(ctx, scope, apicontract.SourceRef{Source: "src", Collection: "coll"})
	assert.Error(t, err)
}

func TestRemaining100_SemanticSchema_Errors(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	// InGitDB source where recordset definition does not exist
	origResolve := apiResolveSource
	apiResolveSource = func(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment, source string) (api.ResolvedSource, error) {
		return api.ResolvedSource{Kind: api.SourceKindInGitDB, ID: "missing_rs", URL: "ingitdb://missing"}, nil
	}
	_, err := resolveSource(ctx, nil, tempDir, "env", "missing_rs", "coll")
	assert.Error(t, err)

	// Unsupported source kind
	apiResolveSource = func(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment, source string) (api.ResolvedSource, error) {
		return api.ResolvedSource{Kind: api.SourceKind("unknown_kind")}, nil
	}
	_, err = resolveSource(ctx, nil, tempDir, "env", "bad_src", "coll")
	assert.Error(t, err)
	apiResolveSource = origResolve

	// resolveSQLSourceURL with open error
	_, err = resolveSQLSourceURL(ctx, "sqlite://nonexistent/path/cannot/open/db.sqlite", "coll")
	assert.Error(t, err)

	// foreignKey with refTable.Name() != "" via recordsetUnmarshalJSON seam
	origUnmarshal := recordsetUnmarshalJSON
	recordsetUnmarshalJSON = func(data []byte, v any) error {
		if def, ok := v.(*datatug.RecordsetDefinition); ok {
			*def = datatug.RecordsetDefinition{
				ForeignKeys: datatug.ForeignKeys{
					&datatug.ForeignKey{
						Name:     "fk1",
						RefTable: datatug.NewTableKey("parent", "schema", "catalog", nil),
					},
				},
			}
			return nil
		}
		return json.Unmarshal(data, v)
	}
	defer func() { recordsetUnmarshalJSON = origUnmarshal }()

	rsFile := filepath.Join(tempDir, "fk.recordset.json")
	require.NoError(t, os.WriteFile(rsFile, []byte(`{}`), 0644))
	resolved, err := resolveRecordsetSource("", rsFile, "coll")
	assert.NoError(t, err)
	assert.Len(t, resolved.Schema.ForeignKeys, 1)
	assert.Equal(t, "parent", resolved.Schema.ForeignKeys[0].RefTable.Name())
}

type testStructuredQuery struct {
	dal.StructuredQuery
	from dal.FromSource
}

func (m testStructuredQuery) From() dal.FromSource { return m.from }

type testFromSource struct {
	dal.FromSource
	base dal.RecordsetSource
}

func (m testFromSource) Base() dal.RecordsetSource { return m.base }

func TestRemaining100_QueryCapture_And_Validate(t *testing.T) {
	// validateCaptureParameters > maxCaptureParameters (32)
	params := make([]capturedParameter, maxCaptureParameters+1)
	for i := range params {
		params[i] = capturedParameter{ID: fmt.Sprintf("p%d", i), Type: "string"}
	}
	_, err := validateCaptureParameters(params)
	assert.Error(t, err)

	// validateCaptureDTQL > maxCaptureDTQLBytes
	huge := strings.Repeat("x", maxCaptureDTQLBytes+10)
	_, err = validateCaptureDTQL(huge, nil)
	assert.Error(t, err)

	// dtqlCollection cases
	qNoFrom := testStructuredQuery{from: nil}
	assert.Equal(t, "", dtqlCollection(qNoFrom))

	cRefVal := dal.NewRootCollectionRef("items", "")
	qCollVal := testStructuredQuery{from: testFromSource{base: cRefVal}}
	assert.Equal(t, "items", dtqlCollection(qCollVal))

	cRef := dal.NewRootCollectionRef("items_ptr", "")
	qColl := testStructuredQuery{from: testFromSource{base: &cRef}}
	assert.Equal(t, "items_ptr", dtqlCollection(qColl))

	var nilCRef *dal.CollectionRef
	qNilColl := testStructuredQuery{from: testFromSource{base: nilCRef}}
	assert.Equal(t, "", dtqlCollection(qNilColl))

	qOtherBase := testStructuredQuery{from: testFromSource{base: dal.QuerySource{}}}
	assert.Equal(t, "", dtqlCollection(qOtherBase))

	// checkCaptureSource with captureProjectStoreFor error
	origStore := captureProjectStoreFor
	captureProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store err")
	}
	defer func() { captureProjectStoreFor = origStore }()
	err = checkCaptureSource(context.Background(), "p", "d", "e", "s")
	assert.Error(t, err)

	// query_capture.go: captureQueryHandler with broken reader
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/queries/capture", &brokenReader{})
	captureQueryHandler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// query_capture.go: authorizeProjectQueryWrite non-access-denied error
	origAuth := authorizeProjectQueryWrite
	authorizeProjectQueryWrite = func(ctx context.Context, projectID, queryID string, operation access.Operations) error {
		return errors.New("db connection failure")
	}
	defer func() { authorizeProjectQueryWrite = origAuth }()
	req := captureQueryRequest{
		Project:     "p",
		Environment: "e",
		Query: capturedQuery{
			ID:     "q1",
			Source: "s",
			DTQL:   "SELECT * FROM coll",
		},
	}
	_, _, err = computeCaptureQuery(context.Background(), req)
	assert.Error(t, err)
}

func TestRemaining100_CompareKey(t *testing.T) {
	ctx := context.Background()

	// mappedCompareKey with CompareSideRecord
	sideRecord := apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord}
	_, err := mappedCompareKey(ctx, "q1", sideRecord)
	assert.Error(t, err)

	// mappedCompareKey with unknown project
	sideUnknownProj := apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, Project: "unknown_proj"}
	_, err = mappedCompareKey(ctx, "q1", sideUnknownProj)
	assert.Error(t, err)

	// project registered in session
	tempDir := t.TempDir()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{"comp_proj": tempDir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	// query not found
	sideValidProj := apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, Project: "comp_proj"}
	_, err = mappedCompareKey(ctx, "nonexistent_query", sideValidProj)
	assert.Error(t, err)

	// compareKeyProjectStoreFor error
	origStore := compareKeyProjectStoreFor
	compareKeyProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store err")
	}
	qDir := filepath.Join(tempDir, "queries")
	require.NoError(t, os.MkdirAll(qDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "myq.query.json"), []byte(`{"id":"myq"}`), 0644))
	_, err = mappedCompareKey(ctx, "myq", sideValidProj)
	assert.Error(t, err)
	compareKeyProjectStoreFor = origStore

	// query recordsets missing primary key
	filestore.SetProjectPath("comp_proj", tempDir)
	qNoPK := datatug.QueryDef{
		ID: "myq",
		Recordsets: []datatug.RecordsetDefinition{{
			Columns: datatug.RecordsetColumnDefs{{Name: "id", Type: "string"}},
		}},
	}
	data, _ := json.Marshal(qNoPK)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "myq.query.json"), data, 0644))
	_, err = mappedCompareKey(ctx, "myq", sideValidProj)
	assert.Error(t, err)

	// query recordset column has no mapped entity field
	qNoMeta := datatug.QueryDef{
		ID: "myq",
		Recordsets: []datatug.RecordsetDefinition{{
			Columns:    datatug.RecordsetColumnDefs{{Name: "id", Type: "string"}},
			RecordsetBaseDef: datatug.RecordsetBaseDef{
				PrimaryKey: &datatug.UniqueKey{Columns: []string{"id"}},
			},
		}},
	}
	data, _ = json.Marshal(qNoMeta)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "myq.query.json"), data, 0644))
	_, err = mappedCompareKey(ctx, "myq", sideValidProj)
	assert.Error(t, err)

	// column not in columns list (findRecordsetColumn returns nil)
	qMissingCol := datatug.QueryDef{
		ID: "myq",
		Recordsets: []datatug.RecordsetDefinition{{
			Columns:    datatug.RecordsetColumnDefs{{Name: "other", Type: "string"}},
			RecordsetBaseDef: datatug.RecordsetBaseDef{
				PrimaryKey: &datatug.UniqueKey{Columns: []string{"id"}},
			},
		}},
	}
	data, _ = json.Marshal(qMissingCol)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "myq.query.json"), data, 0644))
	_, err = mappedCompareKey(ctx, "myq", sideValidProj)
	assert.Error(t, err)

	// resolveCompareKey where left fails, right fails, or not equal
	req := apicontract.CompareRequest{
		QueryID: "myq",
		Left:    sideRecord,
		Right:   sideValidProj,
	}
	_, err = resolveCompareKey(ctx, req)
	assert.Error(t, err)

	req.Left = sideValidProj
	req.Right = sideRecord
	_, err = resolveCompareKey(ctx, req)
	assert.Error(t, err)
}

func TestRemaining100_CompareError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/datatug/compare", nil)

	// failWriter in writeCompareError
	fw := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	writeCompareError(fw, req, errors.New("unclassified"))

	// secureread.ErrAccessDenied in writeCompareError
	w1 := httptest.NewRecorder()
	writeCompareError(w1, req, secureread.ErrAccessDenied)
	assert.Equal(t, http.StatusForbidden, w1.Code)

	// codeInternal in writeCompareError
	w2 := httptest.NewRecorder()
	writeCompareError(w2, req, newContractError(codeInternal, "internal error", ""))
	assert.Equal(t, http.StatusInternalServerError, w2.Code)

	// compareComputationError with cause and Unwrap
	compErr := &compareComputationError{cause: errors.New("cause")}
	assert.Equal(t, "cause", compErr.Error())
	assert.Equal(t, compErr.cause, compErr.Unwrap())
}

func TestRemaining100_CompareHandler(t *testing.T) {
	// GET not allowed
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/datatug/compare", nil)
	compareHandler(Capabilities{})(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Broken body
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/datatug/compare", &brokenReader{})
	compareHandler(Capabilities{})(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Invalid body (fails decodeContractBody)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader(`{invalid`))
	compareHandler(Capabilities{})(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// req.Validate error (empty queryId)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader(`{"securityContextId":"sec"}`))
	compareHandler(Capabilities{})(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Stale context
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader(`{
		"securityContextId":"stale",
		"queryId":"q1",
		"left":{"kind":"scope","storeId":"s","project":"p","environment":"e"},
		"right":{"kind":"scope","storeId":"s","project":"p","environment":"e"}
	}`))
	compareHandler(Capabilities{})(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)

	// Incident with !caps.AllowWrites
	secID := api.SecurityContextID()
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/datatug/compare", strings.NewReader(fmt.Sprintf(`{
		"securityContextId":%q,
		"queryId":"q1",
		"incident":{"storeId":"ops","incidentId":"INC-1"},
		"mutationId":"mut-1",
		"left":{"kind":"scope","storeId":"s","project":"p","environment":"e"},
		"right":{"kind":"scope","storeId":"s","project":"p","environment":"e"}
	}`, secID)))
	compareHandler(Capabilities{AllowWrites: false})(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestRemaining100_ComputeCompareWith_Cases(t *testing.T) {
	ctx := context.Background()

	// req.Validate() error
	_, err := computeCompareWith(ctx, apicontract.CompareRequest{}, compareDependencies{})
	assert.Error(t, err)

	validReq := apicontract.CompareRequest{
		SecurityContextID: "sec",
		QueryID:           "q1",
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &apicontract.ExecutionRef{ExecutionID: "e1"}},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &apicontract.ExecutionRef{ExecutionID: "e2"}},
	}

	// len(req.Key) == 0 && (Left.Kind == CompareSideRecord || Right.Kind == CompareSideRecord)
	_, err = computeCompareWith(ctx, validReq, compareDependencies{})
	assert.Error(t, err)

	// len(req.Key) == 0 && resolveKey == nil
	validScopeReq := apicontract.CompareRequest{
		SecurityContextID: "sec",
		QueryID:           "q1",
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "s", Project: "p", Environment: "e"},
	}
	_, err = computeCompareWith(ctx, validScopeReq, compareDependencies{})
	assert.Error(t, err)

	// resolveKey returns error
	_, err = computeCompareWith(ctx, validScopeReq, compareDependencies{
		resolveKey: func(context.Context, apicontract.CompareRequest) ([]string, error) {
			return nil, errors.New("resolveKey failed")
		},
	})
	assert.Error(t, err)

	// executeSide == nil
	validScopeReqWithKey := validScopeReq
	validScopeReqWithKey.Key = []string{"id"}
	_, err = computeCompareWith(ctx, validScopeReqWithKey, compareDependencies{})
	assert.Error(t, err)

	// executeSide on left returns error
	_, err = computeCompareWith(ctx, validScopeReqWithKey, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{}, errors.New("left failed")
		},
	})
	assert.Error(t, err)

	// executeSide on left returns truncated
	_, err = computeCompareWith(ctx, validScopeReqWithKey, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{truncated: true}, nil
		},
	})
	assert.Error(t, err)

	// executeSide on right returns error
	callCount := 0
	_, err = computeCompareWith(ctx, validScopeReqWithKey, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			callCount++
			if callCount == 1 {
				return compareSideData{truncated: false}, nil
			}
			return compareSideData{}, errors.New("right failed")
		},
	})
	assert.Error(t, err)

	// executeSide on right returns truncated
	callCount = 0
	_, err = computeCompareWith(ctx, validScopeReqWithKey, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			callCount++
			if callCount == 1 {
				return compareSideData{truncated: false}, nil
			}
			return compareSideData{truncated: true}, nil
		},
	})
	assert.Error(t, err)

	// recordsetcompare.Compare returns error (e.g. key column not found in columns)
	limit := 10
	reqWithLimit := validScopeReqWithKey
	reqWithLimit.Limit = &limit
	_, err = computeCompareWith(ctx, reqWithLimit, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{
				recordset: apicontract.Recordset{
					Columns: []apicontract.Column{{Name: "other", Type: "string"}},
				},
			}, nil
		},
	})
	assert.Error(t, err)

	// Incident != nil with preflight returning error or prior conflict
	incRef := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	reqWithInc := reqWithLimit
	reqWithInc.Incident = &incRef
	// preflight error
	_, err = computeCompareWith(ctx, reqWithInc, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
			return nil, errors.New("preflight err")
		},
	})
	assert.Error(t, err)
	// preflight prior conflict
	_, err = computeCompareWith(ctx, reqWithInc, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
			return &incidents.ComparisonRef{}, nil
		},
	})
	assert.Error(t, err)

	// req.Incident != nil && appendRun == nil
	recSet := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("1")}},
	}
	receipt := apicontract.CompareSideReceipt{
		Execution: apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e1"},
		RowCount:  1,
	}
	_, err = computeCompareWith(ctx, reqWithInc, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{recordset: recSet, receipt: receipt}, nil
		},
	})
	assert.Error(t, err)

	// req.Incident != nil && appendRun returns error
	_, err = computeCompareWith(ctx, reqWithInc, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return compareSideData{recordset: recSet, receipt: receipt}, nil
		},
		appendRun: func(context.Context, apicontract.CompareRequest, incidents.ComparisonRef) error {
			return errors.New("appendRun failed")
		},
	})
	assert.Error(t, err)
}

func TestRemaining100_CompareSides(t *testing.T) {
	ctx := context.Background()

	// executeCompareSide with unsupported kind
	_, err := executeCompareSide(ctx, apicontract.CompareRequest{}, apicontract.CompareSideSpec{Kind: "unsupported"})
	assert.Error(t, err)

	// executeCompareSide with CompareSideScope and parameters
	sideScopeParams := apicontract.CompareSideSpec{
		Kind:        apicontract.CompareSideScope,
		StoreID:     "s",
		Project:     "p",
		Environment: "e",
		Parameters:  map[string]apicontract.TypedValue{"foo": apicontract.NewStringValue("bar"), "num": apicontract.NewStringValue("123")},
	}
	req := apicontract.CompareRequest{
		SecurityContextID: "sec",
		QueryID:           "q1",
	}
	_, err = executeCompareSide(ctx, req, sideScopeParams)
	assert.Error(t, err)

	// executeCompareLiveSide with invalid Normalize
	_, err = executeCompareLiveSide(ctx, req, apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope}, nil, nil)
	assert.Error(t, err)

	// loadCompareRecordSide with Execution == nil
	_, err = loadCompareRecordSide(ctx, req, apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: nil})
	assert.Error(t, err)

	// loadCompareRecordSide with unknown execution store
	execRef := apicontract.ExecutionRef{StoreID: "unknown_store", ProjectID: "p", ExecutionID: "e1"}
	_, err = loadCompareRecordSide(ctx, req, apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &execRef})
	assert.Error(t, err)

	// snapshotRetentionLimitations
	rec1 := apicontract.Recordset{Columns: []apicontract.Column{{Name: "a"}, {Name: "b"}}}
	rec2 := apicontract.Recordset{Columns: []apicontract.Column{{Name: "a"}}}
	lims := snapshotRetentionLimitations(rec1, rec2)
	assert.Len(t, lims, 1)
	assert.Equal(t, []string{"b"}, lims[0].HiddenColumns)

	// recordedSnapshotRetentionLimitations
	recExec := apicontract.ExecutionRecord{
		AuthorizedFields: []apicontract.FieldAccessRef{{Column: "b"}},
	}
	limsRecorded := recordedSnapshotRetentionLimitations(recExec, rec2)
	assert.Len(t, limsRecorded, 1)

	// readableCompareSnapshot errors
	_, err = readableCompareSnapshot(apicontract.SnapshotReadResponse{}, executionstore.ErrSnapshotNotFound)
	assert.Error(t, err)

	_, err = readableCompareSnapshot(apicontract.SnapshotReadResponse{}, errors.New("read err"))
	assert.Error(t, err)

	_, err = readableCompareSnapshot(apicontract.SnapshotReadResponse{SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotExpired}}, nil)
	assert.Error(t, err)

	// rows > compareInputMaximumRows
	manyRows := make([][]apicontract.TypedValue, compareInputMaximumRows+1)
	_, err = readableCompareSnapshot(apicontract.SnapshotReadResponse{
		SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotAvailable},
		Recordset:     &apicontract.Recordset{Rows: manyRows},
	}, nil)
	assert.Error(t, err)

	// truncated
	_, err = readableCompareSnapshot(apicontract.SnapshotReadResponse{
		SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotAvailable},
		Recordset:     &apicontract.Recordset{Rows: [][]apicontract.TypedValue{}},
		Truncated:     true,
	}, nil)
	assert.Error(t, err)
}

func TestRemaining100_CompareIncident(t *testing.T) {
	ctx := context.Background()

	// compareIncidentState error cases
	// Incident == nil
	_, _, _, err := compareIncidentState(ctx, apicontract.CompareRequest{})
	assert.Error(t, err)

	// leftProject == ""
	incRef := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	_, _, _, err = compareIncidentState(ctx, apicontract.CompareRequest{Incident: &incRef})
	assert.Error(t, err)

	// leftProject != rightProject
	reqDiffProj := apicontract.CompareRequest{
		Incident: &incRef,
		Left:     apicontract.CompareSideSpec{Project: "p1"},
		Right:    apicontract.CompareSideSpec{Project: "p2"},
	}
	_, _, _, err = compareIncidentState(ctx, reqDiffProj)
	assert.Error(t, err)

	// IncidentStoreByID error
	reqStoreErr := apicontract.CompareRequest{
		Incident: &incRef,
		Left:     apicontract.CompareSideSpec{Project: "p1"},
		Right:    apicontract.CompareSideSpec{Project: "p1"},
	}
	_, _, _, err = compareIncidentState(ctx, reqStoreErr)
	assert.Error(t, err)

	// compareSidePlaceholder with Execution != nil
	execRef := apicontract.ExecutionRef{ExecutionID: "e1"}
	ph := compareSidePlaceholder(reqStoreErr, apicontract.CompareSideSpec{Execution: &execRef})
	assert.Equal(t, "e1", ph.ExecutionID)

	// compareRunDraft validation error (e.g. invalid incident ref)
	_, err = compareRunDraft(incidents.IncidentRef{}, incidents.Actor{}, []string{"id"}, incidents.ComparisonRef{})
	assert.Error(t, err)

	// comparisonFromEvent cases
	// wrong type
	evWrongType := incidents.Event{Type: incidents.EventNoteAdded}
	_, ok := comparisonFromEvent(evWrongType)
	assert.False(t, ok)

	// empty key in payload
	payload, _ := json.Marshal(incidents.CompareRunPayload{Key: []string{}})
	evEmptyKey := incidents.Event{
		Type:      incidents.EventCompareRun,
		Assertion: incidents.Assertion{Kind: incidents.AssertionDeterministicResult},
		Payload:   payload,
	}
	_, ok = comparisonFromEvent(evEmptyKey)
	assert.False(t, ok)

	// ref.Kind != RefCompare
	payloadValid, _ := json.Marshal(incidents.CompareRunPayload{Key: []string{"id"}})
	evWrongRef := incidents.Event{
		Type:      incidents.EventCompareRun,
		Assertion: incidents.Assertion{Kind: incidents.AssertionDeterministicResult},
		Payload:   payloadValid,
		Refs:      []incidents.ArtifactRef{{Kind: "other"}},
	}
	_, ok = comparisonFromEvent(evWrongRef)
	assert.False(t, ok)

	// comparison == nil (no refs)
	evNoRefs := incidents.Event{
		Type:      incidents.EventCompareRun,
		Assertion: incidents.Assertion{Kind: incidents.AssertionDeterministicResult},
		Payload:   payloadValid,
	}
	_, ok = comparisonFromEvent(evNoRefs)
	assert.False(t, ok)
}

func TestRemaining100_CompareFacts(t *testing.T) {
	ctx := context.Background()

	// executeCompareFactsSide with Incident == nil
	_, err := executeCompareFactsSide(ctx, apicontract.CompareRequest{Incident: nil}, apicontract.CompareSideSpec{})
	assert.Error(t, err)

	// visibleFactCohort error: no facts
	view := incidents.IncidentView{}
	_, _, _, _, err = visibleFactCohort(view, apicontract.CompareSideSpec{CohortRole: apicontract.CompareCohortControl})
	assert.Error(t, err)

	// ambiguous entity field
	scope := investigation.ProjectScope{StoreID: "s", ProjectID: "p", Environment: "e"}
	f1 := investigation.Fact{ID: "f1", Entity: "E1", Field: "F1", Enabled: true, Layer: investigation.FactLayerCanonical, Role: investigation.FactRoleAffected, Scope: &scope, Value: investigation.NewStringValue("v1")}
	f2 := investigation.Fact{ID: "f2", Entity: "E2", Field: "F2", Enabled: true, Layer: investigation.FactLayerCanonical, Role: investigation.FactRoleAffected, Scope: &scope, Value: investigation.NewStringValue("v2")}
	viewAmbiguous := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				investigation.VisibleFact(f1),
				investigation.VisibleFact(f2),
			},
		},
	}
	sideSpec := apicontract.CompareSideSpec{StoreID: "s", Project: "p", Environment: "e"}
	_, _, _, _, err = visibleFactCohort(viewAmbiguous, sideSpec)
	assert.Error(t, err)

	// proveNativeFactsBinding: unknown project
	_, err = proveNativeFactsBinding(ctx, "q1", apicontract.CompareSideSpec{Project: "unknown_proj"}, "E", "F")
	assert.Error(t, err)

	// proveNativeFactsBinding: ResolveStoreID error
	tempDir := t.TempDir()
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{"facts_proj": tempDir}, api.Capabilities{})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	_, err = proveNativeFactsBinding(ctx, "q1", apicontract.CompareSideSpec{Project: "facts_proj", StoreID: "bad_store"}, "E", "F")
	assert.Error(t, err)

	// proveNativeFactsBinding: query not found
	filestore.SetProjectPath("facts_proj", tempDir)
	_, err = proveNativeFactsBinding(ctx, "nonexistent_query", apicontract.CompareSideSpec{Project: "facts_proj", StoreID: api.LocalStoreID}, "E", "F")
	assert.Error(t, err)

	// proveNativeFactsBinding: compareFactsProjectStoreFor error
	origStore := compareFactsProjectStoreFor
	compareFactsProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return nil, errors.New("store err")
	}
	qDir := filepath.Join(tempDir, "queries")
	require.NoError(t, os.MkdirAll(qDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "facts_q.query.json"), []byte(`{"id":"facts_q"}`), 0644))
	_, err = proveNativeFactsBinding(ctx, "facts_q", apicontract.CompareSideSpec{Project: "facts_proj", StoreID: api.LocalStoreID}, "E", "F")
	assert.Error(t, err)
	compareFactsProjectStoreFor = origStore

	// queryDef.Type != QueryTypeDTQL
	qSQL := datatug.QueryDef{ID: "facts_q", Type: datatug.QueryTypeSQL}
	data, _ := json.Marshal(qSQL)
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "facts_q.query.json"), data, 0644))
	_, err = proveNativeFactsBinding(ctx, "facts_q", apicontract.CompareSideSpec{Project: "facts_proj", StoreID: api.LocalStoreID}, "E", "F")
	assert.Error(t, err)

	// matchingCohortParameter cases
	// no matching parameter
	qDTQL := &datatug.QueryDef{
		ID:         "facts_q",
		Type:       datatug.QueryTypeDTQL,
		Parameters: []datatug.ParameterDef{{ID: "p1", Meta: &datatug.EntityFieldRef{Entity: "Other", Field: "F"}}},
	}
	_, err = matchingCohortParameter(qDTQL, "E", "F")
	assert.Error(t, err)

	// not multi-value
	qSingleVal := &datatug.QueryDef{
		Parameters: []datatug.ParameterDef{{ID: "p1", IsMultiValue: false, Meta: &datatug.EntityFieldRef{Entity: "E", Field: "F"}}},
	}
	_, err = matchingCohortParameter(qSingleVal, "E", "F")
	assert.Error(t, err)

	// unambiguous multi-value succeeds
	qMultiVal := &datatug.QueryDef{
		Parameters: []datatug.ParameterDef{{ID: "p1", IsMultiValue: true, Meta: &datatug.EntityFieldRef{Entity: "E", Field: "F"}}},
	}
	pID, err := matchingCohortParameter(qMultiVal, "E", "F")
	assert.NoError(t, err)
	assert.Equal(t, "p1", pID)

	// countNativeInBindings cases
	// Comparison: Left and Right matching In condition
	fRef := dal.NewFieldRef("", "my_field")
	pRef := dal.NewParam("my_param")
	cmp := dal.NewComparison(fRef, dal.In, pRef)
	cnt, safe := countNativeInBindings(cmp, "my_field", "my_param")
	assert.Equal(t, 1, cnt)
	assert.True(t, safe)

	// Comparison mentions binding but operator is not In
	cmpEq := dal.NewComparison(fRef, dal.Equal, pRef)
	cnt, safe = countNativeInBindings(cmpEq, "my_field", "my_param")
	assert.False(t, safe)

	// *Comparison
	cnt, safe = countNativeInBindings(&cmp, "my_field", "my_param")
	assert.Equal(t, 1, cnt)
	assert.True(t, safe)

	// GroupCondition Or (not safe)
	grpOr := dal.NewGroupCondition(dal.Or, cmp)
	cnt, safe = countNativeInBindings(grpOr, "my_field", "my_param")
	assert.False(t, safe)

	// GroupCondition And (safe)
	grpAnd := dal.NewGroupCondition(dal.And, cmp)
	cnt, safe = countNativeInBindings(grpAnd, "my_field", "my_param")
	assert.Equal(t, 1, cnt)
	assert.True(t, safe)

	// *GroupCondition
	cnt, safe = countNativeInBindings(&grpAnd, "my_field", "my_param")
	assert.Equal(t, 1, cnt)
	assert.True(t, safe)
}
