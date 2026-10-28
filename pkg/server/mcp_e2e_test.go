package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/mcpserver"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The default child is this test executable, running only the real MCP entry
// point below. DATATUG_MCP_TEST_BINARY instead exercises a built/released CLI.
func TestMCPChildProcess(t *testing.T) {
	if os.Getenv("DATATUG_MCP_HELPER") != "1" {
		return
	}
	client, err := mcpserver.NewClient(os.Getenv("DATATUG_MCP_AGENT_URL"))
	if err == nil {
		defer client.Close()
		err = mcpserver.Run(context.Background(), client, "test", os.Stdin, os.Stdout)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func mcpChildCommand(t *testing.T, ctx context.Context, base string) *exec.Cmd {
	t.Helper()
	binary := os.Getenv("DATATUG_MCP_TEST_BINARY")
	var cmd *exec.Cmd
	if binary != "" {
		if !filepath.IsAbs(binary) {
			t.Fatal("DATATUG_MCP_TEST_BINARY must be an absolute path")
		}
		cmd = exec.CommandContext(ctx, binary, "mcp", "--agent-url", base)
	} else {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.CommandContext(ctx, self, "-test.run=^TestMCPChildProcess$")
	}
	cmd.Env = append(os.Environ(), "DATATUG_MCP_HELPER=1", "DATATUG_MCP_AGENT_URL="+base, "DATATUG_TELEMETRY=0", "DO_NOT_TRACK=1")
	return cmd
}

func TestMCPRealSavedSQLChildProcess(t *testing.T) {
	projectDir := copyDemoProjectToTempDir(t, resolveDemoProjectDir(t))
	installLocalChinookFixture(t, projectDir)
	dir := filepath.Join(t.TempDir(), "daemon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentaccess.DirEnv, dir)
	secret, err := agentaccess.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", Roles: []string{"admin"}, PoliciesDir: filepath.Join(projectDir, "policies")})
	if err != nil {
		t.Fatal(err)
	}
	base := startServeHTTPWithSessionAndCapabilities(t, map[string]string{demoProjectID: projectDir}, session, api.Capabilities{AllowOpaqueSQL: true, Access: agentaccess.New(secret)})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	if err := agentaccess.WriteKey(agentaccess.StateDir(), port, secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agentaccess.RemoveKey(agentaccess.StateDir(), port) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := mcpChildCommand(t, ctx, base)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "datatug-real-e2e", Version: "test"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v (stderr %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = cs.Close() })
	listed, err := cs.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 8 {
		t.Fatalf("tools/list: %+v %v", listed, err)
	}
	call := func(name string, args map[string]any) json.RawMessage {
		t.Helper()
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError {
			text, _ := json.Marshal(out)
			t.Fatalf("%s: %s %v", name, text, err)
		}
		body, err := json.Marshal(out.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Content) == 0 {
			t.Fatal("missing compatible text content")
		}
		return body
	}
	projects := call("datatug_list_projects", map[string]any{})
	if !bytes.Contains(projects, []byte(demoProjectID)) {
		t.Fatalf("missing real project: %s", projects)
	}
	description := call("datatug_describe_project", map[string]any{"project": demoProjectID})
	if !bytes.Contains(description, []byte(demoProjectSource)) {
		t.Fatalf("missing source: %s", description)
	}
	schema := call("datatug_describe_table", map[string]any{"project": demoProjectID, "environment": demoProjectEnv, "source": demoProjectSource, "schema": "", "table": "Invoice"})
	if !bytes.Contains(schema, []byte("InvoiceId")) || bytes.Contains(schema, []byte(`"rows"`)) {
		t.Fatalf("not a real schema: %s", schema)
	}
	queries := call("datatug_list_queries", map[string]any{"project": demoProjectID})
	if !bytes.Contains(queries, []byte("customers/customer-purchases-by-genre")) {
		t.Fatalf("saved SQL not discoverable: %s", queries)
	}
	call("datatug_get_query", map[string]any{"project": demoProjectID, "queryId": "customers/customer-purchases-by-genre"})
	counts := []string{}
	for _, id := range []string{"1", "2"} {
		body := call("datatug_run_query", map[string]any{"project": demoProjectID, "environment": demoProjectEnv, "source": demoProjectSource, "queryId": "customers/customer-purchases-by-genre", "parameters": map[string]string{"CustomerId": id}, "limit": 100})
		var result apicontract.Result
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if result.Provenance.ExecutionProfile != "opaque-privileged" || result.Provenance.Source != demoProjectSource {
			t.Fatalf("wrong execution: %+v", result.Provenance)
		}
		name, tracks := -1, -1
		for i, c := range result.Recordset.Columns {
			if c.Name == "GenreName" {
				name = i
			}
			if c.Name == "TracksPurchased" {
				tracks = i
			}
		}
		if name < 0 || tracks < 0 {
			t.Fatalf("missing SQL columns: %s", body)
		}
		for _, row := range result.Recordset.Rows {
			if typedValueKey(row[name]) == "Rock" {
				counts = append(counts, typedValueKey(row[tracks]))
			}
		}
	}
	if strings.Join(counts, ",") != "14,17" {
		t.Fatalf("real SQL parameter effect = %v, want [14 17]", counts)
	}
	// Mutate only this test's private saved definition, then use the same
	// real MCP child and authenticated HTTP session for attempted writes.
	queryPath := filepath.Join(projectDir, "queries", "customers", "customer-purchases-by-genre.query.sql")
	originalSQL, err := os.ReadFile(queryPath)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(projectDir, "fixtures", "chinook-local.sqlite")
	originalDB, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenCopy := filepath.Join(t.TempDir(), "forbidden.sqlite")
	for _, sql := range []string{
		"PRAGMA query_only=OFF; DELETE FROM Invoice; SELECT 1",
		"ATTACH DATABASE '" + strings.ReplaceAll(databasePath, "'", "''") + "' AS other; DELETE FROM other.Invoice; SELECT 1",
		"VACUUM INTO '" + strings.ReplaceAll(forbiddenCopy, "'", "''") + "'",
	} {
		if err := os.WriteFile(queryPath, []byte(sql), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_run_query", Arguments: map[string]any{"project": demoProjectID, "environment": demoProjectEnv, "source": demoProjectSource, "queryId": "customers/customer-purchases-by-genre", "parameters": map[string]string{"CustomerId": "1"}}})
		if err != nil || !out.IsError {
			t.Fatalf("MCP accepted saved mutation: %v %v", out, err)
		}
	}
	if err := os.WriteFile(queryPath, originalSQL, 0o600); err != nil {
		t.Fatal(err)
	}
	afterDB, err := os.ReadFile(databasePath)
	if err != nil || !bytes.Equal(originalDB, afterDB) {
		t.Fatal("saved SQL mutation changed SQLite fixture")
	}
	if _, err := os.Stat(forbiddenCopy); !os.IsNotExist(err) {
		t.Fatalf("VACUUM created a file: %v", err)
	}
	api.ConfigureSecureSession(session, map[string]string{demoProjectID: projectDir}, api.Capabilities{})
	denied, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_run_query", Arguments: map[string]any{"project": demoProjectID, "environment": demoProjectEnv, "source": demoProjectSource, "queryId": "customers/customer-purchases-by-genre", "parameters": map[string]string{"CustomerId": "1"}}})
	deniedBody, _ := json.Marshal(denied)
	if err != nil || !denied.IsError || !bytes.Contains(deniedBody, []byte("UNSUPPORTED_PROTECTED_EXECUTION")) {
		t.Fatalf("SQL grant was bypassed: %s %v", deniedBody, err)
	}
	agentaccess.RemoveKey(agentaccess.StateDir(), port)
	unauth, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_list_projects", Arguments: map[string]any{}})
	unauthBody, _ := json.Marshal(unauth)
	if err != nil || !unauth.IsError || !bytes.Contains(unauthBody, []byte("AUTHENTICATION_REQUIRED")) {
		t.Fatalf("missing auth accepted: %s %v", unauthBody, err)
	}
	if err := agentaccess.WriteKey(agentaccess.StateDir(), port, secret); err != nil {
		t.Fatal(err)
	}
	if err := cs.Close(); err != nil {
		t.Fatalf("clean shutdown: %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("child did not exit cleanly: %v (%s)", cmd.ProcessState, stderr.String())
	}
	// A supported older client initializes explicitly. Inspect every stdout
	// line, including process shutdown, for protocol-only JSON.
	legacy := mcpChildCommand(t, ctx, base)
	stdin, err := legacy.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := legacy.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if legacy.ProcessState == nil {
			_ = legacy.Process.Kill()
			_ = legacy.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	lines := 0
	readFrame := func() {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("missing legacy response: %v", scanner.Err())
		}
		var frame struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.JSONRPC != "2.0" {
			t.Fatalf("non-protocol stdout: %s", scanner.Bytes())
		}
		lines++
	}
	_, _ = io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"legacy-test\",\"version\":\"1\"}}}\n")
	readFrame()
	_, _ = io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")
	readFrame()
	_ = stdin.Close()
	for scanner.Scan() {
		if !json.Valid(scanner.Bytes()) {
			t.Fatalf("non-JSON shutdown output: %s", scanner.Bytes())
		}
		lines++
	}
	if err := scanner.Err(); err != nil || lines < 2 {
		t.Fatalf("legacy frames=%d err=%v", lines, err)
	}
	if err := legacy.Wait(); err != nil {
		t.Fatalf("legacy shutdown: %v", err)
	}
	t.Log("SDK child-process MCP→authenticated agent→real project→SQLite saved SQL: CustomerId 1 Rock=14, CustomerId 2 Rock=17; legacy stdout is JSON-only; child exited cleanly")
}
