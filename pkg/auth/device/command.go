// Package device provides DataTug's shared auth.sneat.co device-login command.
package device

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

const (
	defaultIssuer   = "https://auth.sneat.co"
	clientID        = "datatug-cli"
	customTokenType = "urn:ietf:params:oauth:token-type:firebase-custom-token"
	firebaseAPIKey  = "AIzaSyCeQu1WC182yD0VHrRm4nHUxVf27fY-MLQ"
)

var scopes = []string{"openid", "profile", "datatug:projects:read", "datatug:projects:write"}

type session struct {
	IDToken      string
	RefreshToken string
	UID          string
	Email        string
	ExpiresAt    time.Time
}

type dependencies struct {
	httpClient  *http.Client
	openBrowser func(string) error
	exchange    func(context.Context, string) (session, error)
	refresh     RefreshSession
	newClient   func(string, bool) (*deviceauth.Client, deviceauth.Store, string, error)
}

// Command returns the DataTug auth.sneat.co commands. It is separate from
// auth google and the GitHub repository authorization flow, which grant
// different provider permissions and remain unchanged.
func Command() *cobra.Command {
	return newCommand(defaultDependencies())
}

func defaultDependencies() dependencies {
	return dependencies{
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		openBrowser: deviceauth.OpenBrowser,
		exchange:    exchangeCustomToken,
		refresh:     refreshFirebaseSession,
		newClient:   newClient,
	}
}

// LoginCommand, StatusCommand, and LogoutCommand are mounted directly under
// `datatug auth` so the shared sign-in has the conventional CLI shape.
func LoginCommand() *cobra.Command  { return directCommand(loginCommand) }
func StatusCommand() *cobra.Command { return directCommand(statusCommand) }
func LogoutCommand() *cobra.Command { return directCommand(logoutCommand) }

func directCommand(factory func(*string, *bool, dependencies) *cobra.Command) *cobra.Command {
	issuer := strings.TrimSpace(os.Getenv("DATATUG_AUTH_HOST"))
	if issuer == "" {
		issuer = defaultIssuer
	}
	var insecure bool
	cmd := factory(&issuer, &insecure, defaultDependencies())
	cmd.Flags().StringVar(&issuer, "auth-host", issuer, "shared authorization server URL (HTTP loopback only for development)")
	cmd.Flags().BoolVar(&insecure, "insecure-storage", false, "store the Firebase session in a plaintext 0600 file for headless environments")
	return cmd
}

func newCommand(deps dependencies) *cobra.Command {
	issuer := strings.TrimSpace(os.Getenv("DATATUG_AUTH_HOST"))
	if issuer == "" {
		issuer = defaultIssuer
	}
	var insecure bool
	cmd := &cobra.Command{Use: "device", Short: "Manage DataTug sign-in through auth.sneat.co"}
	cmd.PersistentFlags().StringVar(&issuer, "auth-host", issuer, "shared authorization server URL (HTTP loopback only for development)")
	cmd.PersistentFlags().BoolVar(&insecure, "insecure-storage", false, "store the Firebase session in a plaintext 0600 file for headless environments")
	cmd.AddCommand(loginCommand(&issuer, &insecure, deps), statusCommand(&issuer, &insecure, deps), logoutCommand(&issuer, &insecure, deps))
	return cmd
}

func loginCommand(rawIssuer *string, insecure *bool, deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "login", Short: "Sign in through your browser", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, store, description, err := deps.newClient(*rawIssuer, *insecure)
			if err != nil {
				return fmt.Errorf("configure credential storage: %w", err)
			}
			if *insecure {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: --insecure-storage writes the Firebase session unencrypted to %s.\n", description)
			}
			auth, err := client.DeviceLoginAndStore(context.WithValue(cmd.Context(), oauth2.HTTPClient, deps.httpClient), deviceauth.DeviceLoginOptions{
				DeviceInfo: deviceInfo(), OpenBrowser: deps.openBrowser, Output: cmd.OutOrStdout(), ErrorOutput: cmd.ErrOrStderr(),
				TokenTransformer: func(ctx context.Context, token *oauth2.Token) (*oauth2.Token, error) {
					if !strings.EqualFold(token.TokenType, customTokenType) {
						return nil, errors.New("authorization server returned an unexpected token type")
					}
					sess, err := deps.exchange(ctx, token.AccessToken)
					if err != nil {
						return nil, fmt.Errorf("exchange device login for Firebase session: %w", err)
					}
					return &oauth2.Token{AccessToken: sess.IDToken, TokenType: "Bearer", RefreshToken: sess.RefreshToken, Expiry: sess.ExpiresAt}, nil
				},
			}, store)
			if err != nil {
				return err
			}
			printWarnings(cmd.ErrOrStderr(), auth.Warnings)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s as %s.\nToken storage: %s\n", client.Issuer(), auth.Identity.Subject, description)
			return nil
		},
	}
}

