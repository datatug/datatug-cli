package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func saveLocalQueryRequest(projectID, branch, head string) dto.SaveQueryRequest {
	var query datatug.QueryDefWithFolderPath
	query.FolderPath = datatug.RootSharedFolderName
	query.ID, query.Title, query.Type, query.Text = "customer", "Customers", datatug.QueryTypeDTQL, "SELECT CustomerId FROM Customer"
	query.Federation = &datatug.QueryFederation{OVDBBaseURL: "https://demodb.dev/ovdb", Tables: []datatug.QueryFederationTable{{Database: "chinook", Name: "Customer", Fields: []string{"CustomerId"}}}}
	return dto.SaveQueryRequest{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: projectID}, Branch: branch, ExpectedBranchHead: head, OperationID: "save-customer-1", IfNoneMatch: true, Query: query}
}

func postLocalSave(t *testing.T, request dto.SaveQueryRequest) (*httptest.ResponseRecorder, dto.SaveQueryResponse) {
	t.Helper()
	body, err := json.Marshal(request)
	require.NoError(t, err)
	httpRequest := httptest.NewRequest(http.MethodPost, "/datatug/queries/save_query", bytes.NewReader(body))
	w := httptest.NewRecorder()
	saveProjectQueryHandler(w, httpRequest)
	var response dto.SaveQueryResponse
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	}
	return w, response
}

func TestLocalSaveQueryStagesPairAndRejectsStaleOrChangedReplay(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	// A preexisting unrelated file remains unstaged after the query save.
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "unrelated.txt"), []byte("leave me alone"), 0o600))
	w, created := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotEmpty(t, created.Revision)
	require.Equal(t, head, created.BranchHead)
	require.Equal(t, request.Query.Federation, created.Query.Federation)
	staged := runGit(t, projectDir, "diff", "--cached", "--name-only")
	require.Contains(t, staged, "queries/customer.query.json")
	require.Contains(t, staged, "queries/customer.query.dtql")
	require.NotContains(t, staged, "unrelated.txt")
	metadata, err := os.ReadFile(filepath.Join(projectDir, "queries", "customer.query.json"))
	require.NoError(t, err)
	require.Contains(t, string(metadata), `"ovdbBaseUrl"`)
	require.NotContains(t, string(metadata), request.Query.Text)
	body, err := os.ReadFile(filepath.Join(projectDir, "queries", "customer.query.dtql"))
	require.NoError(t, err)
	require.Equal(t, request.Query.Text, string(body))
	// Same operation returns the original result, not a second write.
	replay, same := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, created.Revision, same.Revision)
	changed := request
	changed.Query.Text = "SELECT FirstName FROM Customer"
	conflict, _ := postLocalSave(t, changed)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.Contains(t, conflict.Body.String(), "OPERATION_CONFLICT")
	changed.OperationID = "save-customer-2"
	changed.IfNoneMatch, changed.IfMatch = false, created.Revision
	update, updated := postLocalSave(t, changed)
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	require.NotEqual(t, created.Revision, updated.Revision)
	changed.OperationID = "save-customer-3"
	changed.Query.Text = "SELECT CustomerId, FirstName FROM Customer"
	stale, _ := postLocalSave(t, changed)
	require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	require.Contains(t, stale.Body.String(), "QUERY_REVISION_CONFLICT")
	changed.IfMatch = updated.Revision
	changed.ExpectedBranchHead = strings.Repeat("0", len(head))
	staleHead, _ := postLocalSave(t, changed)
	require.Equal(t, http.StatusConflict, staleHead.Code, staleHead.Body.String())
	require.Contains(t, staleHead.Body.String(), "BRANCH_HEAD_CONFLICT")
}

func TestLocalSaveQueryReadOnlyActorCannotReplay(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	w, _ := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// Reconfigure the same served project to a read-only principal. A stored
	// receipt must not become a permission bypass; this route still checks the
	// current principal before reading it.
	// The policy fixture's support role is read-only.
	session, err := secureread.NewSession(secureread.SessionOptions{As: "sam", Roles: []string{"support"}, PoliciesDir: projectDir + "/policies"})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{scope.Project: projectDir}, api.Capabilities{AllowWrites: true})
	replay, _ := postLocalSave(t, request)
	require.Equal(t, http.StatusForbidden, replay.Code, replay.Body.String())
	require.Contains(t, replay.Body.String(), "ACCESS_DENIED")
}

func TestLocalSaveQueryRejectsUnsupportedFederationMetadataWithoutWriting(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	// The browser has richer federation fields than this Core version. A
	// strict transport must refuse them, never acknowledge a lossy save.
	body := bytes.Replace(encoded, []byte(`"ovdbBaseUrl":`), []byte(`"bounds":{"maxRows":10},"ovdbBaseUrl":`), 1)
	require.NotEqual(t, string(encoded), string(body))
	w := httptest.NewRecorder()
	saveProjectQueryHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/queries/save_query", bytes.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.NoFileExists(t, filepath.Join(projectDir, "queries", "customer.query.json"))
	require.NoFileExists(t, filepath.Join(projectDir, "queries", "customer.query.dtql"))
}

