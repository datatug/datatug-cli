package commands

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/sshdemo"
)

func TestDemoSSHServerCommand_Flags(t *testing.T) {
	origRun := sshDemoRunServer
	origPrint := sshDemoPrintHostKey
	t.Cleanup(func() {
		sshDemoRunServer = origRun
		sshDemoPrintHostKey = origPrint
	})

	var gotCfg sshdemo.Config
	sshDemoRunServer = func(_ context.Context, cfg sshdemo.Config) error {
		gotCfg = cfg
		return nil
	}

	cmd := demoSSHServerCommand()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{
		"--listen", "127.0.0.1:2299",
		"--proxy-protocol",
		"--host-key", "/tmp/hk",
		"--state-dir", "/tmp/state",
		"--sessions-dir", "/tmp/sess",
		"--repo", "https://example.com/repo.git",
		"--ref", "main",
		"--project-dir", "proj",
		"--max-sessions", "8",
		"--max-sessions-per-ip", "2",
		"--idle-timeout", "5m",
		"--max-session-duration", "30m",
		"--handshake-timeout", "10s",
		"--isolation", "process",
		"--uid-base", "30000",
		"--allow-insecure-isolation",
		"--github-client-id", "cid-1",
		"--save-branch", "custom-demo",
		"--egress-proxy", "http://edge:3128",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotCfg.Listen != "127.0.0.1:2299" || !gotCfg.ProxyProtocol || gotCfg.HostKey != "/tmp/hk" ||
		gotCfg.StateDir != "/tmp/state" || gotCfg.SessionsDir != "/tmp/sess" ||
		gotCfg.Repo != "https://example.com/repo.git" || gotCfg.Ref != "main" ||
		gotCfg.ProjectDir != "proj" || gotCfg.MaxSessions != 8 || gotCfg.MaxSessionsPerIP != 2 ||
		gotCfg.IdleTimeout != 5*time.Minute || gotCfg.MaxSessionDuration != 30*time.Minute ||
		gotCfg.HandshakeTimeout != 10*time.Second || gotCfg.Isolation != "process" ||
		gotCfg.UIDBase != 30000 || !gotCfg.AllowInsecureIsolation ||
		gotCfg.GitHubClientID != "cid-1" || gotCfg.SaveBranch != "custom-demo" ||
		gotCfg.EgressProxy != "http://edge:3128" {
		t.Fatalf("unexpected parsed config: %+v", gotCfg)
	}
}

func TestDemoSSHServerCommand_PrintHostKey(t *testing.T) {
	origPrint := sshDemoPrintHostKey
	t.Cleanup(func() { sshDemoPrintHostKey = origPrint })

	var gotPath string
	sshDemoPrintHostKey = func(path string, w io.Writer) error {
		gotPath = path
		_, err := io.WriteString(w, "ssh-ed25519 AAAA\nSHA256:test\n")
		return err
	}

	var out bytes.Buffer
	cmd := demoSSHServerCommand()
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--state-dir", "/tmp/st", "--print-host-key"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/tmp/st/ssh_host_ed25519_key" {
		t.Fatalf("got path %q, want /tmp/st/ssh_host_ed25519_key", gotPath)
	}
	if out.String() != "ssh-ed25519 AAAA\nSHA256:test\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestDemoSSHHealthCommand_Flags(t *testing.T) {
	orig := sshDemoCheckHealth
	t.Cleanup(func() { sshDemoCheckHealth = orig })

	var gotAddr string
	var gotTimeout time.Duration
	sshDemoCheckHealth = func(_ context.Context, addr string, timeout time.Duration) error {
		gotAddr = addr
		gotTimeout = timeout
		return nil
	}

	cmd := demoSSHHealthCommand()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--addr", "127.0.0.1:2223", "--timeout", "2s"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAddr != "127.0.0.1:2223" || gotTimeout != 2*time.Second {
		t.Fatalf("got (%q, %s), want (127.0.0.1:2223, 2s)", gotAddr, gotTimeout)
	}
}

func TestDemoSSHHealthCommand_ProxyProtocol(t *testing.T) {
	origPlain, origProxy := sshDemoCheckHealth, sshDemoCheckHealthProxyLocal
	t.Cleanup(func() { sshDemoCheckHealth, sshDemoCheckHealthProxyLocal = origPlain, origProxy })

	sshDemoCheckHealth = func(context.Context, string, time.Duration) error {
		t.Fatal("plain probe used with --proxy-protocol")
		return nil
	}
	var gotAddr string
	sshDemoCheckHealthProxyLocal = func(_ context.Context, addr string, _ time.Duration) error {
		gotAddr = addr
		return nil
	}
	cmd := demoSSHHealthCommand()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--addr", "127.0.0.1:2222", "--proxy-protocol"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAddr != "127.0.0.1:2222" {
		t.Fatalf("addr = %q", gotAddr)
	}
}

func TestDemoSSHEdgeCommand_Flags(t *testing.T) {
	orig := sshDemoRunEdge
	t.Cleanup(func() { sshDemoRunEdge = orig })

	var gotCfg sshdemo.EdgeConfig
	sshDemoRunEdge = func(_ context.Context, cfg sshdemo.EdgeConfig) error {
		gotCfg = cfg
		return nil
	}

	cmd := demoSSHEdgeCommand()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{
		"--listen", ":2224",
		"--upstream", "demo:2225",
		"--proxy-listen", ":3129",
		"--allow", "github.com,api.github.com",
		"--max-conns", "64",
		"--max-conns-per-ip", "4",
		"--idle-timeout", "2m",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotCfg.Listen != ":2224" || gotCfg.Upstream != "demo:2225" || gotCfg.ProxyListen != ":3129" ||
		len(gotCfg.Allow) != 2 || gotCfg.Allow[0] != "github.com" || gotCfg.Allow[1] != "api.github.com" ||
		gotCfg.MaxConns != 64 || gotCfg.MaxConnsPerIP != 4 || gotCfg.IdleTimeout != 2*time.Minute {
		t.Fatalf("unexpected edge config: %+v", gotCfg)
	}
}
