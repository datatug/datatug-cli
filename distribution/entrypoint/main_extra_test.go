//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type closeErrorBody struct{}

func (closeErrorBody) Read([]byte) (int, error) { return 0, io.EOF }

func (closeErrorBody) Close() error { return errors.New("close failed") }

type extraRoundTripFunc func(*http.Request) (*http.Response, error)

func (f extraRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func useDefaultRoundTripper(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestHealthcheckAndCoordinatorAdmission(t *testing.T) {
	if err := healthcheck(nil, "://"); err == nil || err.Error() != "invalid health URL" {
		t.Fatalf("healthcheck with malformed URL = %v; want invalid health URL", err)
	}

	getenv := func(key string) string {
		if key == "DATATUG_DAEMON_ROLE" {
			return "coordinator"
		}
		if key == "DATATUG_HEALTH_URL" {
			return "://"
		}
		return ""
	}
	if err := run([]string{"healthcheck"}, getenv); err == nil || err.Error() != "invalid health URL" {
		t.Fatalf("coordinator run with malformed health URL = %v; want invalid health URL", err)
	}
	if err := run([]string{"healthcheck"}, func(string) string { return "worker" }); err == nil || !strings.Contains(err.Error(), "Coordinator") {
		t.Fatalf("worker healthcheck = %v; want Coordinator-only denial", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	getenv = func(key string) string {
		if key == "DATATUG_DAEMON_ROLE" {
			return "coordinator"
		}
		if key == "DATATUG_HEALTH_URL" {
			return server.URL + "/health"
		}
		return ""
	}
	if err := run([]string{"healthcheck"}, getenv); err != nil {
		t.Fatalf("healthy coordinator run = %v; want success", err)
	}
}

func TestHealthcheckCommandIsHandledBeforeCLIInvocation(t *testing.T) {
	if _, err := invocation([]string{"healthcheck"}, "true"); err == nil || !strings.Contains(err.Error(), "handled by the entrypoint") {
		t.Fatalf("healthcheck CLI invocation = %v; want entrypoint-only rejection", err)
	}
	if got, err := invocation([]string{"daemon", "--", "--run"}, ""); err != nil || strings.Join(got, " ") != "daemon -- --run" {
		t.Fatalf("runner-like positional argument = %q, %v; want unchanged passthrough", got, err)
	}
}

func TestHealthcheckRejectsOutOfRangePorts(t *testing.T) {
	client := &http.Client{Transport: extraRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid port reached the HTTP transport")
		return nil, nil
	})}
	for _, rawURL := range []string{
		"http://127.0.0.1:0/health",
		"http://127.0.0.1:65536/health",
	} {
		if err := healthcheckWithClient(context.Background(), rawURL, client); err == nil || !strings.Contains(err.Error(), "invalid port") {
			t.Errorf("healthcheck(%q) = %v; want invalid port rejection", rawURL, err)
		}
	}
}

func TestHealthcheckUsesRedirectRejectionAndReportsRequestFailure(t *testing.T) {
	var redirectRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectRequests++
		http.Redirect(w, r, "http://127.0.0.1:12346/health", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	if err := healthcheck(context.Background(), server.URL+"/health"); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirected healthcheck = %v; want redirect response rejection", err)
	}
	if redirectRequests != 1 {
		t.Fatalf("redirect handler requests = %d; want one request without following redirect", redirectRequests)
	}

	useDefaultRoundTripper(t, extraRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport unavailable")
	}))
	if err := healthcheck(context.Background(), "http://127.0.0.1:12345/health"); err == nil || err.Error() != "coordinator health request failed" {
		t.Fatalf("healthcheck transport failure = %v; want request failure", err)
	}
}

func TestHealthcheckRedactsRequestConstructionError(t *testing.T) {
	previous := newHealthRequest
	newHealthRequest = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("sensitive request detail")
	}
	t.Cleanup(func() { newHealthRequest = previous })
	err := healthcheckWithClient(context.Background(), "http://127.0.0.1:12345/health", http.DefaultClient)
	if err == nil || err.Error() != "could not create health request" || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("request construction error = %v; want constant redacted error", err)
	}
}

func TestRunPassesCLIArgumentsAndEnvironmentToExec(t *testing.T) {
	previous := execProcess
	var gotPath string
	var gotArgs []string
	var gotEnv []string
	expectedError := errors.New("exec failed")
	execProcess = func(path string, args []string, env []string) error {
		gotPath = path
		gotArgs = append([]string(nil), args...)
		gotEnv = append([]string(nil), env...)
		return expectedError
	}
	t.Cleanup(func() { execProcess = previous })

	args := []string{"--config", "/private/settings.yaml", "daemon", "--run"}
	getenv := func(key string) string {
		if key == "DATATUG_LOCAL_OWNER" {
			return "true"
		}
		return ""
	}
	expectedEnv := os.Environ()
	if err := run(args, getenv); !errors.Is(err, expectedError) {
		t.Fatalf("run exec error = %v; want original exec error", err)
	}
	wantArgs := append([]string{cliPath}, args...)
	if gotPath != cliPath || strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("exec invocation path=%q args=%q; want path=%q args=%q", gotPath, gotArgs, cliPath, wantArgs)
	}
	if strings.Join(gotEnv, "\x00") != strings.Join(expectedEnv, "\x00") {
		t.Fatal("exec invocation environment differs from process environment")
	}
}

func TestHealthcheckReportsResponseCloseFailure(t *testing.T) {
	client := &http.Client{Transport: extraRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: closeErrorBody{}, Request: request}, nil
	})}
	err := healthcheckWithClient(context.Background(), "http://127.0.0.1:12345/health", client)
	if err == nil || err.Error() != "could not close coordinator health response" {
		t.Fatalf("healthcheck close failure = %v; want response close error", err)
	}
}

func TestEntrypointMainReportsAdmissionFailure(t *testing.T) {
	if os.Getenv("DATATUG_ENTRYPOINT_MAIN_HELPER") == "1" {
		os.Args = []string{"datatug-entrypoint", "daemon", "--run"}
		_ = os.Unsetenv("DATATUG_LOCAL_OWNER")
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEntrypointMainReportsAdmissionFailure$")
	cmd.Env = append(os.Environ(), "DATATUG_ENTRYPOINT_MAIN_HELPER=1", "DATATUG_LOCAL_OWNER=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("entrypoint subprocess succeeded; want owner-consent failure (output: %s)", output)
	}
	if !strings.Contains(string(output), "DATATUG_LOCAL_OWNER=true") {
		t.Fatalf("entrypoint subprocess output = %q; want explicit owner-consent error", output)
	}
}
