package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
)

func cloudCommandConfig(t *testing.T) watchdaemon.Config {
	t.Helper()
	cfg, _ := watchCommandFixture(t)
	cfg.Runner.Cloud = &watchdaemon.CloudRunnerConfig{SpaceID: "northwind", ProjectID: "remote-project", SourceBindings: map[string]string{strings.Repeat("a", 64): "https://approved.example"}}
	cfg.Runner.CoordinatorEndpoint = "https://coordinator.example/v0/datatug/watch/northwind/remote-project/training"
	return cfg
}

func TestCloudWatchRunnerConfigurationRejectsUntrustedScopeAndSources(t *testing.T) {
	for _, mode := range []string{"nil", "space", "project", "environment", "principal", "bindings-empty", "bindings-limit", "namespace", "flag-namespace", "url", "url-scope", "url-encoded", "binding-id", "binding-hex", "binding-empty", "project-missing", "execution"} {
		t.Run(mode, func(t *testing.T) {
			cfg := cloudCommandConfig(t)
			namespace := ""
			switch mode {
			case "nil":
				cfg.Runner.Cloud = nil
			case "space":
				cfg.Runner.Cloud.SpaceID = "../other"
			case "project":
				cfg.Runner.Cloud.ProjectID = ""
			case "environment":
				cfg.Runner.Environment = "../other"
			case "principal":
				cfg.Runner.Principal = ""
			case "bindings-empty":
				cfg.Runner.Cloud.SourceBindings = nil
			case "bindings-limit":
				for n := range 51 {
					cfg.Runner.Cloud.SourceBindings[strings.Repeat("x", n+1)] = "local"
				}
			case "namespace":
				cfg.Namespace = "other"
			case "flag-namespace":
				namespace = "other"
			case "url":
				cfg.Runner.CoordinatorEndpoint = "http://%"
			case "url-scope":
				cfg.Runner.CoordinatorEndpoint += "/other"
			case "url-encoded":
				cfg.Runner.CoordinatorEndpoint = "https://coordinator.example/v0/datatug/watch/%6eorthwind/remote-project/training"
			case "binding-id":
				cfg.Runner.Cloud.SourceBindings = map[string]string{"short": "local"}
			case "binding-hex":
				cfg.Runner.Cloud.SourceBindings = map[string]string{strings.Repeat("g", 64): "local"}
			case "binding-empty":
				cfg.Runner.Cloud.SourceBindings = map[string]string{strings.Repeat("0", 64): ""}
			case "project-missing":
				cfg.Runner.ProjectDir = t.TempDir()
			case "execution":
				old := watchConfigureExecution
				defer func() { watchConfigureExecution = old }()
				watchConfigureExecution = func(watchdaemon.Config, string) (func(), error) {
					return nil, errors.New("source admission unavailable")
				}
			}
			runner, cleanup, err := configureCloudWatchRunner(&cfg, "", namespace)
			if err == nil || runner != nil || cleanup != nil {
				t.Fatal("untrusted configuration returned an active runner", mode, err)
			}
		})
	}
	for id, valid := range map[string]bool{"": false, strings.Repeat("a", 129): false, "bad.id": false, "Space_01-project": true} {
		if validCloudWatchID(id) != valid {
			t.Fatal(id, valid)
		}
	}
}

func TestCloudWatchRunnerMissingTokenClosesConfiguredEvidence(t *testing.T) {
	cfg := cloudCommandConfig(t)
	cfg.Runner.TokenEnv = "CLOUD_WATCH_MISSING_TOKEN"
	t.Setenv(cfg.Runner.TokenEnv, "")
	closed := 0
	old := watchConfigureExecution
	defer func() { watchConfigureExecution = old }()
	watchConfigureExecution = func(watchdaemon.Config, string) (func(), error) {
		return func() { closed++ }, nil
	}
	if runner, cleanup, err := configureCloudWatchRunner(&cfg, "", ""); err == nil || runner != nil || cleanup != nil || closed != 1 {
		t.Fatal("credential failure leaked execution resources", runner, closed, err)
	}
}