func statusCommand(rawIssuer *string, insecure *bool, deps dependencies) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show the current DataTug login", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, store, description, err := deps.newClient(*rawIssuer, *insecure)
		if err != nil {
			return fmt.Errorf("configure credential storage: %w", err)
		}
		token, err := newTokenSource(cmd.Context(), client, store, deps.refresh).Token()
		if errors.Is(err, deviceauth.ErrCredentialNotFound) {
			return fmt.Errorf("not logged in to %s; run 'datatug auth login'", client.Issuer())
		}
		if err != nil {
			return fmt.Errorf("load Firebase session: %w", err)
		}
		identity, err := client.UserInfo(context.WithValue(cmd.Context(), oauth2.HTTPClient, deps.httpClient), token)
		if err != nil {
			return fmt.Errorf("validate Firebase session: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n  Logged in as: %s\n  Scopes: %s\n  Expires: %s\n  Token storage: %s\n", client.Issuer(), identity.Subject, strings.Join(identity.Scopes, " "), token.Expiry.UTC().Format(time.RFC3339), description)
		return nil
	}}
}

func logoutCommand(rawIssuer *string, insecure *bool, deps dependencies) *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Remove the stored DataTug login", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, store, _, err := deps.newClient(*rawIssuer, *insecure)
		if err != nil {
			return fmt.Errorf("configure credential storage: %w", err)
		}
		if _, err := newTokenSource(cmd.Context(), client, store, deps.refresh).Token(); errors.Is(err, deviceauth.ErrCredentialNotFound) {
			return fmt.Errorf("not logged in to %s", client.Issuer())
		} else if err != nil {
			return fmt.Errorf("refresh Firebase session before logout: %w", err)
		}
		if err := client.Logout(cmd.Context(), store); err != nil {
			return fmt.Errorf("logout DataTug session: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Logged out of %s.\n", client.Issuer())
		return nil
	}}
}

func printWarnings(output io.Writer, warnings []error) {
	for range warnings {
		// Warning causes can carry provider response detail. Keep stderr useful
		// without echoing a credential, token, or response body.
		_, _ = fmt.Fprintln(output, "Warning: the previous device login could not be revoked; it may remain active until it expires.")
	}
}

func exchangeCustomToken(ctx context.Context, token string) (session, error) {
	body, _ := json.Marshal(map[string]any{"token": token, "returnSecureToken": true})
	endpoint := "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=" + url.QueryEscape(firebaseAPIKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return session{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return session{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return session{}, fmt.Errorf("firebase custom-token exchange returned HTTP %d", response.StatusCode)
	}
	var out struct {
		IDToken      string `json:"idToken"`
		RefreshToken string `json:"refreshToken"`
		LocalID      string `json:"localId"`
		Email        string `json:"email"`
		ExpiresIn    string `json:"expiresIn"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&out); err != nil {
		return session{}, err
	}
	seconds, err := time.ParseDuration(out.ExpiresIn + "s")
	if err != nil {
		return session{}, fmt.Errorf("decode Firebase session expiry: %w", err)
	}
	return session{IDToken: out.IDToken, RefreshToken: out.RefreshToken, UID: out.LocalID, Email: out.Email, ExpiresAt: time.Now().Add(seconds)}, nil
}

func refreshFirebaseSession(ctx context.Context, refreshToken string) (session, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	endpoint := "https://securetoken.googleapis.com/v1/token?key=" + url.QueryEscape(firebaseAPIKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return session{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return session{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return session{}, fmt.Errorf("firebase session refresh returned HTTP %d", response.StatusCode)
	}
	var out struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		UserID       string `json:"user_id"`
		ExpiresIn    string `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&out); err != nil {
		return session{}, err
	}
	seconds, err := time.ParseDuration(out.ExpiresIn + "s")
	if err != nil {
		return session{}, fmt.Errorf("decode Firebase refreshed session expiry: %w", err)
	}
	return session{IDToken: out.IDToken, RefreshToken: out.RefreshToken, UID: out.UserID, ExpiresAt: time.Now().Add(seconds)}, nil
}

func newClient(rawIssuer string, insecure bool) (*deviceauth.Client, deviceauth.Store, string, error) {
	client, err := deviceauth.NewClient(deviceauth.ClientConfig{
		Issuer:         rawIssuer,
		ClientID:       clientID,
		Scopes:         scopes,
		RequiredScopes: scopes,
		KeyringService: "datatug-cli",
		KeyringAccount: "firebase-session",
	})
	if err != nil {
		return nil, nil, "", err
	}
	if !insecure {
		store, err := client.NewKeyringStore()
		return client, store, "operating system keyring", err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, nil, "", err
	}
	issuerHost, err := url.Parse(client.Issuer())
	if err != nil {
		return nil, nil, "", err
	}
	path := filepath.Join(dir, "datatug", "auth", base64.RawURLEncoding.EncodeToString([]byte(issuerHost.Host))+".json")
	store, err := deviceauth.NewFileStore(path)
	return client, store, path, err
}

func deviceInfo() deviceauth.DeviceInfo {
	hostname, _ := os.Hostname()
	return deviceauth.DeviceInfo{Name: strings.TrimSpace(hostname), OS: runtime.GOOS, Arch: runtime.GOARCH}
}
