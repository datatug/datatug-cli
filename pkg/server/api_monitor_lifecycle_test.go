package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
)

func TestHTTPAPIMonitorAdmissionAndLifecycle(t *testing.T) {
	previous, listen := apimonitor.Default, listenAndServeFn
	t.Cleanup(func() { apimonitor.Default, listenAndServeFn = previous, listen })
	apimonitor.Default = &apimonitor.Registry{}
	for i := 0; i < apimonitor.MaxServers; i++ {
		apimonitor.Default.NewServer("occupied")
	}
	listenAndServeFn = func(*http.Server) error { t.Fatal("unobserved server started"); return nil }
	s := NewHttpServer()
	if err := s.ServeHTTP(nil, "127.0.0.1", 8989, unrestrictedTestSession(t), api.Capabilities{}); !errors.Is(err, apimonitor.ErrRegistryFull) {
		t.Fatalf("admission: %v", err)
	}
	apimonitor.Default = &apimonitor.Registry{}
	listenAndServeFn = func(server *http.Server) error {
		server.BaseContext(monitorListener{})
		if got := apimonitor.Default.Snapshot(); got.State != "live" || got.BaseURL != "http://127.0.0.1:8989" {
			t.Fatalf("live: %+v", got)
		}
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/?secret=never-retain", nil))
		if got := apimonitor.Default.Snapshot(); got.Total != 1 || got.Active != 0 {
			t.Fatalf("request: %+v", got)
		}
		return http.ErrServerClosed
	}
	if err := s.ServeHTTP(nil, "127.0.0.1", 8989, unrestrictedTestSession(t), api.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	if got := apimonitor.Default.Snapshot(); got.State != "stopped" || got.Total != 1 {
		t.Fatalf("stop: %+v", got)
	}
	// Existing callers may construct an HTTP server without an observer.
	plain := HttpServer{s: &http.Server{}}
	if err := plain.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type monitorListener struct{}

func (monitorListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (monitorListener) Close() error              { return nil }
func (monitorListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8989} }
