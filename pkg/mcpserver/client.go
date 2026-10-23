// Package mcpserver exposes selected reads of a separately running local DataTug
// agent over MCP. It owns neither the agent process nor its policy session.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
)

const (
	DefaultAgentURL = "http://127.0.0.1:8989"
	maxBody         = 2 << 20
	maxRequest      = 64 << 10
	requestTimeout  = 30 * time.Second
)

// Client deliberately has no exported HTTP passthrough or configurable transport.
type Client struct {
	base *url.URL
	http *http.Client
}

// NewClient accepts only a literal loopback address or localhost, with no path,
// credentials, query or fragment. Proxies and redirects are disabled: a redirect
// to another port must never receive the original agent's bearer token.
func NewClient(address string) (*Client, error) {
	u, err := url.Parse(address)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("--agent-url must be an http:// loopback origin with an explicit port")
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, errors.New("--agent-url must use localhost, 127.0.0.1 or [::1]")
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil || u.Port() == "" {
		return nil, errors.New("--agent-url requires an explicit port")
	}
	if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
		return nil, errors.New("--agent-url requires a port from 1 to 65535")
	}
	u.Path = ""
	transport := &http.Transport{
		DialContext:     (&net.Dialer{Timeout: requestTimeout, KeepAlive: requestTimeout}).DialContext,
		IdleConnTimeout: requestTimeout, MaxIdleConns: 4, MaxIdleConnsPerHost: 4,
	}
	return &Client{base: u, http: &http.Client{
		Transport:     transport,
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Close releases idle HTTP connections; the separately owned agent keeps running.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// request is an internal fixed allowlist, rather than a user-selected URL.
func (c *Client) request(ctx context.Context, route string, values url.Values, input, output any) error {
	method := http.MethodGet
	switch route {
	case "agent-info", "projects/projects_summary", "projects/project_full", "queries/all_queries", "queries/get_query", "catalog-tables":
		if input != nil {
			return errors.New("unsupported agent request")
		}
	case "records/schema", "exec/run_query":
		method = http.MethodPost
	default:
		return errors.New("unsupported agent request")
	}
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil || len(body) > maxRequest {
			return errors.New("agent request is invalid or too large")
		}
	}
	u := *c.base
	u.Path = "/datatug/" + route
	u.RawQuery = values.Encode()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("could not create local agent request")
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	agentaccess.Apply(req)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("local agent request cancelled or timed out")
		}
		return errors.New("local agent unavailable; start datatug serve and check --agent-url")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return errors.New("local agent response is unreadable or exceeds 2 MiB")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Retain the established error code, never a driver error, request echo,
		// connection string, token, filesystem path or raw upstream body.
		var envelope struct {
			Code  string `json:"code"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		code := envelope.Code
		if code == "" {
			code = envelope.Error.Code
		}
		switch code {
		case "ACCESS_DENIED", "NOT_FOUND", "INVALID_REQUEST", "STALE_CONTEXT", "MISSING_PARAMETER", "TYPE_MISMATCH", "TARGET_REQUIRED", "SOURCE_UNAVAILABLE", "UNSUPPORTED_PROTECTED_EXECUTION", "TIMEOUT", "RESPONSE_TOO_LARGE":
		default:
			code = "AGENT_ERROR"
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("AUTHENTICATION_REQUIRED: local agent key is missing or invalid; use the same OS account and DATATUG_DAEMON_DIR as datatug serve")
		}
		return fmt.Errorf("%s: local agent refused the request (HTTP %d)", code, resp.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("local agent returned an invalid response")
	}
	return nil
}

func identifier(value string) bool {
	if value == "" || len(value) > 256 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("-_.", r) {
			return false
		}
	}
	return true
}

func queryIdentifier(value string) bool {
	if len(value) > 1024 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if !identifier(segment) {
			return false
		}
	}
	return true
}
