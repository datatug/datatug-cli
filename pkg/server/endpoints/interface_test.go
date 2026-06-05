package endpoints

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyRequestAccessors(t *testing.T) {
	v := VerifyRequest{
		MinContentLength: 7,
		MaxContentLength: 1024,
		AuthRequired:     true,
	}
	if got := v.MinimumContentLength(); got != 7 {
		t.Errorf("MinimumContentLength() = %v, want 7", got)
	}
	if got := v.MaximumContentLength(); got != 1024 {
		t.Errorf("MaximumContentLength() = %v, want 1024", got)
	}
	if got := v.AuthenticationRequired(); !got {
		t.Errorf("AuthenticationRequired() = %v, want true", got)
	}
}

func TestGetContextFromRequestDefault(t *testing.T) {
	// Save the current value and restore after test
	saved := getContextFromRequest
	defer func() { getContextFromRequest = saved }()

	// Capture the default implementation before RegisterDatatugHandlers overwrites it
	defaultImpl := getContextFromRequest

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := defaultImpl(req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	if ctx != req.Context() {
		t.Error("expected context to equal r.Context()")
	}
}

func TestHandleDefaultPanics(t *testing.T) {
	saved := handle
	defer func() { handle = saved }()

	// Capture the default panic stub
	defaultHandle := handle

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from default handle, got none")
		}
	}()
	defaultHandle(nil, nil, nil, nil, 0, nil, nil)
}

type stubRouter struct {
	method  string
	path    string
	handler http.HandlerFunc
}

func (s *stubRouter) HandlerFunc(method, path string, handler http.HandlerFunc) {
	s.method = method
	s.path = path
	s.handler = handler
}

func TestRouteWithNilWrapper(t *testing.T) {
	r := &stubRouter{}
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	route(r, nil, http.MethodGet, "/test", handler)
	if r.path != "/test" {
		t.Errorf("expected path '/test', got %q", r.path)
	}
	r.handler(httptest.NewRecorder(), req)
	if !called {
		t.Error("expected handler to be called")
	}
}

func TestRouteWithWrapper(t *testing.T) {
	r := &stubRouter{}
	wrapperCalled := false
	wrap := func(f http.HandlerFunc) http.HandlerFunc {
		wrapperCalled = true
		return f
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	route(r, wrap, http.MethodGet, "/wrapped", handler)
	if !wrapperCalled {
		t.Error("expected wrapper to be called")
	}
	if r.path != "/wrapped" {
		t.Errorf("expected path '/wrapped', got %q", r.path)
	}
}
