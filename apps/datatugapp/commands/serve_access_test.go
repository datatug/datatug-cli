package commands

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
)

// serve always creates its access secret: the key is left for the CLI's own clients and for
// `daemon url`, the server gets the token model, and a hand-started server prints a link that
// opens (the daemon, whose output is a log, prints none).
func TestServe_AccessKeyAndLink(t *testing.T) {
	cov100eHome(t, "")
	dir := t.TempDir()
	t.Setenv(agentaccess.DirEnv, dir)
	call, _ := cov100eSeams(t, nil)
	out := captureStdout(t, func() {
		cmd := serveCommandArgs()
		_ = cmd.Flags().Set(servePortFlag, "18990")
		if err := serveCommandAction(cmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if call.caps.Access == nil {
		t.Fatal("serve must require the session token")
	}
	_, frag, ok := strings.Cut(strings.TrimSpace(strings.Split(out, "DataTug web UI for this agent: ")[1]), "#code=")
	if !ok {
		t.Fatalf("a hand-started server prints a link with a code: %q", out)
	}
	if _, err := os.Stat(agentaccess.KeyPath(dir, 18990)); err == nil {
		t.Fatal("the key is removed when the server returns")
	}
	_ = frag

	// the daemon (an idle timeout) prints no code into its log
	out = captureStdout(t, func() {
		cmd := serveCommandArgs()
		_ = cmd.Flags().Set(servePortFlag, "18990")
		_ = cmd.Flags().Set(serveIdleTimeoutFlag, time.Hour.String())
		if err := serveCommandAction(cmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "code=") {
		t.Fatalf("the daemon's output must not hold a code: %q", out)
	}
}

func TestServe_AccessKeyFailures(t *testing.T) {
	cov100eHome(t, "")
	cov100eSeams(t, nil)
	orig := serveWriteKey
	t.Cleanup(func() { serveWriteKey = orig })
	serveWriteKey = func(string, int, []byte) error { return errors.New("read-only disk") }
	err := serveCommandAction(serveCommandArgs(), nil)
	var ec ExitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != exitCodeDatabase || strings.Contains(err.Error(), "read-only") {
		t.Fatalf("%v", err)
	}
}

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	func() {
		defer func() { os.Stdout = orig }()
		run()
		_ = w.Close()
	}()
	return <-done
}
