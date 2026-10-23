package watchdaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

var protocolScope = watch.Scope{Namespace: "northwind", Project: "northwind", Environment: "local"}

type httpFixture struct {
	server *Server
	http   *httptest.Server
	store  *watch.Store
	tokens map[string]string
	now    atomic.Int64
}

func protocolFixture(t *testing.T) *httpFixture {
	t.Helper()
	f := &httpFixture{tokens: map[string]string{}}
	f.now.Store(time.Now().UTC().UnixNano())
	var credentials []Credential
	for _, id := range []string{"alice", "bob", "carol", "runner-a", "runner-b", "wrong-env"} {
		token := strings.Repeat(id+"-", 12)
		f.tokens[id] = token
		env := "WATCH_TEST_" + strings.ReplaceAll(strings.ToUpper(id), "-", "_")
		t.Setenv(env, token)
		kind := "user"
		if strings.HasPrefix(id, "runner") || id == "wrong-env" {
			kind = "runner"
		}
		environment := "local"
		if id == "wrong-env" {
			environment = "prod"
		}
		email := ""
		if kind == "user" {
			email = id + "@example.test"
		}
		credentials = append(credentials, Credential{TokenEnv: env, Kind: kind, ID: id, AccountID: id, Tier: "free", Namespace: "northwind", Project: "northwind", Environment: environment, SharedEditor: id == "alice", Email: email})
	}
	auth, err := NewAuthenticator(credentials)
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = watch.NewSQLiteStore(filepath.Join(t.TempDir(), "state.sqlite"), watch.Config{Namespace: "northwind", Now: func() time.Time { return time.Unix(0, f.now.Load()).UTC() }, LeaseDuration: time.Second, RunnerTTL: time.Hour, ExecutionTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.server = &Server{Store: f.store, Auth: auth, AllowedOrigins: []string{"https://datatug.app"}, ProjectUIURLs: map[string]string{"northwind": "https://datatug.app/store/test/project/northwind"}, Email: &FileEmailSink{Path: filepath.Join(t.TempDir(), "mail")}}
	f.http = httptest.NewServer(f.server)
	t.Cleanup(func() { f.http.Close(); _ = f.store.Close() })
	return f
}
func (f *httpFixture) request(t *testing.T, actor, method, path string, input any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, f.http.URL+"/v1/watch/"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.tokens[actor])
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data
}
func (f *httpFixture) create(t *testing.T, actor, id, visibility string) {
	t.Helper()
	d := watch.Definition{Scope: protocolScope, ID: id, Title: "Private business title", Visibility: visibility, CheckID: "order-shipped", CheckVersion: "1", Schedule: watch.Schedule{IntervalSeconds: 5}, Trigger: "becomes_true", InitialObservation: "notify", Mode: "one_shot"}
	code, body := f.request(t, actor, "POST", "definition", map[string]any{"definition": d, "operationId": "create-" + id, "expectedVersion": 0})
	if code != 200 {
		t.Fatalf("create %d %s", code, body)
	}
}
func (f *httpFixture) client(id string) Client {
	return Client{Endpoint: f.http.URL, Token: f.tokens[id]}
}

func TestWatchHTTPIdentityVisibilityAndAtomicLease(t *testing.T) {
	f := protocolFixture(t)
	ctx := context.Background()
	for _, id := range []string{"alice", "bob", "carol"} {
		f.create(t, id, id+"-watch", "personal")
	}
	code, body := f.request(t, "bob", "GET", "definitions", nil)
	if code != 200 || bytes.Contains(body, []byte("alice-watch")) || bytes.Contains(body, []byte("carol-watch")) {
		t.Fatalf("personal visibility: %d %s", code, body)
	}
	code, _ = f.request(t, "bob", "GET", "definitions?environment=prod", nil)
	if code != 403 {
		t.Fatal("scope override accepted")
	}
	code, _ = f.request(t, "bob", "POST", "subscription", map[string]any{"watchId": "bob-watch", "channels": []string{"email"}, "userId": "alice"})
	if code != 400 {
		t.Fatal("body actor spoof accepted")
	}
	for _, id := range []string{"runner-a", "runner-b", "wrong-env"} {
		if err := f.client(id).Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c, err := f.client("wrong-env").Claim(ctx); err != nil || c != nil {
		t.Fatalf("wrong-env claim %v %v", c, err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}
	for _, id := range []string{"runner-a", "runner-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			c, err := f.client(id).Claim(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if c == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[c.RunID] {
				t.Error("duplicate active lease")
			}
			seen[c.RunID] = true
		}(id)
	}
	wg.Wait()
	if len(seen) != 2 {
		t.Fatalf("expected distinct due jobs, got %d", len(seen))
	}
	// User tokens can never enter the runner protocol.
	if err := f.client("alice").Register(ctx); !errors.Is(err, watch.ErrForbidden) {
		t.Fatal("user token registered runner")
	}
}

func TestWatchHTTPStaleCompletionInboxAndDurableEmail(t *testing.T) {
	f := protocolFixture(t)
	ctx := context.Background()
	f.create(t, "alice", "shipping", "personal")
	code, body := f.request(t, "alice", "POST", "subscription", map[string]any{"watchId": "shipping", "channels": []string{"in_app", "email"}})
	if code != 200 {
		t.Fatalf("subscription %d %s", code, body)
	}
	for _, id := range []string{"runner-a", "runner-b"} {
		if err := f.client(id).Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stale, err := f.client("runner-a").Claim(ctx)
	if err != nil || stale == nil {
		t.Fatal(err)
	}
	f.now.Add(int64(2 * time.Second))
	current, err := f.client("runner-b").Claim(ctx)
	if err != nil || current == nil {
		t.Fatalf("reclaim: %v %v", current, err)
	}
	result := func(c *watch.Claim) watch.Completion {
		return watch.Completion{ClaimID: c.ClaimID, RunID: c.RunID, Fence: c.Fence, ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "catalog-v1"}
	}
	if err = f.client("runner-a").Complete(ctx, result(stale)); !errors.Is(err, watch.ErrStaleLease) {
		t.Fatalf("stale completion %v", err)
	}
	if err = f.client("runner-b").Complete(ctx, result(current)); err != nil {
		t.Fatal(err)
	}
	if err = f.client("runner-b").Complete(ctx, result(current)); err != nil {
		t.Fatal(err)
	}
	d, err := f.store.ClaimNotifications(ctx)
	if err != nil || d == nil || d.Channel != "email" {
		t.Fatalf("delivery %v %v", d, err)
	}
	if err = f.server.deliver(ctx, *d); err != nil {
		t.Fatal(err)
	}
	if err = f.server.deliver(ctx, *d); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(f.server.Email.(*FileEmailSink).Path)
	if len(entries) != 1 {
		t.Fatalf("duplicate sink delivery: %d", len(entries))
	}
	data, _ := os.ReadFile(filepath.Join(f.server.Email.(*FileEmailSink).Path, entries[0].Name()))
	if !bytes.Contains(data, []byte("https://datatug.app/store/test/project/northwind/watch/shipping")) || bytes.Contains(data, []byte("Private business title")) {
		t.Fatalf("unsafe or missing Watch deep link %s", data)
	}
	if err = f.store.FinishNotification(ctx, d.ID, d.Fence, ""); err != nil {
		t.Fatal(err)
	}
	code, body = f.request(t, "alice", "GET", "inbox", nil)
	if code != 200 {
		t.Fatal(string(body))
	}
	var inbox struct {
		Notifications []watch.Notification `json:"notifications"`
	}
	_ = json.Unmarshal(body, &inbox)
	if len(inbox.Notifications) != 1 {
		t.Fatalf("inbox duplicates %s", body)
	}
	code, _ = f.request(t, "bob", "POST", "read", map[string]string{"id": inbox.Notifications[0].ID})
	if code != 403 && code != 404 {
		t.Fatalf("foreign read %d", code)
	}
	_ = f.store.Close()
	response, err := http.Get(f.http.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("closed DB reported ready")
	}
}

func TestWatchHTTPExactlyOneClaimAndCancellationAfterPause(t *testing.T) {
	f := protocolFixture(t)
	ctx := context.Background()
	f.create(t, "alice", "single", "personal")
	for _, id := range []string{"runner-a", "runner-b"} {
		if err := f.client(id).Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var won *watch.Claim
	var winner string
	var wg sync.WaitGroup
	for _, id := range []string{"runner-a", "runner-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			claim, err := f.client(id).Claim(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if claim != nil {
				mu.Lock()
				defer mu.Unlock()
				if won != nil {
					t.Error("same due job claimed twice")
				}
				won = claim
				winner = id
			}
		}(id)
	}
	wg.Wait()
	if won == nil {
		t.Fatal("no active lease")
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	runner := Runner{Client: f.client(winner), RenewInterval: 10 * time.Millisecond, Executor: ExecutorFunc(func(ctx context.Context, c watch.Claim) (watch.Completion, error) {
		close(started)
		<-ctx.Done()
		return watch.Completion{}, ctx.Err()
	})}
	go func() { finished <- runner.executeClaim(ctx, *won) }()
	<-started
	d, err := f.store.GetDefinition(ctx, watch.Actor{ID: "alice", Scope: protocolScope}, "single")
	if err != nil {
		t.Fatal(err)
	}
	code, body := f.request(t, "alice", "POST", "lifecycle", map[string]any{"id": "single", "lifecycle": "paused", "operationId": "pause-single", "expectedVersion": d.Version})
	if code != 200 {
		t.Fatalf("pause %d %s", code, body)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("paused job accepted completion")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner failed to cancel after lease revoked")
	}
}

func TestWatchTelegramLinkCallbackAndProvider(t *testing.T) {
	f := protocolFixture(t)
	f.server.Telegram = &FileTelegramSink{Sink: &FileEmailSink{Path: filepath.Join(t.TempDir(), "telegram")}}
	f.server.TelegramCallbackSecret = strings.Repeat("bridge-", 8)
	f.server.TelegramRouteID = "local"
	code, body := f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	if code != 200 {
		t.Fatalf("connect %d %s", code, body)
	}
	var link struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(body, &link)
	u, _ := url.Parse(link.URL)
	payload := u.Query().Get("start")
	if len(payload) > 64 {
		t.Fatal("Telegram start payload too long")
	}
	update := map[string]any{"message": map[string]any{"text": "/start " + payload, "chat": map[string]any{"id": 12345, "type": "private"}, "from": map[string]any{"id": 12345, "is_bot": false}}}
	data, _ := json.Marshal(update)
	callback := func(secret string) int {
		req, _ := http.NewRequest("POST", f.http.URL+"/v1/watch/telegram/update", bytes.NewReader(data))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if callback("wrong") != 401 {
		t.Fatal("forged bridge accepted")
	}
	if callback(f.server.TelegramCallbackSecret) != 200 {
		t.Fatal("trusted link rejected")
	}
	if callback(f.server.TelegramCallbackSecret) == 200 {
		t.Fatal("nonce replay accepted")
	}
	code, body = f.request(t, "alice", "GET", "telegram/status", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"connected":true`)) {
		t.Fatalf("link status %d %s", code, body)
	}
	code, body = f.request(t, "bob", "GET", "telegram/status", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"connected":false`)) {
		t.Fatal("link leaked to other user")
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["chat_id"] != "12345" {
			t.Error("wrong linked chat")
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer provider.Close()
	t.Setenv("WATCH_TEST_BOT_TOKEN", "123:fake")
	if err := (BotTelegramSender{TokenEnv: "WATCH_TEST_BOT_TOKEN", APIBaseURL: provider.URL}).Send(context.Background(), TelegramMessage{ID: "telegram-1", ChatID: "12345", Body: "DataTug Watch update"}); err != nil || calls.Load() != 1 {
		t.Fatalf("provider %d %v", calls.Load(), err)
	}
}
