package watch

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/dal-go/dalgo/dal"
)

const (
	telegramProviders = "dt_watch_telegram_providers"
	telegramBots      = "dt_watch_telegram_bots"
	telegramPolls     = "dt_watch_telegram_polls"
)

var botIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var botUsernamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

type telegramProvider struct {
	Scope
	BotID       string `json:"botId"`
	BotUsername string `json:"botUsername"`
}
type telegramPollState struct {
	Scope
	TelegramPollLease
	RunnerID        string    `json:"runnerId"`
	LastProcessedAt time.Time `json:"lastProcessedAt"`
}

func (s *Store) registerTelegramProvider(ctx context.Context, tx dal.ReadwriteTransaction, id RunnerIdentity) error {
	if id.TelegramBotID == "" {
		if id.TelegramBotUsername != "" {
			return ErrInvalid
		}
		return nil
	}
	if !botIDPattern.MatchString(id.TelegramBotID) || !botUsernamePattern.MatchString(id.TelegramBotUsername) {
		return ErrInvalid
	}
	global := Scope{Namespace: s.config.Namespace}
	binding, err := read[telegramProvider](ctx, tx, telegramBots, global, id.TelegramBotID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && binding.Scope != id.Scope {
		return ErrForbidden
	}
	providers, err := list[Runner](ctx, tx, runners, id.Scope, "")
	if err != nil {
		return err
	}
	for _, r := range providers {
		if r.ID != id.ID && r.TelegramBotID != "" && r.TelegramBotID != id.TelegramBotID && s.now().Sub(r.LastSeen) < s.config.RunnerTTL {
			return ErrConflict
		}
	}
	previous, err := read[telegramProvider](ctx, tx, telegramProviders, id.Scope, "active")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && previous.BotID != id.TelegramBotID {
		deliveries, err := list[Delivery](ctx, tx, outbox, id.Scope, "")
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if delivery.Channel == "telegram" && delivery.TelegramBotID != id.TelegramBotID && delivery.State != "delivered" {
				delivery.State = "cancelled"
				delivery.FailureCode = "TELEGRAM_PROVIDER_REPLACED"
				delivery.Fence++
				if err = s.save(ctx, tx, outbox, delivery.Scope, delivery.ID, delivery.WatchID, delivery.UserID, "", delivery); err != nil {
					return err
				}
			}
		}
	}
	provider := telegramProvider{Scope: id.Scope, BotID: id.TelegramBotID, BotUsername: id.TelegramBotUsername}
	if err = s.save(ctx, tx, telegramBots, global, id.TelegramBotID, "", "", "", provider); err != nil {
		return err
	}
	return s.save(ctx, tx, telegramProviders, id.Scope, "active", "", "", "", provider)
}

func (s *Store) telegramProvider(ctx context.Context, tx dal.ReadwriteTransaction, scope Scope) (telegramProvider, error) {
	provider, err := read[telegramProvider](ctx, tx, telegramProviders, scope, "active")
	if err != nil {
		return provider, err
	}
	workers, err := list[Runner](ctx, tx, runners, scope, "")
	if err != nil {
		return telegramProvider{}, err
	}
	for _, worker := range workers {
		if worker.TelegramBotID == provider.BotID && worker.TelegramBotUsername == provider.BotUsername && s.now().Sub(worker.LastSeen) < s.config.RunnerTTL {
			return provider, nil
		}
	}
	return telegramProvider{}, ErrNotFound
}

func (s *Store) TelegramProviderStatus(ctx context.Context, a Actor) (TelegramProviderStatus, error) {
	if err := s.actor(a); err != nil {
		return TelegramProviderStatus{}, err
	}
	result := TelegramProviderStatus{}
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		provider, err := s.telegramProvider(ctx, tx, a.Scope)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		result.BotID = provider.BotID
		result.BotUsername = provider.BotUsername
		link, err := read[telegramLink](ctx, tx, telegramLinks, a.Scope, a.ID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		result.Connected = link.BotID == provider.BotID && (link.ExpiresAt.IsZero() || link.ExpiresAt.After(s.now()))
		return nil
	})
	return result, err
}

