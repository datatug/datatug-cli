package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type summaryCLITransport func(*http.Request) (*http.Response, error)

func (f summaryCLITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type summaryCLITokenSource func() (*oauth2.Token, error)

func (f summaryCLITokenSource) Token() (*oauth2.Token, error) { return f() }

func summaryCLISeams(t *testing.T) {
	t.Helper()
	source, operation, transport := checksSummaryTokenSource, checksSummaryNewOperation, checksSummaryTransport
	t.Cleanup(func() {
		checksSummaryTokenSource, checksSummaryNewOperation, checksSummaryTransport = source, operation, transport
	})
}

func summaryCLIData() (checksRunOptions, []string, []checksRunResult) {
	return checksRunOptions{checks: []string{"check"}, environment: "prod", summarySpace: "space", summaryAPIURL: checksSummaryAPIURL}, []string{"1"}, []checksRunResult{{Index: 1, Status: "pass", ExecutionStatus: "completed", Outcome: "pass", DurationMS: 42}}
}

func summaryCLIResponse(r checksSummaryRequest) checksSummaryResponse {
	now := time.Now().UTC()
	return checksSummaryResponse{SchemaVersion: r.SchemaVersion, SummaryID: strings.Repeat("A", 43), SpaceID: r.SpaceID, Project: r.Project, Environment: r.Environment, ExecutedAt: r.ExecutedAt, CreatedAt: now, ExpiresAt: now.Add(checksSummaryRetention), Provenance: "client-reported", Checks: r.Checks}
}

func executeSummaryCLI(t *testing.T, ctx context.Context, project, private string, flags ...string) (checksRunSummary, string, error) {
	t.Helper()
	cmd := detectiveUICommand("checks")
	var output, diagnostics bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(append([]string{"run", "--project", project, "--env", "local", "--local-owner", "--evidence-dir", private}, flags...))
	err := cmd.ExecuteContext(ctx)
	var summary checksRunSummary
	if json.Unmarshal(output.Bytes(), &summary) != nil {
		t.Fatalf("invalid output %q: %v", output.String(), err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics escaped: %q", diagnostics.String())
	}
	if summary.ExitCode == 0 && err != nil {
		t.Fatal(err)
	}
	if summary.ExitCode != 0 {
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != summary.ExitCode {
			t.Fatalf("local exit changed: %+v %v", summary, err)
		}
	}
	for _, secret := range []string{project, private, "synthetic-private-row-sentinel", "summary-private-token", "SELECT ", "MissingTable", "Trusted demo Check"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("private value escaped: %s", secret)
		}
	}
	return summary, output.String(), err
}

func TestChecksSummaryURLAndCredentialBoundaries(t *testing.T) {
	for _, base := range []string{checksSummaryAPIURL, "http://127.0.0.1:8877/v0/", "http://127.0.0.2:1/v0/", "https://[::1]:65535/v0/"} {
		endpoint, ok := checksSummaryEndpoint(base)
		if !ok || endpoint != base+"datatug/shipping/check-summary" {
			t.Fatalf("rejected allowed endpoint %q", base)
		}
	}
	for _, base := range []string{"", "://bad", "http://api.sneat.cloud/v0/", "https://api.sneat.cloud:443/v0/", "https://api.sneat.cloud.evil/v0/", "https://evil.example/v0/", "http://localhost:8877/v0/", "http://127.0.0.1:8877/v0/#", "http://127.0.0.1/v0/", "http://127.0.0.1:0/v0/", "http://127.0.0.1:65536/v0/", "http://127.0.0.1:bad/v0/", "http://192.168.1.1:8877/v0/", "ftp://127.0.0.1:8877/v0/", "https://user:secret@api.sneat.cloud/v0/", "https://api.sneat.cloud/v0/?token=secret", "https://api.sneat.cloud/v0/?", "https://api.sneat.cloud/v0/#token", "https://api.sneat.cloud/%76%30/", "https://api.sneat.cloud/v0//", "https:opaque/v0/"} {
		if _, ok := checksSummaryEndpoint(base); ok {
			t.Fatalf("unsafe endpoint accepted: %q", base)
		}
	}
	for _, name := range []string{"TOKEN", "_TOKEN_1", "a9"} {
		if !checksSummaryEnvName(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"", "1TOKEN", "TOKEN-VALUE", "TOKEN=secret", strings.Repeat("x", 129)} {
		if checksSummaryEnvName(name) {
			t.Fatal("invalid env reference admitted")
		}
	}
	for _, token := range []string{"", "has space", "bad\r\nHeader:secret", "ä", strings.Repeat("x", 8193)} {
		if checksSummaryBearer(token) {
			t.Fatal("unsafe bearer admitted")
		}
	}
	if !checksSummaryBearer("eyJ.safe-token") {
		t.Fatal("normal bearer rejected")
	}
}

func TestChecksSummaryPayloadAllowsOnlyMinimalCompatibleResults(t *testing.T) {
	o, versions, results := summaryCLIData()
	now := time.Now().UTC()
	r, ok := checksSummaryPayload(o, "project", versions, now, results)
	if !ok || len(r.Checks) != 1 || r.Checks[0].CheckID != "check" || r.Checks[0].CheckVersion != 1 {
		t.Fatal("minimal request lost")
	}
	for _, change := range []func(*checksRunOptions, *[]string, *[]checksRunResult, *time.Time){
		func(o *checksRunOptions, _ *[]string, _ *[]checksRunResult, _ *time.Time) { o.summarySpace = "/" }, func(o *checksRunOptions, _ *[]string, _ *[]checksRunResult, _ *time.Time) { o.environment = "/" }, func(_ *checksRunOptions, _ *[]string, _ *[]checksRunResult, at *time.Time) { *at = time.Time{} }, func(_ *checksRunOptions, _ *[]string, _ *[]checksRunResult, at *time.Time) {
			*at = now.Add(-25 * time.Hour)
		}, func(_ *checksRunOptions, _ *[]string, _ *[]checksRunResult, at *time.Time) {
			*at = now.Add(6 * time.Minute)
		},
		func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) { *r = nil }, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) {
			*r = make([]checksRunResult, 101)
		}, func(o *checksRunOptions, _ *[]string, _ *[]checksRunResult, _ *time.Time) { o.checks = nil }, func(_ *checksRunOptions, v *[]string, _ *[]checksRunResult, _ *time.Time) { *v = nil },
		func(_ *checksRunOptions, v *[]string, _ *[]checksRunResult, _ *time.Time) { (*v)[0] = "v1" }, func(_ *checksRunOptions, v *[]string, _ *[]checksRunResult, _ *time.Time) { (*v)[0] = "0" }, func(o *checksRunOptions, _ *[]string, _ *[]checksRunResult, _ *time.Time) { o.checks[0] = "check.path" },
		func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) { (*r)[0].DurationMS = -1 }, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) {
			(*r)[0].DurationMS = 3600001
		}, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) { (*r)[0].Index = 2 }, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) {
			(*r)[0].ExecutionStatus = "running"
		}, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) { (*r)[0].Outcome = "secret" }, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) {
			(*r)[0].ExecutionStatus = "error"
		}, func(_ *checksRunOptions, _ *[]string, r *[]checksRunResult, _ *time.Time) {
			(*r)[0].Severity = "raw error"
		},
	} {
		candidate, v, rr := summaryCLIData()
		at := now
		change(&candidate, &v, &rr, &at)
		if _, ok := checksSummaryPayload(candidate, "project", v, at, rr); ok {
			t.Fatal("incompatible result admitted")
		}
	}
	if _, ok := checksSummaryPayload(o, "/", versions, now, results); ok {
		t.Fatal("unsafe project admitted")
	}
	o.checks = append(o.checks, "check")
	versions = append(versions, "1")
	results = append(results, results[0])
	results[1].Index = 2
	results[1].Outcome = "fail"
	if r, ok = checksSummaryPayload(o, "project", versions, now, results); !ok || r.Checks[0].Outcome == r.Checks[1].Outcome {
		t.Fatal("duplicate Check ordering lost")
	}
	versions[1] = "2"
	if _, ok := checksSummaryPayload(o, "project", versions, now, results); ok {
		t.Fatal("conflicting duplicate versions admitted")
	}
	for _, status := range []string{"error", "skipped"} {
		candidate, v, rr := summaryCLIData()
		rr[0].ExecutionStatus = status
		rr[0].Outcome = "unknown"
		if _, ok := checksSummaryPayload(candidate, "project", v, now, rr); !ok {
			t.Fatal("compatible uncertain result rejected")
		}
	}
}

