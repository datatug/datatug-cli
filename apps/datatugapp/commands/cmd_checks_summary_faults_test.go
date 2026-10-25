package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type summaryCLIErrorBody struct{}

func (summaryCLIErrorBody) Read([]byte) (int, error) {
	return 0, errors.New("private upstream credential/body error")
}
func (summaryCLIErrorBody) Close() error { return nil }

func TestChecksSummaryAuthFailuresStaySafeAndNeverReachNetwork(t *testing.T) {
	for _, kind := range []string{"load-error", "nil-source", "token-error", "nil-token", "expired-token", "empty-token", "cancelled-refresh", "local-no-env", "missing-env", "unsafe-env", "bad-env-name", "bad-url", "incompatible", "entropy"} {
		t.Run(kind, func(t *testing.T) {
			summaryCLISeams(t)
			o, v, r := summaryCLIData()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := "summary_publish_auth_required"
			sourceCalls := 0
			checksSummaryTransport = summaryCLITransport(func(*http.Request) (*http.Response, error) { t.Fatal("failure reached network"); return nil, nil })
			checksSummaryTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
				sourceCalls++
				switch kind {
				case "load-error":
					return nil, errors.New("private keyring credential")
				case "nil-source":
					return nil, nil
				}
				return summaryCLITokenSource(func() (*oauth2.Token, error) {
					switch kind {
					case "token-error":
						return nil, errors.New("private refresh token")
					case "nil-token":
						return nil, nil
					case "expired-token":
						return &oauth2.Token{AccessToken: "secret", Expiry: time.Now().Add(-time.Hour)}, nil
					case "empty-token":
						return &oauth2.Token{Expiry: time.Now().Add(time.Hour)}, nil
					case "cancelled-refresh":
						cancel()
						return nil, errors.New("secret refresh")
					}
					return &oauth2.Token{AccessToken: "valid", Expiry: time.Now().Add(time.Hour)}, nil
				}), nil
			}
			switch kind {
			case "cancelled-refresh":
				want = "summary_publish_cancelled"
			case "local-no-env":
				o.summaryAPIURL = "http://127.0.0.1:8877/v0/"
			case "missing-env":
				o.summaryTokenEnv = "SUMMARY_ABSENT_TOKEN"
				t.Setenv(o.summaryTokenEnv, "")
			case "unsafe-env":
				o.summaryTokenEnv = "SUMMARY_UNSAFE_TOKEN"
				t.Setenv(o.summaryTokenEnv, "secret\r\nX:private")
			case "bad-env-name":
				o.summaryTokenEnv = "value=secret"
				want = "summary_publish_configuration_error"
			case "bad-url":
				o.summaryAPIURL = "https://evil.example/v0/"
				want = "summary_publish_configuration_error"
			case "incompatible":
				v[0] = "v1"
				want = "summary_publish_incompatible_result"
			case "entropy":
				checksSummaryNewOperation = func() (uuid.UUID, error) { return uuid.Nil, errors.New("private random error") }
				want = "summary_publish_unavailable"
			}
			link, reason := publishChecksSummary(ctx, o, "project", v, time.Now(), r)
			if link != "" || reason != want {
				t.Fatalf("unsafe/incorrect refusal: %q %q", link, reason)
			}
			if (kind == "local-no-env" || kind == "missing-env" || kind == "unsafe-env" || kind == "bad-env-name" || kind == "bad-url" || kind == "incompatible") && sourceCalls != 0 {
				t.Fatal("ineligible request loaded saved login")
			}
		})
	}
}

