package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/daemon"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server"
	"github.com/spf13/cobra"
)

// daemonFake is a daemon.Manager over plain fields: no process, port or network.
type daemonFake struct {
	up       bool // the agent answers on the default port
	spawnErr error
	termErr  error
	spawned  int
}

func useDaemonFake(t *testing.T, f *daemonFake) {
	t.Helper()
	orig := newDaemonManager
	t.Cleanup(func() { newDaemonManager = orig })
	dir := t.TempDir()
	newDaemonManager = func() *daemon.Manager {
		return &daemon.Manager{
			Dir:       dir,
			Exe:       func() (string, error) { return "/bin/datatug", nil },
			Probe:     func(int) (string, bool) { return "v9", f.up },
			Dial:      func(int) bool { return f.up },
			PortFree:  func(int) bool { return true },
			Alive:     func(int) bool { return f.up },
			Owns:      func(int, int) bool { return true },
			Terminate: func(int) error { return f.termErr },
			Spawn: func(string, []string, string) (int, <-chan struct{}, error) {
				f.spawned++
				if f.spawnErr == nil {
					f.up = true
				}
				return 4242, make(chan struct{}), f.spawnErr
			},
			Now:       time.Now,
			Sleep:     func(time.Duration) {},
			StartWait: time.Millisecond, Poll: time.Millisecond,
		}
	}
}

func daemonRoot() *cobra.Command {
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	addClientFlag(root)
	root.AddCommand(daemonCommand())
	return root
}

