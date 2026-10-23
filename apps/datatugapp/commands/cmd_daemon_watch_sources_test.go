package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestWatchExecutionPreservesPolicyAndEvidenceBoundaries(t *testing.T) {
	cfg, id := watchCommandFixture(t)
	cfg.Runner.ProjectDir = projectWithPolicies(t)
	if _, err := configureWatchExecution(cfg, id+"-secured"); err == nil {
		t.Fatal("secured asset permission bypassed")
	}
	if err := os.WriteFile(filepath.Join(cfg.Runner.ProjectDir, "policies", "policy.yaml"), []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configureWatchExecution(cfg, id+"-invalid-policy"); err == nil {
		t.Fatal("invalid policy ignored")
	}
	cfg, _ = watchCommandFixture(t)
	cfg.Runner.EvidenceDir = ""
	original := watchUserHomeDir
	defer func() { watchUserHomeDir = original }()
	watchUserHomeDir = func() (string, error) { return "", errors.New("home unavailable") }
	if _, err := configureWatchExecution(cfg, id+"-no-home"); err == nil {
		t.Fatal("missing private evidence location ignored")
	}
	home := t.TempDir()
	watchUserHomeDir = func() (string, error) { return home, nil }
	cleanup, err := configureWatchExecution(cfg, id+"-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = storage.NewDatatugStore("local"); err != nil {
		t.Fatal(err)
	}
	cleanup()
	cfg.Runner.EvidenceDir = cfg.Runner.ProjectDir
	if _, err = configureWatchExecution(cfg, id+"-unsafe-evidence"); err == nil {
		t.Fatal("private evidence stored inside shared project")
	}
}

func TestWatchProjectManifestBoundsAndExplicitTrustOverride(t *testing.T) {
	if _, _, _, err := watchProjectCoordination(t.TempDir()); err == nil {
		t.Fatal("missing manifest accepted")
	}
	cfg, _ := watchCommandFixture(t)
	path := filepath.Join(cfg.Runner.ProjectDir, storage.ProjectSummaryFileName)
	body, _ := os.ReadFile(path)
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	fields["large"] = strings.Repeat("x", 1024*1024)
	body, _ = json.Marshal(fields)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := watchProjectCoordination(cfg.Runner.ProjectDir); err == nil {
		t.Fatal("oversized manifest accepted")
	}
	if _, err := resolveWatchCoordinator("https://trusted.example", "https://trusted.example", "http://public.example"); err == nil {
		t.Fatal("insecure explicit override accepted")
	}
}

func TestWatchCoordinatorReceivesCurrentDeploymentEntitlement(t *testing.T) {
	cfg, _ := watchCommandFixture(t)
	originalOpen, originalServe := watchOpenCoordinatorStore, watchServeHTTP
	defer func() { watchOpenCoordinatorStore, watchServeHTTP = originalOpen, originalServe }()
	watchOpenCoordinatorStore = func(dsn string, config watch.Config) (*watch.Store, error) {
		if !config.IsPaidAccount("alice") || config.IsPaidAccount("runner") {
			t.Error("account authority was not limited to paid user identities")
		}
		return watch.NewSQLiteStore(dsn, config)
	}
	watchServeHTTP = func(_ *http.Server, l net.Listener, _, _ string) error { _ = l.Close(); return http.ErrServerClosed }
	if err := invokeWatchCommand(t, context.Background(), cfg, "--coordinate"); err != nil {
		t.Fatal(err)
	}
}

func TestWatchCombinedDaemonRegistersThroughHTTP(t *testing.T) {
	cfg, id := watchCommandFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	endpoint := "http://" + listener.Addr().String()
	cfg.Listen = listener.Addr().String()
	cfg.Runner.CoordinatorEndpoint = endpoint
	body, _ := os.ReadFile(filepath.Join(cfg.Runner.ProjectDir, storage.ProjectSummaryFileName))
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	fields["coordination"] = map[string]string{"endpoint": endpoint, "namespace": "northwind"}
	body, _ = json.Marshal(fields)
	if err = os.WriteFile(filepath.Join(cfg.Runner.ProjectDir, storage.ProjectSummaryFileName), body, 0600); err != nil {
		t.Fatal(err)
	}
	oldListen := watchListen
	defer func() { watchListen = oldListen }()
	watchListen = func(string, string) (net.Listener, error) { return listener, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- invokeWatchCommand(t, ctx, cfg, "--coordinate", "--run") }()
	deadline := time.Now().Add(3 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest("GET", endpoint+"/v1/watch/status", nil)
		request.Header.Set("Authorization", "Bearer "+os.Getenv("WATCH_COMMAND_USER"))
		response, e := (&http.Client{Timeout: 200 * time.Millisecond}).Do(request)
		if e == nil {
			var status watch.Status
			_ = json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			if len(status.Runners) == 1 && status.Runners[0].Project == id {
				registered = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("combined daemon bypassed or failed HTTP registration")
	}
}
