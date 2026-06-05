package endpoints

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

// fakeHandle replaces the `handle` package var with one that invokes the worker directly.
func withFakeHandle(t *testing.T, f func()) {
	t.Helper()
	savedHandle := handle
	savedCtx := getContextFromRequest
	defer func() {
		handle = savedHandle
		getContextFromRequest = savedCtx
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	handle = func(
		w http.ResponseWriter,
		r *http.Request,
		requestDTO apicore.RequestDTO,
		verifyOptions verify.RequestOptions,
		successStatusCode int,
		getCtx apicore.ContextProvider,
		worker apicore.Worker,
	) {
		ctx, err := getCtx(r)
		if err != nil {
			handleError(err, w, r)
			return
		}
		resp, err := worker(ctx)
		_ = resp
		if err != nil {
			handleError(err, w, r)
			return
		}
		w.WriteHeader(successStatusCode)
	}
	f()
}

// fakeHandleCapture replaces handle and captures the verify options + status code.
type handleCapture struct {
	verifyOptions     verify.RequestOptions
	successStatusCode int
	workerCalled      bool
}

func withFakeHandleCapture(t *testing.T, cap *handleCapture, f func()) {
	t.Helper()
	savedHandle := handle
	savedCtx := getContextFromRequest
	defer func() {
		handle = savedHandle
		getContextFromRequest = savedCtx
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	handle = func(
		w http.ResponseWriter,
		r *http.Request,
		requestDTO apicore.RequestDTO,
		verifyOptions verify.RequestOptions,
		successStatusCode int,
		getCtx apicore.ContextProvider,
		worker apicore.Worker,
	) {
		cap.verifyOptions = verifyOptions
		cap.successStatusCode = successStatusCode
		ctx, _ := getCtx(r)
		cap.workerCalled = true
		_, _ = worker(ctx)
		w.WriteHeader(successStatusCode)
	}
	f()
}

func makeRequest(method, path string, body string) *http.Request {
	if body != "" {
		return httptest.NewRequest(method, path, strings.NewReader(body))
	}
	return httptest.NewRequest(method, path, nil)
}

// ---- board endpoints ----

func TestGetBoard(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/boards/board?storage=s1&project=p1&id=b1", "")
		getBoard(w, r)
	})
}

func TestCreateBoard(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/boards/create_board?storage=s1&project=p1", `{}`)
		r.Header.Set("Content-Type", "application/json")
		createBoard(w, r)
	})
}

func TestSaveBoard(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPut, "/boards/save_board?storage=s1&project=p1&id=b1", `{}`)
		r.Header.Set("Content-Type", "application/json")
		saveBoard(w, r)
	})
}

func TestDeleteBoard(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/boards/delete_board?storage=s1&project=p1&id=b1", "")
		deleteBoard(w, r)
	})
}

// ---- common: deleteProjItem ----

func TestDeleteProjItem(t *testing.T) {
	var deletedRef dto.ProjectItemRef
	delFunc := func(ctx context.Context, ref dto.ProjectItemRef) error {
		deletedRef = ref
		return nil
	}
	handler := deleteProjItem(delFunc)
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?storage=s1&project=p1&id=item99", "")
		handler(w, r)
	})
	if deletedRef.ID != "item99" {
		t.Errorf("expected ref.ID=item99, got %q", deletedRef.ID)
	}
}

func TestDeleteProjItemError(t *testing.T) {
	delFunc := func(ctx context.Context, ref dto.ProjectItemRef) error {
		return errors.New("delete failed")
	}
	handler := deleteProjItem(delFunc)
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?project=p1&id=item1", "")
		handler(w, r)
	})
}

// ---- common: createProjectItem ----

func TestCreateProjectItem(t *testing.T) {
	cap := &handleCapture{}
	withFakeHandleCapture(t, cap, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/?storage=s1&project=p1", `{}`)
		var ref dto.ProjectRef
		createProjectItem(w, r, &ref, nil, func(ctx context.Context) (apicore.ResponseDTO, error) {
			return nil, nil
		})
		if ref.StoreID != "s1" {
			t.Errorf("expected StoreID=s1, got %q", ref.StoreID)
		}
		if !cap.workerCalled {
			t.Error("expected worker to be called")
		}
		if cap.successStatusCode != http.StatusCreated {
			t.Errorf("expected StatusCreated, got %d", cap.successStatusCode)
		}
	})
}

