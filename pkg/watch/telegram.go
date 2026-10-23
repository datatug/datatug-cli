package watch

import (
	"context"
	"errors"
	"time"

	"github.com/dal-go/dalgo/dal"
)

const (
	telegramLinks    = "dt_watch_telegram_links"
	telegramRequests = "dt_watch_telegram_requests"
	quotas           = "dt_watch_quotas"
)

type telegramLink struct {
	DeliveryCapability string `json:"deliveryCapability,omitempty"`
	Scope
	UserID string    `json:"userId"`
	ChatID string    `json:"chatId"`
	At     time.Time `json:"at"`
}
type telegramRequest struct {
	Scope
	UserID    string    `json:"userId"`
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Store) BeginTelegramLink(ctx context.Context, a Actor, nonceHash string, expiresAt time.Time) error {
	if err := s.actor(a); err != nil {
		return err
	}
	if len(nonceHash) != 64 || !expiresAt.After(s.now()) || expiresAt.After(s.now().Add(15*time.Minute)) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		items, err := list[telegramRequest](ctx, tx, telegramRequests, a.Scope, "")
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.UserID == a.ID {
				if err = tx.Delete(ctx, key(telegramRequests, item.Scope, item.Hash)); err != nil {
					return err
				}
			}
		}
		item := telegramRequest{Scope: a.Scope, UserID: a.ID, Hash: nonceHash, ExpiresAt: expiresAt}
		return s.save(ctx, tx, telegramRequests, a.Scope, nonceHash, "", a.ID, "", item)
	})
}

// CompleteTelegramLink is called only after the daemon verifies DataTugBot's
// webhook secret. A browser-supplied chat ID is never accepted as identity proof.
func (s *Store) CompleteTelegramLink(ctx context.Context, nonceHash, chatID string) error {
	return s.completeTelegramLink(ctx, nonceHash, chatID, "")
}

func (s *Store) CompleteTelegramLinkWithCapability(ctx context.Context, nonceHash, chatID, capability string) error {
	if capability == "" || len(capability) > 2048 {
		return ErrInvalid
	}
	return s.completeTelegramLink(ctx, nonceHash, chatID, capability)
}

func (s *Store) completeTelegramLink(ctx context.Context, nonceHash, chatID, capability string) error {
	if len(nonceHash) != 64 || len(chatID) == 0 || len(chatID) > 20 {
		return ErrInvalid
	}
	for _, r := range chatID {
		if r < '0' || r > '9' {
			return ErrInvalid
		}
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		items, err := list[telegramRequest](ctx, tx, telegramRequests, Scope{Namespace: s.config.Namespace}, "")
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Hash != nonceHash {
				continue
			}
			if !item.ExpiresAt.After(s.now()) {
				return ErrForbidden
			}
			existing, err := list[telegramLink](ctx, tx, telegramLinks, Scope{Namespace: s.config.Namespace}, "")
			if err != nil {
				return err
			}
			for _, link := range existing {
				if link.ChatID == chatID && link.UserID != item.UserID {
					return ErrConflict
				}
			}
			link := telegramLink{Scope: item.Scope, UserID: item.UserID, ChatID: chatID, DeliveryCapability: capability, At: s.now()}
			if err = s.save(ctx, tx, telegramLinks, item.Scope, item.UserID, "", item.UserID, "", link); err != nil {
				return err
			}
			if err = tx.Delete(ctx, key(telegramRequests, item.Scope, item.Hash)); err != nil {
				return err
			}
			return s.log(ctx, tx, item.Scope, item.UserID, "telegram.connect", item.UserID)
		}
		return ErrNotFound
	})
}
func (s *Store) TelegramStatus(ctx context.Context, a Actor) (bool, error) {
	if err := s.actor(a); err != nil {
		return false, err
	}
	_, err := read[telegramLink](ctx, s.db, telegramLinks, a.Scope, a.ID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
func (s *Store) DisconnectTelegram(ctx context.Context, a Actor) error {
	if err := s.actor(a); err != nil {
		return err
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := tx.Delete(ctx, key(telegramLinks, a.Scope, a.ID)); err != nil {
			return err
		}
		requests, err := list[telegramRequest](ctx, tx, telegramRequests, a.Scope, "")
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
