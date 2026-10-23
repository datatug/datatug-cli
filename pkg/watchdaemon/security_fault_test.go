package watchdaemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/watch"
)

func TestWatchAuthenticationRejectsAmbiguousAuthority(t *testing.T) {
	t.Setenv("WATCH_AUTH_A", strings.Repeat("a", 40))
	t.Setenv("WATCH_AUTH_B", strings.Repeat("b", 40))
	t.Setenv("WATCH_AUTH_SHORT", "short")
	base := Credential{ID: "alice", TokenEnv: "WATCH_AUTH_A", Kind: "user", Namespace: "n", Project: "p", Environment: "e"}
	for _, change := range []func(*Credential){func(c *Credential) { c.Tier = "unknown" }, func(c *Credential) { c.Email = "invalid" }, func(c *Credential) { c.Phone = "invalid" }, func(c *Credential) { c.ID = "" }, func(c *Credential) { c.TokenEnv = "WATCH_AUTH_SHORT" }, func(c *Credential) { c.Kind = "runner"; c.Tier = "paid" }} {
		c := base
		change(&c)
		if _, err := NewAuthenticator([]Credential{c}); err == nil {
			t.Fatalf("invalid identity accepted %+v", c)
		}
	}
	if _, err := NewAuthenticator(nil); err == nil {
		t.Fatal("empty authority accepted")
	}
	if _, err := NewAuthenticator([]Credential{base, base}); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	other := base
	other.ID = "bob"
	if _, err := NewAuthenticator([]Credential{base, other}); err == nil {
		t.Fatal("shared token accepted")
	}
	other.TokenEnv = "WATCH_AUTH_B"
	other.AccountID = "alice"
	other.Tier = "paid"
	if _, err := NewAuthenticator([]Credential{base, other}); err == nil {
		t.Fatal("conflicting account tier accepted")
	}
	var nilAuth *Authenticator
	if _, ok := nilAuth.Authenticate(httptest.NewRequest("GET", "/", nil), "user"); ok {
		t.Fatal("nil authority authenticated")
	}
	auth, err := NewAuthenticator([]Credential{base})
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "Basic fake", "Bearer " + strings.Repeat("z", 5000)} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", header)
		if _, ok := auth.Authenticate(req, "user"); ok {
			t.Fatal("invalid header authenticated")
		}
	}
}

func TestWatchDeliveryChannelRoutingAndUnsafeMetadata(t *testing.T) {
	f := protocolFixture(t)
	ctx := context.Background()
	f.server.SMS = FileSMSSink{Sink: &FileEmailSink{Path: filepath.Join(t.TempDir(), "sms")}}
	f.server.Telegram = FileTelegramSink{Sink: &FileEmailSink{Path: filepath.Join(t.TempDir(), "telegram")}}
	for _, channel := range []string{"in_app", "sms", "telegram"} {
		d := watch.Delivery{Notification: watch.Notification{ID: channel + "-1", Scope: protocolScope, WatchID: "shipping"}, Channel: channel, Phone: "+353123456789", TelegramChatID: "12345"}
		if err := f.server.deliver(ctx, d); err != nil {
			t.Fatal(channel, err)
		}
	}
	if f.server.deliver(ctx, watch.Delivery{Channel: "unknown"}) == nil {
		t.Fatal("unknown provider delivered")
	}
	f.server.Email = emailSenderFunc(func(context.Context, EmailMessage) error { return errors.New("upstream unavailable") })
	if f.server.deliver(ctx, watch.Delivery{Channel: "email"}) == nil {
		t.Fatal("failed provider considered delivered")
	}
	for _, n := range []watch.Notification{{Scope: protocolScope, WatchID: "../../escape"}, {Scope: watch.Scope{Project: "unknown"}, WatchID: "shipping"}} {
		text := f.server.notificationText(n)
		if strings.Contains(text, "https://") {
			t.Fatal("unsafe/unmapped deep link sent")
		}
	}
	f.server.ProjectUIURLs = nil
	f.server.PublicUIURL = "https://datatug.app/store/test/project/northwind"
	if !strings.Contains(f.server.notificationText(watch.Notification{Scope: protocolScope, WatchID: "shipping"}), "/watch/shipping") {
		t.Fatal("single project fallback missing")
	}
	for _, m := range []EmailMessage{{ID: "bad", To: "invalid", Subject: "Update"}, {ID: "bad", To: "alice@example.test", Subject: "bad\r\nheader"}, {ID: "", To: "alice@example.test"}} {
		if validateEmail(m) == nil {
			t.Fatal("invalid email accepted")
		}
	}
	if validPhone("+1234x6789") || validPhone("invalid") {
		t.Fatal("invalid phone accepted")
	}
	if (FileSMSSink{}).Send(ctx, SMSMessage{To: "bad"}) == nil {
		t.Fatal("invalid SMS sink recipient accepted")
	}
	if (FileTelegramSink{}).Send(ctx, TelegramMessage{ChatID: "-123"}) == nil {
		t.Fatal("group Telegram sink accepted")
	}
}

func TestWatchClientRejectsNetworkAndResponseFailuresWithoutLeakingTokens(t *testing.T) {
	ctx := context.Background()
	token := strings.Repeat("secret", 8)
	if err := (Client{Endpoint: "http://evil.example", Token: token}).Register(ctx); err == nil {
		t.Fatal("insecure coordinator accepted")
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Test-Mode") {
		default:
		}
	}))
	provider.Close()
	client := Client{Endpoint: provider.URL, Token: token}
	if err := client.Register(ctx); err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("network error leaked token")
	}
	for _, status := range []int{400, 404, 409, 500, 302} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://evil.example")
			w.WriteHeader(status)
		}))
		err := (Client{Endpoint: server.URL, Token: token}).Register(ctx)
		server.Close()
		if err == nil {
			t.Fatal("failure response accepted", status)
		}
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("invalid JSON")) }))
	defer bad.Close()
	if _, err := (Client{Endpoint: bad.URL, Token: token}).Claim(ctx); err == nil {
		t.Fatal("invalid claim JSON accepted")
	}
	if err := (Client{Endpoint: bad.URL, Token: token}).call(ctx, "runner/register", make(chan int), nil); !errors.Is(err, watch.ErrInvalid) {
		t.Fatal("unencodable request accepted")
	}
}