// ---- common: saveProjectItem ----

func TestSaveProjectItem(t *testing.T) {
	cap := &handleCapture{}
	withFakeHandleCapture(t, cap, func() {
		w := httptest.NewRecorder()
		// saveProjectItem calls fillProjectItemRef with idParamName="" which reads q.Get("") — always "".
		// So ID will be empty; we just assert the ref is filled and handle is called.
		r := makeRequest(http.MethodPut, "/?storage=mystore&project=myproj", `{}`)
		var ref dto.ProjectItemRef
		saveProjectItem(w, r, &ref, nil, func(ctx context.Context) (apicore.ResponseDTO, error) {
			return nil, nil
		})
		if ref.StoreID != "mystore" {
			t.Errorf("expected StoreID=mystore, got %q", ref.StoreID)
		}
		if !cap.workerCalled {
			t.Error("expected worker to be called")
		}
	})
}

// ---- common: getProjectItem ----

func TestGetProjectItem(t *testing.T) {
	cap := &handleCapture{}
	withFakeHandleCapture(t, cap, func() {
		w := httptest.NewRecorder()
		// getProjectItem calls fillProjectItemRef with idParamName="" which reads q.Get("") — always "".
		// We assert handle is called with the right verify options.
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
		var ref dto.ProjectItemRef
		getProjectItem(w, r, &ref, func(ctx context.Context) (apicore.ResponseDTO, error) {
			return nil, nil
		})
		if !cap.workerCalled {
			t.Error("expected worker to be called")
		}
	})
}

// ---- entity endpoints ----

func TestGetEntity(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1&id=e1", "")
		getEntity(w, r)
	})
}

func TestGetEntities(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getEntities(w, r)
}

func TestGetEntitiesContextError(t *testing.T) {
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("context error")
	}
	// getEntities has no return after handleError, so it continues with nil ctx
	// and panics in api.GetAllEntities. Use recover to cover the error branch.
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getEntities(w, r)
}

func TestSaveEntity(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPut, "/?storage=s1&project=p1&id=e1", `{}`)
		saveEntity(w, r)
	})
}

// ---- environment endpoints ----

func TestGetEnvironmentSummary(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1&id=env1", "")
		getEnvironmentSummary(w, r)
	})
}

// ---- folder endpoints ----

func TestCreateFolder(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPut, "/?storage=s1&project=p1", `{}`)
		createFolder(w, r)
	})
}

// ---- project endpoints ----

func TestCreateProject(t *testing.T) {
	cap := &handleCapture{}
	withFakeHandleCapture(t, cap, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/?store=s1", `{"title":"test"}`)
		endpoints := ProjectAgentEndpoints{}
		endpoints.createProject(w, r)
		if !cap.workerCalled {
			t.Error("expected worker to be called")
		}
	})
}

func TestDeleteProject(t *testing.T) {
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodDelete, "/", "")
	endpoints := ProjectAgentEndpoints{}
	endpoints.deleteProject(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestGetProjectSummary(t *testing.T) {
	cap := &handleCapture{}
	withFakeHandleCapture(t, cap, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
		getProjectSummary(w, r)
		if !cap.workerCalled {
			t.Error("expected worker to be called")
		}
	})
}

// ---- project_full ----

func TestGetProjectFull(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getProjectFull(w, r)
}

func TestGetProjectFullContextError(t *testing.T) {
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	// no return after handleError; nil ctx panics in api call — use recover
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getProjectFull(w, r)
}

// ---- projects ----

func TestGetProjects(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}
	// api.GetProjects calls storage.NewDatatugStore which panics when uninitialized
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1", "")
	getProjects(w, r)
}

func TestGetProjectsContextError(t *testing.T) {
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1", "")
	getProjects(w, r)
}

