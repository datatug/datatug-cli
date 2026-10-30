package gauth

import (
	"context"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

func TestTokenSource(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()

	// Not signed in: nothing stored.
	if _, err := TokenSource(ctx, nil); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("want ErrNotSignedIn, got %v", err)
	}

	// An empty stored token counts as not signed in.
	if err := keyring.Set(keyringService, keyringUser, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := TokenSource(ctx, nil); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("want ErrNotSignedIn for empty token, got %v", err)
	}

	// Signed in: the source is built from the stored refresh token and scopes.
	if err := keyring.Set(keyringService, keyringUser, "stored-refresh"); err != nil {
		t.Fatal(err)
	}
	orig := tokenSourceFromConfig
	defer func() { tokenSourceFromConfig = orig }()
	var gotCfg *oauth2.Config
	var gotTok *oauth2.Token
	want := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "a"})
	tokenSourceFromConfig = func(_ context.Context, cfg *oauth2.Config, tok *oauth2.Token) oauth2.TokenSource {
		gotCfg, gotTok = cfg, tok
		return want
	}
	scopes := []string{"scope-1"}
	ts, err := TokenSource(ctx, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if ts != want {
		t.Error("unexpected token source")
	}
	if gotTok.RefreshToken != "stored-refresh" {
		t.Errorf("refresh token = %q", gotTok.RefreshToken)
	}
	if len(gotCfg.Scopes) != 1 || gotCfg.Scopes[0] != "scope-1" {
		t.Errorf("scopes = %v", gotCfg.Scopes)
	}
}