func runDaemon(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := daemonRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(append([]string{"daemon"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestDaemonStatus(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	out, err := runDaemon(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	var st daemon.Status
	if err = json.Unmarshal([]byte(out), &st); err != nil || st.Running || st.UIBase == "" {
		t.Fatalf("%s %v", out, err)
	}
	text, err := runDaemon(t, "status", "--format", "text")
	if err != nil || !strings.Contains(text, "running: false") {
		t.Fatalf("%q %v", text, err)
	}
	if _, err = runDaemon(t, "status", "--format", "yaml"); exitCodeOf(t, err) != exitCodeUsage {
		t.Fatal(err)
	}
}

func TestDaemonStart(t *testing.T) {
	f := &daemonFake{}
	useDaemonFake(t, f)
	out, err := runDaemon(t, "start", "--client", "claude-mod")
	if err != nil || f.spawned != 1 || !strings.Contains(out, `"running": true`) || !strings.Contains(out, `"startedBy": "claude-mod"`) {
		t.Fatalf("%s %v", out, err)
	}
	// Already running: reused, and `cli` is the owner of a hand-started one.
	if out, err = runDaemon(t, "start", "--format", "text"); err != nil || f.spawned != 1 || !strings.Contains(out, "running: true") {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = runDaemon(t, "start", "--format", "nope"); exitCodeOf(t, err) != exitCodeUsage {
		t.Fatal(err)
	}
}

func TestDaemonStartCallerDefaultsToCLI(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	t.Setenv(ClientEnv, "")
	out, err := runDaemon(t, "start")
	if err != nil || !strings.Contains(out, `"startedBy": "cli"`) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestDaemonStartFailureStillPrintsStatus(t *testing.T) {
	useDaemonFake(t, &daemonFake{spawnErr: errors.New("boom")})
	out, err := runDaemon(t, "start")
	if exitCodeOf(t, err) != exitCodeDatabase || !strings.Contains(out, `"error": "`+daemon.ErrPhraseSpawn+`"`) {
		t.Fatalf("%s %v", out, err)
	}
	if strings.Contains(out, "boom") {
		t.Fatal("raw errors must not reach the status")
	}
}

func TestDaemonStop(t *testing.T) {
	t.Run("nothing to stop", func(t *testing.T) {
		useDaemonFake(t, &daemonFake{})
		out, err := runDaemon(t, "stop")
		if err != nil || !strings.Contains(out, `"running": false`) {
			t.Fatalf("%s %v", out, err)
		}
	})
	t.Run("external server is refused", func(t *testing.T) {
		useDaemonFake(t, &daemonFake{up: true})
		if _, err := runDaemon(t, "stop"); exitCodeOf(t, err) != exitCodeNotFound {
			t.Fatal(err)
		}
	})
	t.Run("started by the CLI", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		if _, err := runDaemon(t, "start"); err != nil {
			t.Fatal(err)
		}
		f.termErr = errors.New("denied")
		if _, err := runDaemon(t, "stop"); exitCodeOf(t, err) != exitCodeDatabase {
			t.Fatal(err)
		}
		f.termErr = nil
		f.up = false // the signal worked
		out, err := runDaemon(t, "stop")
		if err != nil || !strings.Contains(out, `"running": false`) {
			t.Fatalf("%s %v", out, err)
		}
	})
	t.Run("bad format", func(t *testing.T) {
		useDaemonFake(t, &daemonFake{})
		if _, err := runDaemon(t, "stop", "--format", "x"); exitCodeOf(t, err) != exitCodeUsage {
			t.Fatal(err)
		}
	})
}

func TestDaemonURL(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	t.Setenv(daemon.UIBaseEnv, "")
	args := []string{"url", "--project", "demo", "--env", "dev", "--db", "Chinook", "--table", "Album"}
	out, err := runDaemon(t, args...)
	var link daemon.Link
	if err != nil || json.Unmarshal([]byte(out), &link) != nil ||
		link.URL != "https://datatug.app/store/http-127.0.0.1:8989/project/demo/env/dev/db/Chinook/table/Album?schema=" ||
		link.Kind != daemon.KindTableMetadata || link.Running {
		t.Fatalf("%s %v", out, err)
	}
	out, err = runDaemon(t, append(args, "--records", "--limit", "10", "--format", "text")...)
	if err != nil || strings.TrimSpace(out) != "https://datatug.app/store/http-127.0.0.1:8989/project/demo/env/dev/db/Chinook/table/Album/records?limit=10&schema=" {
		t.Fatalf("%q %v", out, err)
	}
	out, err = runDaemon(t, append(args, "--record", `{"AlbumId":1}`, "--view", "--schema", "main")...)
	if err != nil || !strings.Contains(out, `/view/Album/record?schema=main#pk=`) || !strings.Contains(out, `"uiSupport": "pending"`) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = runDaemon(t, "url", "--project", "demo"); exitCodeOf(t, err) != exitCodeUsage {
		t.Fatal(err)
	}
	if _, err = runDaemon(t, append(args, "--format", "x")...); exitCodeOf(t, err) != exitCodeUsage {
		t.Fatal(err)
	}
}

func TestDaemonURLNeverEchoesConnectionURLs(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	out, err := runDaemon(t, "url", "--project", "p", "--env", "e", "--db", "postgres://u:secret@h/db", "--table", "t")
	if exitCodeOf(t, err) != exitCodeUsage || strings.Contains(out+err.Error(), "secret") {
		t.Fatalf("%s %v", out, err)
	}
}

func TestPrintDaemonStatusWriteError(t *testing.T) {
	if err := printDaemonStatus(failWriter{}, "text", daemon.Status{}); err == nil {
		t.Fatal("want the write error")
	}
}

func TestDaemonStartWriteError(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	root := daemonRoot()
	root.SetOut(failWriter{})
	root.SetArgs([]string{"daemon", "start"})
	if err := root.Execute(); err == nil {
		t.Fatal("want the write error")
	}
}

func TestServeIdleTimeoutReachesServer(t *testing.T) {
	origStart := serveStartHTTP
	t.Cleanup(func() { serveStartHTTP = origStart })
	var got time.Duration
	serveStartHTTP = func(s *server.HttpServer, _ map[string]string, _ string, _ int, _ secureread.Session, _ api.Capabilities) error {
		got = s.IdleTimeout
		return nil
	}
	cmd := serveCommandArgs()
	cmd.SetArgs([]string{"--idle-timeout", "2h"})
	t.Setenv("HOME", t.TempDir())
	if err := cmd.Execute(); err != nil || got != 2*time.Hour {
		t.Fatalf("idle = %v, err = %v", got, err)
	}
}

func TestDaemonURLByReference(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	t.Setenv(daemon.UIBaseEnv, "")
	registeredSQLiteSource(t)
	const prefix = "https://datatug.app/store/http-127.0.0.1:8989/project/shop/env/local/db/shop/table/Customer"
	for _, ref := range []string{"datatug://shop/shop", "datatug://shop/shop?env=local"} {
		out, err := runDaemon(t, "url", "--db", ref, "--table", "Customer", "--format", "text")
		if err != nil || strings.TrimSpace(out) != prefix+"?schema=" {
			t.Fatalf("%s: %q %v", ref, out, err)
		}
	}
	out, err := runDaemon(t, "url", "--db", "datatug://shop/shop", "--table", "Customer", "--record", `{"CustomerId":9007199254740993}`, "--format", "text")
	if err != nil || strings.TrimSpace(out) != prefix+"/record?schema=#pk=%7B%22CustomerId%22%3A%229007199254740993%22%7D" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestDaemonURLReferenceRefusals(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	dbPath := registeredSQLiteSource(t)
	_, _, err := runDbAdd(t, "--project", "shop", "--db", dbPath, "--env", "dev", "--name", "shop")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		args []string
		code int
		msg  string
	}{
		"both forms":        {[]string{"--db", "datatug://shop/shop?env=dev", "--project", "shop"}, exitCodeUsage, "not both"},
		"both forms by env": {[]string{"--db", "datatug://shop/shop?env=dev", "--env", "dev"}, exitCodeUsage, "not both"},
		"no project":        {[]string{"--db", "datatug:///shop"}, exitCodeUsage, "names no project"},
		"malformed":         {[]string{"--db", "datatug://shop/"}, exitCodeUsage, "datatug://<project>/<db>"},
		"ambiguous env":     {[]string{"--db", "datatug://shop/shop"}, exitCodeUsage, "dev, local"},
		"unknown project":   {[]string{"--db", "datatug://nope/shop"}, exitCodeNotFound, "unknown project"},
		"unknown source":    {[]string{"--db", "datatug://shop/ghost"}, exitCodeNotFound, "not listed in any environment"},
		"other scheme":      {[]string{"--project", "p", "--env", "e", "--db", "postgres://u:s3cret@h/d"}, exitCodeUsage, "never a connection URL"},
		"sqlite scheme":     {[]string{"--project", "p", "--env", "e", "--db", "sqlite:///tmp/x.db"}, exitCodeUsage, "never a connection URL"},
		"a path":            {[]string{"--project", "p", "--env", "e", "--db", "/home/user/secret-dir/x.db"}, exitCodeUsage, "never a connection URL"},
		"dot dot":           {[]string{"--project", "p", "--env", "e", "--db", ".."}, exitCodeUsage, ""},
	}
	for name, c := range cases {
		out, err := runDaemon(t, append([]string{"url", "--table", "Customer"}, c.args...)...)
		if err == nil || exitCodeOf(t, err) != c.code || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %q %v", name, out, err)
		}
		if out != "" || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "secret-dir") {
			t.Errorf("%s: leaked: %q %v", name, out, err)
		}
	}
}

func TestDaemonURLNeverStartsTheDaemon(t *testing.T) {
	f := &daemonFake{}
	useDaemonFake(t, f)
	if _, err := runDaemon(t, "url", "--project", "p", "--env", "e", "--db", "d", "--table", "t"); err != nil || f.spawned != 0 {
		t.Fatalf("%v %d", err, f.spawned)
	}
}

func TestDaemonURLReferenceStoreFailure(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	registeredSQLiteSource(t)
	// the project's environments cannot be read
	dir := t.TempDir()
	registerProjectForTest(t, "broken", dir)
	if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"broken","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environments"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runDaemon(t, "url", "--db", "datatug://broken/shop", "--table", "t")
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase {
		t.Fatalf("%v", err)
	}
}
