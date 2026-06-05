package ghauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	keyringService = "datatug-app"
	keyringUser    = "github-oauth-token"
)

// tickerIface abstracts time.Ticker for testing.
type tickerIface interface {
	C() <-chan time.Time
	Stop()
	Reset(d time.Duration)
}

// realTicker wraps *time.Ticker to implement tickerIface.
type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time   { return r.t.C }
func (r *realTicker) Stop()                 { r.t.Stop() }
func (r *realTicker) Reset(d time.Duration) { r.t.Reset(d) }

// seams for testing
var (
	httpClient     = &http.Client{}
	deviceCodeURL  = "https://github.com/login/device/code"
	accessTokenURL = "https://github.com/login/oauth/access_token"
	newTicker      = func(d time.Duration) tickerIface { return &realTicker{time.NewTicker(d)} }
	jsonMarshal    = json.Marshal
)

// SaveToken stores the GitHub OAuth token in the system keyring.
func SaveToken(token *oauth2.Token) error {
	data, err := jsonMarshal(token)
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}
	if err := keyring.Set(keyringService, keyringUser, string(data)); err != nil {
		return fmt.Errorf("failed to save token to keyring: %w", err)
	}
	return nil
}

// GetToken retrieves the GitHub OAuth token from the system keyring.
func GetToken() (*oauth2.Token, error) {
	data, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		return nil, fmt.Errorf("failed to get token from keyring: %w", err)
	}
	var token oauth2.Token
	if err := json.Unmarshal([]byte(data), &token); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token: %w", err)
	}
	return &token, nil
}

// DeleteToken removes the GitHub OAuth token from the system keyring.
func DeleteToken() error {
	if err := keyring.Delete(keyringService, keyringUser); err != nil {
		return fmt.Errorf("failed to delete token from keyring: %w", err)
	}
	return nil
}

// DeviceCodeResponse represents the response from GitHub's device code request.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func postJSON(ctx context.Context, url string, data interface{}, target interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if target == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	return nil
}

// RequestDeviceCode requests a device code from GitHub.
func RequestDeviceCode(ctx context.Context, clientID string) (*DeviceCodeResponse, error) {
	data := map[string]string{
		"client_id": clientID,
		"scope":     "repo",
	}
	var res DeviceCodeResponse
	if err := postJSON(ctx, deviceCodeURL, data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// PollForToken polls GitHub for an access token.
func PollForToken(ctx context.Context, clientID, clientSecret, deviceCode string, interval int, onAttempt func(attempt int)) (*oauth2.Token, error) {
	if interval == 0 {
		interval = 5
	}
	ticker := newTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C():
			attempt++
			if onAttempt != nil {
				onAttempt(attempt)
			}
			token, err := requestToken(ctx, clientID, clientSecret, deviceCode)
			if err != nil {
				if errors.Is(err, errAuthorizationPending) {
					continue
				}
				if errors.Is(err, errSlowDown) {
					interval += 5
					ticker.Reset(time.Duration(interval) * time.Second)
					continue
				}
				return nil, err
			}
			return token, nil
		}
	}
}

var (
	errAuthorizationPending = errors.New("authorization_pending")
	errSlowDown             = errors.New("slow_down")
)

func requestToken(ctx context.Context, clientID, clientSecret, deviceCode string) (*oauth2.Token, error) {
	data := map[string]string{
		"client_id":   clientID,
		"device_code": deviceCode,
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
	}
	if clientSecret != "" {
		data["client_secret"] = clientSecret
	}

	var res struct {
		oauth2.Token
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	if err := postJSON(ctx, accessTokenURL, data, &res); err != nil {
		return nil, err
	}

	if res.Error != "" {
		switch res.Error {
		case "authorization_pending":
			return nil, errAuthorizationPending
		case "slow_down":
			return nil, errSlowDown
		default:
			return nil, fmt.Errorf("github error: %s (%s)", res.Error, res.ErrorDescription)
		}
	}

	return &res.Token, nil
}
