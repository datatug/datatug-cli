package openvaultdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dtql/authorization"
)

const (
	MaxRequestBytes  = 1 << 20
	MaxResponseBytes = 8 << 20
)

type Target struct {
	BaseURL    string
	DatabaseID string
	Token      string
}

func (t Target) Validate() error {
	u, err := url.Parse(t.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid OpenVaultDB base URL")
	}
	if strings.ContainsAny(t.DatabaseID, "/\\?#") || strings.TrimSpace(t.DatabaseID) != t.DatabaseID || t.DatabaseID == "" {
		return errors.New("invalid OpenVaultDB database ID")
	}
	if strings.TrimSpace(t.Token) == "" {
		return errors.New("missing OpenVaultDB bearer token")
	}
	return nil
}

type Client struct{ HTTP *http.Client }

type Response struct {
	StatusCode int
	Body       []byte
	Type       string
}

func (c Client) Query(ctx context.Context, target Target, dtql []byte) (Response, error) {
	ctx, cancel := withMaxDeadline(ctx, 10*time.Second)
	defer cancel()
	return c.do(ctx, target, http.MethodPost, "/v1/databases/"+url.PathEscape(target.DatabaseID)+"/dtql", "application/yaml", dtql, 0)
}

func (c Client) Explain(ctx context.Context, target Target, body []byte) (Response, error) {
	ctx, cancel := withMaxDeadline(ctx, 2*time.Second)
	defer cancel()
	return c.do(ctx, target, http.MethodPost, "/v1/databases/"+url.PathEscape(target.DatabaseID)+"/access/evaluate", "application/json", body, 1)
}

func (c Client) Update(ctx context.Context, target Target, table, row string, body []byte) (Response, error) {
	path := "/v1/databases/" + url.PathEscape(target.DatabaseID) + "/records/" + url.PathEscape(table) + "/" + url.PathEscape(row)
	ctx, cancel := withMaxDeadline(ctx, 10*time.Second)
	defer cancel()
	return c.do(ctx, target, http.MethodPatch, path, "application/vnd.dtql.operation+json", body, 2)
}

func (c Client) Evidence(ctx context.Context, target Target, body []byte) (Response, error) {
	ctx, cancel := withMaxDeadline(ctx, 2*time.Second)
	defer cancel()
	return c.do(ctx, target, http.MethodPost, "/v1/databases/"+url.PathEscape(target.DatabaseID)+"/access/evidence", "application/json", body, 0)
}

func withMaxDeadline(ctx context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= maximum {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, maximum)
}

func (c Client) do(ctx context.Context, target Target, method, path, contentType string, body []byte, validation int) (Response, error) {
	if err := target.Validate(); err != nil {
		return Response{}, err
	}
	if len(body) == 0 || len(body) > MaxRequestBytes {
		return Response{}, errors.New("invalid OpenVaultDB request size")
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(target.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return Response{}, errors.New("failed to create OpenVaultDB request")
	}
	req.Header.Set("Authorization", "Bearer "+target.Token)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	baseClient := c.HTTP
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	httpClient := *baseClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, fmt.Errorf("OpenVaultDB request failed: %w", ctxErr)
		}
		return Response{}, errors.New("OpenVaultDB request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		return Response{}, errors.New("failed to read OpenVaultDB response")
	}
	if len(data) > MaxResponseBytes {
		return Response{}, errors.New("OpenVaultDB response exceeds size limit")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || DecodeJSONObjectStrict(data, nil) != nil {
		return Response{}, errors.New("OpenVaultDB returned an invalid JSON response")
	}
	if validation != 0 && res.StatusCode >= 200 && res.StatusCode < 300 {
		var validationErr error
		if validation == 1 {
			_, validationErr = authorization.DecodeResultCompatible(data)
		} else {
			var result authorization.Result
			var revision string
			result, revision, validationErr = ValidateAuthorizationEnvelope(data)
			if validationErr == nil && (result.Mode != authorization.ModeExecution || result.Result != authorization.OutcomeAllow ||
				!result.Allowed || result.Coverage.Evaluation != authorization.EvaluationComplete || result.Coverage.Truncated ||
				revision == "" || res.StatusCode != http.StatusOK) {
				validationErr = errors.New("update response did not contain a completed execution allow")
			}
		}
		if validationErr != nil {
			return Response{}, fmt.Errorf("invalid OpenVaultDB authorization result: %w", validationErr)
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var envelope map[string]json.RawMessage
		if DecodeJSONObjectStrict(data, &envelope) == nil {
			if nested := envelope["authorization"]; len(nested) != 0 {
				if _, err := authorization.DecodeResultCompatible(nested); err != nil {
					return Response{}, errors.New("OpenVaultDB returned an invalid authorization error")
				}
			}
		}
	}
	return Response{StatusCode: res.StatusCode, Body: data, Type: res.Header.Get("Content-Type")}, nil
}

func ValidateAuthorizationEnvelope(data []byte) (authorization.Result, string, error) {
	var envelope map[string]json.RawMessage
	if err := DecodeJSONObjectStrict(data, &envelope); err != nil {
		return authorization.Result{}, "", errors.New("invalid OpenVaultDB authorization response")
	}
	authData := envelope["authorization"]
	if len(authData) == 0 {
		return authorization.Result{}, "", errors.New("OpenVaultDB response omitted authorization result")
	}
	result, err := authorization.DecodeResultCompatible(authData)
	if err != nil {
		return authorization.Result{}, "", fmt.Errorf("invalid OpenVaultDB authorization result: %w", err)
	}
	var revision string
	if raw := envelope["dataRevision"]; len(raw) != 0 && json.Unmarshal(raw, &revision) != nil {
		return authorization.Result{}, "", errors.New("invalid OpenVaultDB data revision")
	}
	return result, revision, nil
}

// DecodeJSONObjectStrict rejects duplicate keys at every depth before
// optionally decoding the top-level object.
func DecodeJSONObjectStrict(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := scanJSONValue(d, map[string]bool{}); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return errors.New("expected JSON object")
	}
	if target != nil {
		return json.Unmarshal(data, target)
	}
	return nil
}

func scanJSONValue(d *json.Decoder, _ map[string]bool) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || keys[key] {
				return errors.New("duplicate or invalid JSON object key")
			}
			keys[key] = true
			if err := scanJSONValue(d, nil); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := scanJSONValue(d, nil); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}