func TestChecksSummaryOptInAbsentDoesNotReadAuthOrUseNetwork(t *testing.T) {
	summaryCLISeams(t)
	checksSummaryTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) { t.Fatal("default read auth"); return nil, nil }
	checksSummaryTransport = summaryCLITransport(func(*http.Request) (*http.Response, error) { t.Fatal("default used network"); return nil, nil })
	project, private := checksDemo(t)
	s, _, _ := executeSummaryCLI(t, context.Background(), project, private, "--check", "pass", "--summary-space", "invalid/", "--summary-api-url", "https://evil.example/v0/")
	if s.Status != "pass" || s.DeepLink != "https://datatug.app" || s.DeepLinkReason != "hosted_result_reference_unsupported" {
		t.Fatal("default behavior changed")
	}
}

func TestChecksSummaryActualCheckHTTPRoundTripAndLocalExitPreservation(t *testing.T) {
	for _, tc := range []struct {
		check    string
		httpCode int
		wantCode int
	}{{"pass", 200, 0}, {"fail", 200, 1}, {"pass", 403, 0}, {"fail", 403, 1}, {"source-error", 403, 4}} {
		t.Run(tc.check+strconv.Itoa(tc.httpCode), func(t *testing.T) {
			summaryCLISeams(t)
			project, private := checksDemo(t)
			manifest, e := os.ReadFile(filepath.Join(project, "datatug-project.json"))
			if e != nil {
				t.Fatal(e)
			}
			var metadata struct {
				ID string `json:"id"`
			}
			if e = json.Unmarshal(manifest, &metadata); e != nil {
				t.Fatal(e)
			}
			t.Setenv("SUMMARY_TEST_TOKEN", "summary-private-token")
			checksSummaryTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
				t.Fatal("loopback loaded production login")
				return nil, nil
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v0/datatug/shipping/check-summary" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer summary-private-token" {
					t.Error("request origin/auth changed")
				}
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
					return
				}
				if len(raw) > checksSummaryMaxBytes {
					t.Error("unbounded request")
				}
				for _, forbidden := range []string{"SELECT ", "inputs", "definition", "rows", "index", "status"} {
					if strings.Contains(string(raw), `"`+forbidden+`"`) || strings.Contains(string(raw), "SELECT ") {
						t.Error("private execution data escaped")
					}
				}
				var request checksSummaryRequest
				if e = json.Unmarshal(raw, &request); e != nil {
					t.Error(e)
					return
				}
				operation, e := uuid.Parse(request.OperationID)
				if e != nil || operation.Version() != 7 || request.Project != metadata.ID || request.SpaceID != "space" || request.Environment != "local" || len(request.Checks) != 1 || request.Checks[0].CheckID != tc.check || request.Checks[0].CheckVersion != 1 {
					t.Errorf("wrong admitted request: %+v", request)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.httpCode)
				if tc.httpCode == 200 {
					_ = json.NewEncoder(w).Encode(summaryCLIResponse(request))
				} else {
					_, _ = io.WriteString(w, "private upstream SQL and credential")
				}
			}))
			defer server.Close()
			s, _, _ := executeSummaryCLI(t, context.Background(), project, private, "--check", tc.check, "--publish-summary", "--summary-space", "space", "--summary-token-env", "SUMMARY_TEST_TOKEN", "--summary-api-url", server.URL+"/v0/")
			if s.ExitCode != tc.wantCode || calls != 1 {
				t.Fatalf("local result changed: %+v calls %d", s, calls)
			}
			if tc.httpCode == 200 {
				prefix := "https://datatug.app/space/space/store/firestore/project/" + metadata.ID + "/overview#check-summary="
				if !strings.HasPrefix(s.DeepLink, prefix) || s.DeepLinkReason != "hosted_summary_published" {
					t.Fatal("hosted link missing")
				}
				fragment, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s.DeepLink, prefix))
				if e != nil || string(fragment) != `{"v":1,"environment":"local","summaryId":"`+strings.Repeat("A", 43)+`"}` {
					t.Fatal("fragment contract changed")
				}
			} else if s.DeepLink != "" || s.DeepLinkReason != "summary_publish_denied" {
				t.Fatal("publication refusal hidden")
			}
		})
	}
}

