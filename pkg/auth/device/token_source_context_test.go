package device

import (
	"context"
	"testing"
	"time"

	"github.com/strongo/deviceauth"
)

func TestTokenContextBoundsRefreshWithRequestDeadline(t *testing.T) {
	client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: "https://auth.example", ClientID: clientID, Scopes: scopes, RequiredScopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	if err := client.ScopedStore(store).Save(deviceauth.Credential{
		AccessToken: "expired", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute), Scopes: scopes,
	}); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var passedContext context.Context
	source := newTokenSource(context.Background(), client, store, func(ctx context.Context, _ string) (session, error) {
		passedContext = ctx
		return session{}, ctx.Err()
	})
	if _, err := source.TokenContext(requestCtx); err == nil {
		t.Fatal("canceled request unexpectedly refreshed a token")
	}
	if passedContext != requestCtx {
		t.Fatal("request deadline was not passed to Firebase refresh")
	}
}
