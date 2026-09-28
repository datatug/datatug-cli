package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestHttpServer_Shutdown_NilServer(t *testing.T) {
	s := HttpServer{}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on nil server returned error: %v", err)
	}
}

func TestRootHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	root(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("root status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DataTug API") {
		t.Fatalf("unexpected root body: %s", body)
	}
}

func TestGlobalOptionsHandler_MissingHeaders(t *testing.T) {
	// Missing origin
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	rec := httptest.NewRecorder()
	globalOptionsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	// Missing access control request method
	req = httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec = httptest.NewRecorder()
	globalOptionsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestNewDatatugStoreFactory_StoreError(t *testing.T) {
	orig := filestoreNewStore
	defer func() { filestoreNewStore = orig }()

	filestoreNewStore = func(id string, pathsByID map[string]string) (storage.Store, error) {
		return nil, errors.New("boom")
	}

	factory := newDatatugStoreFactory(map[string]string{"p": "/tmp"})
	if _, err := factory("p"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestHttpServer_ServeHTTP_EvidenceError(t *testing.T) {
	s := NewHttpServer()
	session := unrestrictedTestSession(t)
	caps := api.Capabilities{
		IncidentStores: []incidentstore.ConfiguredStore{
			{StoreID: "bad", Repository: ""}, // triggers Validate() failure
		},
	}
	err := s.ServeHTTP(map[string]string{"p": "/tmp"}, "", 0, session, caps)
	if err == nil || !strings.Contains(err.Error(), "configure execution evidence") {
		t.Fatalf("expected configure execution evidence error, got %v", err)
	}
}

func TestHttpServer_ServeHTTP_Defaults(t *testing.T) {
	origListen := listenAndServeFn
	defer func() { listenAndServeFn = origListen }()

	var capturedServer *http.Server
	listenAndServeFn = func(srv *http.Server) error {
		capturedServer = srv
		return nil
	}

	s := NewHttpServer()
	session := unrestrictedTestSession(t)
	err := s.ServeHTTP(map[string]string{}, "", 0, session, api.Capabilities{})
	if err != nil {
		t.Fatalf("ServeHTTP failed: %v", err)
	}
	if capturedServer == nil {
		t.Fatal("listenAndServeFn was not called")
	}
	if capturedServer.Addr != "localhost:8989" {
		t.Fatalf("captured Addr = %q, want localhost:8989", capturedServer.Addr)
	}
}
