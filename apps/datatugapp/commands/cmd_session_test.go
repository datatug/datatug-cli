package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
)

type sessionTestTransport struct{ handler http.Handler }

func (t sessionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

type sessionFixtureTransport struct {
	body     []byte
	fallback http.RoundTripper
}

func (transport sessionFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() == sessionChinookURL {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(transport.body)), Request: r}, nil
	}
	return transport.fallback.RoundTrip(r)
}

type sessionLostACKTransport struct {
	handler http.Handler
	action  string
	commit  bool
	drop    bool
	report  sessioncontext.Command
}

func (transport *sessionLostACKTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/commands") {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var command sessioncontext.Command
		if json.Unmarshal(body, &command) == nil && command.Action == transport.action && transport.drop {
			transport.drop = false
			transport.report = command
			if transport.commit {
				w := httptest.NewRecorder()
				transport.handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					return w.Result(), nil
				}
			}
			return nil, errors.New("test lost outcome acknowledgement")
		}
	}
	w := httptest.NewRecorder()
	transport.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestContextSessionSendRecoversLostACKWithoutProviderRedispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		commit bool
	}{{"completed-report-ack-lost", true}, {"accepted-outcome-unknown", false}} {
		t.Run(test.name, func(t *testing.T) {
			project, private := t.TempDir(), t.TempDir()
			env := filepath.Join(project, "environments", "local")
			if err := os.MkdirAll(env, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(env, "local.env.json"), []byte(`{"id":"local","dbServers":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			originalStore, originalTransport, originalSettings, originalInterpret := storage.NewDatatugStore, http.DefaultTransport, getChatSettings, sessionInterpretDetailed
			paths := map[string]string{"send-recovery": project}
			storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
			api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
			router := httprouter.New()
			endpoints.RegisterDatatugHandlersWithCapabilities("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) { return r.Context(), nil }, apicore.Execute, endpoints.Capabilities{AllowWrites: true})
			transport := &sessionLostACKTransport{handler: router, action: "reportClientRun", commit: test.commit, drop: true}
			http.DefaultTransport = transport
			t.Cleanup(func() {
				storage.NewDatatugStore = originalStore
				http.DefaultTransport = originalTransport
				getChatSettings = originalSettings
				sessionInterpretDetailed = originalInterpret
				_ = api.CloseSessionContext()
				api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
			})
			t.Setenv("TEST_SEND_RECOVERY_KEY", "test-key")
			getChatSettings = func() (dtconfig.Settings, error) {
				return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{"test": {Model: "gpt-test", BaseURL: "https://api.openai.com", APIKeyEnv: "TEST_SEND_RECOVERY_KEY"}}}}, nil
			}
			providerCalls := 0
			sessionInterpretDetailed = func(context.Context, chat.InterpretRequest) (chat.InterpretResult, error) {
				providerCalls++
				return chat.InterpretResult{DTQL: "from:\n  name: Invoice\n"}, nil
			}
			execute := func(input string, args ...string) (string, error) {
				cmd := sessionCommand()
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				cmd.SetIn(strings.NewReader(input))
				cmd.SetArgs(append([]string{"--project", "send-recovery"}, args...))
				err := cmd.ExecuteContext(context.Background())
				return out.String(), err
			}
			if _, err := execute("", "create", "--id", "session", "--title", "Recovery"); err != nil {
				t.Fatal(err)
			}
			message := `{"text":"Show invoices","schema":"Invoice(InvoiceId integer)","attachments":[]}`
			if _, err := execute(message, "send", "session", "--ai", "test", "--op", "original-send"); err == nil || providerCalls != 1 || transport.report.RunID == "" {
				t.Fatalf("lost ACK err=%v providerCalls=%d report=%+v", err, providerCalls, transport.report)
			}
			out, err := execute(message, "send", "session", "--ai", "test", "--op", "original-send")
			if providerCalls != 1 {
				t.Fatalf("accepted replay dispatched provider %d times", providerCalls)
			}
			var response struct {
				Run            sessioncontext.Run    `json:"run"`
				Replayed       bool                  `json:"replayed"`
				Interpretation *chat.InterpretResult `json:"interpretation"`
			}
			if json.Unmarshal([]byte(out), &response) != nil || !response.Replayed || response.Run.ID != transport.report.RunID {
				t.Fatalf("recovery response %s err=%v", out, err)
			}
			if test.commit {
				if err != nil || response.Run.State != "completed" || response.Interpretation == nil || response.Interpretation.DTQL != "from:\n  name: Invoice\n" {
					t.Fatalf("completed recovery %+v %v", response, err)
				}
				// The printed report receipt is also a read-only recovery identity.
				if _, err = execute("", "send", "session", "--op", transport.report.OpID); err != nil || providerCalls != 1 {
					t.Fatalf("report receipt recovery calls=%d err=%v", providerCalls, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "outcome unknown") || response.Run.State != "accepted" || response.Interpretation != nil {
				t.Fatalf("accepted unknown %+v %v", response, err)
			}
		})
	}
}

func TestContextSessionTerminalDurableComposerAndDashboard(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	t.Setenv("DATATUG_SESSION_RESULTS_DIR", t.TempDir())
	env := filepath.Join(project, "environments", "local")
	if err := os.MkdirAll(env, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "local.env.json"), []byte(`{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["chinook"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "connections"), 0700); err != nil {
		t.Fatal(err)
	}
	catalog := `{"format":"` + datatug.ConnectionCatalogFormat + `","connections":[{"id":"chinook-sqlite","dataset":"chinook","storage":"sqlite","readiness":"public-api","source":"https://demodb.dev/ovdb/v1/databases/chinook","fixtureSha256":"` + sessionChinookHash + `","browserFixture":{"url":"` + sessionChinookURL + `","bytes":1007616}}]}`
	if err := os.WriteFile(filepath.Join(project, "connections", "demo-db.json"), []byte(catalog), 0600); err != nil {
		t.Fatal(err)
	}
	originalStore, originalTransport := storage.NewDatatugStore, http.DefaultTransport
	paths := map[string]string{"terminal-context": project}
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	router := httprouter.New()
	endpoints.RegisterDatatugHandlersWithCapabilities("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) { return r.Context(), nil }, apicore.Execute, endpoints.Capabilities{AllowWrites: true})
	http.DefaultTransport = sessionTestTransport{router}
	originalSettings, originalInterpret := getChatSettings, sessionInterpretDetailed
	t.Cleanup(func() {
		storage.NewDatatugStore = originalStore
		http.DefaultTransport = originalTransport
		getChatSettings = originalSettings
		sessionInterpretDetailed = originalInterpret
		_ = api.CloseSessionContext()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	execute := func(input string, args ...string) (string, string, error) {
		cmd := sessionCommand()
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs(append([]string{"--project", "terminal-context"}, args...))
		err := cmd.ExecuteContext(context.Background())
		return out.String(), stderr.String(), err
	}
	out, stderr, err := execute("", "create", "--id", "s1", "--title", "private-title")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "operation ") {
		t.Fatal("operation recovery identity was not emitted before mutation")
	}
	var created struct {
		Snapshot sessioncontext.Snapshot `json:"snapshot"`
	}
	if json.Unmarshal([]byte(out), &created) != nil {
		t.Fatal(out)
	}
	service, p, caps, err := api.ResolveSessionContext(context.Background(), "local", "terminal-context")
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Subjects) != 1 {
		t.Fatal("verified sample subject absent")
	}
	entryInput, _ := json.Marshal(map[string]any{"subject": caps.Subjects[0].Subject, "value": map[string]any{"schemaVersion": 1, "kind": "scalar", "scalar": map[string]string{"type": "integer", "value": "1"}}})
	if _, _, err = execute(string(entryInput), "put", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = execute("", "apply", "s1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.GetSnapshot(context.Background(), p, "s1")
	if err != nil {
		t.Fatal(err)
	}
	entry := snapshot.Entries[0]
	t.Setenv("TEST_SESSION_MODEL_KEY", "private-model-key")
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{"test": {Model: "gpt-test", BaseURL: "https://api.openai.com", APIKeyEnv: "TEST_SESSION_MODEL_KEY"}}}}, nil
	}
	providerCalls := 0
	sessionInterpretDetailed = func(ctx context.Context, request chat.InterpretRequest) (chat.InterpretResult, error) {
		providerCalls++
		messages, readErr := service.ListMessages(ctx, p, "s1", "", 100)
		if readErr != nil || len(messages.Items) != 1 {
			t.Fatalf("provider dispatched before durable question: %+v %v", messages, readErr)
		}
		input, revisions, readErr := service.ReadInput(ctx, p, messages.Items[0].InputSnapshotID)
		if readErr != nil || len(input.Entries) != 1 || len(revisions) != 1 {
			t.Fatalf("provider missing immutable attachment %+v %v", input, readErr)
		}
		if request.Provider.APIKey != "private-model-key" || !strings.Contains(request.Schema, entry.CurrentRevisionID) {
			t.Fatal("runtime did not receive explicit pinned inputs")
		}
		return chat.InterpretResult{DTQL: "from:\n  name: InvoiceLine\n"}, nil
	}
	message, _ := json.Marshal(map[string]any{"text": "Show this invoice's lines", "schema": "Invoice(InvoiceId integer); InvoiceLine(InvoiceId integer)", "attachments": []sessioncontext.EntryRef{{EntryID: entry.ID, RevisionID: entry.CurrentRevisionID}}})
	out, _, err = execute(string(message), "send", "s1", "--ai", "test")
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls != 1 || strings.Contains(out, "private-model-key") {
		t.Fatal("provider dispatch or key privacy failed")
	}
	messages, err := service.ListMessages(context.Background(), p, "s1", "", 100)
	if err != nil || len(messages.Items) != 2 {
		t.Fatalf("durable question/response %+v %v", messages, err)
	}
	if _, _, err = execute("", "messages", "s1"); err != nil || providerCalls != 1 {
		t.Fatalf("reopen provider calls=%d err=%v", providerCalls, err)
	}

	t.Run("PinnedChinookFixture", func(t *testing.T) {
		fixture := os.Getenv("DATATUG_TEST_CHINOOK_FIXTURE")
		if fixture == "" {
			t.Skip("DATATUG_TEST_CHINOOK_FIXTURE is required for the real pinned fixture journey")
		}
		originalDashboard := sessionRunInvoiceDashboard
		sourceCalls := 0
		sessionRunInvoiceDashboard = func(ctx context.Context, path, invoice string) (sessionDashboardResult, error) {
			sourceCalls++
			return originalDashboard(ctx, path, invoice)
		}
		t.Cleanup(func() { sessionRunInvoiceDashboard = originalDashboard })
		lostACK := &sessionLostACKTransport{handler: router, action: "reportDashboard", commit: true, drop: true}
		http.DefaultTransport = lostACK
		_, _, reportErr := execute("", "dashboard", "s1", "--fixture", fixture, "--op", "dashboard-lost-ack")
		if reportErr == nil || sourceCalls != 1 || !strings.Contains(reportErr.Error(), "session operation "+lostACK.report.OpID) || !strings.Contains(reportErr.Error(), "session run "+lostACK.report.RunID) || !strings.Contains(reportErr.Error(), "original dashboard operation dashboard-lost-ack") {
			t.Fatalf("lost dashboard ACK calls=%d report=%+v err=%v", sourceCalls, lostACK.report, reportErr)
		}
		// Follow the exact printed read-only recovery instructions.
		for _, args := range [][]string{{"operation", lostACK.report.OpID}, {"run", lostACK.report.RunID}, {"dashboard", "s1", "--op", "dashboard-lost-ack"}} {
			if _, _, err = execute("", args...); err != nil || sourceCalls != 1 {
				t.Fatalf("recovery %v calls=%d err=%v", args, sourceCalls, err)
			}
		}
		if err = api.CloseSessionContext(); err != nil {
			t.Fatal(err)
		}
		out, _, err = execute("", "result", lostACK.report.RunID)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Result       sessionDashboardResult `json:"result"`
			Availability string                 `json:"availability"`
			Storage      string                 `json:"storage"`
		}
		if json.Unmarshal([]byte(out), &result) != nil || result.Availability != "available" || result.Storage != "local-device" || sourceCalls != 1 || len(result.Result.Grid.Rows) != 2 || len(result.Result.Chart.Rows) != 1 || result.Result.OutcomeOrigin != "client-reported" {
			t.Fatalf("dashboard actual Invoice1 fixture %s", out)
		}
		total := new(big.Rat)
		for _, row := range result.Result.Grid.Rows {
			value, ok := new(big.Rat).SetString(row[len(row)-1].Str)
			if !ok {
				t.Fatal("nondecimal grid amount")
			}
			total.Add(total, value)
		}
		expected, _ := new(big.Rat).SetString("1.98")
		if total.Cmp(expected) != 0 || result.Result.Chart.Rows[0][1].Str != "1.98" {
			t.Fatalf("Invoice1 grid total=%s chart=%+v", total.RatString(), result.Result.Chart.Rows)
		}
		if err = result.Result.Grid.Validate(); err != nil {
			t.Fatal(err)
		}
		if err = result.Result.Chart.Validate(); err != nil {
			t.Fatal(err)
		}
		fixtureBytes, readErr := os.ReadFile(fixture)
		if readErr != nil {
			t.Fatal(readErr)
		}
		downloadDir := t.TempDir()
		t.Setenv("TMPDIR", downloadDir)
		http.DefaultTransport = sessionFixtureTransport{body: fixtureBytes, fallback: lostACK}
		if _, readErr = runSessionInvoiceDashboard(context.Background(), "", "1"); readErr != nil {
			t.Fatal(readErr)
		}
		http.DefaultTransport = lostACK
		remaining, readErr := os.ReadDir(downloadDir)
		if readErr != nil || len(remaining) != 0 {
			t.Fatalf("sample download/snapshot tempfiles leaked: %v %v", remaining, readErr)
		}
		cachedDirectory := os.Getenv("DATATUG_SESSION_RESULTS_DIR")
		t.Setenv("DATATUG_SESSION_RESULTS_DIR", filepath.Join(t.TempDir(), "missing"))
		out, _, err = execute("", "result", lostACK.report.RunID)
		if err != nil || !strings.Contains(out, `"availability":"unavailable"`) || strings.Contains(out, `"result":`) || sourceCalls != 1 {
			t.Fatalf("absent device cache reran or exposed rows: %s %v calls=%d", out, err, sourceCalls)
		}
		t.Setenv("DATATUG_SESSION_RESULTS_DIR", cachedDirectory)
		api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "bob"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
		out, _, err = execute("", "result", lostACK.report.RunID)
		if err == nil || strings.Contains(out, `"result":`) || sourceCalls != 1 {
			t.Fatalf("another actor read cached rows: %s %v calls=%d", out, err, sourceCalls)
		}
		api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	})
	t.Run("ExplicitLegacyTerminalChatImport", func(t *testing.T) {
		legacyDir := t.TempDir()
		if chmodErr := os.Chmod(legacyDir, 0700); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		legacyPath := filepath.Join(legacyDir, "legacy.sqlite")
		legacyScope := chat.ChatScope{ProjectID: "terminal-context", Environment: "local", Database: "chinook", AccessFingerprint: "legacy-policy", Sources: map[string]string{"chinook": "https://demodb.dev/ovdb/v1/databases/chinook"}}
		legacyStore, openErr := chat.OpenSessionStore(legacyPath, legacyScope)
		if openErr != nil {
			t.Fatal(openErr)
		}
		legacySession, createErr := legacyStore.Create(context.Background(), "Legacy title")
		if createErr != nil {
			t.Fatal(createErr)
		}
		question, appendErr := legacyStore.AppendUser(context.Background(), legacySession.ID, "Legacy question")
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		if _, appendErr = legacyStore.AppendTurn(context.Background(), legacySession.ID, question.ID, "", chat.Turn{Text: "Legacy reply", TextFormat: "markdown"}); appendErr != nil {
			t.Fatal(appendErr)
		}
		if closeErr := legacyStore.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		before, readErr := os.ReadFile(legacyPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		out, _, importErr := execute("", "import", "--legacy-chat-store", legacyPath, "--legacy-session", legacySession.ID, "--op", "legacy-import")
		if importErr != nil {
			t.Fatal(importErr)
		}
		var imported struct {
			Receipt  sessioncontext.Receipt  `json:"receipt"`
			Snapshot sessioncontext.Snapshot `json:"snapshot"`
		}
		if json.Unmarshal([]byte(out), &imported) != nil || imported.Snapshot.Session.ID == "" || imported.Snapshot.ContextSet.Enabled || imported.Snapshot.Session.Version != 1 {
			t.Fatalf("import snapshot %s", out)
		}
		currentService, currentPrincipal, _, resolveErr := api.ResolveSessionContext(context.Background(), "local", "terminal-context")
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		importedMessages, listErr := currentService.ListMessages(context.Background(), currentPrincipal, imported.Snapshot.Session.ID, "", 100)
		if listErr != nil || len(importedMessages.Items) != 2 || importedMessages.Items[0].Provenance != "legacy-import" || importedMessages.Items[0].HistoricalAt.IsZero() {
			t.Fatalf("legacy history %+v %v", importedMessages, listErr)
		}
		out, _, importErr = execute("", "import", "--legacy-chat-store", legacyPath, "--legacy-session", legacySession.ID, "--op", "new-import-op")
		var replay struct {
			Snapshot sessioncontext.Snapshot `json:"snapshot"`
		}
		if importErr != nil || json.Unmarshal([]byte(out), &replay) != nil || replay.Snapshot.Session.ID != imported.Snapshot.Session.ID || replay.Snapshot.Session.Version != 1 {
			t.Fatalf("source replay %s %v", out, importErr)
		}
		after, readErr := os.ReadFile(legacyPath)
		if readErr != nil || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("import mutated legacy source")
		}
		payload, readErr := readLegacyTerminalChat(context.Background(), legacyPath, legacySession.ID, sessioncontext.Scope{EnvironmentID: caps.Environments[0].EnvironmentID, IdentityDomain: caps.Environments[0].IdentityDomain})
		if readErr != nil {
			t.Fatal(readErr)
		}
		payload.Messages[0].Text = "Changed source payload"
		encoded, _ := json.Marshal(payload)
		if _, _, importErr = execute(string(encoded), "import", "--op", "changed-import-op"); importErr == nil {
			t.Fatal("changed import silently replaced legacy history")
		}
		if providerCalls != 1 {
			t.Fatal("legacy import dispatched provider")
		}
	})
	if _, _, err = execute("", "stop", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = execute("", "show", "s1"); err != nil || providerCalls != 1 {
		t.Fatal("stopped reopen dispatched work")
	}
}

func TestContextSessionFixtureMismatchAndDomainAmbiguity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untrusted.sqlite")
	if err := os.WriteFile(path, []byte("untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSessionInvoiceDashboard(context.Background(), path, "1"); err == nil {
		t.Fatal("unverified sample fixture executed")
	}
	cmd := sessionCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cli := sessionCLI{caps: api.SessionContextCapabilities{Environments: []api.SessionContextEnvironment{{EnvironmentID: "prod", IdentityDomain: "one"}, {EnvironmentID: "prod", IdentityDomain: "two"}}}}
	if err := cmd.ParseFlags([]string{"--environment", "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.scope(cmd); err == nil || !strings.Contains(err.Error(), "multiple identity domains") {
		t.Fatalf("ambiguous identity domain result %v", err)
	}
}
