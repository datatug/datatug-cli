package watchdaemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

type TelegramMessage struct{ ID, ChatID, Body, DeliveryCapability string }
type TelegramSender interface {
	Send(context.Context, TelegramMessage) error
}
type FileTelegramSink struct{ Sink *FileEmailSink }

func (s FileTelegramSink) Send(ctx context.Context, m TelegramMessage) error {
	if !validChatID(m.ChatID) {
		return errors.New("invalid Watch Telegram recipient")
	}
	return s.Sink.Send(ctx, EmailMessage{ID: m.ID, To: m.ChatID + "@telegram.test", Subject: "DataTug Watch Telegram", Body: m.Body})
}
func validChatID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

// BotTelegramSender sends through the existing DataTugBot deployment identity.
// This provider never registers a webhook or starts a competing polling owner.
type BotTelegramSender struct {
	TokenEnv, APIBaseURL string
	HTTP                 *http.Client
}

func (s BotTelegramSender) Send(ctx context.Context, m TelegramMessage) error {
	if !validChatID(m.ChatID) || len(m.Body) > 3500 {
		return errors.New("invalid Watch Telegram message")
	}
	token := os.Getenv(s.TokenEnv)
	if token == "" || strings.ContainsAny(token, "/\r\n? #") {
		return errors.New("Watch Telegram token unavailable")
	}
	base := s.APIBaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	if ValidateEndpoint(base) != nil {
		return errors.New("Watch Telegram provider URL invalid")
	}
	body, _ := json.Marshal(map[string]any{"chat_id": m.ChatID, "text": m.Body, "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/bot"+token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return errors.New("Watch Telegram request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := owned.Do(req)
	if err != nil {
		return errors.New("Watch Telegram delivery failed")
	}
	defer response.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result) != nil || !result.OK {
		return errors.New("Watch Telegram delivery failed")
	}
	return nil
}

type telegramLinkStore interface {
	BeginTelegramLink(context.Context, watch.Actor, string, time.Time) error
	CompleteTelegramLink(context.Context, string, string) error
	TelegramStatus(context.Context, watch.Actor) (bool, error)
	DisconnectTelegram(context.Context, watch.Actor) error
}
type telegramCapabilityStore interface {
	CompleteTelegramLinkWithCapability(context.Context, string, string, string) error
}

// GatewayTelegramSender uses an operator-allowlisted existing bot host. The
// opaque host-minted capability binds delivery to the chat proven during link.
type GatewayTelegramSender struct {
	Endpoint, TokenEnv string
	HTTP               *http.Client
}

func (s GatewayTelegramSender) Send(ctx context.Context, m TelegramMessage) error {
	if ValidateEndpoint(s.Endpoint) != nil || !validChatID(m.ChatID) || m.DeliveryCapability == "" || len(m.DeliveryCapability) > 4096 || len(m.Body) > 3500 {
		return errors.New("Watch Telegram gateway configuration unavailable")
	}
	token := os.Getenv(s.TokenEnv)
	if len(token) < 32 {
		return errors.New("Watch Telegram gateway credential unavailable")
	}
	body, _ := json.Marshal(map[string]string{"deliveryId": m.ID, "chatId": m.ChatID, "body": m.Body, "deliveryCapability": m.DeliveryCapability})
	req, err := http.NewRequestWithContext(ctx, "POST", s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("Watch Telegram gateway request invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := owned.Do(req)
	if err != nil {
		return errors.New("Watch Telegram gateway delivery failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("Watch Telegram gateway delivery failed")
	}
	return nil
}

func (s *Server) telegramUser(w http.ResponseWriter, r *http.Request, path string, c Credential) bool {
	if !strings.HasPrefix(path, "telegram/") {
		return false
	}
	store, ok := s.Store.(telegramLinkStore)
	if !ok {
		fail(w, errors.New("link store unavailable"))
		return true
	}
	a := credentialActor(c)
	switch {
	case path == "telegram/status" && r.Method == "GET":
		connected, err := store.TelegramStatus(r.Context(), a)
		if err != nil {
			fail(w, err)
		} else {
			reply(w, 200, map[string]any{"connected": connected, "botUsername": "DataTugBot"})
		}
	case path == "telegram/connect" && r.Method == "POST":
		var empty struct{}
		if decode(w, r, &empty) != nil {
			fail(w, watch.ErrInvalid)
			return true
		}
		if s.Telegram == nil || s.TelegramCallbackSecret == "" {
			fail(w, errors.New("Telegram link bridge unavailable"))
			return true
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			fail(w, err)
			return true
		}
		nonce := base64.RawURLEncoding.EncodeToString(raw[:])
		hash := sha256.Sum256([]byte(nonce))
		expires := time.Now().UTC().Add(10 * time.Minute)
		if err := store.BeginTelegramLink(r.Context(), a, hex.EncodeToString(hash[:]), expires); err != nil {
			fail(w, err)
		} else {
			reply(w, 200, map[string]any{"url": "https://t.me/DataTugBot?start=watch-" + nonce, "expiresAt": expires})
		}
	case path == "telegram/disconnect" && r.Method == "POST":
		var empty struct{}
		if decode(w, r, &empty) != nil {
			fail(w, watch.ErrInvalid)
			return true
		}
		if err := store.DisconnectTelegram(r.Context(), a); err != nil {
			fail(w, err)
		} else {
			reply(w, 200, map[string]bool{"ok": true})
		}
	default:
		fail(w, watch.ErrNotFound)
	}
	return true
}

// telegramUpdate accepts only an operator-configured existing bot owner's
// authenticated forwarding request. A private chat proves its own recipient;
// users never submit chat IDs through the browser API.
func (s *Server) telegramUpdate(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	secret := s.TelegramCallbackSecret
	if r.Method != "POST" || len(secret) < 32 || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
		reply(w, 401, map[string]any{"error": apiError{"UNAUTHENTICATED", "Telegram bridge authentication required"}})
		return
	}
	store, ok := s.Store.(telegramLinkStore)
	if !ok {
		fail(w, errors.New("link store unavailable"))
		return
	}
	var update struct {
		DeliveryCapability string `json:"deliveryCapability"`
		Message            *struct {
			Text string `json:"text"`
			From *struct {
				ID  int64 `json:"id"`
				Bot bool  `json:"is_bot"`
			} `json:"from"`
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&update) != nil {
		fail(w, watch.ErrInvalid)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		fail(w, watch.ErrInvalid)
		return
	}
	message := update.Message
	if message == nil || message.From == nil || message.From.Bot || message.Chat.Type != "private" || message.Chat.ID <= 0 || message.From.ID != message.Chat.ID {
		fail(w, watch.ErrInvalid)
		return
	}
	fields := strings.Fields(message.Text)
	if len(fields) != 2 || (fields[0] != "/start" && fields[0] != "/start@DataTugBot") || !strings.HasPrefix(fields[1], "watch-") {
		fail(w, watch.ErrInvalid)
		return
	}
	nonce := strings.TrimPrefix(fields[1], "watch-")
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != nonce {
		fail(w, watch.ErrInvalid)
		return
	}
	hash := sha256.Sum256([]byte(nonce))
	if _, gateway := s.Telegram.(GatewayTelegramSender); gateway && update.DeliveryCapability == "" {
		fail(w, watch.ErrInvalid)
		return
	}
	if update.DeliveryCapability != "" {
		capabilityStore, ok := s.Store.(telegramCapabilityStore)
		if !ok || len(update.DeliveryCapability) > 4096 {
			fail(w, watch.ErrInvalid)
			return
		}
		err = capabilityStore.CompleteTelegramLinkWithCapability(r.Context(), hex.EncodeToString(hash[:]), strconv.FormatInt(message.Chat.ID, 10), update.DeliveryCapability)
	} else {
		err = store.CompleteTelegramLink(r.Context(), hex.EncodeToString(hash[:]), strconv.FormatInt(message.Chat.ID, 10))
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
