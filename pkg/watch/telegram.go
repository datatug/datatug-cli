package watch

import (
	"context"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"regexp"
	"strconv"
	"time"
)

const (
	telegramLinks    = "dt_watch_telegram_links"
	telegramRequests = "dt_watch_telegram_requests"
	quotas           = "dt_watch_quotas"
)

type telegramLink struct {
	Generation  int64  `json:"generation"`
	FailureCode string `json:"failureCode,omitempty"`
	Scope
	BotID     string    `json:"botId"`
	UserID    string    `json:"userId"`
	ChatID    string    `json:"chatId"`
	At        time.Time `json:"at"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}
type telegramRequest struct {
	Scope
	BotID          string    `json:"botId"`
	UserID         string    `json:"userId"`
	Hash           string    `json:"hash"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ConsumedChatID string    `json:"consumedChatId,omitempty"`
}

var nonceHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// BeginTelegramLink binds a short-lived connection request to the authenticated
// user, project scope and one registered bot. No browser supplies a chat ID.
func (s *Store) BeginTelegramLink(ctx context.Context, a Actor, nonceHash string, expiresAt time.Time) error {
	if err := s.actor(a); err != nil {
		return err
	}
	if !nonceHashPattern.MatchString(nonceHash) || !expiresAt.After(s.now()) || expiresAt.After(s.now().Add(15*time.Minute)) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		provider, err := s.telegramProvider(ctx, tx, a.Scope)
		if err != nil {
			return err
		}
		requests, err := s.telegramRequestsFor(ctx, tx, a)
		if err != nil {
			return err
		}
		for _, request := range requests {
			if request.UserID == a.ID {
				if err = tx.Delete(ctx, key(telegramRequests, request.Scope, request.Hash)); err != nil {
					return err
				}
			}
		}
		request := telegramRequest{Scope: a.Scope, BotID: provider.BotID, UserID: a.ID, Hash: nonceHash, ExpiresAt: expiresAt}
		return s.save(ctx, tx, telegramRequests, a.Scope, nonceHash, "", a.ID, "", request)
	})
}

// CompleteTelegramLinkByRunner accepts proof only from the fenced provider
// poller whose operator grant owns this bot and scope. Replayed messages for the
// same chat are idempotent; another chat never steals a consumed connection.
func (s *Store) CompleteTelegramLinkByRunner(ctx context.Context, id RunnerIdentity, pollFence int64, nonceHash, chatID string) error {
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if _, err := s.poll(ctx, tx, id, pollFence); err != nil {
			return err
		}
		if !nonceHashPattern.MatchString(nonceHash) {
			return ErrInvalid
		}
		chat, err := strconv.ParseInt(chatID, 10, 64)
		if err != nil || chat <= 0 || strconv.FormatInt(chat, 10) != chatID {
			return ErrInvalid
		}
		request, err := read[telegramRequest](ctx, tx, telegramRequests, id.Scope, nonceHash)
		if err != nil {
			return err
		}
		if request.BotID != id.TelegramBotID {
			return ErrForbidden
		}
		if request.ConsumedChatID != "" {
			if request.ConsumedChatID == chatID {
				return nil
			}
			return ErrForbidden
		}
		if !request.ExpiresAt.After(s.now()) {
			return ErrForbidden
		}
		if s.cloud != nil {
			binding, e := read[telegramLink](ctx, tx, telegramBots, Scope{Namespace: s.config.Namespace}, stable("chat", id.TelegramBotID, chatID))
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil && (binding.Scope != request.Scope || binding.UserID != request.UserID) {
				return ErrForbidden
			}
		} else {
			links, err := list[telegramLink](ctx, tx, telegramLinks, Scope{Namespace: s.config.Namespace}, "")
			if err != nil {
				return err
			}
			for _, link := range links {
				if link.BotID == id.TelegramBotID && link.ChatID == chatID && (link.Scope != request.Scope || link.UserID != request.UserID) {
					return ErrForbidden
				}
			}
		}
		previous, err := read[telegramLink](ctx, tx, telegramLinks, request.Scope, request.UserID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		link := telegramLink{Scope: request.Scope, BotID: id.TelegramBotID, UserID: request.UserID, ChatID: chatID, At: s.now(), Generation: previous.Generation + 1}
		if err = s.save(ctx, tx, telegramLinks, link.Scope, link.UserID, "", link.UserID, "", link); err != nil {
			return err
		}
		if s.cloud != nil {
			if err = s.save(ctx, tx, telegramBots, Scope{Namespace: s.config.Namespace}, stable("chat", id.TelegramBotID, chatID), "", request.UserID, "", link); err != nil {
				return err
			}
		}
		request.ConsumedChatID = chatID
		return s.save(ctx, tx, telegramRequests, request.Scope, request.Hash, "", request.UserID, "", request)
	})
}
func (s *Store) TelegramStatus(ctx context.Context, a Actor) (bool, error) {
	status, err := s.TelegramProviderStatus(ctx, a)
	return status.Connected, err
}

func (s *Store) DisconnectTelegram(ctx context.Context, a Actor) error {
	if err := s.actor(a); err != nil {
		return err
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if s.cloud != nil {
			link, e := read[telegramLink](ctx, tx, telegramLinks, a.Scope, a.ID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil {
				if link.FailureCode != "TELEGRAM_DISCONNECTED" {
					s.workReady(tx, WorkEvent{Scope: a.Scope, ID: stable("telegram-disconnect", a.Scope.Namespace, a.Scope.Project, a.Scope.Environment, a.ID, fmt.Sprint(link.Generation)), Kind: "WatchConsentChanged"})
				}
				link.FailureCode = "TELEGRAM_DISCONNECTED"
				if e = s.save(ctx, tx, telegramLinks, a.Scope, a.ID, "", a.ID, "", link); e != nil {
					return e
				}
				if e = tx.Delete(ctx, key(telegramBots, Scope{Namespace: s.config.Namespace}, stable("chat", link.BotID, link.ChatID))); e != nil {
					return e
				}
			}
		}
		if s.cloud == nil {
			if err := tx.Delete(ctx, key(telegramLinks, a.Scope, a.ID)); err != nil {
				return err
			}
		}
		requests, err := s.telegramRequestsFor(ctx, tx, a)
		if err != nil {
			return err
		}
		for _, request := range requests {
			if request.UserID == a.ID {
				if err = tx.Delete(ctx, key(telegramRequests, request.Scope, request.Hash)); err != nil {
					return err
				}
			}
		}
		if s.cloud != nil {
			return s.log(ctx, tx, a.Scope, a.ID, "telegram.disconnect", a.ID)
		} // Delivery-time link check revokes pending sends.
		items, err := list[Delivery](ctx, tx, outbox, a.Scope, "")
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.UserID == a.ID && item.Channel == "telegram" && item.State != "delivered" {
				item.State = "cancelled"
				item.Fence++
				item.FailureCode = "TELEGRAM_DISCONNECTED"
				if err = s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item); err != nil {
					return err
				}
			}
		}
		return s.log(ctx, tx, a.Scope, a.ID, "telegram.disconnect", a.ID)
	})
}

func (s *Store) telegramRequestsFor(ctx context.Context, tx dal.ReadwriteTransaction, a Actor) ([]telegramRequest, error) {
	if s.cloud == nil {
		return list[telegramRequest](ctx, tx, telegramRequests, a.Scope, "")
	}
	return cloudItems[telegramRequest](ctx, tx, cloudQuery(telegramRequests, a.Scope, "", []dal.Condition{dal.WhereField("owner", dal.Equal, a.ID)}, 16))
}
