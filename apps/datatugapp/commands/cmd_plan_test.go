package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloudproto"
	"github.com/strongo/aichat/ai/decision"
	"golang.org/x/oauth2"
)

const contractFixtureDir = "testdata/plan-contract"

func contractFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractFixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlanContractFixtureChecksumsAndDecoding(t *testing.T) {
	manifest := contractFixture(t, "CHECKSUMS")
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		var digest, name string
		if _, err := fmt.Sscanf(line, "%s %s", &digest, &name); err != nil {
			t.Fatal(err)
		}
		b := contractFixture(t, name)
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != digest {
			t.Fatalf("%s checksum %s, want %s", name, got, digest)
		}
		var v map[string]json.RawMessage
		if err := json.Unmarshal(b, &v); err != nil || len(v) == 0 {
			t.Fatalf("%s is not a contract object: %v", name, err)
		}
	}
	var plan cloudPlan
	if err := json.Unmarshal(contractFixture(t, "plan-response-several-accounts.json"), &plan); err != nil || plan.V != 1 || plan.Payer != "personal" || !plan.hasModel("example-standard") || plan.hasModel("missing") {
		t.Fatalf("plan decode: %+v %v", plan, err)
	}
}

func TestPlanRejectsMissingOrNullUsageInsteadOfShowingZero(t *testing.T) {
	for _, body := range []string{
		`{"v":1,"ai":{"used":null,"limit":7,"left":7}}`,
		`{"v":1,"ai":{"used":0,"left":7}}`,
		`{"v":1,"ai":{"used":0,"limit":7,"left":-1}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := fakeCloudSession(server.URL+"/v0/").getPlan(context.Background(), "")
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "no valid usage") {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}

type failPlanWriter struct {
	remaining int
	writes    int
}

func (w *failPlanWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, io.ErrClosedPipe
	}
	w.remaining--
	w.writes++
	return len(p), nil
}

func TestWritePlanEveryOutputStepPropagatesFailure(t *testing.T) {
	var plan cloudPlan
	if err := json.Unmarshal(contractFixture(t, "plan-response-several-accounts.json"), &plan); err != nil {
		t.Fatal(err)
	}
	blocked := "daily"
	plan.AI.Blocked = &blocked
	plan.AI.Enforced = false
	var free cloudPlan
	if err := json.Unmarshal(contractFixture(t, "plan-response-free.json"), &free); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []cloudPlan{plan, free} {
		all := &failPlanWriter{remaining: 100}
		if err := writePlan(all, variant); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < all.writes; i++ {
			failed := &failPlanWriter{remaining: i}
			if err := writePlan(failed, variant); err != io.ErrClosedPipe {
				t.Fatalf("write %d/%d: %v", i, all.writes, err)
			}
		}
	}
	var proOutput, freeOutput bytes.Buffer
	if err := writePlan(&proOutput, plan); err != nil {
		t.Fatal(err)
	}
	if err := writePlan(&freeOutput, free); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(proOutput.String(), "Manage Pro:") || strings.Contains(proOutput.String(), "Get Pro:") || !strings.Contains(freeOutput.String(), "Get Pro:") {
		t.Fatalf("Pro/Free actions: pro=%q free=%q", proOutput.String(), freeOutput.String())
	}
	plan.AI.Used = nil
	var out bytes.Buffer
	if err := writePlan(&out, plan); err == nil || out.Len() != 0 {
		t.Fatalf("missing usage was printed: %q %v", out.String(), err)
	}
}

func TestPlanLookupFailsClosedOnTransportAndSelectionErrors(t *testing.T) {
	if _, err := fakeCloudSession("%invalid").getPlan(context.Background(), ""); err == nil {
		t.Fatal("malformed URL accepted")
	}
	badToken := fakeCloudSession("https://example.com/v0/")
	badToken.token = func(context.Context) (string, error) { return "", errors.New("expired") }
	if _, err := badToken.getPlan(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "sign-in unavailable") {
		t.Fatalf("token failure: %v", err)
	}
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	if _, err := fakeCloudSession(closedURL+"/v0/").getPlan(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("transport failure: %v", err)
	}
	var response atomic.Value
	response.Store("not json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(response.Load().(string)))
	}))
	defer server.Close()
	session := fakeCloudSession(server.URL + "/v0/")
	if _, err := session.getPlan(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("JSON failure: %v", err)
	}
	response.Store(`{"v":2}`)
	if plan, err := session.getPlan(context.Background(), ""); err != nil || plan.requirePersonalFreeOrPro() == nil {
		t.Fatalf("unknown-version plan accepted: %+v %v", plan, err)
	}
	response.Store(string(contractFixture(t, "plan-response-several-accounts.json")))
	server.Close()
	if _, err := resolveCloudSelection(context.Background(), chatCommand(), &chatOptions{}, session); err == nil {
		t.Fatal("selection proceeded after plan lookup failure")
	}
}

func TestHostedPlanRefreshPreservesLookupAndWriterErrors(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		currentStatus := int(status.Load())
		w.WriteHeader(currentStatus)
		if currentStatus == http.StatusOK {
			_, _ = w.Write(contractFixture(t, "plan-response-several-accounts.json"))
		}
	}))
	defer server.Close()
	session := fakeCloudSession(server.URL + "/v0/")
	var out bytes.Buffer
	if err := writeHostedPlan(context.Background(), session, "", &out); err != nil || !strings.Contains(out.String(), "9 left of 11") {
		t.Fatalf("refresh output=%q err=%v", out.String(), err)
	}
	if err := writeHostedPlan(context.Background(), session, "", &failPlanWriter{}); err != io.ErrClosedPipe {
		t.Fatalf("writer failure: %v", err)
	}
	status.Store(http.StatusServiceUnavailable)
	if err := writeHostedPlan(context.Background(), session, "", &out); err == nil || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatalf("refresh lookup failure: %v", err)
	}
}

func TestHostedPlanAndCommandRefuseOrganisationPayer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(contractFixture(t, "plan-response-team-member.json"))
	}))
	defer server.Close()
	var output bytes.Buffer
	if err := writeHostedPlan(context.Background(), fakeCloudSession(server.URL+"/v0/"), "", &output); err == nil || !strings.Contains(err.Error(), "personal Free and Pro") || output.Len() != 0 {
		t.Fatalf("organisation hosted plan: output=%q err=%v", output.String(), err)
	}
	previous := chatSavedTokenSource
	chatSavedTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)}), nil
	}
	t.Cleanup(func() { chatSavedTokenSource = previous })
	cmd := planCommand()
	cmd.SetArgs([]string{"--base-url", server.URL + "/v0/"})
	cmd.SetOut(&output)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "personal Free and Pro") || strings.Contains(output.String(), "Example Team") {
		t.Fatalf("organisation plan command: output=%q err=%v", output.String(), err)
	}
}

func TestCloudSessionRejectsKeyAndInstallationFailure(t *testing.T) {
	if _, err := loadCloudSession(context.Background(), chatOptions{apiKey: "not-a-cloud-login"}); err == nil || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("API key accepted as cloud login: %v", err)
	}
	previous := chatUserConfigDir
	chatUserConfigDir = func() (string, error) { return "", errors.New("unavailable") }
	t.Cleanup(func() { chatUserConfigDir = previous })
	if _, _, err := cloudChatClientFromSession(chatOptions{}, fakeCloudSession("https://example.com/v0/")); err == nil || !strings.Contains(err.Error(), "resolve DataTug configuration") {
		t.Fatalf("installation failure: %v", err)
	}
}

func TestPlanCommandRejectsInvalidCloudSession(t *testing.T) {
	cmd := planCommand()
	cmd.SetArgs([]string{"--base-url", "https://example.com/v1/"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "/v0/") {
		t.Fatalf("invalid cloud session: %v", err)
	}
}

func fakeCloudSession(base string) cloudSession {
	return cloudSession{baseURL: base, httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, token: func(context.Context) (string, error) { return "test-token", nil }, identity: "first-login"}
}

func TestCloudSelectionValidatesPersonalPlanAndModel(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		if r.URL.Path != "/v0/datatug/plan" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected request: %s %v", r.URL, r.Header)
		}
		_, _ = w.Write(contractFixture(t, "plan-response-several-accounts.json"))
	}))
	defer server.Close()
	session := fakeCloudSession(server.URL + "/v0/")
	cmd := chatCommand()
	options := chatOptions{project: "/local/project", cloudProject: "opaque-project", cloudModel: "example-standard"}
	plan, err := resolveCloudSelection(context.Background(), cmd, &options, session)
	if err != nil || plan.Payer != "personal" || options.cloudIdentity != session.identity || len(paths) != 1 || !strings.Contains(paths[0], "project=opaque-project") || strings.Contains(paths[0], "account=") {
		t.Fatalf("plan=%+v err=%v paths=%v options=%+v", plan, err, paths, options)
	}
	options.cloudModel = "not-allowed"
	if _, err := resolveCloudSelection(context.Background(), cmd, &options, session); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unlisted model accepted: %v", err)
	}
}

func TestCloudSelectionRejectsOtherPayerAndDiscardsForeignLogin(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Query().Get("project") == "team-project" {
			_, _ = w.Write(contractFixture(t, "plan-response-team-member.json"))
		} else {
			_, _ = w.Write(contractFixture(t, "plan-response-several-accounts.json"))
		}
	}))
	defer server.Close()
	session := fakeCloudSession(server.URL + "/v0/")
	options := chatOptions{project: "local", cloudProject: "team-project"}
	if _, err := resolveCloudSelection(context.Background(), chatCommand(), &options, session); err == nil || !strings.Contains(err.Error(), "personal Free and Pro") || hits != 1 {
		t.Fatalf("foreign payer err=%v hits=%d", err, hits)
	}
	options = chatOptions{project: "local", cloudModel: "example-standard", cloudProject: "old-hint", cloudIdentity: "other-login", cloudScope: "local"}
	plan, err := resolveCloudSelection(context.Background(), chatCommand(), &options, session)
	if err != nil || plan.AccountID != "p7Xk2" || options.cloudModel != "" || options.cloudProject != "" || hits != 2 {
		t.Fatalf("foreign preference leaked: plan=%+v err=%v options=%+v hits=%d", plan, err, options, hits)
	}
}

func TestPlanCommandShowsServerValuesAndHandlesStatuses(t *testing.T) {
	old := chatSavedTokenSource
	t.Cleanup(func() { chatSavedTokenSource = old })
	chatSavedTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token", Expiry: time.Now().Add(time.Hour)}), nil
	}
	status := http.StatusOK
	version := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/datatug/plan" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			return
		}
		b := contractFixture(t, "plan-response-several-accounts.json")
		if version != 1 {
			var obj map[string]any
			_ = json.Unmarshal(b, &obj)
			obj["v"] = version
			b, _ = json.Marshal(obj)
		}
		_, _ = w.Write(b)
	}))
	defer server.Close()
	for _, tc := range []struct {
		status  int
		version int
		want    string
		absent  string
	}{
		{200, 1, "9 left of 11", "out of date"},
		{200, 2, "out of date", "9 left"},
		{401, 1, "auth login", "p7Xk2"},
		{403, 1, "unavailable for your sign-in", "p7Xk2"},
		{404, 1, "HTTP 404", "9 left"},
		{503, 1, "temporarily unavailable", "9 left"},
	} {
		status, version = tc.status, tc.version
		cmd := planCommand()
		cmd.SetArgs([]string{"--base-url", server.URL + "/v0/"})
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		err := cmd.Execute()
		got := output.String()
		if err != nil {
			got += err.Error()
		}
		if !strings.Contains(got, tc.want) || strings.Contains(got, tc.absent) {
			t.Fatalf("status=%d v=%d got=%q err=%v", status, version, got, err)
		}
	}
}

func TestUnknownPlanVersionRefusesHostedCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/datatug/plan" {
			t.Errorf("unexpected hosted call: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"v":2,"accountId":"not-trusted","ai":{"left":999}}`))
	}))
	defer server.Close()
	options := chatOptions{project: "local-only-id", cloudProject: "opaque-project"}
	_, err := resolveCloudSelection(context.Background(), chatCommand(), &options, fakeCloudSession(server.URL+"/v0/"))
	if err == nil || !strings.Contains(err.Error(), "current plan version") {
		t.Fatalf("unknown version admitted hosted AI: %v", err)
	}
}

