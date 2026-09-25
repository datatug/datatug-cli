package commands

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/clientctx"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/buildinfo"
	"golang.org/x/oauth2"
)

const defaultAIAPIURL = "https://api.sneat.cloud/v0/"

var chatUserConfigDir = os.UserConfigDir
var chatSavedTokenSource = device.SavedTokenSource

// cloudChatClient is selected only by --model cloud. Existing BYOK providers
// retain their direct provider connections and credentials.
func cloudChatClient(ctx context.Context, options chatOptions) (*cloud.Client, ai.ClientContext, error) {
	if options.apiKey != "" {
		return nil, ai.ClientContext{}, fmt.Errorf("cloud chat uses 'datatug auth login', not an AI profile API key")
	}
	baseURL := strings.TrimSpace(options.baseURL)
	if baseURL == "" {
		baseURL = defaultAIAPIURL
	}
	if err := validateAIAPIURL(baseURL); err != nil {
		return nil, ai.ClientContext{}, err
	}
	configDir, err := chatUserConfigDir()
	if err != nil {
		return nil, ai.ClientContext{}, fmt.Errorf("resolve DataTug configuration: %w", err)
	}
	id, err := clientctx.InstallationID(filepath.Join(configDir, "datatug", "installation_id"))
	if err != nil {
		return nil, ai.ClientContext{}, fmt.Errorf("load DataTug installation ID: %w", err)
	}
	tokens, err := chatSavedTokenSource(ctx, options.insecureStorage)
	if err != nil {
		return nil, ai.ClientContext{}, fmt.Errorf("load DataTug login: %w", err)
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	initialToken, err := tokenWithContext(preflightCtx, tokens)
	if err != nil {
		return nil, ai.ClientContext{}, fmt.Errorf("DataTug cloud chat requires 'datatug auth login': %w", err)
	}
	if initialToken == nil || initialToken.AccessToken == "" {
		return nil, ai.ClientContext{}, fmt.Errorf("DataTug cloud chat requires 'datatug auth login': stored session is empty")
	}
	clientContext := ai.ClientContext{
		InstallationID: id,
		Feature:        "chat",
		Client:         ai.ClientInfo{Type: "cli", Name: "datatug", Version: buildinfo.Get("datatug").Version},
		Platform:       ai.PlatformInfo{OS: runtime.GOOS, Arch: runtime.GOARCH},
	}
	var tokenMu sync.Mutex
	client := cloud.New(cloud.Config{
		BaseURL:       baseURL,
		Product:       "datatug",
		ClientContext: &clientContext,
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // bearer tokens never follow a redirect
		}},
		Token: func(requestCtx context.Context) (string, error) {
			tokenMu.Lock()
			defer tokenMu.Unlock()
			token, err := tokenWithContext(requestCtx, tokens)
			if err != nil {
				return "", err
			}
			if token == nil || token.AccessToken == "" {
				return "", fmt.Errorf("DataTug login session is empty")
			}
			return token.AccessToken, nil
		},
	})
	return client, clientContext, nil
}

func tokenWithContext(ctx context.Context, source oauth2.TokenSource) (*oauth2.Token, error) {
	if contextual, ok := source.(interface {
		TokenContext(context.Context) (*oauth2.Token, error)
	}); ok {
		return contextual.TokenContext(ctx)
	}
	return source.Token()
}

// A bearer must not be sent to plaintext non-loopback endpoints. Requiring
// the version prefix also prevents accidental delivery to a model provider's
// unrelated /v1 endpoint when --base-url is reused from a BYOK profile.
func validateAIAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v0/") {
		return fmt.Errorf("cloud AI base URL must be an API /v0/ URL without credentials, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("cloud AI base URL must use HTTPS, except for a loopback development server")
}