func TestChecksSummaryHTTPFailuresAndInvalidResponses(t *testing.T) {
	for _, kind := range []string{"network", "unauthorized", "redirect-status", "service", "media", "declared-size", "actual-size", "read", "malformed", "unknown", "foreign-space", "expired"} {
		t.Run(kind, func(t *testing.T) {
			summaryCLISeams(t)
			o, v, r := summaryCLIData()
			o.summaryTokenEnv = "SUMMARY_EXPLICIT_TOKEN"
			t.Setenv(o.summaryTokenEnv, "summary-private-token")
			want := "summary_publish_invalid_response"
			checksSummaryTransport = summaryCLITransport(func(req *http.Request) (*http.Response, error) {
				d, ok := req.Context().Deadline()
				if !ok || time.Until(d) > 15*time.Second {
					t.Fatal("missing publication deadline")
				}
				var request checksSummaryRequest
				if e := json.NewDecoder(req.Body).Decode(&request); e != nil {
					t.Fatal(e)
				}
				out := summaryCLIResponse(request)
				switch kind {
				case "foreign-space":
					out.SpaceID = "foreign"
				case "expired":
					out.CreatedAt = time.Now().Add(-8 * 24 * time.Hour)
					out.ExpiresAt = out.CreatedAt.Add(checksSummaryRetention)
				}
				raw, e := json.Marshal(out)
				if e != nil {
					t.Fatal(e)
				}
				response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: req}
				switch kind {
				case "network":
					return nil, errors.New("private token in request URL")
				case "unauthorized":
					response.StatusCode = 401
				case "redirect-status":
					response.StatusCode = 302
				case "service":
					response.StatusCode = 503
				case "media":
					response.Header.Set("Content-Type", "text/html")
				case "declared-size":
					response.ContentLength = checksSummaryMaxBytes + 1
				case "actual-size":
					response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", checksSummaryMaxBytes+1)))
				case "read":
					response.Body = summaryCLIErrorBody{}
				case "malformed":
					response.Body = io.NopCloser(strings.NewReader("not-json private SQL"))
				case "unknown":
					response.Body = io.NopCloser(strings.NewReader(strings.Replace(string(raw), `"project":`, `"url":"https://evil.example","project":`, 1)))
				}
				return response, nil
			})
			if kind == "network" || kind == "redirect-status" || kind == "service" || kind == "read" {
				want = "summary_publish_unavailable"
			}
			if kind == "unauthorized" {
				want = "summary_publish_denied"
			}
			link, reason := publishChecksSummary(context.Background(), o, "project", v, time.Now(), r)
			if link != "" || reason != want {
				t.Fatalf("unsafe response failure: %s %s", link, reason)
			}
		})
	}
}

func TestChecksSummaryRedirectNeverForwardsBearer(t *testing.T) {
	summaryCLISeams(t)
	o, v, r := summaryCLIData()
	o.summaryTokenEnv = "SUMMARY_REDIRECT_TOKEN"
	t.Setenv(o.summaryTokenEnv, "summary-private-token")
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	o.summaryAPIURL = server.URL + "/v0/"
	link, reason := publishChecksSummary(context.Background(), o, "project", v, time.Now(), r)
	if forwarded || link != "" || reason != "summary_publish_unavailable" {
		t.Fatal("redirect followed or returned URL trusted")
	}
}

func TestChecksSummaryPublicationCancellationAndDeadline(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		summaryCLISeams(t)
		o, v, r := summaryCLIData()
		o.summaryTokenEnv = "SUMMARY_CANCEL_TOKEN"
		t.Setenv(o.summaryTokenEnv, "summary-private-token")
		ctx, cancel := context.WithCancel(context.Background())
		want := "summary_publish_cancelled"
		cancel()
		if timeout {
			ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
			defer cancel()
			<-ctx.Done()
			want = "summary_publish_timeout"
		}
		link, reason := publishChecksSummary(ctx, o, "project", v, time.Now(), r)
		if link != "" || reason != want {
			t.Fatal("cancelled request admitted")
		}
	}
	if checksSummaryAuthFailure(context.Background()) != "summary_publish_auth_required" {
		t.Fatal("auth failure exposed details")
	}
}

func TestChecksSummaryDecoderArrayAndIdentifierBounds(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), "project.path"} {
		if checksSummaryIdentifier(id) {
			t.Fatal("unsafe or unbounded identifier admitted")
		}
	}
	o, v, r := summaryCLIData()
	request, ok := checksSummaryPayload(o, "project", v, time.Now(), r)
	if !ok {
		t.Fatal("fixture")
	}
	for _, count := range []int{0, 101} {
		out := summaryCLIResponse(request)
		out.Checks = make([]checksSummaryEntry, count)
		raw, e := json.Marshal(out)
		if e != nil {
			t.Fatal(e)
		}
		if decodeChecksSummaryResponse(raw, new(checksSummaryResponse)) {
			t.Fatal("unbounded response array admitted")
		}
	}
}