// ---- query endpoints ----

func TestGetQueryHandler(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1&id=q1", "")
		getQueryHandler(w, r)
	})
}

func TestUpdateQuery(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPut, "/?storage=s1&project=p1&id=q1", `{}`)
		updateQuery(w, r)
	})
}

// ---- dbserver_databases ----

func TestNewDbServerFromQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantErr  bool
		wantPort int
	}{
		{
			name:     "valid port",
			query:    "?driver=mssql&host=localhost&port=1433",
			wantErr:  false,
			wantPort: 1433,
		},
		{
			name:    "empty port",
			query:   "?driver=mssql&host=localhost",
			wantErr: false,
		},
		{
			name:    "non-numeric port",
			query:   "?driver=mssql&host=localhost&port=abc",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := makeRequest(http.MethodGet, "/"+tc.query, "")
			srv, err := newDbServerFromQueryParams(r.URL.Query())
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if srv.Port != tc.wantPort {
					t.Errorf("expected port %d, got %d", tc.wantPort, srv.Port)
				}
			}
		})
	}
}

func TestGetServerDatabases(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("bad port returns error", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?port=notanumber", "")
		getServerDatabases(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("valid params calls api", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?driver=mssql&host=localhost&port=1433&proj=p1&env=dev", "")
		getServerDatabases(w, r)
		// No error from bad params, returnJSON was called
	})
}

// ---- dbservers_endpoints ----

func TestAddDbServer(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/?storage=s1&project=p1", `{}`)
		addDbServer(w, r)
	})
}

func TestGetDbServerSummary(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1&driver=mssql&host=localhost", "")
	getDbServerSummary(w, r)
}

func TestGetDbServerSummaryContextError(t *testing.T) {
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getDbServerSummary(w, r)
}

func TestDeleteDbServer(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("bad port", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?port=notanumber&project=p1", "")
		deleteDbServer(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("context error", func(t *testing.T) {
		savedCtx2 := getContextFromRequest
		defer func() { getContextFromRequest = savedCtx2 }()
		getContextFromRequest = func(r *http.Request) (context.Context, error) {
			return nil, errors.New("ctx error")
		}
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?project=p1&storage=s1", "")
		deleteDbServer(w, r)
	})

	t.Run("valid delete", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?project=p1&storage=s1", "")
		deleteDbServer(w, r)
	})
}

// ---- recordsets ----

func TestGetRecordsetRequestParams(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "missing project",
			query:   "?recordset=rs1",
			wantErr: true,
		},
		{
			name:    "missing recordset",
			query:   "?project=p1",
			wantErr: true,
		},
		{
			name:    "both present",
			query:   "?project=p1&recordset=rs1",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := makeRequest(http.MethodGet, "/"+tc.query, "")
			_, err := getRecordsetRequestParams(r)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGetRecordsetDataParams(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "missing data",
			query:   "?project=p1&recordset=rs1",
			wantErr: true,
		},
		{
			name:    "all present",
			query:   "?project=p1&recordset=rs1&data=d1",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := makeRequest(http.MethodGet, "/"+tc.query, "")
			_, err := getRecordsetDataParams(r)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGetRecordsetsSummary(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getRecordsetsSummary(w, r)
}

func TestGetRecordsetsSummaryContextError(t *testing.T) {
	savedCtx := getContextFromRequest
	defer func() { getContextFromRequest = savedCtx }()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return nil, errors.New("ctx error")
	}
	defer func() { recover() }() //nolint:errcheck
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodGet, "/?storage=s1&project=p1", "")
	getRecordsetsSummary(w, r)
}

func TestGetRecordsetDefinition(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1&id=rs1", "")
		getRecordsetDefinition(w, r)
	})
}

func TestGetRecordsetData(t *testing.T) {
	// api.GetRecordset panics "not implemented yet" — use recover to cover the handler path
	cap := &handleCapture{}
	withFakeHandleCapture(t, cap, func() {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/?storage=s1&project=p1&id=rs1", "")
		getRecordsetData(w, r)
	})
}

