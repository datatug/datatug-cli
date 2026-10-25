package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/google/uuid"
)

const (
	checksSummaryAPIURL    = "https://api.sneat.cloud/v0/"
	checksSummarySchema    = "datatug.check-summary/v1"
	checksSummaryMaxBytes  = 32 << 10
	checksSummaryRetention = 7 * 24 * time.Hour
)

var checksSummaryTokenSource = device.SavedTokenSource
var checksSummaryNewOperation = uuid.NewV7
var checksSummaryTransport http.RoundTripper = http.DefaultTransport

// This closed DTO is independent of CheckRun and cannot carry source evidence.
type checksSummaryEntry struct {
	CheckID         string `json:"checkId"`
	CheckVersion    int64  `json:"checkVersion"`
	ExecutionStatus string `json:"executionStatus"`
	Outcome         string `json:"outcome"`
	Severity        string `json:"severity"`
	DurationMs      int64  `json:"durationMs"`
}

type checksSummaryRequest struct {
	SchemaVersion string               `json:"schemaVersion"`
	SpaceID       string               `json:"spaceID"`
	Project       string               `json:"project"`
	Environment   string               `json:"environment"`
	OperationID   string               `json:"operationId"`
	ExecutedAt    time.Time            `json:"executedAt"`
	Checks        []checksSummaryEntry `json:"checks"`
}

type checksSummaryResponse struct {
	SchemaVersion string               `json:"schemaVersion"`
	SummaryID     string               `json:"summaryId"`
	SpaceID       string               `json:"spaceID"`
	Project       string               `json:"project"`
	Environment   string               `json:"environment"`
	ExecutedAt    time.Time            `json:"executedAt"`
	CreatedAt     time.Time            `json:"createdAt"`
	ExpiresAt     time.Time            `json:"expiresAt"`
	Provenance    string               `json:"provenance"`
	Checks        []checksSummaryEntry `json:"checks"`
}

func checksSummaryIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func checksSummaryEndpoint(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Fragment != "" || u.RawFragment != "" || u.Path != "/v0/" || u.RawPath != "" {
		return "", false
	}
	if raw == checksSummaryAPIURL {
		return raw + "datatug/shipping/check-summary", true
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return u.String() + "datatug/shipping/check-summary", true
}

func checksSummaryEnvName(name string) bool {
	if len(name) < 1 || len(name) > 128 {
		return false
	}
	for i, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func checksSummaryBearer(token string) bool {
	if len(token) < 1 || len(token) > 8192 {
		return false
	}
	for _, c := range token {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func checksSummaryPayload(o checksRunOptions, project string, versions []string, executedAt time.Time, results []checksRunResult) (checksSummaryRequest, bool) {
	r := checksSummaryRequest{SchemaVersion: checksSummarySchema, SpaceID: o.summarySpace, Project: project, Environment: o.environment, ExecutedAt: executedAt.UTC(), Checks: make([]checksSummaryEntry, 0, len(results))}
	now := time.Now()
	if !checksSummaryIdentifier(r.SpaceID) || !checksSummaryIdentifier(project) || !checksSummaryIdentifier(r.Environment) || executedAt.IsZero() || executedAt.Before(now.Add(-24*time.Hour)) || executedAt.After(now.Add(5*time.Minute)) || len(results) < 1 || len(results) > 100 || len(results) > len(o.checks) || len(results) > len(versions) {
		return r, false
	}
	seen := map[string]int64{}
	for i, result := range results {
		version, err := strconv.ParseInt(versions[i], 10, 64)
		if err != nil || version < 1 || !checksSummaryIdentifier(o.checks[i]) || result.DurationMS < 0 || result.DurationMS > 3600000 || result.Index != i+1 {
			return r, false
		}
		if old, found := seen[o.checks[i]]; found && old != version {
			return r, false
		}
		seen[o.checks[i]] = version
		if checksEnum(result.ExecutionStatus, "completed", "error", "skipped") == "" || checksEnum(result.Outcome, "pass", "fail", "unknown") == "" || result.ExecutionStatus != "completed" && result.Outcome != "unknown" || result.Severity != "" && checksEnum(result.Severity, "critical", "error", "warning", "info") == "" {
			return r, false
		}
		r.Checks = append(r.Checks, checksSummaryEntry{CheckID: o.checks[i], CheckVersion: version, ExecutionStatus: result.ExecutionStatus, Outcome: result.Outcome, Severity: result.Severity, DurationMs: result.DurationMS})
	}
	return r, true
}

func publishChecksSummary(ctx context.Context, o checksRunOptions, project string, versions []string, executedAt time.Time, results []checksRunResult) (string, string) {
	endpoint, ok := checksSummaryEndpoint(o.summaryAPIURL)
	if !ok || o.summaryTokenEnv != "" && !checksSummaryEnvName(o.summaryTokenEnv) {
		return "", "summary_publish_configuration_error"
	}
	request, ok := checksSummaryPayload(o, project, versions, executedAt, results)
	if !ok {
		return "", "summary_publish_incompatible_result"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return "", checksSummaryRequestFailure(ctx)
	}
	var token string
	if o.summaryTokenEnv != "" {
		token = os.Getenv(o.summaryTokenEnv)
	} else {
		// Saved production credentials must never be loaded for a local host.
		if o.summaryAPIURL != checksSummaryAPIURL {
			return "", "summary_publish_auth_required"
		}
		source, err := checksSummaryTokenSource(ctx, false)
		if err != nil || source == nil {
			return "", checksSummaryAuthFailure(ctx)
		}
		current, err := tokenWithContext(ctx, source)
		if err != nil || current == nil || !current.Valid() {
			return "", checksSummaryAuthFailure(ctx)
		}
		token = current.AccessToken
	}
	if !checksSummaryBearer(token) {
		return "", "summary_publish_auth_required"
	}
	operation, err := checksSummaryNewOperation()
	if err != nil {
		return "", "summary_publish_unavailable"
	}
	request.OperationID = operation.String()
	// The closed payload's 100-entry/128-byte identifier limits keep its maximum
	// serialized size below 32 KiB (also verified against the largest valid DTO).
	body, _ := json.Marshal(request)
	httpRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: checksSummaryTransport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(httpRequest)
	if err != nil {
		return "", checksSummaryRequestFailure(ctx)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return "", "summary_publish_denied"
		}
		return "", "summary_publish_unavailable"
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.ContentLength > checksSummaryMaxBytes {
		return "", "summary_publish_invalid_response"
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, checksSummaryMaxBytes+1))
	if err != nil {
		return "", checksSummaryRequestFailure(ctx)
	}
	if len(body) > checksSummaryMaxBytes {
		return "", "summary_publish_invalid_response"
	}
	var out checksSummaryResponse
	if !decodeChecksSummaryResponse(body, &out) || !validChecksSummaryResponse(out, request, time.Now()) {
		return "", "summary_publish_invalid_response"
	}
	fragment, _ := json.Marshal(struct {
		Version     int    `json:"v"`
		Environment string `json:"environment"`
		SummaryID   string `json:"summaryId"`
	}{1, out.Environment, out.SummaryID})
	return "https://datatug.app/space/" + url.PathEscape(out.SpaceID) + "/store/firestore/project/" + url.PathEscape(out.Project) + "/overview#check-summary=" + base64.RawURLEncoding.EncodeToString(fragment), "hosted_summary_published"
}

func checksSummaryRequestFailure(ctx context.Context) string {
	if ctx.Err() == context.Canceled {
		return "summary_publish_cancelled"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "summary_publish_timeout"
	}
	return "summary_publish_unavailable"
}

func checksSummaryAuthFailure(ctx context.Context) string {
	if ctx.Err() != nil {
		return checksSummaryRequestFailure(ctx)
	}
	return "summary_publish_auth_required"
}

func checksSummaryObject(body []byte, fields ...string) bool {
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil || len(members) != len(fields) {
		return false
	}
	for _, field := range fields {
		raw, found := members[field]
		if !found || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	return true
}

func decodeChecksSummaryResponse(body []byte, out *checksSummaryResponse) bool {
	if apicontract.DecodeStrict(body, out) != nil || !checksSummaryObject(body, "schemaVersion", "summaryId", "spaceID", "project", "environment", "executedAt", "createdAt", "expiresAt", "provenance", "checks") {
		return false
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(body, &members)
	var entries []json.RawMessage
	_ = json.Unmarshal(members["checks"], &entries)
	if len(entries) < 1 || len(entries) > 100 {
		return false
	}
	for _, entry := range entries {
		if !checksSummaryObject(entry, "checkId", "checkVersion", "executionStatus", "outcome", "severity", "durationMs") {
			return false
		}
	}
	return true
}

func validChecksSummaryResponse(out checksSummaryResponse, request checksSummaryRequest, now time.Time) bool {
	id, err := base64.RawURLEncoding.DecodeString(out.SummaryID)
	return err == nil && len(id) == 32 && base64.RawURLEncoding.EncodeToString(id) == out.SummaryID && out.SchemaVersion == checksSummarySchema && out.SpaceID == request.SpaceID && out.Project == request.Project && out.Environment == request.Environment && out.ExecutedAt.Equal(request.ExecutedAt) && out.Provenance == "client-reported" && slices.Equal(out.Checks, request.Checks) && !out.CreatedAt.IsZero() && !out.CreatedAt.After(now.Add(5*time.Minute)) && !out.CreatedAt.Before(request.ExecutedAt.Add(-5*time.Minute)) && !out.CreatedAt.Before(now.Add(-24*time.Hour)) && out.ExpiresAt.Equal(out.CreatedAt.Add(checksSummaryRetention)) && now.Before(out.ExpiresAt)
}
