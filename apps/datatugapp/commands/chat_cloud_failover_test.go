package commands

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func stubCloudLogin(t *testing.T) {
	t.Helper()
	old := chatSavedTokenSource
	t.Cleanup(func() { chatSavedTokenSource = old })
	chatSavedTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "failover-test-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}), nil
	}
}

func TestDefaultCloudAPIURLsIsTodaysSingleHost(t *testing.T) {
	if len(defaultAIAPIURLs) != 1 || defaultAIAPIURLs[0] != "https://api.sneat.cloud/v0/" {
		t.Fatalf("production default changed: %v", defaultAIAPIURLs)
	}
	stubCloudLogin(t)
	session, err := defaultLoadCloudSession(context.Background(), chatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if session.baseURL != defaultAIAPIURL || session.httpClient.Transport != nil {
		t.Fatalf("default session: base=%q transport=%v", session.baseURL, session.httpClient.Transport)
	}
}

func TestCloudSessionSingleBaseURLHasNoFailoverTransport(t *testing.T) {
	stubCloudLogin(t)
	session, err := defaultLoadCloudSession(context.Background(), chatOptions{baseURL: " http://127.0.0.1:8877/v0/ "})
	if err != nil {
		t.Fatal(err)
	}
	if session.baseURL != "http://127.0.0.1:8877/v0/" || session.httpClient.Transport != nil {
		t.Fatalf("single URL session: base=%q transport=%v", session.baseURL, session.httpClient.Transport)
	}
}

func TestCloudSessionRejectsAnyInvalidURLInTheList(t *testing.T) {
	stubCloudLogin(t)
	_, err := defaultLoadCloudSession(context.Background(), chatOptions{baseURL: "https://api.sneat.cloud/v0/,http://api2.example.com/v0/"})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudSessionFailsOverToTheSecondURL(t *testing.T) {
	stubCloudLogin(t)
	var gotAuth, gotPath string
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	var notice bytes.Buffer
	old := cloudFailoverNotice
	t.Cleanup(func() { cloudFailoverNotice = old })
	cloudFailoverNotice = &notice

	session, err := defaultLoadCloudSession(context.Background(), chatOptions{baseURL: deadURL + "/v0/, " + fallback.URL + "/v0/"})
	if err != nil {
		t.Fatal(err)
	}
	if session.baseURL != deadURL+"/v0/" {
		t.Fatalf("primary must stay the first URL: %q", session.baseURL)
	}
	token, err := session.token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, session.baseURL+"ai/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := session.httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotPath != "/v0/ai/models" || gotAuth != "Bearer failover-test-token" {
		t.Fatalf("status=%d path=%q auth=%q", resp.StatusCode, gotPath, gotAuth)
	}
	if !strings.Contains(notice.String(), "did not answer") || strings.Contains(notice.String(), "failover-test-token") {
		t.Fatalf("notice = %q", notice.String())
	}
}
