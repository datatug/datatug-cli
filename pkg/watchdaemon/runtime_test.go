package watchdaemon

import (
	"context"
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
)

type emailSenderFunc func(context.Context, EmailMessage) error

func (f emailSenderFunc) Send(ctx context.Context, m EmailMessage) error { return f(ctx, m) }

func TestWatchCoordinatorSchedulesDuringSlowDelivery(t *testing.T) {
	f := protocolFixture(t)
	ctx := context.Background()
	f.create(t, "alice", "continuous", "personal")
	a := watch.Actor{ID: "alice", Email: "alice@example.test", AccountID: "alice", Scope: protocolScope, Tier: "free", SharedEditor: true}
	d, err := f.store.GetDefinition(ctx, a, "continuous")
	if err != nil {
		t.Fatal(err)
	}
	d.Mode = "continuous"
	if _, err = f.store.PutDefinition(ctx, a, d, "make-continuous", d.Version); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Subscribe(ctx, a, watch.Subscription{WatchID: d.ID, Channels: []string{"email"}}); err != nil {
		t.Fatal(err)
	}
	client := f.client("runner-a")
	if err = client.Register(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	claim, err := client.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = client.Complete(ctx, watch.Completion{ClaimID: claim.ClaimID, RunID: claim.RunID, Fence: claim.Fence, ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "v1"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	f.server.Email = emailSenderFunc(func(ctx context.Context, _ EmailMessage) error { close(started); <-ctx.Done(); return ctx.Err() })
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.server.RunCoordinator(runCtx, 5*time.Millisecond) }()
	<-started
	f.now.Add(int64(5 * time.Second))
	deadline := time.Now().Add(time.Second)
	var next *watch.Claim
	for time.Now().Before(deadline) {
		next, err = client.Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if next != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if next == nil {
		t.Fatal("slow delivery blocked scheduling")
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if (&Server{}).RunCoordinator(ctx, time.Second) == nil {
		t.Fatal("nil storage accepted")
	}
}

func TestWatchRunnerUsesHTTPAndReportsEvaluationErrorAsUnknown(t *testing.T) {
	f := protocolFixture(t)
	f.create(t, "alice", "failure", "personal")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int32
	runner := Runner{Client: f.client("runner-a"), PollInterval: 5 * time.Millisecond, RenewInterval: 5 * time.Millisecond, Executor: ExecutorFunc(func(ctx context.Context, c watch.Claim) (watch.Completion, error) {
		executions.Add(1)
		return watch.Completion{}, errors.New("source credential must never leave runner")
	})}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(ctx, watch.Actor{ID: "alice", Scope: protocolScope}, "failure")
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0].ExecutionStatus == "error" {
			if runs[0].Outcome != "unknown" || runs[0].FailureCode != "SOURCE_UNAVAILABLE" {
				t.Fatalf("source error became business false %+v", runs[0])
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if executions.Load() != 1 {
		t.Fatalf("unexpected query retries %d", executions.Load())
	}
	if (Runner{}).Run(context.Background()) == nil {
		t.Fatal("missing executor accepted")
	}
	wrong := Runner{Client: f.client("alice"), Executor: ExecutorFunc(func(context.Context, watch.Claim) (watch.Completion, error) {
		t.Error("user credential executed source")
		return watch.Completion{}, nil
	})}
	if err := wrong.Run(context.Background()); !errors.Is(err, watch.ErrForbidden) {
		t.Fatal(err)
	}
}

func TestWatchUserManagementRoutesAndDefaultSharedSubscription(t *testing.T) {
	f := protocolFixture(t)
	f.create(t, "alice", "shared", "shared")
	code, body := f.request(t, "bob", "GET", "subscriptions?watchId=shared", nil)
	if code != 200 || !strings.Contains(string(body), `"unsubscribed":true`) {
		t.Fatalf("new subscriber could not open Watch: %d %s", code, body)
	}
	for _, path := range []string{"identity", "definition?id=shared", "status", "runs?watchId=shared", "deliveries?watchId=shared"} {
		code, body = f.request(t, "alice", "GET", path, nil)
		if code != 200 {
			t.Fatalf("GET %s %d %s", path, code, body)
		}
	}
	code, _ = f.request(t, "bob", "POST", "reset", map[string]any{"id": "shared", "operationId": "bad-reset", "expectedVersion": 1})
	if code != 403 {
		t.Fatal("noneditor reset shared baseline")
	}
	code, body = f.request(t, "alice", "POST", "reset", map[string]any{"id": "shared", "operationId": "reset", "expectedVersion": 1})
	if code != 200 {
		t.Fatalf("reset %d %s", code, body)
	}
	var out struct {
		Definition watch.Definition `json:"definition"`
	}
	_ = json.Unmarshal(body, &out)
	code, body = f.request(t, "alice", "POST", "delete", map[string]any{"id": "shared", "operationId": "delete", "expectedVersion": out.Definition.Version})
	if code != 200 {
		t.Fatalf("delete %d %s", code, body)
	}
	code, _ = f.request(t, "alice", "POST", "retry", map[string]string{"id": "foreign-delivery"})
	if code != 404 && code != 403 {
		t.Fatal("foreign delivery retried")
	}
	f.create(t, "alice", "no-provider", "personal")
	f.server.Email = nil
	for _, channel := range []string{"email", "sms", "telegram"} {
		code, _ = f.request(t, "alice", "POST", "subscription", map[string]any{"watchId": "no-provider", "channels": []string{channel}})
		if code != 400 {
			t.Fatalf("unconfigured %s channel accepted", channel)
		}
	}
}

func TestWatchUnavailableStorageRemainsObservable(t *testing.T) {
	f := protocolFixture(t)
	req := httptest.NewRequest("GET", "/v1/watch/status", nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens["alice"])
	rec := httptest.NewRecorder()
	(&Server{Auth: f.server.Auth}).ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatal("missing storage accepted")
	}
	f.server.Limits = Limits{SMSPerDay: 101}
	code, _ := f.request(t, "alice", "GET", "identity", nil)
	if code != 503 {
		t.Fatal("invalid resolved limits advertised")
	}
	f.server.Limits = Limits{}
	code, _ = f.request(t, "alice", "GET", "subscriptions?watchId=missing", nil)
	if code != 404 {
		t.Fatal("unknown Watch granted default subscription")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := f.server.RunCoordinator(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	_ = f.store.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel2()
	if err := f.server.RunCoordinator(ctx2, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestWatchHTTPGuardsAndConfiguration(t *testing.T) {
	f := protocolFixture(t)
	for _, tc := range []struct {
		method, path, origin string
		want                 int
	}{{"OPTIONS", "/v1/watch/definitions", "https://datatug.app", 204}, {"GET", "/v1/watch/definitions", "https://evil.example", 403}, {"GET", "/health", "", 200}, {"GET", "/unknown", "", 404}, {"GET", "/v1/watch/unknown", "", 404}} {
		req, _ := http.NewRequest(tc.method, f.http.URL+tc.path, nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Authorization", "Bearer "+f.tokens["alice"])
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("%s %d", tc.path, response.StatusCode)
		}
	}
	for _, body := range []string{`{"id":"x"} {}`, `{"unexpected":true}`} {
		req := httptest.NewRequest("POST", "/v1/watch/read", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.tokens["alice"])
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatal("noncanonical JSON accepted")
		}
	}
	rec := httptest.NewRecorder()
	(&Server{Auth: f.server.Auth}).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 503 {
		t.Fatal("nil DB ready")
	}
	for _, path := range []string{"runner/register", "runner/claim", "runner/heartbeat", "runner/complete", "runner/unknown"} {
		code, _ := f.request(t, "runner-a", "GET", path, nil)
		if code != 400 {
			t.Fatal(path, code)
		}
	}
	code, _ := f.request(t, "runner-a", "POST", "runner/unknown", struct{}{})
	if code != 404 {
		t.Fatal(code)
	}
	file := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{`{`, `{} {}`, `{"unknown":true}`} {
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(file); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if err := os.WriteFile(file, []byte(`{"namespace":"northwind"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(file); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(t.TempDir()); err == nil {
		t.Fatal("directory configuration accepted")
	}
	if _, err := LoadConfig(file + "missing"); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if _, err := (Limits{SMSPerDay: 101}).Resolve(); err == nil {
		t.Fatal("unbounded SMS quota accepted")
	}
	if _, err := (Limits{MinIntervalSeconds: 1}).Resolve(); err == nil {
		t.Fatal("unbounded schedule rate accepted")
	}
}
