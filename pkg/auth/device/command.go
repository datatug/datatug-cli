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
	"net/netip"
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
	login       func(context.Context, deviceauth.LoginOptions) (deviceauth.LoginResult, error)
	exchange    func(context.Context, string) (session, error)
	identity    func(context.Context, *http.Client, *url.URL, string) (identity, error)
	newStore    func(*url.URL, bool) (deviceauth.Store, string, error)
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
		login:       deviceauth.Login,
		exchange:    exchangeCustomToken,
		identity:    fetchIdentity,
		newStore:    newStore,
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
			issuer, err := issuerURL(*rawIssuer)
			if err != nil {
				return err
			}
			store, description, err := deps.newStore(issuer, *insecure)
			if err != nil {
				return fmt.Errorf("configure credential storage: %w", err)
			}
			if *insecure {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: --insecure-storage writes the Firebase session unencrypted to %s.\n", description)
			}
			result, err := deps.login(context.WithValue(cmd.Context(), oauth2.HTTPClient, deps.httpClient), deviceauth.LoginOptions{
				OAuthConfig: oauth2.Config{ClientID: clientID, Scopes: scopes, Endpoint: oauth2.Endpoint{DeviceAuthURL: issuer.String() + "/oauth/device/code", TokenURL: issuer.String() + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams}},
				DeviceInfo:  deviceInfo(), OpenBrowser: deps.openBrowser, Output: cmd.OutOrStdout(), ErrorOutput: cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			if !strings.EqualFold(result.Token.TokenType, customTokenType) {
				return errors.New("device login: authorization server returned an unexpected token type")
			}
			sess, err := deps.exchange(cmd.Context(), result.Token.AccessToken)
			if err != nil {
				return fmt.Errorf("exchange device login for Firebase session: %w", err)
			}
			identity, err := deps.identity(cmd.Context(), deps.httpClient, issuer, sess.IDToken)
			if err != nil {
				return err
			}
			if identity.Subject == "" || identity.Audience != clientID {
				return errors.New("device login: identity is not authorized for datatug-cli")
			}
			if sess.UID != "" && sess.UID != identity.Subject {
				return errors.New("device login: Firebase identity does not match authorization identity")
			}
			sess.UID = identity.Subject
			if err := store.Save(deviceauth.Credential{AccessToken: sess.IDToken, RefreshToken: sess.RefreshToken, Expiry: sess.ExpiresAt, AccountID: sess.UID, AccountName: sess.Email, Scopes: strings.Fields(identity.Scope)}); err != nil {
				return fmt.Errorf("save Firebase session: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s as %s.\nToken storage: %s\n", issuer.Host, sess.UID, description)
			return nil
		},
	}
}

func statusCommand(rawIssuer *string, insecure *bool, deps dependencies) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show the current DataTug login", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		issuer, err := issuerURL(*rawIssuer)
		if err != nil {
			return err
		}
		store, description, err := deps.newStore(issuer, *insecure)
		if err != nil {
			return fmt.Errorf("configure credential storage: %w", err)
		}
		credential, err := store.Load()
		if errors.Is(err, deviceauth.ErrCredentialNotFound) {
			return fmt.Errorf("not logged in to %s; run 'datatug auth device login'", issuer.Host)
		}
		if err != nil {
			return fmt.Errorf("load Firebase session: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n  Logged in as: %s\n  Scopes: %s\n  Expires: %s\n  Token storage: %s\n", issuer.Host, credential.AccountID, strings.Join(credential.Scopes, " "), credential.Expiry.UTC().Format(time.RFC3339), description)
		return nil
	}}
}

func logoutCommand(rawIssuer *string, insecure *bool, deps dependencies) *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Remove the stored DataTug login", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		issuer, err := issuerURL(*rawIssuer)
		if err != nil {
			return err
		}
		store, _, err := deps.newStore(issuer, *insecure)
		if err != nil {
			return fmt.Errorf("configure credential storage: %w", err)
		}
		if _, err := store.Load(); errors.Is(err, deviceauth.ErrCredentialNotFound) {
			return fmt.Errorf("not logged in to %s", issuer.Host)
		} else if err != nil {
			return fmt.Errorf("load Firebase session: %w", err)
		}
		if err := store.Delete(); err != nil {
			return fmt.Errorf("remove Firebase session: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Logged out of %s.\n", issuer.Host)
		return nil
	}}
}

type identity struct {
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	Scope    string `json:"scope"`
}

func fetchIdentity(ctx context.Context, client *http.Client, issuer *url.URL, token string) (identity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer.String()+"/oauth/userinfo", nil)
	if err != nil {
		return identity{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return identity{}, fmt.Errorf("validate device identity: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return identity{}, fmt.Errorf("validate device identity: auth server returned HTTP %d", response.StatusCode)
	}
	var result identity
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return identity{}, fmt.Errorf("decode device identity: %w", err)
	}
	return result, nil
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
		return session{}, fmt.Errorf("Firebase custom-token exchange returned HTTP %d", response.StatusCode)
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

func newStore(issuer *url.URL, insecure bool) (deviceauth.Store, string, error) {
	if !insecure {
		store, err := deviceauth.NewKeyringStore("datatug-cli", issuer.String()+"|"+clientID)
		return store, "operating system keyring", err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "datatug", "auth", base64.RawURLEncoding.EncodeToString([]byte(issuer.Host))+".json")
	store, err := deviceauth.NewFileStore(path)
	return store, path, err
}

func deviceInfo() deviceauth.DeviceInfo {
	hostname, _ := os.Hostname()
	return deviceauth.DeviceInfo{Name: strings.TrimSpace(hostname), OS: runtime.GOOS, Arch: runtime.GOARCH}
}

func issuerURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("--auth-host must be an absolute authorization server URL")
	}
	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost"
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		loopback = address.IsLoopback()
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback) {
		return nil, errors.New("--auth-host must use HTTPS (HTTP is allowed only for loopback development)")
	}
	parsed.Scheme, parsed.Host, parsed.Path = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host), ""
	return parsed, nil
}
