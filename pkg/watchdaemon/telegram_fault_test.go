package watchdaemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/datatug/datatug-cli/pkg/watch"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type opaqueCoordinatorStore struct{ CoordinatorStore }
type telegramFaultStore struct {
	CoordinatorStore
	Provider                       watch.TelegramProviderStatus
	StatusError, BeginError, Error error
	Delivery                       *watch.Delivery
	Lease                          *watch.TelegramPollLease
}

func (s telegramFaultStore) TelegramProviderStatus(context.Context, watch.Actor) (watch.TelegramProviderStatus, error) {
	return s.Provider, s.StatusError
}
func (s telegramFaultStore) BeginTelegramLink(context.Context, watch.Actor, string, time.Time) error {
	return s.BeginError
}
func (s telegramFaultStore) DisconnectTelegram(context.Context, watch.Actor) error { return s.Error }
func (s telegramFaultStore) ClaimTelegramPoll(context.Context, watch.RunnerIdentity) (*watch.TelegramPollLease, error) {
	return s.Lease, s.Error
}
func (s telegramFaultStore) RenewTelegramPoll(context.Context, watch.RunnerIdentity, int64) (*watch.TelegramPollLease, error) {
	return s.Lease, s.Error
}
func (s telegramFaultStore) DeferTelegramPoll(context.Context, watch.RunnerIdentity, int64, int) error {
	return s.Error
}
func (s telegramFaultStore) AdvanceTelegramPoll(context.Context, watch.RunnerIdentity, int64, int64) error {
	return s.Error
}
func (s telegramFaultStore) CompleteTelegramLinkByRunner(context.Context, watch.RunnerIdentity, int64, string, string) error {
	return s.Error
}
func (s telegramFaultStore) ClaimTelegramNotification(context.Context, watch.RunnerIdentity, int64) (*watch.Delivery, error) {
	return s.Delivery, s.Error
}
func (s telegramFaultStore) FinishTelegramNotification(context.Context, watch.RunnerIdentity, int64, string, int64, string, ...int) error {
	return s.Error
}
func TestWatchOwnBotAPIFaultsFailClosed(t *testing.T) {
	f := protocolFixture(t)
	grantOwnBot(f)
	upstream := errors.New("private storage detail")
	st := telegramFaultStore{CoordinatorStore: f.store, Provider: watch.TelegramProviderStatus{BotID: "123", BotUsername: "PrivateWatchBot"}}
	f.server.Store = st
	for _, path := range []string{"telegram/connect", "telegram/disconnect"} {
		code, _ := f.request(t, "alice", "POST", path, map[string]string{"chatId": "42"})
		if code != 400 {
			t.Fatal(path, code)
		}
	}
	code, _ := f.request(t, "alice", "GET", "telegram/unknown", nil)
	if code != 404 {
		t.Fatal(code)
	}
	st.StatusError = upstream
	f.server.Store = st
	for _, path := range []string{"telegram/status", "identity"} {
		code, _ = f.request(t, "alice", "GET", path, nil)
		if code != 503 {
			t.Fatal(path, code)
		}
	}
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	if code != 503 {
		t.Fatal(code)
	}
	st.StatusError = nil
	st.BeginError = upstream
	f.server.Store = st
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	if code != 503 {
		t.Fatal(code)
	}
	original := telegramRandomRead
	telegramRandomRead = func([]byte) (int, error) { return 0, upstream }
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	telegramRandomRead = original
	if code != 503 {
		t.Fatal(code)
	}
	st.Error = upstream
	f.server.Store = st
	code, _ = f.request(t, "alice", "POST", "telegram/disconnect", struct{}{})
	if code != 503 {
		t.Fatal(code)
	}
	for _, path := range []string{"poll", "renew", "advance", "defer", "link", "claim", "complete"} {
		input := map[string]any{"fence": 1}
		if path == "link" {
			input["chatId"] = "42"
			input["nonceHash"] = strings.Repeat("a", 64)
		}
		code, _ = f.request(t, "runner-a", "POST", "runner/telegram/"+path, input)
		if code != 503 {
			t.Fatal(path, code)
		}
	}
	code, _ = f.request(t, "runner-a", "POST", "runner/telegram/complete", map[string]any{"fence": 1, "deliveryId": "d", "deliveryFence": 1, "failureCode": "TELEGRAM_RATE_LIMITED", "retryAfterSeconds": 60})
	if code != 503 {
		t.Fatal(code)
	}
	for _, hash := range []string{"short", strings.Repeat("z", 64)} {
		code, _ = f.request(t, "runner-a", "POST", "runner/telegram/link", map[string]any{"fence": 1, "chatId": "42", "nonceHash": hash})
		if code != 400 {
			t.Fatal(code)
		}
	}
	code, _ = f.request(t, "runner-a", "POST", "runner/telegram/unknown", struct{}{})
	if code != 404 {
		t.Fatal(code)
	}
	code, _ = f.request(t, "runner-a", "POST", "runner/telegram/poll", map[string]any{"extra": 1})
	if code != 400 {
		t.Fatal(code)
	}
	f.server.Store = opaqueCoordinatorStore{f.store}
	code, _ = f.request(t, "runner-a", "POST", "runner/telegram/poll", struct{}{})
	if code != 503 {
		t.Fatal(code)
	}
	code, _ = f.request(t, "alice", "GET", "telegram/status", nil)
	if code != 503 {
		t.Fatal(code)
	}
	code, _ = f.request(t, "alice", "GET", "identity", nil)
	if code != 200 {
		t.Fatal(code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/watch/runner/telegram/poll", nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens["runner-a"])
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
}
func TestWatchOwnBotDeploymentGrants(t *testing.T) {
	base := Credential{TokenEnv: "WATCH_GRANT_A", Kind: "runner", ID: "a", Namespace: "n", Project: "p", Environment: "e", TelegramBotID: "123"}
	t.Setenv(base.TokenEnv, strings.Repeat("a", 40))
	t.Setenv("WATCH_GRANT_B", strings.Repeat("b", 40))
	if _, e := NewAuthenticator([]Credential{base}); e != nil {
		t.Fatal(e)
	}
	for _, scenario := range []string{"user", "invalid", "cross-scope", "two-bots"} {
		a, b := base, base
		b.TokenEnv = "WATCH_GRANT_B"
		b.ID = "b"
		switch scenario {
		case "user":
			a.Kind = "user"
		case "invalid":
			a.TelegramBotID = "-1"
		case "cross-scope":
			b.Project = "other"
		case "two-bots":
			b.TelegramBotID = "456"
		}
		if _, e := NewAuthenticator([]Credential{a, b}); e == nil {
			t.Fatal(scenario)
		}
	}
}
func TestWatchOwnBotLeaseAndProviderFailures(t *testing.T) {
	original := telegramRenewInterval
	telegramRenewInterval = 2 * time.Millisecond
	defer func() { telegramRenewInterval = original }()
	for _, scenario := range []string{"wrong-bot", "renew-error", "nil-lease", "changed-fence", "claim-error", "foreign-delivery", "bad-chat", "large-body", "send-failure", "complete-error", "poll-error", "oversized-batch", "negative-update", "overflow-update", "old-update", "link-error", "advance-error", "renew-during-poll", "send-rate", "poll-rate", "poll-rate-defer-error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			var renew atomic.Int64
			var completeFailure string
			coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/v1/watch/runner/telegram/")
				switch path {
				case "renew":
					renew.Add(1)
					if scenario == "renew-error" || scenario == "renew-during-poll" && renew.Load() > 1 {
						w.WriteHeader(409)
						return
					}
					if scenario == "nil-lease" {
						_, _ = fmt.Fprint(w, `{"lease":null}`)
						return
					}
					fence := int64(1)
					if scenario == "changed-fence" {
						fence = 2
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"lease": watch.TelegramPollLease{BotID: "123", Fence: fence, Offset: 5}})
				case "claim":
					if scenario == "claim-error" {
						w.WriteHeader(503)
						return
					}
					if strings.Contains(scenario, "delivery") || scenario == "bad-chat" || scenario == "large-body" || scenario == "send-failure" || scenario == "send-rate" || scenario == "complete-error" {
						bot, chat, body := "123", "42", "Generic"
						if scenario == "foreign-delivery" {
							bot = "999"
						}
						if scenario == "bad-chat" {
							chat = "-42"
						}
						if scenario == "large-body" {
							body = strings.Repeat("x", 3501)
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"delivery": watch.Delivery{Notification: watch.Notification{ID: "d"}, TelegramBotID: bot, TelegramChatID: chat, Fence: 2}, "body": body})
						return
					}
					_, _ = fmt.Fprint(w, `{"delivery":null}`)
				case "complete":
					var b map[string]any
					_ = json.NewDecoder(r.Body).Decode(&b)
					completeFailure, _ = b["failureCode"].(string)
					if scenario == "complete-error" {
						w.WriteHeader(503)
					} else {
						if scenario != "send-rate" {
							cancel()
						}
						_, _ = fmt.Fprint(w, `{"ok":true}`)
					}
				case "defer":
					if scenario == "poll-rate-defer-error" {
						w.WriteHeader(503)
					} else {
						_, _ = fmt.Fprint(w, `{"ok":true}`)
					}
				case "link":
					w.WriteHeader(503)
				case "advance":
					if scenario == "advance-error" {
						w.WriteHeader(503)
					} else {
						cancel()
						_, _ = fmt.Fprint(w, `{"ok":true}`)
					}
				default:
					t.Error("unexpected coordinator path", path)
					w.WriteHeader(503)
				}
			}))
			defer coord.Close()
			provider := fakeOwnBot(t, func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "poll-rate", "poll-rate-defer-error":
					w.WriteHeader(429)
					_, _ = fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":60}}`)
				case "poll-error":
					w.WriteHeader(503)
				case "renew-during-poll":
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(300 * time.Millisecond):
					}
				case "oversized-batch":
					updates := make([]telegramUpdate, 6)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": updates})
				case "negative-update":
					_, _ = fmt.Fprint(w, `{"ok":true,"result":[{"update_id":-1}]}`)
				case "overflow-update":
					_, _ = fmt.Fprint(w, `{"ok":true,"result":[{"update_id":9223372036854775807}]}`)
				case "old-update":
					_, _ = fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1},{"update_id":5}]}`)
				case "link-error":
					nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
					_, _ = fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":5,"message":{"text":"/start watch-%s","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`, nonce)
				default:
					_, _ = fmt.Fprint(w, `{"ok":true,"result":[{"update_id":5}]}`)
				}
			}, func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "send-rate":
					w.WriteHeader(429)
					_, _ = fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":60}}`)
				case "send-failure":
					w.WriteHeader(503)
				default:
					_, _ = fmt.Fprint(w, `{"ok":true,"result":{}}`)
				}
			})
			bot := &telegramBot{token: "123:private", botID: "123", base: provider.URL, http: provider.Client()}
			lease := watch.TelegramPollLease{BotID: "123", Fence: 1}
			if scenario == "wrong-bot" {
				lease.BotID = "999"
			}
			if e := bot.runLease(ctx, Client{Endpoint: coord.URL, Token: strings.Repeat("runner", 8)}, lease); e == nil {
				t.Fatal("failure accepted")
			}
			if scenario == "send-failure" && completeFailure != "PROVIDER_UNAVAILABLE" {
				t.Fatal("unsafe or missing failure", completeFailure)
			}
		})
	}
}
func TestWatchOwnBotRunnerLifecycleAndRetry(t *testing.T) {
	cfg := ownBotConfig(t)
	provider := fakeOwnBot(t, nil, nil)
	original := telegramRetryInterval
	telegramRetryInterval = time.Millisecond
	defer func() { telegramRetryInterval = original }()
	for _, scenario := range []string{"unconfigured-secret", "worker-forbidden", "worker-retry", "no-lease", "expired-lease"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			var calls atomic.Int32
			coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if bytes.Contains(raw, []byte("privateprivate")) || strings.Contains(r.Header.Get("Authorization"), "privateprivate") {
					t.Error("bot secret reached coordinator")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/register"):
					if !bytes.Contains(raw, []byte("PrivateWatchBot")) {
						t.Error("verified bot absent")
					}
					_, _ = fmt.Fprint(w, `{"ok":true}`)
				case strings.HasSuffix(r.URL.Path, "runner/claim"):
					_, _ = fmt.Fprint(w, `{"claim":null}`)
				case strings.HasSuffix(r.URL.Path, "telegram/poll"):
					calls.Add(1)
					switch scenario {
					case "worker-forbidden":
						w.WriteHeader(403)
					case "worker-retry":
						w.WriteHeader(503)
					case "expired-lease":
						_, _ = fmt.Fprint(w, `{"lease":{"botId":"123","fence":1}}`)
					default:
						_, _ = fmt.Fprint(w, `{"lease":null}`)
					}
				case strings.HasSuffix(r.URL.Path, "telegram/renew"):
					w.WriteHeader(409)
				default:
					w.WriteHeader(503)
				}
			}))
			defer coord.Close()
			botcfg := cfg
			if scenario == "unconfigured-secret" {
				botcfg.SecretEnv = "missing"
			}
			runner := Runner{Client: Client{Endpoint: coord.URL, Token: strings.Repeat("runner", 8)}, Executor: ExecutorFunc(func(context.Context, watch.Claim) (watch.Completion, error) { return watch.Completion{}, nil }), Telegram: &botcfg, telegramAPIBase: provider.URL, PollInterval: time.Millisecond}
			if e := runner.Run(ctx); e == nil {
				t.Fatal("runner stop error missing")
			}
			if scenario != "unconfigured-secret" && calls.Load() == 0 {
				t.Fatal("worker never started")
			}
		})
	}
	bot := &telegramBot{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(bot.run(ctx, Client{}), context.Canceled) {
		t.Fatal("already canceled worker")
	}
}
func TestWatchOwnBotCancellationAndWebhookInspectionFailure(t *testing.T) {
	cfg := ownBotConfig(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getMe") {
			_, _ = fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":true,"username":"PrivateWatchBot"}}`)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer provider.Close()
	if _, e := newTelegramBot(context.Background(), cfg, provider.URL, nil); e == nil {
		t.Fatal("failed webhook inspection accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	bot := &telegramBot{botID: "123"}
	cancel()
	if !errors.Is(bot.runLease(ctx, Client{}, watch.TelegramPollLease{BotID: "123", Fence: 1}), context.Canceled) {
		t.Fatal("canceled lease lost cancellation")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		_, _ = fmt.Fprint(w, `{"lease":null}`)
	}))
	defer coordinator.Close()
	if e := bot.run(ctx, Client{Endpoint: coordinator.URL, Token: strings.Repeat("runner", 8)}); !errors.Is(e, context.Canceled) {
		t.Fatal("poll cancellation hidden", e)
	}
}
