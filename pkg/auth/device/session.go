package device

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/strongo/deviceauth"
)

// ErrNotSignedIn is returned by EnsureSignedIn when there is no saved login
// and signing in was not allowed.
var ErrNotSignedIn = errors.New("not signed in to DataTug")

// SignInOptions say where the login is kept and whether EnsureSignedIn may
// start the device flow.
type SignInOptions struct {
	// Issuer overrides DATATUG_AUTH_HOST and the default issuer when set.
	Issuer string
	// Insecure uses the 0600 file store of --insecure-storage.
	Insecure bool
	// Interactive lets EnsureSignedIn run the device flow when no login is
	// saved; otherwise a missing login is ErrNotSignedIn.
	Interactive bool
	// Out and ErrOut receive the device flow's URL, code and warnings.
	Out, ErrOut io.Writer
}

// Session is a saved DataTug login. Token refreshes the Firebase session a
// minute before it expires and is safe for concurrent use.
type Session struct {
	Token func(context.Context) (string, error)
	// Email is the signed-in account's email from the session's claims, for
	// messages only; "" when unknown.
	Email string
}

// EnsureSignedIn returns the saved login, running the same device flow as
// `datatug auth login` first when there is none and opts.Interactive is set.
// The login is stored only where `auth login` stores it.
func EnsureSignedIn(ctx context.Context, opts SignInOptions) (Session, error) {
	return ensureSignedIn(ctx, opts, defaultDependencies())
}

func ensureSignedIn(ctx context.Context, opts SignInOptions, deps dependencies) (Session, error) {
	issuer := strings.TrimSpace(opts.Issuer)
	if issuer == "" {
		issuer = strings.TrimSpace(os.Getenv("DATATUG_AUTH_HOST"))
	}
	if issuer == "" {
		issuer = defaultIssuer
	}
	client, store, description, err := deps.newClient(issuer, opts.Insecure)
	if err != nil {
		return Session{}, fmt.Errorf("configure credential storage: %w", err)
	}
	tokens := newTokenSource(ctx, client, store, deps.refresh)
	token, err := tokens.TokenContext(ctx)
	if err != nil {
		// No login, or one whose refresh was refused (revoked, expired): a
		// new sign-in is the remedy either way.
		if !opts.Interactive {
			if errors.Is(err, deviceauth.ErrCredentialNotFound) {
				return Session{}, ErrNotSignedIn
			}
			return Session{}, fmt.Errorf("%w: %v", ErrNotSignedIn, err)
		}
		if opts.Insecure && opts.ErrOut != nil {
			_, _ = fmt.Fprintf(opts.ErrOut, "Warning: --insecure-storage writes the Firebase session unencrypted to %s.\n", description)
		}
		if _, err = deviceLogin(ctx, client, store, deps, opts.Out, opts.ErrOut); err != nil {
			return Session{}, err
		}
		token, err = tokens.TokenContext(ctx)
	}
	if err != nil {
		return Session{}, fmt.Errorf("load DataTug login: %w", err)
	}
	var mu sync.Mutex
	return Session{
		Token: func(ctx context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			t, err := tokens.TokenContext(ctx)
			if err != nil {
				return "", err
			}
			return t.AccessToken, nil
		},
		Email: emailClaim(token.AccessToken),
	}, nil
}

// emailClaim reads the email claim of a Firebase ID token without verifying
// it: the value is only shown in messages, never trusted.
func emailClaim(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Email
}
