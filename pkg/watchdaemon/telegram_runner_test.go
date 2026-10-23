package watchdaemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/datatug/datatug-cli/pkg/watch"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeOwnBot(t *testing.T, updates func(http.ResponseWriter, *http.Request), send func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":true,"username":"PrivateWatchBot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if updates != nil {
				updates(w, r)
			} else {
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if send != nil {
				send(w, r)
			} else {
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
			}
		default:
			t.Error("unexpected Bot API method", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(p.Close)
	return p
}
func ownBotConfig(t *testing.T) TelegramBotConfig {
	t.Helper()
	t.Setenv("WATCH_OWN_BOT_SECRET", strings.Repeat("private", 8))
	return TelegramBotConfig{BotID: "123", SecretEnv: "WATCH_OWN_BOT_SECRET"}
}
func TestWatchOwnBotVerifiesIdentityWebhookAndSecrets(t *testing.T) {
	ctx := context.Background()
	cfg := ownBotConfig(t)
	p := fakeOwnBot(t, nil, nil)
	b, e := newTelegramBot(ctx, cfg, p.URL, nil)
	if e != nil || b.botID != "123" || b.username != "PrivateWatchBot" {
		t.Fatalf("verification %v %v", b, e)
	}
	t.Setenv("WATCH_FULL_BOT_TOKEN", "123:"+strings.Repeat("whole", 8))
	if _, e = newTelegramBot(ctx, TelegramBotConfig{BotID: "123", TokenEnv: "WATCH_FULL_BOT_TOKEN"}, p.URL, nil); e != nil {
		t.Fatal(e)
	}
	for _, c := range []TelegramBotConfig{{}, {BotID: "bad", SecretEnv: cfg.SecretEnv}, {BotID: "123", SecretEnv: "absent"}, {BotID: "123", SecretEnv: cfg.SecretEnv, TokenEnv: "WATCH_FULL_BOT_TOKEN"}, {BotID: "999", TokenEnv: "WATCH_FULL_BOT_TOKEN"}} {
		if _, e = newTelegramBot(ctx, c, p.URL, nil); e == nil {
			t.Fatal("invalid credential accepted", c.BotID)
		}
	}
	t.Setenv("WATCH_BAD_SECRET", "bad/secret")
	if _, e = newTelegramBot(ctx, TelegramBotConfig{BotID: "123", SecretEnv: "WATCH_BAD_SECRET"}, p.URL, nil); e == nil {
		t.Fatal("URL token injection")
	}
	if _, e = newTelegramBot(ctx, cfg, "http://public.example", nil); e == nil {
		t.Fatal("insecure provider")
	}
	for _, response := range []string{`{"ok":false}`, `not json`, `{"ok":true,"result":{}}`, `{"ok":true,"result":{"id":999,"is_bot":true,"username":"OtherBot"}}`, `{"ok":true,"result":{"id":123,"is_bot":false,"username":"PrivateWatchBot"}}`} {
		p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
		_, e = newTelegramBot(ctx, cfg, p.URL, nil)
		p.Close()
		if e == nil {
			t.Fatal("unverified bot accepted", response)
		}
	}
	hooked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":true,"username":"PrivateWatchBot"}}`)
		} else {
			fmt.Fprint(w, `{"ok":true,"result":{"url":"https://existing.example/webhook"}}`)
		}
	}))
	defer hooked.Close()
	if _, e = newTelegramBot(ctx, cfg, hooked.URL, nil); e == nil || !strings.Contains(e.Error(), "dedicated bot") {
		t.Fatal("existing webhook was not rejected", e)
	}
	failing := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret provider URL") })}
	if _, e = newTelegramBot(ctx, cfg, "", failing); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("raw provider error leaked", e)
	}
	if e = b.call(ctx, "getMe", make(chan bool), nil); e == nil {
		t.Fatal("invalid JSON accepted")
	}
	if e = b.call(nil, "getMe", struct{}{}, nil); e == nil {
		t.Fatal("invalid context")
	}
	if e = b.call(ctx, "getMe", struct{}{}, new([]int)); e == nil {
		t.Fatal("invalid result accepted")
	}
}
func TestWatchOwnBotPrivateStartValidation(t *testing.T) {
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	valid := fmt.Sprintf(`{"update_id":10,"message":{"text":"/start watch-%s","from":{"id":42},"chat":{"id":42,"type":"private"}}}`, nonce)
	for _, raw := range []string{`{}`, `{"message":{}}`, strings.Replace(valid, `"id":42,"type"`, `"id":43,"type"`, 1), strings.Replace(valid, `"private"`, `"group"`, 1), strings.Replace(valid, `"from":{"id":42}`, `"from":{"id":42,"is_bot":true}`, 1), strings.Replace(valid, "/start watch-", "/other watch-", 1), strings.Replace(valid, nonce, "bad", 1), strings.Replace(valid, nonce, base64.RawURLEncoding.EncodeToString([]byte{1}), 1)} {
		var u telegramUpdate
		if e := json.Unmarshal([]byte(raw), &u); e != nil {
			t.Fatal(e)
		}
		if h, _ := u.link(); h != "" {
			t.Fatal("forged private link accepted", raw)
		}
	}
	var u telegramUpdate
	_ = json.Unmarshal([]byte(valid), &u)
	h, chat := u.link()
	if len(h) != 64 || chat != "42" {
		t.Fatal(h, chat)
	}
	for _, name := range []string{"Bad", "Bad/Bot", "bot?query", "123456789012345678901234567890123Bot"} {
		if validBotUsername(name) {
			t.Fatal(name)
		}
	}
}
func grantOwnBot(f *httpFixture) {
	for i := range f.server.Auth.entries {
		c := &f.server.Auth.entries[i].credential
		if c.Kind == "runner" && c.Environment == "local" {
			c.TelegramBotID = "123"
		}
	}
}
func TestWatchOwnBotScopedRunnerLinksAndDelivers(t *testing.T) {
	f := protocolFixture(t)
	grantOwnBot(f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := f.client("runner-a")
	if e := client.register(ctx, runnerRegistration{TelegramBotID: "999", TelegramBotUsername: "OtherBot"}); !errors.Is(e, watch.ErrForbidden) {
		t.Fatal("registration granted arbitrary bot", e)
	}
	if e := client.register(ctx, runnerRegistration{TelegramBotID: "123", TelegramBotUsername: "PrivateWatchBot"}); e != nil {
		t.Fatal(e)
	}
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
	if !strings.HasPrefix(link.URL, "https://t.me/PrivateWatchBot?") || len(payload) > 64 {
		t.Fatal(link.URL)
	}
	lease, e := client.telegramPoll(ctx, "poll", 0)
	if e != nil || lease == nil {
		t.Fatal(lease, e)
	}
	if _, e = f.client("wrong-env").telegramPoll(ctx, "poll", 0); !errors.Is(e, watch.ErrForbidden) {
		t.Fatal("unauthorized runner poll", e)
	}
	var delivered, polled atomic.Int32
	p := fakeOwnBot(t, func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Offset         int64 `json:"offset"`
			Limit, Timeout int
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Limit != 5 || q.Timeout != 5 {
			t.Error("unbounded polling")
		}
		poll := polled.Add(1)
		if poll == 1 {
			// A real Unicode backlog must be consumed instead of repeating a
			// response larger than the bounded provider decoder can accept.
			batch := make([]map[string]any, 5)
			for i := range batch {
				batch[i] = map[string]any{"update_id": 6 + i, "message": map[string]string{"text": strings.Repeat("🚢", 4096)}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": batch})
		} else if poll == 2 {
			fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":11,"message":{"text":"/start %s","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`, payload)
		} else {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(15 * time.Millisecond):
			}
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.ChatID != "42" || !strings.Contains(m.Text, "/watch/ownbot") || strings.Contains(m.Text, "Private business") {
			t.Error("wrong recipient or unsafe notification", m)
		}
		delivered.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	})
	cfg := ownBotConfig(t)
	b, e := newTelegramBot(ctx, cfg, p.URL, nil)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- b.runLease(ctx, client, *lease) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		code, body = f.request(t, "alice", "GET", "telegram/status", nil)
		if code == 200 && bytes.Contains(body, []byte(`"connected":true`)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bytes.Contains(body, []byte(`"connected":true`)) {
		t.Fatal("link never connected", string(body))
	}
	f.create(t, "alice", "ownbot", "personal")
	code, body = f.request(t, "alice", "POST", "subscription", map[string]any{"watchId": "ownbot", "channels": []string{"telegram"}})
	if code != 200 {
		t.Fatal(code, string(body))
	}
	if e = f.store.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	claim, e := client.Claim(ctx)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = client.Complete(ctx, watch.Completion{ClaimID: claim.ClaimID, RunID: claim.RunID, Fence: claim.Fence, ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "fingerprint"}); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(2 * time.Second)
	for delivered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if delivered.Load() != 1 {
		t.Fatal("delivery missing or duplicated", delivered.Load())
	}
	cancel()
	<-done
	code, _ = f.request(t, "bob", "GET", "telegram/status", nil)
	if code != 200 {
		t.Fatal(code)
	}
	code, body = f.request(t, "bob", "POST", "telegram/connect", map[string]any{"chatId": "42"})
	if code != 400 {
		t.Fatal("browser recipient spoof", code, string(body))
	}
	code, _ = f.request(t, "alice", "POST", "telegram/disconnect", struct{}{})
	if code != 200 {
		t.Fatal(code)
	}
}
func TestWatchOwnBotNoCallbackAndNoCoordinatorProvider(t *testing.T) {
	f := protocolFixture(t)
	code, _ := f.request(t, "alice", "POST", "telegram/update", struct{}{})
	if code != 404 {
		t.Fatal("removed callback remains", code)
	}
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	if code != 503 {
		t.Fatal("unconfigured runner advertised", code)
	}
	if f.server.deliver(context.Background(), watch.Delivery{Channel: "telegram"}) == nil {
		t.Fatal("coordinator sent Telegram")
	}
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://evil.example", 302) }))
	defer p.Close()
	cfg := ownBotConfig(t)
	if _, e := newTelegramBot(context.Background(), cfg, p.URL, nil); e == nil {
		t.Fatal("bot token followed redirect")
	}
}
func TestWatchOwnBotClassifiesRecipientFailuresWithoutLeakingProviderText(t *testing.T) {
	for _, c := range []struct {
		status      int
		description string
		permanent   bool
	}{{403, "Forbidden: bot was blocked by the user", true}, {403, "Forbidden: user is deactivated", true}, {400, "Bad Request: chat not found", true}, {403, "Forbidden: bot token secret invalid", false}, {400, "Bad Request: message is too long", false}, {429, "Too Many Requests: retry after123 secret", false}} {
		t.Run(fmt.Sprint(c.status, c.description), func(t *testing.T) {
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": c.status, "description": c.description})
			}))
			defer p.Close()
			b := telegramBot{base: p.URL, token: "123:secret", http: p.Client()}
			e := b.call(context.Background(), "sendMessage", struct{}{}, nil)
			if errors.Is(e, errTelegramRecipientUnavailable) != c.permanent || e == nil || strings.Contains(e.Error(), c.description) {
				t.Fatal("unsafe classification", e)
			}
			if e = b.call(context.Background(), "getMe", struct{}{}, nil); errors.Is(e, errTelegramRecipientUnavailable) {
				t.Fatal("bot-wide failure disconnected recipient")
			}
		})
	}
}
func TestWatchOwnBotMultipleSubscribersAndBlockedRecipientIsolation(t *testing.T) {
	f := protocolFixture(t)
	grantOwnBot(f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := f.client("runner-a")
	if e := c.register(ctx, runnerRegistration{TelegramBotID: "123", TelegramBotUsername: "PrivateWatchBot"}); e != nil {
		t.Fatal(e)
	}
	lease, e := c.telegramPoll(ctx, "poll", 0)
	if e != nil || lease == nil {
		t.Fatal(lease, e)
	}
	for i, user := range []string{"alice", "bob"} {
		code, body := f.request(t, user, "POST", "telegram/connect", struct{}{})
		if code != 200 {
			t.Fatal(code, string(body))
		}
		var link struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(body, &link)
		u, _ := url.Parse(link.URL)
		nonce := strings.TrimPrefix(u.Query().Get("start"), "watch-")
		sum := sha256.Sum256([]byte(nonce))
		if e = c.call(ctx, "runner/telegram/link", map[string]any{"fence": lease.Fence, "nonceHash": hex.EncodeToString(sum[:]), "chatId": fmt.Sprint(42 + i)}, nil); e != nil {
			t.Fatal(e)
		}
	}
	f.create(t, "alice", "shared-ownbot", "shared")
	for _, user := range []string{"alice", "bob"} {
		code, body := f.request(t, user, "POST", "subscription", map[string]any{"watchId": "shared-ownbot", "channels": []string{"telegram", "in_app"}})
		if code != 200 {
			t.Fatal(user, code, string(body))
		}
	}
	if e = f.store.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	claim, e := c.Claim(ctx)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = c.Complete(ctx, watch.Completion{ClaimID: claim.ClaimID, RunID: claim.RunID, Fence: claim.Fence, ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "fingerprint"}); e != nil {
		t.Fatal(e)
	}
	var blocked, delivered atomic.Int32
	provider := fakeOwnBot(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ChatID string `json:"chat_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.ChatID == "42" {
			blocked.Add(1)
			w.WriteHeader(403)
			fmt.Fprint(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
		} else if m.ChatID == "43" {
			delivered.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
		} else {
			t.Error("arbitrary recipient", m.ChatID)
		}
	})
	bot, e := newTelegramBot(ctx, ownBotConfig(t), provider.URL, nil)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- bot.runLease(ctx, c, *lease) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (delivered.Load() == 0 || blocked.Load() == 0) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if delivered.Load() != 1 || blocked.Load() != 1 {
		t.Fatal("fanout affected other recipient", delivered.Load(), blocked.Load())
	}
	code, body := f.request(t, "alice", "GET", "telegram/status", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"reconnectRequired":true`)) || !bytes.Contains(body, []byte(`"connected":false`)) {
		t.Fatal("blocked recipient state", code, string(body))
	}
	code, body = f.request(t, "bob", "GET", "telegram/status", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"connected":true`)) || bytes.Contains(body, []byte(`"reconnectRequired":true`)) {
		t.Fatal("other recipient disconnected", code, string(body))
	}
	for _, user := range []string{"alice", "bob"} {
		code, body = f.request(t, user, "GET", "inbox", nil)
		if code != 200 || !bytes.Contains(body, []byte("shared-ownbot")) {
			t.Fatal("other channel affected", user, code, string(body))
		}
	}
}
func TestWatchOwnBotRateLimitErrorExposesOnlySafeBound(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"sensitive provider text","parameters":{"retry_after":60}}`)
	}))
	defer provider.Close()
	bot := telegramBot{base: provider.URL, token: "123:private", http: provider.Client()}
	e := bot.call(context.Background(), "sendMessage", struct{}{}, nil)
	var rate *telegramRateLimitError
	if !errors.As(e, &rate) || rate.Seconds != 60 || strings.Contains(e.Error(), "sensitive") {
		t.Fatal("unsafe rate limit error", e)
	}
}