func TestCloudWatchCommandParsesHostedConfigAndRefusesLocalCoordination(t *testing.T) {
	cfg := cloudCommandConfig(t)
	old := watchRunRunner
	defer func() { watchRunRunner = old }()
	calls := 0
	watchRunRunner = func(_ context.Context, runner *watchdaemon.Runner) error {
		calls++
		if runner.Client.Endpoint != cfg.Runner.CoordinatorEndpoint || runner.PollInterval != 5*time.Second || runner.Executor == nil {
			t.Error("cloud deployment configuration was not used", runner.Client.Endpoint, runner.PollInterval)
		}
		return context.Canceled
	}
	if err := invokeWatchCommand(t, context.Background(), cfg, "--run"); err != nil || calls != 1 {
		t.Fatal("hosted runner did not start from parsed deployment config", err, calls)
	}
	if err := invokeWatchCommand(t, context.Background(), cfg, "--coordinate", "--run"); err == nil || calls != 1 {
		t.Fatal("hosted project started a second local coordinator", err, calls)
	}
	if _, err := os.Stat(os.Getenv("WATCH_COMMAND_DB")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hosted-only runner created local coordinator state", err)
	}
	cfg.Runner.Cloud.ProjectID = ""
	if err := invokeWatchCommand(t, context.Background(), cfg, "--run"); err == nil || calls != 1 {
		t.Fatal("invalid parsed cloud scope started a worker", err, calls)
	}
	path := filepath.Join(t.TempDir(), "unknown-cloud-field.json")
	if err := os.WriteFile(path, []byte(`{"runner":{"cloud":{"spaceID":"space","projectID":"project","untrustedToken":"secret"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := watchdaemon.LoadConfig(path); err == nil {
		t.Fatal("unknown cloud deployment fields were silently admitted")
	}
}

func commandCloudBundle(t *testing.T) (watchdaemon.CloudExecutionBundle, watch.Claim) {
	t.Helper()
	q := datatug.QueryDefWithFolderPath{}
	q.ID, q.Title, q.CreatedAt, q.Type, q.Text = "query", "Admitted SQL", "2026-10-24T00:00:00Z", datatug.QueryTypeSQL, "SELECT 1"
	q.Federation = &datatug.QueryFederation{OVDBBaseURL: "https://approved.example", Tables: []datatug.QueryFederationTable{{Database: "northwind", Name: "orders"}}}
	query, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	check := core.Check{ID: "check", Title: "Cloud admitted Check", Version: 1, Steps: []core.Step{{ID: "read", Kind: "query", QueryID: "query"}}}
	b := watchdaemon.CloudExecutionBundle{Contract: "watch-execution-bundle/1", Scope: watch.Scope{Namespace: "northwind", Project: "remote-project", Environment: "training"}, CheckID: "check", CheckVersion: "1", Catalog: core.Catalog{Checks: map[string]core.Check{"check": check}}, Queries: map[string]watchdaemon.CloudBundleQuery{"query": {Revision: "saved-revision", JSON: query}}, Sources: []watchdaemon.CloudBundleSource{{ID: strings.Repeat("a", 64), Kind: "ovdb", Origin: "https://approved.example", Database: "northwind", Collection: "orders"}}}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	b.SourceRevision = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(raw)
	b.BundleDigest = hex.EncodeToString(sum[:])
	return b, watch.Claim{RunID: "run", Fence: 7, Definition: watch.Definition{Scope: b.Scope, CheckID: b.CheckID, CheckVersion: b.CheckVersion, SourceRevision: b.SourceRevision}}
}

func TestCloudWatchRunnerPinsEndpointAndPreservesExecutionErrorTruth(t *testing.T) {
	cfg := cloudCommandConfig(t)
	bundle, claim := commandCloudBundle(t)
	var calls, failure atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v0/datatug/watch/northwind/remote-project/training/v1/watch/runner/bundle" || r.Header.Get("Authorization") != "Bearer "+os.Getenv(cfg.Runner.TokenEnv) {
			t.Error("enrollment credential escaped its pinned endpoint")
		}
		if failure.Load() != 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"bundle": bundle})
	}))
	defer server.Close()
	cfg.Namespace = ""
	runner, cleanup, err := configureCloudWatchRunner(&cfg, server.URL+"/v0/datatug/watch/northwind/remote-project/training", "northwind")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cfg.Namespace != "northwind" {
		t.Fatal("hosted scope not configured", cfg.Namespace)
	}
	foreign := claim
	foreign.Definition.Environment = "other"
	if _, err = runner.Executor.Execute(context.Background(), foreign); !errors.Is(err, watch.ErrForbidden) || calls.Load() != 0 {
		t.Fatal("foreign scope reached authenticated transport", err, calls.Load())
	}
	failure.Store(1)
	if _, err = runner.Executor.Execute(context.Background(), claim); err == nil {
		t.Fatal("bundle outage became a successful evaluation")
	}
	failure.Store(0)
	claim.Definition.SourceFingerprint = "previous-admission"
	completion, err := runner.Executor.Execute(context.Background(), claim)
	if err != nil || completion.ExecutionStatus != "error" || completion.Outcome != "unknown" || completion.FailureCode != "SOURCE_CHANGED" {
		t.Fatal("source revision conflict was reported as a business result", completion, err)
	}
	claim.Definition.SourceFingerprint = ""
	completion, err = runner.Executor.Execute(context.Background(), claim)
	if err != nil || completion.ExecutionStatus != "error" || completion.Outcome != "unknown" || completion.SourceRevision != bundle.SourceRevision {
		t.Fatal("missing local SQL source did not preserve unknown outcome", completion, err)
	}
	if runner.PollInterval != 5*time.Second {
		t.Fatal("bounded hosted polling cadence not configured", runner.PollInterval)
	}
}
