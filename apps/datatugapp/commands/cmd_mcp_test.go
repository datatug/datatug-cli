package commands

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestMCPCommandRegisteredAndValidatesOrigin(t *testing.T) {
	root := DatatugCommand()
	cmd, _, err := root.Find([]string{"mcp"})
	if err != nil || cmd.Name() != "mcp" {
		t.Fatalf("mcp not registered: %v", err)
	}
	for _, args := range [][]string{{"--agent-url", "http://evil.test:8989"}, {"unexpected"}} {
		cmd := mcpCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestMCPCommandCleanEOFAndCancellation(t *testing.T) {
	for _, closer := range []bool{false, true} {
		cmd := mcpCommand()
		cmd.SetArgs([]string{})
		var input io.Reader = strings.NewReader("")
		if closer {
			input = io.NopCloser(input)
		}
		cmd.SetIn(input)
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if output.Len() != 0 {
			t.Fatalf("stdout was not protocol-only: %q", output.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := mcpCommand()
	cmd.SetArgs([]string{})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMCPNeverStartsDaemonForInheritedCaller(t *testing.T) {
	fake := &daemonFake{}
	useDaemonFake(t, fake)
	t.Setenv(ClientEnv, "codex-mod")
	for _, args := range [][]string{{"mcp"}, {"mcp", "--client", "claude-mod"}} {
		var stderr bytes.Buffer
		EnsureDaemonForCaller(DatatugCommand(), args, &stderr)
		if stderr.Len() != 0 || fake.spawned != 0 {
			t.Fatalf("MCP changed daemon lifecycle: %q spawned=%d", stderr.String(), fake.spawned)
		}
	}
}
