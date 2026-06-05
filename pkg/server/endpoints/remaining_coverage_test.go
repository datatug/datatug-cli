package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
)

// TestRegisterRoutesNilPanic covers the panic("router == nil") branch in registerRoutes.
func TestRegisterRoutesNilPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil router")
		}
	}()
	registerRoutes("", nil, nil, false)
}

// TestCreateQueryVar covers the createQuery package-level var.
func TestCreateQueryVar(t *testing.T) {
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodPost, "/?storage=s1&project=p1", `{}`)
		createQuery(w, r)
	})
}

// TestDeleteDbServerSuccess covers the returnJSON success line in deleteDbServer
// by stubbing the deleteDbServerFunc seam to return nil.
func TestDeleteDbServerSuccess(t *testing.T) {
	savedCtx := getContextFromRequest
	savedJSON := returnJSON
	savedDel := deleteDbServerFunc
	defer func() {
		getContextFromRequest = savedCtx
		returnJSON = savedJSON
		deleteDbServerFunc = savedDel
	}()
	getContextFromRequest = func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}
	deleteDbServerFunc = func(_ context.Context, _ dto.ProjectRef, _ datatug.ServerRef) error {
		return nil
	}
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodDelete, "/?project=p1&storage=s1", "")
	deleteDbServer(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestExecuteSelectNumberStringParams covers the empty "number" and "string" cases.
func TestExecuteSelectNumberStringParams(t *testing.T) {
	savedJSON := returnJSON
	defer func() { returnJSON = savedJSON }()
	returnJSON = func(w http.ResponseWriter, r *http.Request, statusCode int, err error, content interface{}) {
		w.WriteHeader(statusCode)
	}

	t.Run("number param", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:number=3.14", "")
		executeSelectHandler(w, r)
	})

	t.Run("string param", func(t *testing.T) {
		defer func() { recover() }() //nolint:errcheck
		w := httptest.NewRecorder()
		r := makeRequest(http.MethodGet, "/exec/select?proj=p1&p:mykey:string=hello", "")
		executeSelectHandler(w, r)
	})
}

// TestReturnJSONOptionsRequestPanics covers the panic on OPTIONS requests in returnJSON.
func TestReturnJSONOptionsRequestPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for OPTIONS request")
		}
	}()
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodOptions, "/", "")
	returnJSON(w, r, http.StatusOK, nil, map[string]string{"k": "v"})
}

// TestReturnJSONOriginHeader covers the Access-Control-Allow-Origin branch in returnJSON (non-GET).
func TestReturnJSONOriginHeader(t *testing.T) {
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodPost, "/", "")
	r.Header.Set("Origin", "https://datatug.app")
	returnJSON(w, r, http.StatusOK, nil, map[string]string{"k": "v"})
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://datatug.app" {
		t.Errorf("expected Access-Control-Allow-Origin header, got %q", got)
	}
}

// TestReturnJSONNonGETEncodeError covers the json.Encode error branch in the non-GET path.
func TestReturnJSONNonGETEncodeError(t *testing.T) {
	w := httptest.NewRecorder()
	r := makeRequest(http.MethodPost, "/", "")
	// A channel cannot be JSON-encoded — triggers the error log branch
	returnJSON(w, r, http.StatusOK, nil, make(chan int))
}

// TestWriteResponseJSONWriteError covers the w.Write error branch (line 57).
// We use a responseWriter that fails on Write after headers are sent.
type failWriter struct {
	httptest.ResponseRecorder
}

func (f *failWriter) Write(b []byte) (int, error) {
	return 0, http.ErrHandlerTimeout
}

func TestWriteResponseJSONWriteError(t *testing.T) {
	w := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	r := makeRequest(http.MethodGet, "/", "")
	writeResponseJSONForGetRequestWithCacheControl(w, r, http.StatusOK, map[string]string{"k": "v"})
}

// TestHandleErrorEncodeError covers the encoder.Encode error branch in handleError.
// Use a failing writer to trigger encode failure.
func TestHandleErrorEncodeError(t *testing.T) {
	w := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	r := makeRequest(http.MethodGet, "/", "")
	// generic error triggers 500 + encode attempt which will fail on failWriter
	handleError(errForTest, w, r)
}

var errForTest = &alwaysError{}

type alwaysError struct{}

func (e *alwaysError) Error() string { return "test error" }