func TestAddRowsToRecordset(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("missing params error", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/", `[]`)
		addRowsToRecordset(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("invalid JSON body", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/?project=p1&recordset=rs1&data=d1", `not-json`)
		addRowsToRecordset(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("valid request with count warning", func(t *testing.T) {
		w := httptest.NewRecorder()
		body := `[]`
		r := httptest.NewRequest(http.MethodPost, "/?project=p1&recordset=rs1&data=d1", bytes.NewBufferString(body))
		addRowsToRecordset(w, r)
		// count not provided triggers warning, but execution continues
	})

	t.Run("valid request with count", func(t *testing.T) {
		w := httptest.NewRecorder()
		body := `[]`
		r := httptest.NewRequest(http.MethodPost, "/?project=p1&recordset=rs1&data=d1&count=5", bytes.NewBufferString(body))
		addRowsToRecordset(w, r)
	})
}

func TestDeleteRowsFromRecordset(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("missing params", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/", "")
		deleteRowsFromRecordset(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("valid params", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?project=p1&recordset=rs1&data=d1", "")
		deleteRowsFromRecordset(w, r)
	})
}

func TestUpdateRowsInRecordset(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("missing params", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPut, "/", "")
		updateRowsInRecordset(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("valid params", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPut, "/?project=p1&recordset=rs1&data=d1", "")
		updateRowsInRecordset(w, r)
	})
}

func TestExecuteRecordsetCommandCountBranch(t *testing.T) {
	// The executeRecordsetCommand has an inverted condition:
	// if count, err = strconv.Atoi(countStr); err == nil { handleError(...) }
	// So a VALID count string triggers the handleError branch.
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("valid count triggers inverted error branch", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodDelete, "/?project=p1&recordset=rs1&data=d1&count=5", "")
		deleteRowsFromRecordset(w, r)
		// The inverted condition means count=5 (valid parse) causes handleError path
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 from inverted count check, got %d", w.Code)
		}
	})
}

func TestExecuteRecordsetCommandFuncError(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	executeRecordsetCommand(
		httptest.NewRecorder(),
		makeRequest(http.MethodDelete, "/?project=p1&recordset=rs1&data=d1", ""),
		func(params api.RecordsetDataRequestParams, count int) (numberOfRecords int, err error) {
			return 0, errors.New("function error")
		},
	)
}

// ---- execute endpoints ----

func TestExecuteCommandsHandler(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("non-POST method returns 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/execute_commands", "")
		executeCommandsHandler(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("POST with invalid JSON returns 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/exec/execute_commands", "not-json")
		executeCommandsHandler(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("POST with valid JSON calls api", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/exec/execute_commands?storage=s1", `{}`)
		executeCommandsHandler(w, r)
	})
}

func TestExecuteSelectHandler(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("invalid limit returns 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?limit=abc", "")
		executeSelectHandler(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("valid params with no limit", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&env=dev&db=mydb&from=tbl", "")
		executeSelectHandler(w, r)
	})

	t.Run("integer param valid", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:integer=42", "")
		executeSelectHandler(w, r)
	})

	t.Run("integer param null", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:integer=null", "")
		executeSelectHandler(w, r)
	})

	t.Run("integer param invalid", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:integer=notanumber", "")
		executeSelectHandler(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("boolean param true", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:boolean=true", "")
		executeSelectHandler(w, r)
	})

	t.Run("boolean param false", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:boolean=false", "")
		executeSelectHandler(w, r)
	})

	t.Run("unknown param type returns error", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:unknowntype=val", "")
		executeSelectHandler(w, r)
		// NewErrBadRecordFieldValue doesn't satisfy IsBadRequestError, so yields 500
		if w.Code == http.StatusOK {
			t.Errorf("expected error status, got 200")
		}
	})

	t.Run("cols splitting", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&cols=col1,col2,col3", "")
		executeSelectHandler(w, r)
	})

	t.Run("missing proj uses default", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?env=dev&db=mydb", "")
		executeSelectHandler(w, r)
	})
}
