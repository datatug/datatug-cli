package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// BrowserHTTPRequest uses the same request engine and origin-scoped settings
// as the TUI's /http command. The caller must hold the browser capability.
type BrowserHTTPRequest struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	ReplaceHeaders bool              `json:"replaceHeaders"`
	Body           string            `json:"body"`
}

type BrowserHTTPSetting struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Scope  string `json:"scope"`
	Origin string `json:"origin"`
}

func (c *SessionChat) BrowserHTTPSettings(ctx context.Context, sessionID, rawURL string) ([]BrowserHTTPSetting, error) {
	c.mu.Lock()
	if c.activeID != sessionID {
		c.mu.Unlock()
		return nil, ErrActiveSessionChanged
	}
	store := c.store
	c.mu.Unlock()
	origin := ""
	if strings.TrimSpace(rawURL) != "" {
		var err error
		origin, err = httpOrigin(rawURL)
		if err != nil {
			return nil, err
		}
	}
	settings, err := store.HTTPRequestSettings(ctx, origin)
	if err != nil {
		return nil, err
	}
	items := make([]BrowserHTTPSetting, 0, len(settings.Headers)+len(settings.Cookies))
	for name, scope := range settings.HeaderScopes {
		items = append(items, BrowserHTTPSetting{Kind: "header", Name: name, Scope: scope, Origin: settings.HeaderOrigins[name]})
	}
	for name, scope := range settings.CookieScopes {
		items = append(items, BrowserHTTPSetting{Kind: "cookie", Name: name, Scope: scope, Origin: origin})
	}
	return items, nil
}

func (c *SessionChat) ChangeBrowserHTTPSetting(ctx context.Context, sessionID, action, scope, kind, rawURL, name, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ErrActiveSessionChanged
	}
	if scope != "cli" && scope != "project" {
		return fmt.Errorf("choose CLI or project scope")
	}
	origin := ""
	if strings.TrimSpace(rawURL) != "" {
		var err error
		origin, err = httpOrigin(rawURL)
		if err != nil {
			return err
		}
	}
	var err error
	switch action {
	case "set":
		err = c.store.SetHTTPRequestSetting(ctx, scope, kind, origin, name, value)
	case "remove":
		err = c.store.RemoveHTTPRequestSetting(ctx, scope, kind, origin, name)
	default:
		return fmt.Errorf("unknown HTTP setting action")
	}
	if err == nil {
		c.notifyChanged()
	}
	return err
}

func (c *SessionChat) SendHTTPRequestActive(ctx context.Context, sessionID string, request BrowserHTTPRequest) error {
	c.mu.Lock()
	if c.activeID != sessionID {
		c.mu.Unlock()
		return ErrActiveSessionChanged
	}
	store := c.store
	c.mu.Unlock()

	spec := httpRequestSpec{Method: strings.ToUpper(strings.TrimSpace(request.Method)), URL: strings.TrimSpace(request.URL), Headers: request.Headers, ReplaceHeaders: request.ReplaceHeaders, Body: request.Body}
	if !supportedHTTPMethod(spec.Method) {
		return fmt.Errorf("unsupported HTTP method")
	}
	if (spec.Method == http.MethodGet || spec.Method == http.MethodHead) && spec.Body != "" {
		return fmt.Errorf("GET and HEAD requests cannot have a body")
	}
	if len(spec.Body) > maxHTTPRequestBytes {
		return fmt.Errorf("HTTP request body exceeds 1 MiB")
	}
	originURL, err := httpOrigin(spec.URL)
	if err != nil {
		return fmt.Errorf("enter an HTTP or HTTPS URL without embedded credentials")
	}
	parsed, _ := url.ParseRequestURI(spec.URL)
	displayURL := *parsed
	displayURL.RawQuery, displayURL.ForceQuery, displayURL.Fragment = "", false, ""
	settings, err := store.HTTPRequestSettings(ctx, originURL)
	if err != nil {
		return fmt.Errorf("couldn't load HTTP request settings")
	}
	if spec.ReplaceHeaders {
		settings.Headers = make(map[string]string)
	}
	for name, value := range spec.Headers {
		canonical, validationErr := validateHTTPSetting("header", originURL, name, value)
		if validationErr != nil {
			return fmt.Errorf("invalid request header %q", name)
		}
		settings.Headers[canonical] = value
	}
	response, query, failure := fetchHTTPRequestResult(ctx, spec, displayURL.String(), settings)
	requestText := "/http " + strings.ToLower(spec.Method) + " " + displayURL.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ErrActiveSessionChanged
	}
	user, err := store.AppendUser(ctx, sessionID, requestText)
	if err != nil {
		return err
	}
	if failure != "" {
		_, err = store.AppendTurn(ctx, sessionID, user.ID, displayURL.String(), Turn{Text: failure})
	} else {
		_, err = store.AppendHTTPResponse(ctx, sessionID, user.ID, response, query)
	}
	if err == nil {
		c.notifyChanged()
	}
	return err
}
