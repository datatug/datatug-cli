package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

// RefreshSession exchanges a Firebase refresh token for the current session.
// It is injected so the identity endpoint is never hard-wired in tests.
type RefreshSession func(context.Context, string) (session, error)

// TokenSource loads the DataTug Firebase session, refreshes it before expiry,
// and atomically replaces the scoped credential before returning a bearer.
// Authenticated DataTug commands use this rather than a static one-shot token.
type TokenSource struct {
	ctx     context.Context
	client  *deviceauth.Client
	store   deviceauth.Store
	refresh RefreshSession
	now     func() time.Time
}

func newTokenSource(ctx context.Context, client *deviceauth.Client, store deviceauth.Store, refresh RefreshSession) *TokenSource {
	return &TokenSource{ctx: ctx, client: client, store: store, refresh: refresh, now: time.Now}
}

func (s *TokenSource) Token() (*oauth2.Token, error) {
	if s.client == nil || s.store == nil || s.refresh == nil {
		return nil, errors.New("datatug device auth: token source is not configured")
	}
	credential, err := s.client.ScopedStore(s.store).Load()
	if err != nil {
		return nil, err
	}
	if s.now().Before(credential.Expiry.Add(-time.Minute)) {
		return &oauth2.Token{AccessToken: credential.AccessToken, TokenType: credential.TokenType, Expiry: credential.Expiry}, nil
	}
	session, err := s.refresh(s.ctx, credential.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refresh Firebase session: %w", err)
	}
	credential.AccessToken = session.IDToken
	credential.RefreshToken = session.RefreshToken
	credential.Expiry = session.ExpiresAt
	credential.TokenType = "Bearer"
	if err := s.client.ScopedStore(s.store).Save(credential); err != nil {
		return nil, fmt.Errorf("save refreshed Firebase session: %w", err)
	}
	return &oauth2.Token{AccessToken: credential.AccessToken, TokenType: credential.TokenType, Expiry: credential.Expiry}, nil
}
