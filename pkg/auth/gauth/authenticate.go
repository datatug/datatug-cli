package gauth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/strongo/logus"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v3"
)

const (
	keyringService = "datatug-app"
	keyringUser    = "google-oauth-refresh-token"
)

// getTokenFromWebFn is a seam for testing StartInteractiveLogin without a browser.
var getTokenFromWebFn = getTokenFromWeb

var tokenSourceFromConfig = func(ctx context.Context, config *oauth2.Config, token *oauth2.Token) oauth2.TokenSource {
	return config.TokenSource(ctx, token)
}

var saveRefreshTokenFn = saveRefreshToken

// getGoogleCloudClient handles the OAuth2 flow for desktop apps and caches the token locally.
func getGoogleCloudClient(ctx context.Context) (client *http.Client, err error) {

	// Cloud Resource Manager v3 scope.
	// Use "Desktop app" type so no client secret is needed.
	config := newOAuthConfig([]string{
		// Request broad scopes so the resulting refresh token can be reused for Firestore
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/datastore",
		cloudresourcemanager.CloudPlatformReadOnlyScope,
	})

	var refreshToken string
	refreshToken, err = GetRefreshToken()
	if err != nil {
		log.Printf("Failed to get refresh token: %v", err)
	}

	var token *oauth2.Token
	if refreshToken != "" {
		logus.Infof(ctx, "Found refresh token in keychain, exchanging for access token...")

		started := time.Now()

		token = &oauth2.Token{RefreshToken: refreshToken}
		ts := tokenSourceFromConfig(ctx, config, token) // Use a token source to get a fresh access token
		token, err = ts.Token()

		if err != nil {
			logus.Debugf(ctx, "Failed to refresh access token: %v", err)
		} else {
			logus.Debugf(ctx, "Exchanged refresh token for access token in %v", time.Since(started))
		}
	}

	if token == nil {
		//tok, err := tokenFromFile(tokFile)
		if token, err = getTokenFromWebFn(ctx, config); err != nil {
			err = fmt.Errorf("failed to get token: %w", err)
			return
		}
		if token.RefreshToken != "" {
			if err = saveRefreshTokenFn(token.RefreshToken); err != nil {
				log.Printf("Failed to save refresh token: %v", err)
			}
		}
	}
	return config.Client(ctx, token), nil
}

// newOAuthConfig returns the desktop-app OAuth2 config shared by every sign-in
// and token-refresh path, for the given scopes.
func newOAuthConfig(scopes []string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "588648831063-393c7c5gfj70sstaioked6qpb0sfj87h.apps.googleusercontent.com", // os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
		ClientSecret: "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",                                      // Creation date: 11 August 2025 at 16:03:21 GMT+1
		Scopes:       scopes,
		Endpoint:     google.Endpoint,
		RedirectURL:  "http://localhost:8080/oauth2callback",
	}
}

// ErrNotSignedIn is returned by TokenSource when no refresh token is stored.
var ErrNotSignedIn = errors.New("not signed in: no stored Google refresh token")

// TokenSource returns a token source for the given scopes built from the
// refresh token stored by the Google sign-in. It returns ErrNotSignedIn when
// there is no stored refresh token.
func TokenSource(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
	refreshToken, err := GetRefreshToken()
	if err != nil || refreshToken == "" {
		return nil, ErrNotSignedIn
	}
	return tokenSourceFromConfig(ctx, newOAuthConfig(scopes), &oauth2.Token{RefreshToken: refreshToken}), nil
}

// saveRefreshToken securely stores a token in the system keychain
func saveRefreshToken(token string) error {
	log.Println("Saving refresh token to keyring...")
	return keyring.Set(keyringService, keyringUser, token)
}

// GetRefreshToken retrieves a stored token from the keychain
func GetRefreshToken() (string, error) {
	return keyring.Get(keyringService, keyringUser)
}

// DeleteRefreshToken removes the stored refresh token from the keychain
func DeleteRefreshToken() error {
	return keyring.Delete(keyringService, keyringUser)
}

// StartInteractiveLogin runs an interactive OAuth login with the provided scopes,
// stores the refresh token in keychain, and returns the acquired token.
func StartInteractiveLogin(ctx context.Context, scopes []string) (*oauth2.Token, error) {
	if len(scopes) == 0 {
		scopes = []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/datastore",
		}
	}
	cfg := newOAuthConfig(scopes)
	tok, err := getTokenFromWebFn(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("interactive login failed: %w", err)
	}
	if tok.RefreshToken != "" {
		if err := saveRefreshToken(tok.RefreshToken); err != nil {
			log.Printf("Failed to save refresh token: %v", err)
		}
	}
	return tok, nil
}
