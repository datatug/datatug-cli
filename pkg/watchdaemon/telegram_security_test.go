package watchdaemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type opaqueCoordinatorStore struct{ CoordinatorStore }

func TestWatchTelegramCallbackRejectsForgedAndForeignRoutes(t *testing.T) {
	f := protocolFixture(t)
	f.server.Telegram = FileTelegramSink{}
	f.server.TelegramCallbackSecret = strings.Repeat("bridge", 8)
	f.server.TelegramRouteID = "local"
	invoke := func(body string) int {
		req := httptest.NewRequest("POST", "/v1/watch/telegram/update", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", f.server.TelegramCallbackSecret)
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, body := range []string{`invalid`, `{} {}`, `{}`, `{"message":{"from":{"id":1},"chat":{"id":2,"type":"private"},"text":"/start watch-local_x"}}`, `{"message":{"from":{"id":1},"chat":{"id":1,"type":"group"},"text":"/start watch-local_x"}}`, `{"message":{"from":{"id":1},"chat":{"id":1,"type":"private"},"text":"/other watch-local_x"}}`, `{"message":{"from":{"id":1},"chat":{"id":1,"type":"private"},"text":"/start watch-foreign_x"}}`, `{"message":{"from":{"id":1},"chat":{"id":1,"type":"private"},"text":"/start watch-local_x"}}`} {
		if invoke(body) != 400 {
			t.Fatal("invalid Telegram proof accepted", body)
		}
	}
	for _, path := range []string{"telegram/connect", "telegram/disconnect"} {
		code, _ := f.request(t, "alice", "POST", path, map[string]any{"chatId": "12345"})
		if code != 400 {
			t.Fatal("browser supplied recipient", path, code)
		}
	}
	code, _ := f.request(t, "alice", "GET", "telegram/unknown", nil)
	if code != 404 {
		t.Fatal(code)
	}
	code, _ = f.request(t, "alice", "POST", "telegram/disconnect", struct{}{})
	if code != 200 {
		t.Fatal(code)
	}
	read := telegramRandomRead
	telegramRandomRead = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	telegramRandomRead = read
	if code != 503 {
		t.Fatal("nonce entropy failure accepted")
	}
	f.server.Telegram = nil
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	if code != 503 {
		t.Fatal("unconfigured bridge offered link")
	}
	f.server.Store = opaqueCoordinatorStore{f.store}
	code, _ = f.request(t, "alice", "GET", "telegram/status", nil)
	if code != 503 {
		t.Fatal("missing link store accepted")
	}
	if invoke(`{}`) != 503 {
		t.Fatal("missing callback store accepted")
	}
}

func TestWatchTelegramGatewayRequiresRouteBoundHostCapability(t *testing.T) {
	f := protocolFixture(t)
	f.server.Telegram = GatewayTelegramSender{}
	f.server.TelegramCallbackSecret = strings.Repeat("bridge", 8)
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
	start := u.Query().Get("start")
	post := func(capability string) int {
		body, _ := json.Marshal(map[string]any{"deliveryCapability": capability, "message": map[string]any{"text": "/start " + start, "from": map[string]any{"id": 12345}, "chat": map[string]any{"id": 12345, "type": "private"}}})
		req := httptest.NewRequest("POST", "/v1/watch/telegram/update", strings.NewReader(string(body)))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", f.server.TelegramCallbackSecret)
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		return rec.Code
	}
	if post("") != 400 {
		t.Fatal("gateway linked without host capability")
	}
	if post(strings.Repeat("x", 4097)) != 400 {
		t.Fatal("unbounded host capability accepted")
	}
	capBody := fmt.Sprintf(`{"route":"local","chat":"12345","nonce":"host-bound-nonce","exp":%d}`, time.Now().Add(time.Hour).Unix())
	capability := base64.RawURLEncoding.EncodeToString([]byte(capBody)) + ".hostsignature"
	if code := post(capability); code != 200 {
		t.Fatalf("trusted route-bound capability rejected %d", code)
	}
	_ = f.store.Close()
	code, _ = f.request(t, "alice", "GET", "telegram/status", nil)
	if code != 503 {
		t.Fatal("closed link storage appeared connected")
	}
	code, _ = f.request(t, "alice", "POST", "telegram/connect", struct{}{})
	if code != 503 {
		t.Fatal("failed nonce persistence accepted")
	}
	code, _ = f.request(t, "alice", "POST", "telegram/disconnect", struct{}{})
	if code != 503 {
		t.Fatal("failed disconnect persistence accepted")
	}
	for _, route := range []string{"", strings.Repeat("x", 9), "bad-"} {
		f.server.TelegramRouteID = route
		if f.server.telegramConfigured() {
			t.Fatal("invalid bot route advertised", route)
		}
	}
}

func TestWatchSMSSharedProviderSuccessAndInvalidInput(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"sid":"SMaccepted"}`)), Request: r}, nil
	})
	sender := TwilioSMSSender{}
	if err := sender.Send(context.Background(), SMSMessage{ID: "sms-accepted", To: "+353123456789", Body: "Generic update"}); err != nil {
		t.Fatal(err)
	}
	if sender.Send(context.Background(), SMSMessage{}) == nil {
		t.Fatal("invalid SMS accepted")
	}
}
