//go:build !windows

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(request *http.Request, status int, location string) *http.Response {
	headers := make(http.Header)
	if location != "" {
		headers.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}
}

func TestInvocationDefaultsToHelpAndPreservesArguments(t *testing.T) {
	got, err := invocation(nil, "")
	if err != nil || len(got) != 1 || got[0] != "--help" {
		t.Fatalf("invocation(nil) = %v, %v; want [--help], nil", got, err)
	}
	want := []string{"checks", "run", "--project", "/project with spaces", "--local-owner"}
	got, err = invocation(want, "")
	if err != nil || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("invocation(%q) = %q, %v; arguments must pass through unchanged", want, got, err)
	}
}

func TestRunnerRequiresExactLocalOwnerConsent(t *testing.T) {
	for _, args := range [][]string{
		{"daemon", "--run"},
		{"daemon", "--run=true"},
		{"daemon", "--run=1"},
		{"daemon", "--run=t"},
		{"daemon", "--run=TRUE"},
		{"daemon", "--coordinate", "--run"},
		{"--config", "/private/settings.yaml", "daemon", "--run"},
	} {
		if _, err := invocation(args, ""); err == nil || !strings.Contains(err.Error(), "DATATUG_LOCAL_OWNER=true") {
			t.Errorf("invocation(%q, no consent) error = %v; want explicit owner denial", args, err)
		}
		if _, err := invocation(args, "false"); err == nil {
			t.Errorf("invocation(%q, false consent) unexpectedly succeeded", args)
		}
		if _, err := invocation(args, "TRUE"); err == nil {
			t.Errorf("invocation(%q, non-exact consent) unexpectedly succeeded", args)
		}
		if got, err := invocation(args, "true"); err != nil || strings.Join(got, "\x00") != strings.Join(args, "\x00") {
			t.Errorf("invocation(%q, explicit consent) = %q, %v", args, got, err)
		}
	}
}

func TestRunnerGateDoesNotTreatPrincipalOrFalseFlagAsConsent(t *testing.T) {
	// Principal is intentionally absent from this policy input: only the explicit
	// deployment environment variable can satisfy the container gate.
	if _, err := invocation([]string{"daemon", "--watch-config", "/private/runner.json", "--run"}, ""); err == nil {
		t.Fatal("named principal in a config path must not bypass missing consent")
	}
	for _, args := range [][]string{
		{"daemon", "--run=false"},
		{"daemon", "--coordinate"},
		{"checks", "run", "--local-owner"},
	} {
		if _, err := invocation(args, ""); err != nil {
			t.Errorf("invocation(%q) unexpectedly denied: %v", args, err)
		}
	}
}

func TestHealthcheckRequiresCoordinatorAndLoopbackHealthURL(t *testing.T) {
	goodURL := "http://127.0.0.1:12345/health"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Errorf("request = %s %s, want GET /health", r.Method, r.URL.Path)
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 3*time.Second || time.Until(deadline) <= 0 {
			t.Errorf("health request deadline = %v, present=%v; want a deadline within three seconds", deadline, ok)
		}
		return response(r, http.StatusOK, ""), nil
	})}
	if err := healthcheckWithClient(context.Background(), goodURL, client); err != nil {
		t.Fatalf("healthcheck(%s) = %v", goodURL, err)
	}
	if err := healthcheckWithClient(context.Background(), "https://127.0.0.1:12345/health", client); err != nil {
		t.Fatal("verified TLS health URL rejected", err)
	}
	for _, raw := range []string{
		"https://example.com:12345/health",
		"http://example.com:12345/health",
		"http://127.0.0.1:12345/health?token=secret",
		"http://127.0.0.1:12345/other",
	} {
		if err := healthcheckWithClient(context.Background(), raw, client); err == nil {
			t.Errorf("healthcheck(%q) unexpectedly accepted", raw)
		}
	}
}

func TestHealthcheckRejectsRedirect(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return response(r, http.StatusFound, "http://127.0.0.1:1/health"), nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if err := healthcheckWithClient(context.Background(), "http://127.0.0.1:12345/health", client); err == nil {
		t.Fatal("redirect response must not be treated as a healthy Coordinator")
	}
}
