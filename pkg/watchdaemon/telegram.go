package watchdaemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

var telegramRandomRead = rand.Read

func validChatID(id string) bool {
	n, e := strconv.ParseInt(id, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == id
}
func validBotUsername(name string) bool {
	if len(name) < 5 || len(name) > 32 || !strings.HasSuffix(strings.ToLower(name), "bot") {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

type telegramLinkStore interface {
	BeginTelegramLink(context.Context, watch.Actor, string, time.Time) error
	TelegramProviderStatus(context.Context, watch.Actor) (watch.TelegramProviderStatus, error)
	DisconnectTelegram(context.Context, watch.Actor) error
}
type telegramRunnerStore interface {
	ClaimTelegramPoll(context.Context, watch.RunnerIdentity) (*watch.TelegramPollLease, error)
	RenewTelegramPoll(context.Context, watch.RunnerIdentity, int64) (*watch.TelegramPollLease, error)
	AdvanceTelegramPoll(context.Context, watch.RunnerIdentity, int64, int64) error
	DeferTelegramPoll(context.Context, watch.RunnerIdentity, int64, int) error
	CompleteTelegramLinkByRunner(context.Context, watch.RunnerIdentity, int64, string, string) error
	ClaimTelegramNotification(context.Context, watch.RunnerIdentity, int64) (*watch.Delivery, error)
	FinishTelegramNotification(context.Context, watch.RunnerIdentity, int64, string, int64, string, ...int) error
}

func (s *Server) telegramProvider(ctx context.Context, a watch.Actor) (watch.TelegramProviderStatus, error) {
	if st, ok := s.Store.(telegramLinkStore); ok {
		return st.TelegramProviderStatus(ctx, a)
	}
	return watch.TelegramProviderStatus{}, nil
}
func (s *Server) telegramAvailable(ctx context.Context, a watch.Actor) bool {
	p, e := s.telegramProvider(ctx, a)
	return e == nil && p.BotID != "" && validBotUsername(p.BotUsername)
}
func (s *Server) telegramUser(w http.ResponseWriter, r *http.Request, path string, c Credential) bool {
	if !strings.HasPrefix(path, "telegram/") {
		return false
	}
	st, ok := s.Store.(telegramLinkStore)
	if !ok {
		fail(w, errors.New("Telegram link store unavailable"))
		return true
	}
	a := credentialActor(c)
	switch {
	case path == "telegram/status" && r.Method == "GET":
		p, e := st.TelegramProviderStatus(r.Context(), a)
		if e != nil {
			fail(w, e)
		} else {
			reply(w, 200, p)
		}
	case path == "telegram/connect" && r.Method == "POST":
		var empty struct{}
		if decode(w, r, &empty) != nil {
			fail(w, watch.ErrInvalid)
			return true
		}
		p, e := st.TelegramProviderStatus(r.Context(), a)
		if e != nil {
			fail(w, e)
			return true
		}
		if p.BotID == "" || !validBotUsername(p.BotUsername) {
			fail(w, errors.New("Telegram runner unavailable"))
			return true
		}
		var raw [32]byte
		if _, e = telegramRandomRead(raw[:]); e != nil {
			fail(w, e)
			return true
		}
		nonce := base64.RawURLEncoding.EncodeToString(raw[:])
		hash := sha256.Sum256([]byte(nonce))
		expires := time.Now().UTC().Add(10 * time.Minute)
		if e = st.BeginTelegramLink(r.Context(), a, hex.EncodeToString(hash[:]), expires); e != nil {
			fail(w, e)
		} else {
			reply(w, 200, map[string]any{"url": "https://t.me/" + p.BotUsername + "?start=watch-" + nonce, "expiresAt": expires})
		}
	case path == "telegram/disconnect" && r.Method == "POST":
		var empty struct{}
		if decode(w, r, &empty) != nil {
			fail(w, watch.ErrInvalid)
		} else if e := st.DisconnectTelegram(r.Context(), a); e != nil {
			fail(w, e)
		} else {
			reply(w, 200, map[string]bool{"ok": true})
		}
	default:
		fail(w, watch.ErrNotFound)
	}
	return true
}
func (s *Server) telegramRunner(w http.ResponseWriter, r *http.Request, path string, id watch.RunnerIdentity) bool {
	if !strings.HasPrefix(path, "runner/telegram/") {
		return false
	}
	if id.TelegramBotID == "" {
		fail(w, watch.ErrForbidden)
		return true
	}
	st, ok := s.Store.(telegramRunnerStore)
	if !ok {
		fail(w, errors.New("Telegram runner store unavailable"))
		return true
	}
	var b struct {
		Fence             int64  `json:"fence"`
		Offset            int64  `json:"offset,omitempty"`
		NonceHash         string `json:"nonceHash,omitempty"`
		ChatID            string `json:"chatId,omitempty"`
		DeliveryID        string `json:"deliveryId,omitempty"`
		DeliveryFence     int64  `json:"deliveryFence,omitempty"`
		FailureCode       string `json:"failureCode,omitempty"`
		RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
	}
	if decode(w, r, &b) != nil {
		fail(w, watch.ErrInvalid)
		return true
	}
	var result any = map[string]bool{"ok": true}
	var e error
	switch path {
	case "runner/telegram/poll":
		var lease *watch.TelegramPollLease
		lease, e = st.ClaimTelegramPoll(r.Context(), id)
		result = map[string]any{"lease": lease}
	case "runner/telegram/renew":
		var lease *watch.TelegramPollLease
		lease, e = st.RenewTelegramPoll(r.Context(), id, b.Fence)
		result = map[string]any{"lease": lease}
	case "runner/telegram/defer":
		e = st.DeferTelegramPoll(r.Context(), id, b.Fence, b.RetryAfterSeconds)
	case "runner/telegram/advance":
		e = st.AdvanceTelegramPoll(r.Context(), id, b.Fence, b.Offset)
	case "runner/telegram/link":
		if len(b.NonceHash) != 64 || !validChatID(b.ChatID) {
			e = watch.ErrInvalid
		} else {
			_, e = hex.DecodeString(b.NonceHash)
			if e == nil {
				e = st.CompleteTelegramLinkByRunner(r.Context(), id, b.Fence, b.NonceHash, b.ChatID)
			} else {
				e = watch.ErrInvalid
			}
		}
	case "runner/telegram/claim":
		var d *watch.Delivery
		d, e = st.ClaimTelegramNotification(r.Context(), id, b.Fence)
		body := ""
		if d != nil {
			body = s.notificationText(d.Notification)
			d.Email = ""
			d.Phone = ""
		}
		result = map[string]any{"delivery": d, "body": body}
	case "runner/telegram/complete":
		if b.RetryAfterSeconds != 0 {
			e = st.FinishTelegramNotification(r.Context(), id, b.Fence, b.DeliveryID, b.DeliveryFence, b.FailureCode, b.RetryAfterSeconds)
		} else {
			e = st.FinishTelegramNotification(r.Context(), id, b.Fence, b.DeliveryID, b.DeliveryFence, b.FailureCode)
		}
	default:
		e = watch.ErrNotFound
	}
	if e != nil {
		fail(w, e)
	} else {
		reply(w, 200, result)
	}
	return true
}