func TestLocalQueryRevisionReturnsPersistedSourceAndBody(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	w, saved := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	readURL := "/datatug/queries/query_revision?storage=local&project=" + scope.Project + "&id=customer&branch=" + branch
	read := httptest.NewRecorder()
	getProjectQueryRevisionHandler(read, httptest.NewRequest(http.MethodGet, readURL, nil))
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	var loaded dto.GetQueryResponse
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &loaded))
	require.Equal(t, saved.Revision, loaded.Revision)
	require.Equal(t, saved.Query.Text, loaded.Query.Text)
	require.Equal(t, saved.Query.Federation, loaded.Query.Federation)
	capabilities := httptest.NewRecorder()
	projectCapabilitiesHandler(capabilities, httptest.NewRequest(http.MethodGet, "/datatug/projects/capabilities?storage=local&project="+scope.Project, nil))
	require.Equal(t, http.StatusOK, capabilities.Code, capabilities.Body.String())
	var caps dto.ProjectCapabilities
	require.NoError(t, json.Unmarshal(capabilities.Body.Bytes(), &caps))
	require.True(t, caps.QueryRead && caps.QuerySave)
	require.False(t, caps.Branches || caps.BranchMerge || caps.PullCurrent || caps.PushCurrent)
}

func TestLocalQueryReadRoutesRefuseMissingServingPrincipal(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD")
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{scope.Project: projectDir}, api.Capabilities{})
	read := httptest.NewRecorder()
	getProjectQueryRevisionHandler(read, httptest.NewRequest(http.MethodGet,
		"/datatug/queries/query_revision?storage=local&project="+scope.Project+"&id=customer&branch="+branch, nil))
	require.Equal(t, http.StatusForbidden, read.Code, read.Body.String())
	capabilities := httptest.NewRecorder()
	projectCapabilitiesHandler(capabilities, httptest.NewRequest(http.MethodGet,
		"/datatug/projects/capabilities?storage=local&project="+scope.Project, nil))
	require.Equal(t, http.StatusForbidden, capabilities.Code, capabilities.Body.String())
}

func TestLocalSaveQueryTypeChangeStagesOldSidecarRemoval(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	create := saveLocalQueryRequest(scope.Project, branch, head)
	w, created := postLocalSave(t, create)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	update := create
	update.OperationID = "save-customer-sql"
	update.IfNoneMatch, update.IfMatch = false, created.Revision
	update.Query.Type, update.Query.Text = datatug.QueryTypeSQL, "SELECT 1"
	w, _ = postLocalSave(t, update)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoFileExists(t, filepath.Join(projectDir, "queries", "customer.query.dtql"))
	require.FileExists(t, filepath.Join(projectDir, "queries", "customer.query.sql"))
	staged := runGit(t, projectDir, "diff", "--cached", "--name-only")
	require.Contains(t, staged, "queries/customer.query.json")
	require.Contains(t, staged, "queries/customer.query.sql")
	require.NotContains(t, staged, "queries/customer.query.dtql")
}

func TestLocalSaveQueryRefusesForgedInternalActorScope(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	actorScope, err := api.SecureProjectMutationScope(request.ProjectRef, branch, "save-query")
	require.NoError(t, err)
	actorScope.ActorID = "another-actor"
	_, err = (api.LocalProjectQueryAdapter{}).SaveQuery(context.Background(), actorScope, request)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(projectDir, "queries", "customer.query.json"))
}

func TestLocalSaveQueryOperationIDCannotMoveToAnotherServedRepository(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	paths := map[string]string{"project-one": t.TempDir(), "project-two": t.TempDir()}
	for _, path := range paths {
		require.NoError(t, os.WriteFile(filepath.Join(path, "README.md"), []byte("fixture"), 0o600))
		gitInit(t, path)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	firstDir, secondDir := paths["project-one"], paths["project-two"]
	first := saveLocalQueryRequest("project-one", runGit(t, firstDir, "symbolic-ref", "--short", "HEAD"), runGit(t, firstDir, "rev-parse", "HEAD"))
	w, _ := postLocalSave(t, first)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	second := saveLocalQueryRequest("project-two", runGit(t, secondDir, "symbolic-ref", "--short", "HEAD"), runGit(t, secondDir, "rev-parse", "HEAD"))
	w, _ = postLocalSave(t, second)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "OPERATION_CONFLICT")
	require.NoFileExists(t, filepath.Join(secondDir, "queries", "customer.query.json"))
}