func TestTokenPreferenceScopeUsesIssuerSubjectWithoutStoringBearer(t *testing.T) {
	claim := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"issuer","sub":"person"}`))
	one := tokenPreferenceScope("a." + claim + ".sig1")
	two := tokenPreferenceScope("a." + claim + ".sig2")
	if one != two || strings.Contains(one, "issuer") || tokenPreferenceScope("opaque-a") == tokenPreferenceScope("opaque-b") {
		t.Fatalf("scope mismatch or leaked claim: %q %q", one, two)
	}
}

func TestCloudChoicesRememberedOnlyForTheirLoginAndProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat-last.json")
	previous := lastChatOptionsPath
	lastChatOptionsPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { lastChatOptionsPath = previous })
	cmd := chatCommand()
	_ = cmd.Flags().Set("model", "cloud")
	options := chatOptions{model: "cloud", project: "one-project", cloudModel: "example-standard", cloudProject: "cloud-binding", cloudIdentity: "first-login", cloudScope: "one-project"}
	if err := saveLastChatOptions(cmd, options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "test-token") {
		t.Fatalf("read saved choices: %v", err)
	}
	loaded := chatOptions{project: ".", model: defaultChatModel}
	if err := applyLastChatOptions(chatCommand(), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.cloudModel != options.cloudModel || loaded.cloudProject != options.cloudProject || loaded.cloudIdentity != options.cloudIdentity {
		t.Fatalf("remembered choice = %+v", loaded)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(contractFixture(t, "plan-response-several-accounts.json"))
	}))
	defer server.Close()
	session := fakeCloudSession(server.URL + "/v0/")
	session.identity = "second-login"
	if _, err := resolveCloudSelection(context.Background(), chatCommand(), &loaded, session); err != nil || loaded.cloudModel != "" || loaded.cloudProject != "" {
		t.Fatalf("foreign login choice leaked: %+v %v", loaded, err)
	}
}

func TestPersonalCloudContextAppliesToEveryAIRoute(t *testing.T) {
	for _, project := range []string{"opaque-cloud-project", ""} {
		t.Run(project, func(t *testing.T) {
			var aiHeaders []http.Header
			var planProject string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v0/datatug/plan":
					planProject = r.URL.Query().Get("project")
					_, _ = w.Write(contractFixture(t, "plan-response-free.json"))
				case "/v0/ai/chat", "/v0/ai/decision", "/v0/ai/score", "/v0/ai/interactions":
					aiHeaders = append(aiHeaders, r.Header.Clone())
					if r.URL.Path == "/v0/ai/interactions" {
						w.WriteHeader(http.StatusAccepted)
					} else {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write(contractFixture(t, "refusal-question-context-changed.json"))
					}
				default:
					t.Errorf("unexpected %s", r.URL.Path)
				}
			}))
			defer server.Close()
			session := fakeCloudSession(server.URL + "/v0/")
			options := chatOptions{project: "local-id-never-a-cloud-hint", cloudProject: project}
			if _, err := resolveCloudSelection(context.Background(), chatCommand(), &options, session); err != nil {
				t.Fatal(err)
			}
			client, _, err := cloudChatClientFromSession(options, session)
			if err != nil {
				t.Fatal(err)
			}
			const questionID = "550e8400-e29b-41d4-a716-446655440000"
			for range client.Stream(context.Background(), ai.ChatRequest{InteractionID: questionID}) {
			}
			_, _, _ = client.Decider().Decide(context.Background(), decision.Request{Text: "question"})
			if scorer, ok := client.Decider().(decision.ScoredProvider); ok {
				_, _ = scorer.Score(context.Background(), decision.ScoreRequest{Text: "question", Questions: []decision.Question{{ID: "q", Kind: decision.KindChoice, Instructions: "choose", Candidates: []decision.Candidate{{ID: "a"}, {ID: "b"}}}}})
			} else {
				t.Fatal("cloud decision provider has no scorer")
			}
			_ = client.ReportInteraction(context.Background(), cloudproto.InteractionReport{InteractionID: questionID, Status: "completed"})
			if planProject != project || len(aiHeaders) != 4 {
				t.Fatalf("plan project=%q AI calls=%d", planProject, len(aiHeaders))
			}
			for _, header := range aiHeaders {
				if header.Get(cloudproto.HeaderProduct) != "datatug" || header.Get(cloudproto.HeaderProject) != planProject || header.Get(cloudproto.HeaderAccount) != "" {
					t.Fatalf("inconsistent personal AI context: %+v", header)
				}
			}
		})
	}
}
