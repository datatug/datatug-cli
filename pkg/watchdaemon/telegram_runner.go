package watchdaemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

type telegramRateLimitError struct{ Seconds int }

func (e *telegramRateLimitError) Error() string { return "watch Telegram provider rate limited" }

var errTelegramRecipientUnavailable = errors.New("watch Telegram recipient unavailable")

var telegramRenewInterval = 5 * time.Second
var telegramRetryInterval = 2 * time.Second

// TelegramBotConfig is runner-only deployment configuration. Neither token nor
// secret is sent to the coordinator or saved in a project.
type TelegramBotConfig struct {
	BotID     string `json:"botID"`
	SecretEnv string `json:"secretEnv,omitempty"`
	TokenEnv  string `json:"tokenEnv,omitempty"`
}
type runnerRegistration struct {
	TelegramBotID       string `json:"telegramBotId,omitempty"`
	TelegramBotUsername string `json:"telegramBotUsername,omitempty"`
}
type telegramBot struct {
	token, botID, username, base string
	http                         *http.Client
}

func newTelegramBot(ctx context.Context, c TelegramBotConfig, base string, client *http.Client) (*telegramBot, error) {
	if !validChatID(c.BotID) || (c.SecretEnv == "") == (c.TokenEnv == "") {
		return nil, errors.New("watch runner Telegram requires botID and exactly one secretEnv or tokenEnv")
	}
	token := os.Getenv(c.TokenEnv)
	if c.SecretEnv != "" {
		secret := os.Getenv(c.SecretEnv)
		if secret == "" {
			return nil, errors.New("watch runner Telegram secret unavailable")
		}
		token = c.BotID + ":" + secret
	}
	parts := strings.Split(token, ":")
	if len(parts) != 2 || parts[0] != c.BotID || len(parts[1]) < 20 || strings.ContainsAny(token, "/\r\n? #") {
		return nil, errors.New("watch runner Telegram credential invalid")
	}
	if base == "" {
		base = "https://api.telegram.org"
	}
	if ValidateEndpoint(base) != nil {
		return nil, errors.New("watch Telegram provider URL invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	b := &telegramBot{token: token, botID: c.BotID, base: strings.TrimRight(base, "/"), http: &owned}
	var me struct {
		ID       int64  `json:"id"`
		Bot      bool   `json:"is_bot"`
		Username string `json:"username"`
	}
	if e := b.call(ctx, "getMe", struct{}{}, &me); e != nil {
		return nil, e
	}
	if strconv.FormatInt(me.ID, 10) != c.BotID || !me.Bot || !validBotUsername(me.Username) {
		return nil, errors.New("watch runner Telegram bot identity mismatch")
	}
	b.username = me.Username
	var hook struct {
		URL string `json:"url"`
	}
	if e := b.call(ctx, "getWebhookInfo", struct{}{}, &hook); e != nil {
		return nil, e
	}
	if hook.URL != "" {
		return nil, errors.New("watch runner Telegram bot already has a webhook; configure a dedicated bot")
	}
	return b, nil
}
func (b *telegramBot) call(ctx context.Context, method string, input, output any) error {
	raw, e := json.Marshal(input)
	if e != nil {
		return errors.New("watch Telegram request invalid")
	}
	req, e := http.NewRequestWithContext(ctx, "POST", b.base+"/bot"+b.token+"/"+method, bytes.NewReader(raw))
	if e != nil {
		return errors.New("watch Telegram request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := b.http.Do(req)
	if e != nil {
		return errors.New("watch Telegram provider unavailable")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	decoded := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&envelope)
	if decoded != nil || resp.StatusCode != 200 || !envelope.OK {
		if decoded == nil && resp.StatusCode == 429 && envelope.ErrorCode == 429 && envelope.Parameters.RetryAfter > 0 && envelope.Parameters.RetryAfter <= 86400 {
			return &telegramRateLimitError{Seconds: envelope.Parameters.RetryAfter}
		}
		if decoded == nil && method == "sendMessage" && resp.StatusCode == envelope.ErrorCode {
			description := strings.ToLower(envelope.Description)
			if resp.StatusCode == 403 && (description == "forbidden: bot was blocked by the user" || description == "forbidden: user is deactivated") || resp.StatusCode == 400 && description == "bad request: chat not found" {
				return errTelegramRecipientUnavailable
			}
		}
		return errors.New("watch Telegram provider rejected request")
	}

	if output != nil && json.Unmarshal(envelope.Result, output) != nil {
		return errors.New("watch Telegram provider response invalid")
	}
	return nil
}

type telegramUpdate struct {
	ID      int64 `json:"update_id"`
	Message *struct {
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

func (u telegramUpdate) link() (hash, chat string) {
	m := u.Message
	if m == nil || m.From == nil || m.From.Bot || m.Chat.Type != "private" || m.Chat.ID <= 0 || m.From.ID != m.Chat.ID || !strings.HasPrefix(m.Text, "/start watch-") {
		return "", ""
	}
	nonce := strings.TrimPrefix(m.Text, "/start watch-")
	raw, e := base64.RawURLEncoding.DecodeString(nonce)
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != nonce {
		return "", ""
	}
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:]), strconv.FormatInt(m.Chat.ID, 10)
}
func (c Client) telegramPoll(ctx context.Context, path string, fence int64) (*watch.TelegramPollLease, error) {
	var out struct {
		Lease *watch.TelegramPollLease `json:"lease"`
	}
	e := c.call(ctx, "runner/telegram/"+path, map[string]int64{"fence": fence}, &out)
	return out.Lease, e
}
func (b *telegramBot) run(ctx context.Context, c Client) error {
	for ctx.Err() == nil {
		lease, e := c.telegramPoll(ctx, "poll", 0)
		if e == nil && lease != nil {
			e = b.runLease(ctx, c, *lease)
		}
		if errors.Is(e, watch.ErrForbidden) {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			slog.Warn("watch_telegram_unavailable", "code", "PROVIDER_OR_LEASE_UNAVAILABLE")
		}
		if e = waitContext(ctx, telegramRetryInterval); e != nil {
			return e
		}
	}
	return ctx.Err()
}
func (b *telegramBot) runLease(ctx context.Context, c Client, lease watch.TelegramPollLease) error {
	if lease.BotID != b.botID {
		return watch.ErrForbidden
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewed := make(chan error, 1)
	go func() {
		t := time.NewTicker(telegramRenewInterval)
		defer t.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				renewed <- leaseCtx.Err()
				return
			case <-t.C:
				if _, e := c.telegramPoll(leaseCtx, "renew", lease.Fence); e != nil {
					cancel()
					renewed <- e
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-renewed }()
	for leaseCtx.Err() == nil {
		current, e := c.telegramPoll(leaseCtx, "renew", lease.Fence)
		if e != nil {
			return e
		}
		if current == nil || current.BotID != b.botID || current.Fence != lease.Fence {
			return watch.ErrStaleLease
		}
		lease.Offset = current.Offset
		// A bounded delivery drain keeps linking responsive. Coordinator state checks
		// subscription, bot identity, quota and both poll/delivery fences each time.
		for n := 0; n < 5; n++ {
			var out struct {
				Delivery *watch.Delivery `json:"delivery"`
				Body     string          `json:"body"`
			}
			if e = c.call(leaseCtx, "runner/telegram/claim", map[string]int64{"fence": lease.Fence}, &out); e != nil {
				return e
			}
			if out.Delivery == nil {
				break
			}
			d := out.Delivery
			if d.TelegramBotID != b.botID || !validChatID(d.TelegramChatID) || len(out.Body) > 3500 {
				return watch.ErrForbidden
			}
			failure := ""
			retryAfter := 0
			var sendError error
			if e = b.call(leaseCtx, "sendMessage", map[string]any{"chat_id": d.TelegramChatID, "text": out.Body, "disable_web_page_preview": true}, nil); e != nil {
				sendError = e
				failure = "PROVIDER_UNAVAILABLE"
				var rate *telegramRateLimitError
				if errors.As(e, &rate) {
					failure = "TELEGRAM_RATE_LIMITED"
					retryAfter = rate.Seconds
				}
				if errors.Is(e, errTelegramRecipientUnavailable) {
					failure = "TELEGRAM_RECIPIENT_UNAVAILABLE"
				}
			}
			if e = c.call(leaseCtx, "runner/telegram/complete", map[string]any{"fence": lease.Fence, "deliveryId": d.ID, "deliveryFence": d.Fence, "failureCode": failure, "retryAfterSeconds": retryAfter}, nil); e != nil {
				return e
			}
			if retryAfter > 0 {
				return sendError
			}
		}
		var updates []telegramUpdate
		if e = b.call(leaseCtx, "getUpdates", map[string]any{"offset": lease.Offset, "limit": 5, "timeout": 5, "allowed_updates": []string{"message"}}, &updates); e != nil {
			var rate *telegramRateLimitError
			if errors.As(e, &rate) {
				if deferError := c.call(leaseCtx, "runner/telegram/defer", map[string]any{"fence": lease.Fence, "retryAfterSeconds": rate.Seconds}, nil); deferError != nil {
					return deferError
				}
			}
			return e
		}
		if len(updates) > 5 {
			return errors.New("watch Telegram update batch invalid")
		}
		next := lease.Offset
		for _, u := range updates {
			if u.ID < 0 || u.ID == int64(^uint64(0)>>1) {
				return errors.New("watch Telegram update ID invalid")
			}
			if u.ID < next {
				continue
			}
			hash, chat := u.link()
			if hash != "" {
				e = c.call(leaseCtx, "runner/telegram/link", map[string]any{"fence": lease.Fence, "nonceHash": hash, "chatId": chat}, nil)
				if e != nil && !errors.Is(e, watch.ErrInvalid) && !errors.Is(e, watch.ErrNotFound) && !errors.Is(e, watch.ErrForbidden) {
					return e
				}
			}
			next = u.ID + 1
			if e = c.call(leaseCtx, "runner/telegram/advance", map[string]any{"fence": lease.Fence, "offset": next}, nil); e != nil {
				return e
			}
		}
	}
	return leaseCtx.Err()
}
