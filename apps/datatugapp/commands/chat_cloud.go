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
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dtlog"
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
	id, err := cloudInstallationID()
	if err != nil {
		return nil, ai.ClientContext{}, err
	}
	session, err := loadCloudSession(ctx, options)
	if err != nil {
		return nil, ai.ClientContext{}, err
	}
	return newCloudChatClient(options, session, id), cloudClientContext(id), nil
}

func cloudChatClientFromSession(options chatOptions, session cloudSession) (*cloud.Client, ai.ClientContext, error) {
	id, err := cloudInstallationID()
	if err != nil {
		return nil, ai.ClientContext{}, err
	}
	return newCloudChatClient(options, session, id), cloudClientContext(id), nil
}

func cloudInstallationID() (string, error) {
	configDir, err := chatUserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve DataTug configuration: %w", err)
	}
	id, err := clientctx.InstallationID(filepath.Join(configDir, "datatug", "installation_id"))
	if err != nil {
		return "", fmt.Errorf("load DataTug installation ID: %w", err)
	}
	return id, nil
}

func cloudClientContext(id string) ai.ClientContext {
	return ai.ClientContext{
		InstallationID: id,
		Feature:        "chat",
		Client:         ai.ClientInfo{Type: "cli", Name: "datatug", Version: buildinfo.Get("datatug").Version},
		Platform:       ai.PlatformInfo{OS: runtime.GOOS, Arch: runtime.GOARCH},
	}
}

func newCloudChatClient(options chatOptions, session cloudSession, id string) *cloud.Client {
	clientContext := cloudClientContext(id)
	client := cloud.New(cloud.Config{
		BaseURL:       session.baseURL,
		Product:       "datatug",
		Project:       options.cloudProject,
		ClientContext: &clientContext,
		HTTPClient:    session.httpClient,
		Token:         session.token,
	})
	return client
}

type cloudSession struct {
	baseURL    string
	httpClient *http.Client
	token      func(context.Context) (string, error)
	identity   string // local preference scope only; never sent as authorization
}

func loadCloudSession(ctx context.Context, options chatOptions) (cloudSession, error) {
	if options.apiKey != "" {
		return cloudSession{}, fmt.Errorf("cloud chat uses 'datatug auth login', not an AI profile API key")
	}
	baseURL := strings.TrimSpace(options.baseURL)
	if baseURL == "" {
		baseURL = defaultAIAPIURL
	}
	if err := validateAIAPIURL(baseURL); err != nil {
		return cloudSession{}, err
	}
	tokens, err := chatSavedTokenSource(ctx, options.insecureStorage)
	if err != nil {
		return cloudSession{}, fmt.Errorf("load DataTug login: %w", err)
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	initialToken, err := tokenWithContext(preflightCtx, tokens)
	if err != nil {
		return cloudSession{}, fmt.Errorf("DataTug cloud chat requires 'datatug auth login': %w", err)
	}
	if initialToken == nil || initialToken.AccessToken == "" {
		return cloudSession{}, fmt.Errorf("DataTug cloud chat requires 'datatug auth login': stored session is empty")
	}
	var tokenMu sync.Mutex
	return cloudSession{
		baseURL: baseURL,
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		token: func(requestCtx context.Context) (string, error) {
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
		identity: tokenPreferenceScope(initialToken.AccessToken),
	}, nil
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

// chatReportConfigurer is what enableChatReports needs of a chat session.
type chatReportConfigurer interface {
	ConfigureTelemetry(chat.InteractionReporter, ai.ClientContext)
}

// enableChatReports turns on the metadata report that `chat --model cloud`
// sends per turn (the install id, the conversation id, the length of the
// message, its outcome and the names of the actions that ran). It is usage
// telemetry like the CLI's own events, so it asks the same function and is
// off whenever telemetry is off: with DO_NOT_TRACK, DATATUG_TELEMETRY or CI
// set, on the run that printed the first-run notice and on a run that could
// not tell anyone. The answers of the cloud service are not affected.
func enableChatReports(sessions chatReportConfigurer, reporter chat.InteractionReporter, base ai.ClientContext) bool {
	if !dtlog.Enabled() {
		return false
	}
	sessions.ConfigureTelemetry(reporter, base)
	return true
}