func (s *Store) telegramRunner(ctx context.Context, tx dal.ReadwriteTransaction, id RunnerIdentity) error {
	if id.ID == "" || !s.validScope(id.Scope) || !botIDPattern.MatchString(id.TelegramBotID) {
		return ErrForbidden
	}
	provider, err := s.telegramProvider(ctx, tx, id.Scope)
	if err != nil {
		return err
	}
	worker, err := read[Runner](ctx, tx, runners, id.Scope, id.ID)
	if err != nil {
		return err
	}
	if provider.BotID != id.TelegramBotID || worker.TelegramBotID != id.TelegramBotID || s.now().Sub(worker.LastSeen) >= s.config.RunnerTTL {
		return ErrForbidden
	}
	return nil
}
func (s *Store) poll(ctx context.Context, tx dal.ReadwriteTransaction, id RunnerIdentity, fence int64) (telegramPollState, error) {
	if err := s.telegramRunner(ctx, tx, id); err != nil {
		return telegramPollState{}, err
	}
	state, err := read[telegramPollState](ctx, tx, telegramPolls, Scope{Namespace: s.config.Namespace}, id.TelegramBotID)
	if errors.Is(err, ErrNotFound) {
		return state, ErrStaleLease
	}
	if err != nil {
		return state, err
	}
	if state.Scope != id.Scope || state.RunnerID != id.ID || state.Fence != fence || !state.LeaseUntil.After(s.now()) {
		return telegramPollState{}, ErrStaleLease
	}
	return state, nil
}
func (s *Store) savePoll(ctx context.Context, tx dal.ReadwriteTransaction, state telegramPollState) error {
	return s.save(ctx, tx, telegramPolls, Scope{Namespace: s.config.Namespace}, state.BotID, "", state.RunnerID, "", state)
}
func (s *Store) renewPoll(state *telegramPollState) {
	if state.Offset > 0 && !state.LastProcessedAt.IsZero() && s.now().Sub(state.LastProcessedAt) >= 7*24*time.Hour {
		state.Offset = 0
		state.LastProcessedAt = time.Time{}
	}
	state.LeaseUntil = s.now().Add(s.config.LeaseDuration)
}
func (s *Store) ClaimTelegramPoll(ctx context.Context, id RunnerIdentity) (*TelegramPollLease, error) {
	var result *TelegramPollLease
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = nil
		if err := s.telegramRunner(ctx, tx, id); err != nil {
			return err
		}
		state, err := read[telegramPollState](ctx, tx, telegramPolls, Scope{Namespace: s.config.Namespace}, id.TelegramBotID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil && state.LeaseUntil.After(s.now()) {
			return nil
		}
		state.Scope = id.Scope
		state.BotID = id.TelegramBotID
		state.RunnerID = id.ID
		state.Fence++
		s.renewPoll(&state)
		if err = s.savePoll(ctx, tx, state); err != nil {
			return err
		}
		lease := state.TelegramPollLease
		result = &lease
		return nil
	})
	return result, err
}
func (s *Store) RenewTelegramPoll(ctx context.Context, id RunnerIdentity, fence int64) (*TelegramPollLease, error) {
	var result *TelegramPollLease
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		state, err := s.poll(ctx, tx, id, fence)
		if err != nil {
			return err
		}
		s.renewPoll(&state)
		if err = s.savePoll(ctx, tx, state); err != nil {
			return err
		}
		lease := state.TelegramPollLease
		result = &lease
		return nil
	})
	return result, err
}
func (s *Store) AdvanceTelegramPoll(ctx context.Context, id RunnerIdentity, fence, offset int64) error {
	if offset < 0 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		state, err := s.poll(ctx, tx, id, fence)
		if err != nil {
			return err
		}
		if offset < state.Offset {
			return ErrInvalid
		}
		if offset > state.Offset {
			state.Offset = offset
			state.LastProcessedAt = s.now()
		}
		s.renewPoll(&state)
		return s.savePoll(ctx, tx, state)
	})
}
