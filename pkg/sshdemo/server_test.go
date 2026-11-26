//go:build linux

package sshdemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

type helperReport struct {
	Args    []string `json:"args"`
	Env     []string `json:"env"`
	Cwd     string   `json:"cwd"`
	PID     int      `json:"pid"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	Session string   `json:"session"`
}

// TestHelperProcess is invoked by tests via os.Args[0] with
// `-test.run=^TestHelperProcess$ -- demo ssh-session ...` so the real PTY
// child-spawner and cleanup paths execute with a 100% clean environment.
func TestHelperProcess(t *testing.T) {
	idx := -1
	for i, arg := range os.Args {
		if arg == "--" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+2 >= len(os.Args) || os.Args[idx+1] != "demo" || os.Args[idx+2] != "ssh-session" {
		return
	}
	args := os.Args[idx+1:]
	flagVal := func(name string) string {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}

	// Cleanup mode: `demo ssh-session --cleanup <dir>`
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--cleanup" {
			dir := args[i+1]
			if group := flagVal("--slot-cgroup"); group != "" {
				data, err := json.Marshal(helperReport{Args: args, Env: os.Environ()})
				if err != nil || os.WriteFile(filepath.Join(group, "cleanup-seen.json"), data, 0o600) != nil {
					os.Exit(1)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "fail-cleanup")); err == nil {
				os.Exit(1)
			}
			if _, err := os.Stat(filepath.Join(dir, "cleanup-invariant")); err == nil {
				_, _ = io.WriteString(os.Stdout, CleanupInvariant)
				os.Exit(1)
			}
			if err := os.Remove(filepath.Join(dir, "cleanup-timeout")); err == nil {
				_, _ = io.WriteString(os.Stdout, CleanupProgress)
				time.Sleep(time.Minute)
			}
			if err := os.Remove(filepath.Join(dir, "cleanup-oom")); err == nil {
				_, _ = io.WriteString(os.Stdout, CleanupProgress)
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				os.Exit(2)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				os.Exit(1)
			}
			for _, e := range entries {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
			os.Exit(0)
		}
	}

	// Normal session child mode.
	workspace := flagVal("--workspace")
	saveBranch := flagVal("--save-branch")
	sessionID := flagVal("--session-id")
	slotDir := flagVal("--slot-cgroup")
	if slotDir != "" {
		// The fixture emulates the trusted child. It runs without root or a
		// writable cgroup mount, so it records its PID instead of migrating.
		if err := os.Chdir(workspace); err != nil {
			t.Fatal(err)
		}
	}

	ws, _ := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	cwd, _ := os.Getwd()
	rep := helperReport{
		Args:    args,
		Env:     os.Environ(),
		Cwd:     cwd,
		PID:     os.Getpid(),
		Cols:    int(ws.Col),
		Rows:    int(ws.Row),
		Session: sessionID,
	}
	repJSON, _ := json.Marshal(rep)
	if workspace != "" {
		_ = os.WriteFile(filepath.Join(workspace, "seen.json"), repJSON, 0o600)
	}
	if slotDir != "" {
		if saveBranch == "slot-entry-timeout" {
			signal.Ignore(syscall.SIGHUP)
			fmt.Printf("PID:%d\n", rep.PID)
			time.Sleep(30 * time.Second)
			os.Exit(2)
		}
		if saveBranch == "slot-entry-error" {
			os.Exit(7)
		}
		pid := strconv.Itoa(rep.PID)
		if saveBranch == "slot-entry-wrong-pid" {
			pid += "0"
		}
		writeFixtureFile(t, slotDir, "cgroup.procs", pid+"\n")
		ready := os.NewFile(SlotReadyFD, "slot-ready")
		_, err := ready.WriteString("1")
		if err := errors.Join(err, ready.Close()); err != nil {
			t.Fatal(err)
		}
	}

	switch saveBranch {
	case "exit-now":
		fmt.Printf("SEEN:%s\n", repJSON)
		os.Exit(0)

	case "crash":
		fmt.Printf("CRASHING:%s\n", sessionID)
		os.Exit(42)

	case "wait-winch":
		sigCh := make(chan os.Signal, 4)
		signal.Notify(sigCh, syscall.SIGWINCH)
		fmt.Printf("READY:%dx%d\n", ws.Col, ws.Row)
		select {
		case <-sigCh:
			ws2, _ := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
			fmt.Printf("RESIZED:%dx%d\n", ws2.Col, ws2.Row)
			os.Exit(0)
		case <-time.After(5 * time.Second):
			fmt.Println("WINCH_TIMEOUT")
			os.Exit(2)
		}

	case "noisy-idle":
		fmt.Printf("READY:%s\n", sessionID)
		// Drain stdin in background so input tests work too.
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
		}()
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for range tick.C {
			fmt.Println("tick")
		}

	case "quota-stream":
		chunk := bytes.Repeat([]byte("x"), 4096)
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(1)
			}
		}

	case "graceful-shutdown":
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		fmt.Printf("READY:%s\n", sessionID)
		<-sigCh
		fmt.Println("GOT_SIGTERM")
		os.Exit(0)

	default: // "wait-forever" or default
		fmt.Printf("READY:%s\n", sessionID)
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 && bytes.Contains(buf[:n], []byte("q")) {
				os.Exit(0)
			}
			if err != nil {
				time.Sleep(10 * time.Second)
			}
		}
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testServerHarness struct {
	srv         *Server
	addr        string
	sessionsDir string
	stateDir    string
	logs        *syncBuffer
	stop        func()
}

func startTestServer(t *testing.T, mutate func(*Config), opts ...ServerOption) *testServerHarness {
	t.Helper()
	return startTestServerWithPeer(t, nil, mutate, opts...)
}

func startTestServerWithPeer(t *testing.T, peer net.IP, mutate func(*Config), opts ...ServerOption) *testServerHarness {
	t.Helper()
	stateDir := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	logs := &syncBuffer{}

	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.StateDir = stateDir
	cfg.SessionsDir = sessionsDir
	cfg.Isolation = IsolationProcess
	cfg.SkipTemplate = true
	cfg.Executable = os.Args[0]
	cfg.ExecPrefixArgs = []string{"-test.run=^TestHelperProcess$", "--"}
	cfg.SaveBranch = "exit-now"
	cfg.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	cfg.ShutdownGrace = 500 * time.Millisecond
	cfg.CleanupTimeout = 5 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}

	srv, err := NewServer(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var ln net.Listener
	ln, err = net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	if peer != nil {
		ln = peerListener{Listener: ln, ip: peer}
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(ln)
	}()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if err := <-serveDone; err != nil {
			t.Logf("serve: %v", err)
		}
	}
	t.Cleanup(stop)

	return &testServerHarness{
		srv:         srv,
		addr:        ln.Addr().String(),
		sessionsDir: sessionsDir,
		stateDir:    stateDir,
		logs:        logs,
		stop:        stop,
	}
}

func dialSSH(t *testing.T, addr, user string) *gossh.Client {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User:            user,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := gossh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("gossh.Dial(%s): %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func waitNoSessionDirs(t *testing.T, sessionsDir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(sessionsDir)
		if err == nil && len(entries) == 0 {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	entries, _ := os.ReadDir(sessionsDir)
	t.Fatalf("expected 0 session dirs in %s, still have %d", sessionsDir, len(entries))
}

func waitLogContains(t *testing.T, logs *syncBuffer, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := logs.String()
		if strings.Contains(s, substr) {
			return s
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("logs did not contain %q within %s:\n%s", substr, timeout, logs.String())
	return ""
}

func TestServer_PTYContractEnvTermClampingAndCanary(t *testing.T) {
	// Set a canary secret in supervisor env; it must NEVER reach the child.
	t.Setenv("SSHDEMO_CANARY_SECRET", "do-not-leak-me")

	repoDir := t.TempDir()
	initTestGitRepo(t, repoDir, map[string][]byte{
		"demo-project-1/datatug-project.json": []byte(`{"id":"demo"}`),
	})

	h := startTestServer(t, func(c *Config) {
		c.SkipTemplate = false
		c.Repo = repoDir
		c.ProjectDir = "demo-project-1"
		c.GitHubClientID = "test-cid"
		c.SaveBranch = "exit-now"
		c.EgressProxy = "http://edge:3128"
	}, WithChinookBytes([]byte("mini-chinook")))

	// 1. Connect with oversized PTY (900x400 -> clamped to 500x200), default COLORTERM inferred from xterm-256color.
	client := dialSSH(t, h.addr, "alice")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Disallowed env var must be refused.
	if err := sess.Setenv("EVIL_VAR", "1"); err == nil {
		t.Fatal("expected disallowed env EVIL_VAR to be refused")
	}
	// Allowed env var LANG is accepted.
	if err := sess.Setenv("LANG", "en_US.UTF-8"); err != nil {
		t.Fatalf("expected LANG env to be accepted: %v", err)
	}

	if err := sess.RequestPty("xterm-256color", 400, 900, gossh.TerminalModes{}); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	outBytes, _ := io.ReadAll(stdout)
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v (output=%q)", err, string(outBytes))
	}

	// Parse SEEN JSON line.
	var rep helperReport
	found := false
	for _, line := range strings.Split(strings.ReplaceAll(string(outBytes), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "SEEN:") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "SEEN:")), &rep); err != nil {
				t.Fatalf("unmarshal report: %v (raw=%q)", err, line)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("did not find SEEN marker in output: %q", string(outBytes))
	}

	// Verify initial PTY size was clamped to 500x200.
	if rep.Cols != 500 || rep.Rows != 200 {
		t.Fatalf("initial PTY size = %dx%d, want clamped 500x200", rep.Cols, rep.Rows)
	}

	// Verify exact argv contract.
	wantArgPairs := map[string]string{
		"--template":         filepath.Join(h.stateDir, "template"),
		"--project-dir":      "demo-project-1",
		"--repo":             repoDir,
		"--github-client-id": "test-cid",
		"--save-branch":      "exit-now",
		"--rlimit-as":        "8589934592",
		"--rlimit-cpu":       "900",
		"--rlimit-fsize":     "67108864",
		"--rlimit-nofile":    "512",
		"--rlimit-nproc":     "256",
	}
	if len(rep.Args) < 2 || rep.Args[0] != "demo" || rep.Args[1] != "ssh-session" {
		t.Fatalf("unexpected argv prefix: %v", rep.Args)
	}
	gotFlags := map[string]string{}
	for i := 2; i+1 < len(rep.Args); i += 2 {
		gotFlags[rep.Args[i]] = rep.Args[i+1]
	}
	for k, want := range wantArgPairs {
		if gotFlags[k] != want {
			t.Fatalf("flag %s = %q, want %q", k, gotFlags[k], want)
		}
	}
	if len(gotFlags["--session-id"]) != 32 {
		t.Fatalf("session-id %q should be 32 hex chars", gotFlags["--session-id"])
	}
	if _, err := time.Parse(time.RFC3339, gotFlags["--deadline"]); err != nil {
		t.Fatalf("deadline %q is not RFC3339: %v", gotFlags["--deadline"], err)
	}
	if rep.Cwd != gotFlags["--workspace"] {
		t.Fatalf("cwd %q != workspace %q", rep.Cwd, gotFlags["--workspace"])
	}

	// Verify clean environment & canary absence.
	envMap := map[string]string{}
	for _, kv := range rep.Env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	if _, leaked := envMap["SSHDEMO_CANARY_SECRET"]; leaked {
		t.Fatal("supervisor canary env var leaked to child!")
	}
	if _, leaked := envMap["EVIL_VAR"]; leaked {
		t.Fatal("disallowed SSH env var leaked to child!")
	}
	proxyURL := envMap["HTTPS_PROXY"]
	proxy, err := url.Parse(proxyURL)
	if err != nil || proxy.Scheme != "http" || proxy.Hostname() != "127.0.0.1" ||
		proxy.Port() == "" || proxy.User.Username() != gotFlags["--session-id"] {
		t.Fatal("child proxy URL must identify the session and point to the loopback relay")
	}
	password, _ := proxy.User.Password()
	if token, err := base64.RawURLEncoding.DecodeString(password); err != nil || len(token) != 32 {
		t.Fatal("invalid child proxy token")
	}
	wantEnv := map[string]string{
		"HOME":              filepath.Join(gotFlags["--workspace"], "home"),
		"TMPDIR":            filepath.Join(gotFlags["--workspace"], "tmp"),
		"SQLITE_TMPDIR":     filepath.Join(gotFlags["--workspace"], "tmp"),
		"TERM":              "xterm-256color",
		"COLORTERM":         "truecolor",
		"LANG":              "C.UTF-8",
		"PATH":              "/usr/local/bin:/usr/bin:/bin",
		"DATATUG_TELEMETRY": "0",
		"DO_NOT_TRACK":      "1",
		"GOMEMLIMIT":        "256MiB",
		"HTTPS_PROXY":       proxyURL,
		"HTTP_PROXY":        proxyURL,
		"https_proxy":       proxyURL,
		"http_proxy":        proxyURL,
		"NO_PROXY":          "",
	}
	if len(envMap) != len(wantEnv) {
		t.Fatalf("child env count = %d (%v), want exact %d", len(envMap), rep.Env, len(wantEnv))
	}
	for k, want := range wantEnv {
		if got, ok := envMap[k]; !ok || got != want {
			t.Fatalf("child env %s = %q (present=%v), want %q", k, got, ok, want)
		}
	}

	// Workspace dir must be cleaned up and logs must record session start/end without leaking env/output.
	waitNoSessionDirs(t, h.sessionsDir, 3*time.Second)
	logStr := waitLogContains(t, h.logs, `"end_reason":"client-exit"`, 3*time.Second)
	if strings.Contains(logStr, "do-not-leak-me") || strings.Contains(logStr, "SEEN:") {
		t.Fatalf("logs leaked env or child output: %s", logStr)
	}

	// 2. Test TERM/COLORTERM variations: explicit COLORTERM + non-256color TERM + 0x0 clamped to 1x1.
	sess2, err := dialSSH(t, h.addr, "alice").NewSession()
	if err != nil {
		t.Fatalf("NewSession 2: %v", err)
	}
	defer func() { _ = sess2.Close() }()
	if err := sess2.Setenv("COLORTERM", "24bit"); err != nil {
		t.Fatalf("Setenv COLORTERM: %v", err)
	}
	if err := sess2.RequestPty("vt100", 0, 0, gossh.TerminalModes{}); err != nil {
		t.Fatalf("RequestPty 2: %v", err)
	}
	out2, err := sess2.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe 2: %v", err)
	}
	if err := sess2.Shell(); err != nil {
		t.Fatalf("Shell 2: %v", err)
	}
	b2, _ := io.ReadAll(out2)
	_ = sess2.Wait()
	var rep2 helperReport
	for _, line := range strings.Split(strings.ReplaceAll(string(b2), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "SEEN:") {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "SEEN:")), &rep2)
		}
	}
	if rep2.Cols != 1 || rep2.Rows != 1 {
		t.Fatalf("0x0 PTY should clamp to 1x1, got %dx%d", rep2.Cols, rep2.Rows)
	}
	env2 := map[string]string{}
	for _, kv := range rep2.Env {
		k, v, _ := strings.Cut(kv, "=")
		env2[k] = v
	}
	if env2["TERM"] != "vt100" || env2["COLORTERM"] != "24bit" {
		t.Fatalf("unexpected TERM/COLORTERM: %q / %q", env2["TERM"], env2["COLORTERM"])
	}
}

func TestServer_WindowChangeReachesChild(t *testing.T) {
	h := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-winch"
	})
	client := dialSSH(t, h.addr, "bob")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || !strings.Contains(sc.Text(), "READY:80x24") {
		t.Fatalf("expected READY:80x24, got %q (err=%v)", sc.Text(), sc.Err())
	}
	if err := sess.WindowChange(43, 132); err != nil {
		t.Fatalf("WindowChange: %v", err)
	}
	if !sc.Scan() || !strings.Contains(sc.Text(), "RESIZED:132x43") {
		t.Fatalf("expected RESIZED:132x43, got %q (err=%v)", sc.Text(), sc.Err())
	}
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	waitNoSessionDirs(t, h.sessionsDir, 3*time.Second)
}

func TestServer_RefusalsNoPTYExecSubsystemForwardingAgentX11(t *testing.T) {
	h := startTestServer(t, func(c *Config) { c.MaxSessionsPerIP = 4 })
	client := dialSSH(t, h.addr, "visitor")

	// 1. No-PTY session refused with one-line message and exit status 1.
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var out syncBuffer
	sess.Stdout = &out
	sess.Stderr = &out
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell without PTY: %v", err)
	}
	err = sess.Wait()
	var exitErr *gossh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 1 {
		t.Fatalf("expected exit status 1 for no-PTY, got %v", err)
	}
	if !strings.Contains(out.String(), "This demo needs an interactive terminal") {
		t.Fatalf("unexpected no-PTY message: %q\nlogs:\n%s", out.String(), h.logs.String())
	}
	_ = sess.Close()

	// 2. exec refused.
	execSess, err := dialSSH(t, h.addr, "visitor").NewSession()
	if err != nil {
		t.Fatalf("NewSession exec: %v", err)
	}
	if err := execSess.Run("id"); err == nil {
		t.Fatal("expected exec to be refused")
	}
	_ = execSess.Close()

	// 3. subsystem (sftp) refused.
	subSess, err := dialSSH(t, h.addr, "visitor").NewSession()
	if err != nil {
		t.Fatalf("NewSession subsystem: %v", err)
	}
	if err := subSess.RequestSubsystem("sftp"); err == nil {
		t.Fatal("expected sftp subsystem to be refused")
	}
	_ = subSess.Close()

	// 4. agent forwarding and X11 forwarding refused.
	ch, reqs, err := dialSSH(t, h.addr, "visitor").OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("OpenChannel session: %v", err)
	}
	go gossh.DiscardRequests(reqs)
	ok, err := ch.SendRequest("auth-agent-req@openssh.com", true, nil)
	if err != nil || ok {
		t.Fatalf("agent forwarding should be refused: ok=%v err=%v", ok, err)
	}
	ok, err = ch.SendRequest("x11-req", true, nil)
	if err != nil || ok {
		t.Fatalf("x11 forwarding should be refused: ok=%v err=%v", ok, err)
	}
	_ = ch.Close()

	// 5. direct-tcpip (local port forwarding) refused.
	if _, err := client.Dial("tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("expected direct-tcpip port forwarding to be refused")
	}

	// 6. tcpip-forward (remote port forwarding) refused.
	if _, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Fatal("expected tcpip-forward remote port forwarding to be refused")
	}
}

func TestServer_GlobalAndPerIPCaps(t *testing.T) {
	// Per-IP cap = 1, Global cap = 2.
	h := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
		c.MaxSessions = 2
		c.MaxSessionsPerIP = 1
	})

	c1 := dialSSH(t, h.addr, "u1")
	s1, err := c1.NewSession()
	if err != nil {
		t.Fatalf("s1: %v", err)
	}
	_ = s1.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	out1, _ := s1.StdoutPipe()
	if err := s1.Shell(); err != nil {
		t.Fatalf("s1.Shell: %v", err)
	}
	line1, _ := bufio.NewReader(out1).ReadString('\n')
	if !strings.Contains(line1, "READY:") {
		t.Fatalf("s1 not ready: %q", line1)
	}

	// Also test global cap = 1 (with per-IP = 2).
	h2 := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
		c.MaxSessions = 1
		c.MaxSessionsPerIP = 2
	})
	gc1 := dialSSH(t, h2.addr, "g1")
	gs1, _ := gc1.NewSession()
	_ = gs1.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	gout1, _ := gs1.StdoutPipe()
	_ = gs1.Shell()
	_, _ = bufio.NewReader(gout1).ReadString('\n')

	gc2 := dialSSH(t, h2.addr, "g2")
	gs2, err := gc2.NewSession()
	if err != nil {
		t.Fatalf("gs2: %v", err)
	}
	var gbuf bytes.Buffer
	gs2.Stdout = &gbuf
	_ = gs2.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	_ = gs2.Shell()
	if err := gs2.Wait(); err == nil || !strings.Contains(gbuf.String(), "The DataTug demo is full") {
		t.Fatalf("expected global cap rejection, err=%v out=%q", err, gbuf.String())
	}
	_ = gs2.Close()

	// Close first session -> slot freed -> new session succeeds.
	_ = s1.Close()
	_ = c1.Close()
	waitNoSessionDirs(t, h.sessionsDir, 3*time.Second)

	c3 := dialSSH(t, h.addr, "u3")
	s3, _ := c3.NewSession()
	_ = s3.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	out3, _ := s3.StdoutPipe()
	if err := s3.Shell(); err != nil {
		t.Fatalf("s3.Shell: %v", err)
	}
	line3, _ := bufio.NewReader(out3).ReadString('\n')
	if !strings.Contains(line3, "READY:") {
		t.Fatalf("expected new session after slot release, got %q", line3)
	}
	_ = s3.Close()
	_ = c3.Close()
	waitNoSessionDirs(t, h.sessionsDir, 3*time.Second)
}

func TestServer_TimeoutsHandshakeIdleAndMaxDuration(t *testing.T) {
	// 1. Handshake timeout: raw TCP client stalls without sending SSH handshake.
	hHandshake := startTestServer(t, func(c *Config) {
		c.HandshakeTimeout = 150 * time.Millisecond
	})
	rawConn, err := net.Dial("tcp", hHandshake.addr)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	defer func() { _ = rawConn.Close() }()
	// Read server banner then stall; server must close the socket after handshake timeout.
	_ = rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadAll(rawConn)
	if err != nil {
		t.Fatalf("expected server to close stalled handshake conn cleanly (EOF), got %v", err)
	}

	// 2. Idle timeout (even when child keeps writing output!).
	hIdle := startTestServer(t, func(c *Config) {
		c.SaveBranch = "noisy-idle"
		c.IdleTimeout = 200 * time.Millisecond
		c.MaxSessionDuration = 5 * time.Second
	})
	ic := dialSSH(t, hIdle.addr, "idler")
	is, _ := ic.NewSession()
	_ = is.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	inPipe, _ := is.StdinPipe()
	outPipe, _ := is.StdoutPipe()
	if err := is.Shell(); err != nil {
		t.Fatalf("idle shell: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, outPipe) }()
	// Send one byte of input at 100ms to prove input resets idle timer, then go silent.
	time.Sleep(100 * time.Millisecond)
	_, _ = inPipe.Write([]byte("a"))
	_ = is.Wait()
	waitNoSessionDirs(t, hIdle.sessionsDir, 3*time.Second)
	waitLogContains(t, hIdle.logs, `"end_reason":"idle"`, 3*time.Second)

	// 3. Max session duration.
	hMax := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
		c.IdleTimeout = 5 * time.Second
		c.MaxSessionDuration = 200 * time.Millisecond
	})
	mc := dialSSH(t, hMax.addr, "longrun")
	ms, _ := mc.NewSession()
	_ = ms.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	mout, _ := ms.StdoutPipe()
	if err := ms.Shell(); err != nil {
		t.Fatalf("max-dur shell: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, mout) }()
	_ = ms.Wait()
	waitNoSessionDirs(t, hMax.sessionsDir, 3*time.Second)
	waitLogContains(t, hMax.logs, `"end_reason":"max-duration"`, 3*time.Second)
}

func TestServer_LifecycleClientDropCrashPanicShutdownAndStaleCleanup(t *testing.T) {
	// 1. Client drop mid-session -> child killed, workspace removed.
	hDrop := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
	})
	rawTCP, err := net.Dial("tcp", hDrop.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(rawTCP, hDrop.addr, &gossh.ClientConfig{
		User:            "dropper",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("NewClientConn: %v", err)
	}
	dropClient := gossh.NewClient(sshConn, chans, reqs)
	ds, _ := dropClient.NewSession()
	_ = ds.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	dout, _ := ds.StdoutPipe()
	_ = ds.Shell()
	_, _ = bufio.NewReader(dout).ReadString('\n')
	// Abrupt TCP drop!
	_ = rawTCP.Close()
	waitNoSessionDirs(t, hDrop.sessionsDir, 3*time.Second)
	waitLogContains(t, hDrop.logs, `"end_reason":"client-drop"`, 3*time.Second)

	// 2. Child crash (exit 42) -> workspace removed, logged as child-crash.
	hCrash := startTestServer(t, func(c *Config) {
		c.SaveBranch = "crash"
	})
	cc := dialSSH(t, hCrash.addr, "crasher")
	cs, _ := cc.NewSession()
	_ = cs.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	cout, _ := cs.StdoutPipe()
	_ = cs.Shell()
	_, _ = io.ReadAll(cout)
	err = cs.Wait()
	var exitErr *gossh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 42 {
		t.Fatalf("expected child exit 42, got %v", err)
	}
	waitNoSessionDirs(t, hCrash.sessionsDir, 3*time.Second)
	waitLogContains(t, hCrash.logs, `"end_reason":"child-crash"`, 3*time.Second)

	// 3. Handler panic recovery -> cleanup runs, slot released.
	hPanic := startTestServer(t, nil, WithPanicHookForTest(func() {
		panic("simulated handler panic")
	}))
	pc := dialSSH(t, hPanic.addr, "panicker")
	ps, _ := pc.NewSession()
	_ = ps.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	_ = ps.Shell()
	_ = ps.Wait()
	waitNoSessionDirs(t, hPanic.sessionsDir, 3*time.Second)
	if hPanic.srv.slots.ActiveCount() != 0 {
		t.Fatalf("slot leaked after panic: active=%d", hPanic.srv.slots.ActiveCount())
	}

	// 4. Shutdown with live sessions (both cooperative SIGTERM and forced SIGKILL) -> all cleaned.
	hShut := startTestServer(t, func(c *Config) {
		c.SaveBranch = "graceful-shutdown"
		c.ShutdownGrace = 2 * time.Second
	})
	sc1 := dialSSH(t, hShut.addr, "shutter")
	ss1, _ := sc1.NewSession()
	_ = ss1.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	sout1, _ := ss1.StdoutPipe()
	_ = ss1.Shell()
	_, _ = bufio.NewReader(sout1).ReadString('\n')

	hShut.stop()
	waitNoSessionDirs(t, hShut.sessionsDir, 3*time.Second)
	waitLogContains(t, hShut.logs, `"end_reason":"shutdown"`, 3*time.Second)

	// 5. Stale-dir cleanup at startup + Host key persistence across two server starts.
	sharedState := t.TempDir()
	sharedSessions := filepath.Join(t.TempDir(), "sessions")
	staleDir := filepath.Join(sharedSessions, "deadbeef01234567")
	if err := os.MkdirAll(filepath.Join(staleDir, "home"), 0o700); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "home", "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	startTwo := func() string {
		cfg := DefaultConfig()
		cfg.Listen = "127.0.0.1:0"
		cfg.StateDir = sharedState
		cfg.SessionsDir = sharedSessions
		cfg.Isolation = IsolationProcess
		cfg.SkipTemplate = true
		cfg.Executable = os.Args[0]
		cfg.ExecPrefixArgs = []string{"-test.run=^TestHelperProcess$", "--"}
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
		srv, err := NewServer(context.Background(), cfg)
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		return gossh.FingerprintSHA256(srv.HostSigner().PublicKey())
	}

	fpA := startTwo()
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir should be cleaned on startup: %v", err)
	}
	fpB := startTwo()
	if fpA != fpB {
		t.Fatalf("host key fingerprint changed across server restarts: %s != %s", fpA, fpB)
	}

	// 6. RunServer cancels cleanly on context cancel.
	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		cfg := DefaultConfig()
		cfg.Listen = "127.0.0.1:0"
		cfg.StateDir = t.TempDir()
		cfg.SessionsDir = filepath.Join(t.TempDir(), "sessions")
		cfg.Isolation = IsolationProcess
		cfg.SkipTemplate = true
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
		runDone <- RunServer(runCtx, cfg)
	}()
	time.Sleep(80 * time.Millisecond)
	runCancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunServer returned error on clean cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunServer did not exit after cancel")
	}
}

func TestClampPayloadsAndHelpers(t *testing.T) {
	if _, ok := clampPtyReqPayload([]byte{0, 0}); ok {
		t.Fatal("short pty-req should fail")
	}
	if _, ok := clampPtyReqPayload([]byte{0, 0, 0, 50, 1, 2}); ok {
		t.Fatal("truncated pty-req should fail")
	}
	if _, ok := clampWindowPayload([]byte{1, 2, 3}); ok {
		t.Fatal("short window-change should fail")
	}
	if got := clientIPFromAddr(nil); got != "unknown" {
		t.Fatalf("clientIPFromAddr(nil) = %q", got)
	}
	if got := sanitizeEnvValue("bad\nval"); got != "" {
		t.Fatalf("sanitizeEnvValue should reject newlines, got %q", got)
	}
	if got := BuildChildEnv("/h", "", "", ""); !strings.Contains(strings.Join(got, ","), "TERM=xterm-256color") {
		t.Fatalf("expected default TERM=xterm-256color, got %v", got)
	}
	if got := BuildChildEnv("/h", "vt100", "", ""); strings.Contains(strings.Join(got, ","), "COLORTERM=") {
		t.Fatalf("vt100 should not infer COLORTERM, got %v", got)
	}
	if got := exitCodeFromWaitErr(errors.New("other")); got != -1 {
		t.Fatalf("exitCodeFromWaitErr = %d, want -1", got)
	}
	if got := safetyMargin(10 * time.Millisecond); got != 50*time.Millisecond {
		t.Fatalf("safetyMargin small = %s", got)
	}
	if got := safetyMargin(time.Hour); got != 5*time.Second {
		t.Fatalf("safetyMargin large = %s", got)
	}
	if got := clientIPFromAddr(&net.IPAddr{IP: net.ParseIP("10.1.2.3")}); got != "10.1.2.3" {
		t.Fatalf("clientIPFromAddr IPAddr = %q", got)
	}
	term, ct := extractClientTermAndColorTerm("", []string{"MALFORMED", "TERM=screen", "COLORTERM=24bit"})
	if term != "screen" || ct != "24bit" {
		t.Fatalf("extractClientTermAndColorTerm = (%q, %q)", term, ct)
	}
	drainPTYMaster(nil, time.Millisecond)
}

type errListener struct {
	err error
}

func (e errListener) Accept() (net.Conn, error) { return nil, e.err }
func (e errListener) Close() error              { return nil }
func (e errListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func TestServer_UIDModeFullSessionAndErrorPaths(t *testing.T) {
	// 1. Full session in IsolationUID via WithSysOps seams (without root).
	repoDir := t.TempDir()
	initTestGitRepo(t, repoDir, map[string][]byte{
		"demo-project-1/datatug-project.json": []byte(`{"id":"demo"}`),
	})
	sessDir := t.TempDir()
	_ = os.Chmod(sessDir, 0o711)

	var chowned []string
	ops := DefaultSysOps()
	ops.FileUID = func(os.FileInfo) (int, bool) { return 0, true }
	ops.FileGID = fakeSlotGID(21000)
	ops.StatSharedTemp = noSharedTemp
	ops.Chown = func(p string, uid, gid int) error {
		chowned = append(chowned, p)
		return nil
	}
	root, cgfs := fakeCgroupMount(t)
	ops.cgroups = cgfs
	ops.FilesystemCapacity = ampleSessionFilesystem
	h := startTestServer(t, func(c *Config) {
		c.Isolation = IsolationUID
		c.UIDBase = 21000
		c.CgroupRoot = root
		c.SessionsDir = sessDir
		c.SkipTemplate = false
		c.Repo = repoDir
		c.SaveBranch = "exit-now"
	}, WithSysOps(ops), WithChinookBytes([]byte("mini")))

	if h.srv.TemplateDir() != filepath.Join(h.stateDir, "template") {
		t.Fatalf("TemplateDir = %q", h.srv.TemplateDir())
	}

	client := dialSSH(t, h.addr, "uid-user")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	out, _ := sess.StdoutPipe()
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	b, _ := io.ReadAll(out)
	if err := sess.Wait(); err != nil || !strings.Contains(string(b), "SEEN:") {
		t.Fatalf("uid-mode session failed: err=%v out=%q", err, string(b))
	}
	_ = sess.Close()
	waitNoSessionDirs(t, filepath.Join(sessDir, "slot-0"), 3*time.Second)
	cleanupArgs, err := os.ReadFile(filepath.Join(root, "cleanup", "cleanup-seen.json"))
	cleanupGroup, _ := json.Marshal(filepath.Join(root, "cleanup"))
	if err != nil || !strings.Contains(string(cleanupArgs), `"--slot-cgroup",`+string(cleanupGroup)) ||
		!strings.Contains(string(cleanupArgs), `"--slot-uid","21000","--slot-gid","21000"`) ||
		!strings.Contains(string(cleanupArgs), `"GOMEMLIMIT=32MiB"`) {
		t.Fatalf("cleanup arguments and env = %s, %v", cleanupArgs, err)
	}
	for _, pair := range []string{
		`"--slot-cgroup","` + filepath.Join(root, "slot-0") + `"`,
		`"--slot-uid","21000","--slot-gid","21000"`,
	} {
		if !strings.Contains(string(b), pair) {
			t.Fatalf("expected child flags %s in %s", pair, b)
		}
	}
	if len(chowned) != h.srv.cfg.MaxSessions+4 {
		t.Fatalf("expected slot parent chowns and four workspace chowns, got %v", chowned)
	}

	// 2. Malformed pty-req and window-change payloads are refused.
	ch, reqs, err := dialSSH(t, h.addr, "uid-user").OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	go gossh.DiscardRequests(reqs)
	if ok, err := ch.SendRequest("pty-req", true, []byte{0, 1}); err != nil || ok {
		t.Fatalf("malformed pty-req should be refused: ok=%v err=%v", ok, err)
	}
	if ok, err := ch.SendRequest("window-change", true, []byte{0, 1}); err != nil || ok {
		t.Fatalf("malformed window-change should be refused: ok=%v err=%v", ok, err)
	}
	_ = ch.Close()

	// 3. Forced shutdown (child ignores SIGTERM) kills after ShutdownGrace and cleans up.
	hForce := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
		c.ShutdownGrace = 80 * time.Millisecond
	})
	fc := dialSSH(t, hForce.addr, "force")
	fs, _ := fc.NewSession()
	_ = fs.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	fout, _ := fs.StdoutPipe()
	_ = fs.Shell()
	_, _ = bufio.NewReader(fout).ReadString('\n')
	hForce.stop()
	waitNoSessionDirs(t, hForce.sessionsDir, 3*time.Second)

	// 4. A missing executable fails both the child start and the workspace cleanup.
	hBadExec := startTestServer(t, func(c *Config) {
		c.Executable = filepath.Join(t.TempDir(), "does-not-exist")
		c.ExecPrefixArgs = nil
	})
	bc := dialSSH(t, hBadExec.addr, "badexec")
	bs, _ := bc.NewSession()
	_ = bs.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{})
	_ = bs.Shell()
	_ = bs.Wait()
	// The cleanup executable was unavailable. The slot stays reserved
	// for cleanup and is quarantined after three runs without progress.
	waitLogContains(t, hBadExec.logs, "session workspace awaiting cleanup", 3*time.Second)
	for range 2 {
		hBadExec.srv.cleanupMu.Lock()
		hBadExec.srv.pendingCleanup[0].retryAfter = time.Now()
		hBadExec.srv.cleanupMu.Unlock()
		hBadExec.srv.retryCleanupJobs(t.Context())
	}
	waitLogContains(t, hBadExec.logs, "quarantining session slot", 3*time.Second)
	if !hBadExec.srv.slots.isQuarantined(0) {
		t.Fatal("unverified cleanup did not quarantine slot")
	}

	// 5. NewServer & RunServer & Serve error paths.
	if _, err := NewServer(context.Background(), Config{Isolation: "bogus"}); err == nil {
		t.Fatal("expected NewServer validation error")
	}
	if _, err := NewServer(context.Background(), Config{
		Isolation:   IsolationUID,
		SessionsDir: filepath.Join(t.TempDir(), "missing"),
		StateDir:    t.TempDir(),
	}); err == nil {
		t.Fatal("expected NewServer sessions-dir error")
	}
	badKey := filepath.Join(t.TempDir(), "k")
	_ = os.WriteFile(badKey, []byte("bad"), 0o644)
	if _, err := NewServer(context.Background(), Config{
		Listen:       "127.0.0.1:0",
		Isolation:    IsolationProcess,
		HostKey:      badKey,
		StateDir:     t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "s"),
		SkipTemplate: true,
	}); err == nil {
		t.Fatal("expected NewServer host key error")
	}
	if _, err := NewServer(context.Background(), Config{
		Listen:      "127.0.0.1:0",
		Isolation:   IsolationProcess,
		StateDir:    t.TempDir(),
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Repo:        filepath.Join(t.TempDir(), "missing-repo"),
	}); err == nil {
		t.Fatal("expected NewServer template error")
	}
	if err := RunServer(context.Background(), Config{Isolation: "bogus"}); err == nil {
		t.Fatal("expected RunServer config error")
	}
	if err := RunServer(context.Background(), Config{
		Listen:       "127.0.0.1:-1",
		Isolation:    IsolationProcess,
		StateDir:     t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "s"),
		SkipTemplate: true,
		Logger:       slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}); err == nil {
		t.Fatal("expected RunServer listen error")
	}
	wantErr := errors.New("accept boom")
	if err := h.srv.Serve(errListener{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("Serve error = %v, want %v", err, wantErr)
	}
}
