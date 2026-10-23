package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

type appsBrowserCapture struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *appsBrowserCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *appsBrowserCapture) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

const appsRowSentinel = "ROW_ONLY_CELL_a9246ecae531"
const appsBindingSentinel = "UI_ONLY_BINDING_827193af62bc"

// Optional during ordinary Go tests; the maintained npm test:browser entrypoint
// enables this explicitly and refuses missing binary/project/browser inputs.
// It owns every listener, child, private key and temporary fixture it creates.
func TestMCPAppsBrowserHost(t *testing.T) {
	if os.Getenv("DATATUG_MCP_APPS_BROWSER") != "1" {
		t.Skip("run npm run test:browser with an actual CLI binary")
	}
	binary := os.Getenv("DATATUG_MCP_TEST_BINARY")
	projectInput := os.Getenv(demoProjectDirEnvVar)
	assets := os.Getenv("DATATUG_MCP_APPS_ASSETS")
	node := os.Getenv("DATATUG_MCP_APPS_NODE")
	for name, path := range map[string]string{"binary": binary, "project": projectInput, "assets": assets, "node": node} {
		if !filepath.IsAbs(path) {
			t.Fatalf("acceptance requires an absolute existing %s path", name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("acceptance missing %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(projectInput, "datatug-project.json")); err != nil {
		t.Fatal("acceptance requires the real demo project")
	}
	project := copyDemoProjectToTempDir(t, projectInput)
	installLocalChinookFixture(t, project)
	// Test setup writes only the owned database copy, before the read-only agent starts.
	// The private sentinel is data, never a literal in model-readable saved SQL.
	fixtureDB, err := sql.Open("sqlite", filepath.Join(project, "fixtures", "chinook-local.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixtureDB.Exec("UPDATE Customer SET Company = ? WHERE CustomerId = 1", appsRowSentinel); err != nil {
		_ = fixtureDB.Close()
		t.Fatal(err)
	}
	if err = fixtureDB.Close(); err != nil {
		t.Fatal(err)
	}
	privateQuery := filepath.Join(project, "queries", "acceptance")
	if err := os.MkdirAll(privateQuery, 0700); err != nil {
		t.Fatal(err)
	}
	definition := `{"id":"private-values","title":"Private parameter acceptance","type":"SQL","createdAt":"2026-10-23T00:00:00Z","targets":[{"catalog":"chinook-local"}],"parameters":[{"id":"CustomerId","type":"integer","isRequired":true},{"id":"UiNote","type":"string","isRequired":true},{"id":"OptionalFactor","type":"integer","isRequired":false,"defaultValue":5}]}`
	if err := os.WriteFile(filepath.Join(privateQuery, "private-values.query.json"), []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	querySQL := "SELECT :UiNote AS UiNote, Company AS PrivateCell, :OptionalFactor AS DefaultedValue, (SELECT COUNT(*) FROM Invoice WHERE CustomerId = :CustomerId) AS InvoiceCount FROM Customer WHERE CustomerId = :CustomerId"
	if err := os.WriteFile(filepath.Join(privateQuery, "private-values.query.sql"), []byte(querySQL), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(agentaccess.DirEnv, t.TempDir())
	secret, err := agentaccess.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	access := agentaccess.New(secret)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", Roles: []string{"admin"}, PoliciesDir: filepath.Join(project, "policies")})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	priorListen := listenAndServeFn
	listenAndServeFn = func(server *http.Server) error { return server.Serve(listener) }
	t.Cleanup(func() { listenAndServeFn = priorListen })
	agent := NewHttpServer()
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- agent.ServeHTTP(map[string]string{demoProjectID: project}, "127.0.0.1", port, session, api.Capabilities{AllowOpaqueSQL: true, Access: access})
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = agent.Shutdown(ctx)
		_ = listener.Close()
		select {
		case <-agentDone:
		case <-ctx.Done():
			t.Error("owned agent did not stop")
		}
	})
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitForServer(t, base+"/datatug/ping")
	if err := agentaccess.WriteKey(agentaccess.StateDir(), port, secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agentaccess.RemoveKey(agentaccess.StateDir(), port) })
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	cmd := mcpChildCommand(t, ctx, base)
	// The absolute CLI is executed with a PATH containing no Node or other tools.
	// The test host/browser driver retains its own independent build-host runtime.
	childPath := t.TempDir()
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		if strings.HasPrefix(cmd.Env[i], "PATH=") {
			cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+childPath)
	var stderr appsBrowserCapture
	cmd.Stderr = &stderr
	capabilities := &mcp.ClientCapabilities{Extensions: map[string]any{"io.modelcontextprotocol/ui": map[string]any{"mimeTypes": []string{"text/html;profile=mcp-app"}}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "datatug-official-browser-acceptance", Version: "1"}, &mcp.ClientOptions{Capabilities: capabilities}).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal("actual CLI Apps session initialization failed")
	}
	t.Cleanup(func() { _ = cs.Close() })
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	appsTools := 0
	for _, tool := range tools.Tools {
		if tool.Name == "datatug_apps_result" || tool.Name == "datatug_apps_rerun" {
			appsTools++
		}
	}
	if appsTools != 2 {
		t.Fatal("Apps discovery missing continuation tools")
	}

	embedded, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "ui://datatug/query-explorer"})
	if err != nil || len(embedded.Contents) != 1 || embedded.Contents[0].MIMEType != "text/html;profile=mcp-app" {
		t.Fatal("actual CLI embedded resource unavailable")
	}
	assetSum := sha256.Sum256([]byte(embedded.Contents[0].Text))
	actualAssetHash := hex.EncodeToString(assetSum[:])
	manifestBytes, err := os.ReadFile(filepath.Join("..", "mcpserver", "app", "build-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var appManifest struct {
		HTMLSHA256 string `json:"htmlSHA256"`
	}
	if json.Unmarshal(manifestBytes, &appManifest) != nil || appManifest.HTMLSHA256 != actualAssetHash {
		t.Fatal("supplied CLI embedded asset differs from this reviewed acceptance checkout")
	}
	versionCmd := exec.CommandContext(ctx, binary, "version", "--json")
	versionCmd.Env = cmd.Env
	versionBytes, err := versionCmd.Output()
	if err != nil {
		t.Fatal("supplied CLI version metadata unavailable")
	}
	var binaryVersion map[string]any
	if json.Unmarshal(versionBytes, &binaryVersion) != nil {
		t.Fatal("supplied CLI version metadata invalid")
	}

	var mu sync.Mutex
	var transcripts []byte
	var handles []string
	var active atomic.Int32
	var probe atomic.Int32
	var totalCalls atomic.Int32
	var cancelledCalls atomic.Int32
	var barrier chan struct{}
	var childClosed atomic.Bool
	random := make([]byte, 24)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(random)
	safeRecord := func(result *mcp.CallToolResult) {
		projection, _ := json.Marshal(struct {
			Content    []mcp.Content `json:"content"`
			Structured any           `json:"structuredContent"`
		}{result.Content, result.StructuredContent})
		mu.Lock()
		defer mu.Unlock()
		transcripts = append(transcripts, projection...)
		if p, ok := result.Meta["datatug"]; ok {
			raw, _ := json.Marshal(p)
			var payload struct {
				Handle string `json:"handle"`
			}
			_ = json.Unmarshal(raw, &payload)
			if payload.Handle != "" {
				handles = append(handles, payload.Handle)
			}
		}
	}
	discovery, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_get_query", Arguments: map[string]any{"project": demoProjectID, "queryId": "acceptance/private-values"}})
	if e != nil || discovery.IsError {
		t.Fatal("model-readable saved-query discovery failed")
	}
	safeRecord(discovery)

	var hostOrigin string
	sandboxBytes, err := os.ReadFile(filepath.Join(assets, "sandbox.html"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandbox.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'none'; img-src 'none'; font-src 'none'; media-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(sandboxBytes)
	}))
	t.Cleanup(sandbox.Close)
	fixtureRoot := filepath.Join("..", "mcpserver", "app", "test")
	hostScript, err := os.ReadFile(filepath.Join(assets, "host.js"))
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>DataTug official browser acceptance host</title><style>body{margin:0}#view{width:100%}</style></head><body><div id="view"></div><script type="module" src="/host.js"></script></body></html>`)
			return
		case "/host.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(hostScript)
			return
		case "/config":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "sandboxOrigin": sandbox.URL, "binaryVersion": binaryVersion, "actualAssetSHA256": actualAssetHash})
			return
		case "/probe":
			probe.Add(1)
			w.WriteHeader(204)
			return
		case "/rpc":
		default:
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" || r.Header.Get("X-Test-Token") != token || r.Header.Get("Origin") != hostOrigin || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "test broker denied", 403)
			return
		}
		var input struct {
			Op        string         `json:"op"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Mode      string         `json:"mode"`
			Action    string         `json:"action"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			http.Error(w, "invalid test request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		callCtx, callCancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer callCancel()
		switch input.Op {
		case "resource":
			resource, e := cs.ReadResource(callCtx, &mcp.ReadResourceParams{URI: "ui://datatug/query-explorer"})
			if e != nil {
				http.Error(w, "resource unavailable", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(resource)
		case "initial", "call":
			if input.Op == "initial" && (input.Mode == "typed" || input.Mode == "mixed" || input.Mode == "paged") {
				name := "typed-result.json"
				if input.Mode == "mixed" {
					name = "mixed-result.json"
				}
				data, e := os.ReadFile(filepath.Join(fixtureRoot, name))
				if e != nil {
					http.Error(w, "fixture unavailable", 500)
					return
				}
				if input.Mode == "paged" {
					var fixture map[string]any
					if json.Unmarshal(data, &fixture) != nil {
						http.Error(w, "fixture invalid", 500)
						return
					}
					payload := fixture["_meta"].(map[string]any)["datatug"].(map[string]any)
					result := payload["result"].(map[string]any)
					rs := result["recordset"].(map[string]any)
					template := rs["rows"].([]any)[0].([]any)
					rows := make([]any, 61)
					for i := range rows {
						row := append([]any(nil), template...)
						row[0] = map[string]any{"type": "integer", "value": strconv.Itoa(i + 1)}
						rows[i] = row
					}
					rs["rows"] = rows
					result["truncated"] = true
					data, _ = json.Marshal(fixture)
				}
				_, _ = w.Write(data)
				return
			}
			name := input.Name
			args := input.Arguments
			if input.Op == "initial" {
				query := "customers/customer-purchases-by-genre"
				parameters := map[string]string{"CustomerId": "1"}
				if input.Mode == "sentinel" {
					query = "acceptance/private-values"
					parameters["UiNote"] = "public-seed"
				} else if input.Mode != "real" {
					http.Error(w, "unknown scenario", 400)
					return
				}
				name = "datatug_run_query"
				args = map[string]any{"project": demoProjectID, "environment": demoProjectEnv, "source": demoProjectSource, "queryId": query, "parameters": parameters, "limit": 100}
			} else if name != "datatug_apps_result" && name != "datatug_apps_rerun" {
				http.Error(w, "tool not allowed", 403)
				return
			}
			totalCalls.Add(1)
			active.Add(1)
			defer active.Add(-1)
			mu.Lock()
			wait := barrier
			mu.Unlock()
			if wait != nil && input.Op == "call" {
				select {
				case <-wait:
				case <-callCtx.Done():
					if callCtx.Err() == context.Canceled {
						cancelledCalls.Add(1)
					}
					return
				}
			}
			result, e := cs.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: args})
			if e != nil {
				http.Error(w, "private call unavailable", 503)
				return
			}
			safeRecord(result)
			_ = json.NewEncoder(w).Encode(result)
		case "control":
			switch input.Action {
			case "revoke":
				api.ConfigureSecureSession(session, map[string]string{demoProjectID: project}, api.Capabilities{Access: access})
			case "restore":
				api.ConfigureSecureSession(session, map[string]string{demoProjectID: project}, api.Capabilities{AllowOpaqueSQL: true, Access: access})
			case "delay-on":
				mu.Lock()
				if barrier == nil {
					barrier = make(chan struct{})
				}
				mu.Unlock()
			case "delay-off":
				mu.Lock()
				if barrier != nil {
					close(barrier)
					barrier = nil
				}
				mu.Unlock()
			case "close-child":
				if e := cs.Close(); e != nil {
					http.Error(w, "child close failed", 500)
					return
				}
				childClosed.Store(cmd.ProcessState != nil && cmd.ProcessState.Success())
				request, _ := http.NewRequestWithContext(callCtx, http.MethodGet, base+"/datatug/ping", nil)
				request.Header.Set("Authorization", "Bearer "+agentaccess.SessionToken(secret))
				ping, e := (&http.Client{Timeout: 5 * time.Second}).Do(request)
				if ping != nil {
					_ = ping.Body.Close()
				}
				if e != nil || ping.StatusCode != 200 {
					http.Error(w, "separately owned agent stopped", 500)
					return
				}
			default:
				http.Error(w, "unknown test control", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "report":
			mu.Lock()
			stderrText := stderr.String()
			stderrLeak := strings.Contains(stderrText, appsRowSentinel) || strings.Contains(stderrText, appsBindingSentinel)
			privateLeak := bytes.Contains(transcripts, []byte(appsRowSentinel)) || bytes.Contains(transcripts, []byte(appsBindingSentinel))
			for _, handle := range handles {
				stderrLeak = stderrLeak || strings.Contains(stderrText, handle)
				if bytes.Contains(transcripts, []byte(handle)) {
					privateLeak = true
				}
			}
			count := len(handles)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"modelProjectionPrivateLeak": privateLeak, "capturedContinuations": count, "probeRequests": probe.Load(), "totalCalls": totalCalls.Load(), "cancelledCalls": cancelledCalls.Load(), "activeCalls": active.Load(), "childClosedCleanly": childClosed.Load(), "stderrPrivateLeak": stderrLeak})
		default:
			http.Error(w, "unknown test operation", 400)
		}
	})
	host := httptest.NewUnstartedServer(handler)
	host.Config.ReadTimeout = 10 * time.Second
	host.Config.WriteTimeout = 40 * time.Second
	host.Start()
	hostOrigin = host.URL
	t.Cleanup(host.Close)
	// Node is test-host tooling only. CLI child PATH above intentionally has no Node.
	driver := filepath.Join("..", "mcpserver", "app", "test", "browser-driver.mjs")
	browserCmd := exec.CommandContext(ctx, node, driver)
	browserCmd.Env = append(os.Environ(), "DATATUG_MCP_APPS_HOST="+host.URL, "DATATUG_MCP_APPS_BINARY="+binary)
	browserCmd.Stdout = os.Stdout
	browserCmd.Stderr = os.Stderr
	t.Log("owned ephemeral official-host/sandbox/agent listeners; cleanup by fixture Shutdown/Close, child stdin close and browser.close")
	if err := browserCmd.Run(); err != nil {
		t.Fatal("official browser acceptance failed (private payloads omitted)")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, sentinel := range append([]string{appsRowSentinel, appsBindingSentinel}, handles...) {
		if bytes.Contains(transcripts, []byte(sentinel)) || strings.Contains(stderr.String(), sentinel) {
			t.Fatal("private sentinel leaked into model projection or diagnostics")
		}
	}
	if !childClosed.Load() {
		t.Fatal("actual CLI child did not exit cleanly")
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binaryBytes)
	u, _ := url.Parse(host.URL)
	t.Logf("actual CLI SHA256 %s; official browser journey passed; ephemeral host %s cleaned by fixture", hex.EncodeToString(sum[:]), u.Host)
}