func TestLocalSaveQueryRetryRecoversPairWrittenBeforeStaging(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	indexLock := filepath.Join(runGit(t, projectDir, "rev-parse", "--absolute-git-dir"), "index.lock")
	require.NoError(t, os.WriteFile(indexLock, []byte("held by another Git writer"), 0o600))
	w, _ := postLocalSave(t, request)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "OUTCOME_UNCERTAIN")
	// Core committed the exact pair; only Git staging was interrupted.
	require.FileExists(t, filepath.Join(projectDir, "queries", "customer.query.json"))
	require.FileExists(t, filepath.Join(projectDir, "queries", "customer.query.dtql"))
	require.NoError(t, os.Remove(indexLock))
	w, recovered := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotEmpty(t, recovered.Revision)
	require.Equal(t, request.Query.Text, recovered.Query.Text)
	require.Contains(t, runGit(t, projectDir, "diff", "--cached", "--name-only"), "queries/customer.query.dtql")
	w, replay := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, recovered.Revision, replay.Revision)
}

func TestLocalSaveQueryRetryRefusesForeignBytesAfterInterruptedStaging(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	indexLock := filepath.Join(runGit(t, projectDir, "rev-parse", "--absolute-git-dir"), "index.lock")
	require.NoError(t, os.WriteFile(indexLock, []byte("held"), 0o600))
	w, _ := postLocalSave(t, request)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.NoError(t, os.Remove(indexLock))
	bodyPath := filepath.Join(projectDir, "queries", "customer.query.dtql")
	const foreign = "SELECT FirstName FROM Customer"
	require.NoError(t, os.WriteFile(bodyPath, []byte(foreign), 0o600))
	w, _ = postLocalSave(t, request)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "OUTCOME_UNCERTAIN")
	body, err := os.ReadFile(bodyPath)
	require.NoError(t, err)
	require.Equal(t, foreign, string(body))
}

func TestLocalSaveQueryRetryInFolder(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "queries", "invoices"), 0o700))
	request := saveLocalQueryRequest(scope.Project, branch, head)
	request.Query.FolderPath = "invoices"
	request.OperationID = "save-folder-query"
	w, created := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotEmpty(t, created.Revision)
	require.FileExists(t, filepath.Join(projectDir, "queries", "invoices", "customer.query.json"))
	w, replay := postLocalSave(t, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, created.Revision, replay.Revision)
}

type editAfterPutStore struct {
	storage.Store
	afterPut func()
}

func (s editAfterPutStore) GetProjectStore(id string) datatug.ProjectStore {
	return editAfterPutProject{ProjectStore: s.Store.GetProjectStore(id), afterPut: s.afterPut}
}

type editAfterPutProject struct {
	datatug.ProjectStore
	afterPut func()
}

func (p editAfterPutProject) LoadQueryRevision(ctx context.Context, id string, options ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	return p.ProjectStore.(datatug.RevisionedQueriesStore).LoadQueryRevision(ctx, id, options...)
}

func (p editAfterPutProject) PutQuery(ctx context.Context, query *datatug.QueryDefWithFolderPath, condition datatug.QueryWriteCondition) (*datatug.StoredQuery, error) {
	stored, err := p.ProjectStore.(datatug.RevisionedQueriesStore).PutQuery(ctx, query, condition)
	if err == nil {
		p.afterPut()
	}
	return stored, err
}

func (p editAfterPutProject) DeleteQueryRevision(ctx context.Context, id string, revision datatug.QueryRevision) error {
	return p.ProjectStore.(datatug.RevisionedQueriesStore).DeleteQueryRevision(ctx, id, revision)
}

func TestLocalSaveQueryNeverStagesForeignEditAfterPut(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch, head := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD"), runGit(t, projectDir, "rev-parse", "HEAD")
	request := saveLocalQueryRequest(scope.Project, branch, head)
	original := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = original })
	const foreign = "SELECT FirstName FROM Customer -- external editor"
	storage.NewDatatugStore = func(id string) (storage.Store, error) {
		base, err := original(id)
		return editAfterPutStore{Store: base, afterPut: func() {
			require.NoError(t, os.WriteFile(filepath.Join(projectDir, "queries", "customer.query.dtql"), []byte(foreign), 0o600))
		}}, err
	}
	w, _ := postLocalSave(t, request)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "OUTCOME_UNCERTAIN")
	require.Equal(t, request.Query.Text, runGit(t, projectDir, "show", ":queries/customer.query.dtql"))
	body, err := os.ReadFile(filepath.Join(projectDir, "queries", "customer.query.dtql"))
	require.NoError(t, err)
	require.Equal(t, foreign, string(body))
	retry, _ := postLocalSave(t, request)
	require.Equal(t, http.StatusConflict, retry.Code, retry.Body.String())
}
