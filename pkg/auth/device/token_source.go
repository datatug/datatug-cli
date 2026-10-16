package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

// SavedTokenSource loads the DataTug CLI's own audience- and product-scoped
// login. It never borrows a Sneat or Google credential. The insecure flag
// must match the explicit --insecure-storage choice made during auth login.
func SavedTokenSource(ctx context.Context, insecure bool) (oauth2.TokenSource, error) {
	client, store, _, err := newClient(resolveIssuer(), insecure)
	if err != nil {
		return nil, err
	}
	return newTokenSource(ctx, client, store, refreshFirebaseSession), nil
}

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
	return s.TokenContext(s.ctx)
}

// TokenContext lets a long-running client bound a refresh by its individual
// request deadline. Token() retains oauth2.TokenSource compatibility.
func (s *TokenSource) TokenContext(ctx context.Context) (*oauth2.Token, error) {
	if s.client == nil || s.store == nil || s.refresh == nil {
		return nil, errors.New("datatug device auth: token source is not configured")
	}
	credential, err := s.client.ScopedStore(s.store).Load()
	if errors.Is(err, deviceauth.ErrCredentialIssuerMoved) {
		return nil, signInMovedError{cause: err}
	}
	if err != nil {
		return nil, err
	}
	if s.now().Before(credential.Expiry.Add(-time.Minute)) {
		return &oauth2.Token{AccessToken: credential.AccessToken, TokenType: credential.TokenType, Expiry: credential.Expiry}, nil
	}
	session, err := s.refresh(ctx, credential.RefreshToken)
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

// signInMovedError reports a login saved before the Sneat OAuth server moved.
// Its text is the one message users see; errors.Is/As still reach the
// deviceauth cause.
type signInMovedError struct{ cause error }

func (e signInMovedError) Error() string { return signInMovedMessage }
func (e signInMovedError) Unwrap() error { return e.cause }