func TestChecksSummarySavedLoginRestrictedToProductionOrigin(t *testing.T) {
	summaryCLISeams(t)
	o, v, r := summaryCLIData()
	sourceCalls, networkCalls := 0, 0
	checksSummaryTokenSource = func(ctx context.Context, insecure bool) (oauth2.TokenSource, error) {
		sourceCalls++
		d, ok := ctx.Deadline()
		if insecure || !ok || time.Until(d) > 15*time.Second {
			t.Error("saved auth deadline/storage changed")
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "scoped-login", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}), nil
	}
	checksSummaryTransport = summaryCLITransport(func(req *http.Request) (*http.Response, error) {
		networkCalls++
		if req.URL.String() != checksSummaryAPIURL+"datatug/shipping/check-summary" || req.Header.Get("Authorization") != "Bearer scoped-login" {
			t.Error("production token escaped")
		}
		var request checksSummaryRequest
		if e := json.NewDecoder(req.Body).Decode(&request); e != nil {
			t.Fatal(e)
		}
		body, e := json.Marshal(summaryCLIResponse(request))
		if e != nil {
			t.Fatal(e)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	link, reason := publishChecksSummary(context.Background(), o, "project", v, time.Now().UTC(), r)
	if reason != "hosted_summary_published" || link == "" || sourceCalls != 1 || networkCalls != 1 {
		t.Fatalf("scoped login failed %s", reason)
	}
	for _, base := range []string{"http://127.0.0.1:8877/v0/", "https://evil.example/v0/", "http://localhost:8877/v0/"} {
		o.summaryAPIURL = base
		link, _ = publishChecksSummary(context.Background(), o, "project", v, time.Now().UTC(), r)
		if link != "" || sourceCalls != 1 || networkCalls != 1 {
			t.Fatal("saved credential crossed origin boundary")
		}
	}
}

func TestChecksSummaryResponseFixtureAndStrictTrustChecks(t *testing.T) {
	// Frozen backend api4datatug/testdata/check-summary-v1.json contract.
	fixture := `{"schemaVersion":"datatug.check-summary/v1","spaceID":"space","project":"project","environment":"demo","executedAt":"2026-10-25T12:00:00Z","checks":[{"checkId":"shipping-request-received","checkVersion":1,"executionStatus":"completed","outcome":"pass","severity":"","durationMs":123}],"summaryId":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","createdAt":"2026-10-25T12:00:00Z","expiresAt":"2026-11-01T12:00:00Z","provenance":"client-reported"}`
	var out checksSummaryResponse
	if !decodeChecksSummaryResponse([]byte(fixture), &out) {
		t.Fatal("backend fixture rejected")
	}
	request := checksSummaryRequest{SchemaVersion: out.SchemaVersion, SpaceID: out.SpaceID, Project: out.Project, Environment: out.Environment, ExecutedAt: out.ExecutedAt, Checks: out.Checks}
	if !validChecksSummaryResponse(out, request, out.CreatedAt) {
		t.Fatal("backend fixture identity rejected")
	}
	for _, change := range []func(*checksSummaryResponse){func(s *checksSummaryResponse) { s.SchemaVersion = "other" }, func(s *checksSummaryResponse) { s.SpaceID = "foreign" }, func(s *checksSummaryResponse) { s.Project = "foreign" }, func(s *checksSummaryResponse) { s.Environment = "foreign" }, func(s *checksSummaryResponse) { s.SummaryID = "invalid" }, func(s *checksSummaryResponse) { s.SummaryID = strings.Repeat("A", 42) + "B" }, func(s *checksSummaryResponse) { s.ExecutedAt = s.ExecutedAt.Add(time.Second) }, func(s *checksSummaryResponse) { s.CreatedAt = time.Time{} }, func(s *checksSummaryResponse) { s.CreatedAt = s.CreatedAt.Add(6 * time.Minute) }, func(s *checksSummaryResponse) { s.CreatedAt = s.CreatedAt.Add(-6 * time.Minute) }, func(s *checksSummaryResponse) { s.ExpiresAt = s.CreatedAt }, func(s *checksSummaryResponse) { s.Provenance = "server-attested" }, func(s *checksSummaryResponse) { s.Checks = nil }} {
		candidate := out
		change(&candidate)
		if validChecksSummaryResponse(candidate, request, out.CreatedAt) {
			t.Fatal("foreign/corrupt response admitted")
		}
	}
	for _, bad := range []string{strings.Replace(fixture, `"spaceID":`, `"SpaceID":`, 1), strings.Replace(fixture, `"project":`, `"project":"other","project":`, 1), strings.Replace(fixture, `"project":`, `"Project":"other","project":`, 1), strings.Replace(fixture, `"project":`, `"url":"https://evil.example","project":`, 1), strings.Replace(fixture, `"severity":""`, `"severity":null`, 1), strings.Replace(fixture, `"checkId":`, `"CheckId":`, 1), strings.Replace(fixture, `"checkId":`, `"CheckId":"other","checkId":`, 1), strings.Replace(fixture, `"durationMs":123`, `"durationMs":123,"durationMs":1`, 1), fixture + `{}`, `null`, `{}`} {
		var candidate checksSummaryResponse
		if decodeChecksSummaryResponse([]byte(bad), &candidate) {
			t.Fatal("noncanonical response admitted")
		}
	}
}

func TestChecksSummaryMaximumTypedPayloadIsBounded(t *testing.T) {
	o, _, _ := summaryCLIData()
	o.checks = make([]string, 100)
	versions := make([]string, 100)
	results := make([]checksRunResult, 100)
	for i := range results {
		o.checks[i] = strings.Repeat("x", 128)
		versions[i] = strconv.FormatInt(math.MaxInt64, 10)
		results[i] = checksRunResult{Index: i + 1, ExecutionStatus: "completed", Outcome: "unknown", Severity: "critical", DurationMS: 3600000}
	}
	o.summarySpace, o.environment = strings.Repeat("x", 128), strings.Repeat("x", 128)
	request, ok := checksSummaryPayload(o, strings.Repeat("x", 128), versions, time.Now(), results)
	if !ok {
		t.Fatal("maximum valid payload rejected")
	}
	request.OperationID = uuid.Must(uuid.NewV7()).String()
	body, e := json.Marshal(request)
	if e != nil || len(body) > checksSummaryMaxBytes {
		t.Fatalf("wire budget exceeded %d %v", len(body), e)
	}
	if !reflect.DeepEqual(request.Checks[0], request.Checks[99]) {
		t.Fatal("duplicate ordering/identity lost")
	}
}
