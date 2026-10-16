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

	"github.com/datatug/datatug-cli/pkg/agentaccess"
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
		link.Kind != daemon.KindTableMetadata || link.Running || link.Contract != 1 || !strings.Contains(out, `"contract": 1`) {
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

func TestDaemonURLMintsTheCodeLocally(t *testing.T) {
	f := &daemonFake{up: true}
	useDaemonFake(t, f)
	dir := t.TempDir()
	t.Setenv(agentaccess.DirEnv, dir)
	args := []string{"url", "--project", "p", "--env", "e", "--db", "d", "--table", "t", "--format", "text"}
	// running but no key (an agent that predates the token): a link without a code
	out, err := runDaemon(t, args...)
	if err != nil || strings.Contains(out, "code=") {
		t.Fatalf("%q %v", out, err)
	}
	secret, _ := agentaccess.NewSecret()
	if err = agentaccess.WriteKey(dir, daemon.DefaultPort, secret); err != nil {
		t.Fatal(err)
	}
	out, err = runDaemon(t, append(args, "--record", `{"id":1}`)...)
	_, frag, _ := strings.Cut(strings.TrimSpace(out), "#")
	if err != nil || !strings.HasPrefix(frag, "code=") || !strings.Contains(frag, "&pk=") || strings.Contains(strings.Split(out, "#")[0], "code") {
		t.Fatalf("%q %v", out, err)
	}
	code := strings.TrimPrefix(strings.SplitN(frag, "&", 2)[0], "code=")
	if _, err = agentaccess.New(secret).Exchange(code); err != nil {
		t.Fatalf("the minted code does not exchange: %v", err)
	}
	// not running: no code, whatever a stale key says
	f.up = false
	if out, err = runDaemon(t, args...); err != nil || strings.Contains(out, "code=") {
		t.Fatalf("%q %v", out, err)
	}
	if f.spawned != 0 {
		t.Fatal("daemon url started the daemon")
	}
}

// scannedShop registers the project that a scan of the journey database wrote (environment local,
// source shop: tables Customer and order_line, view customer_names).
func scannedShop(t *testing.T) {
	t.Helper()
	dir, _ := scannedJourneyProjectWithDB(t)
	registerProjectForTest(t, "shop", dir)
}

func TestDaemonURLValidatesAgainstScannedMetadata(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	t.Setenv(daemon.UIBaseEnv, "")
	scannedShop(t)
	base := []string{"url", "--db", "datatug://shop/shop"}
	run := func(args ...string) (string, error) {
		return runDaemon(t, append(append([]string{}, base...), args...)...)
	}

	for _, args := range [][]string{
		{"--table", "Customer"},
		{"--table", "order_line", "--records"},
		{"--table", "customer_names", "--view"},
		{"--table", "order_line", "--record", `{"line_no":1,"order_id":2}`},
	} {
		out, err := run(args...)
		var link daemon.Link
		if err != nil || json.Unmarshal([]byte(out), &link) != nil || link.Warnings == nil || len(link.Warnings) != 0 || !strings.Contains(out, `"warnings": []`) {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	cases := map[string]struct {
		args []string
		code int
		msg  string
	}{
		"unknown table":      {[]string{"--table", "ghost"}, exitCodeNotFound, "not in the project's scanned metadata"},
		"view flag on table": {[]string{"--table", "Customer", "--view"}, exitCodeUsage, "the object is a table"},
		"table flag on view": {[]string{"--table", "customer_names"}, exitCodeUsage, "the object is a view"},
		"missing pk":         {[]string{"--table", "order_line", "--record", `{"order_id":1}`}, exitCodeUsage, "missing primary-key column"},
		"record on a view":   {[]string{"--table", "customer_names", "--view", "--record", `{"CustomerId":1}`}, exitCodeUsage, "a view has no primary key"},
		"not a pk":           {[]string{"--table", "Customer", "--record", `{"CustomerId":1,"FirstName":"s3cret"}`}, exitCodeUsage, "not a primary-key column"},
	}
	for name, c := range cases {
		out, err := run(c.args...)
		if err == nil || exitCodeOf(t, err) != c.code || !strings.Contains(err.Error(), c.msg) || out != "" || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "ghost") {
			t.Errorf("%s: %q %v", name, out, err)
		}
	}
}

func TestDaemonURLWarnsWhenTheSourceIsNotScanned(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	registeredSQLiteSource(t) // listed, never scanned
	for name, args := range map[string][]string{
		"never scanned":   {"--db", "datatug://shop/shop?env=local"},
		"unknown project": {"--project", "nope", "--env", "local", "--db", "shop"},
		"unknown env":     {"--db", "datatug://shop/shop?env=nowhere"},
		"unsafe env":      {"--project", "shop", "--env", "a/b", "--db", "shop"},
	} {
		out, err := runDaemon(t, append([]string{"url", "--table", "Anything"}, args...)...)
		var link daemon.Link
		if err != nil || json.Unmarshal([]byte(out), &link) != nil || len(link.Warnings) != 1 || link.Warnings[0] != daemon.WarningNotScanned {
			t.Errorf("%s: %s %v", name, out, err)
		}
	}
}

func TestDaemonURLScanThatHoldsNothingUsableIsNotScanned(t *testing.T) {
	useDaemonFake(t, &daemonFake{})
	dir, _ := scannedJourneyProjectWithDB(t)
	registerProjectForTest(t, "emptyshop", dir)
	args := []string{"url", "--db", "datatug://emptyshop/shop", "--table", "Customer"}
	warned := func(name string) {
		t.Helper()
		out, err := runDaemon(t, args...)
		var link daemon.Link
		if err != nil || json.Unmarshal([]byte(out), &link) != nil || len(link.Warnings) != 1 || link.Warnings[0] != daemon.WarningNotScanned {
			t.Fatalf("%s: %s %v", name, out, err)
		}
	}
	cols, err := filepath.Glob(filepath.Join(dir, "dbmodels", "*", "main", "tables", "Customer", "*.columns.json"))
	if err != nil || len(cols) != 1 {
		t.Fatalf("%v %v", cols, err)
	}
	if err = os.WriteFile(cols[0], []byte("not json"), 0o644); err != nil { // unreadable: not scanned
		t.Fatal(err)
	}
	warned("unreadable columns file")
	if err = os.WriteFile(cols[0], make([]byte, api.MaxColumnsFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	warned("columns file over the cap")
	models, _ := filepath.Glob(filepath.Join(dir, "dbmodels", "*"))
	for _, m := range models {
		if err = os.RemoveAll(m); err != nil {
			t.Fatal(err)
		}
	}
	warned("no model folder")
}

// useDaemonPolicySet makes the fake manager see a policy set.
func useDaemonPolicySet(t *testing.T, present bool) {
	t.Helper()
	orig := newDaemonManager
	t.Cleanup(func() { newDaemonManager = orig })
	newDaemonManager = func() *daemon.Manager {
		m := daemonManagerWithoutPolicy(t)
		m.PolicySetPresent = func() bool { return present }
		return m
	}
}

func daemonManagerWithoutPolicy(t *testing.T) *daemon.Manager {
	t.Helper()
	return &daemon.Manager{
		Dir:       t.TempDir(),
		Exe:       func() (string, error) { return "/bin/datatug", nil },
		Probe:     func(int) (string, bool) { return "", false },
		Dial:      func(int) bool { return false },
		PortFree:  func(int) bool { return true },
		Alive:     func(int) bool { return false },
		Owns:      func(int, int) bool { return true },
		Terminate: func(int) error { return nil },
		Spawn: func(string, []string, string) (int, <-chan struct{}, error) {
			return 0, nil, errors.New("must not spawn")
		},
		Now: time.Now, Sleep: func(time.Duration) {}, StartWait: time.Millisecond, Poll: time.Millisecond,
	}
}

func TestDaemonStartPolicyPrincipalRequired(t *testing.T) {
	useDaemonPolicySet(t, true)
	root := daemonRoot()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"daemon", "start"})
	err := root.Execute()
	if exitCodeOf(t, err) != exitCodeDatabase {
		t.Fatalf("exit: %v", err)
	}
	var raw map[string]any
	if jsonErr := json.Unmarshal(out.Bytes(), &raw); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if raw["startError"] != "policy-principal-required" || raw["error"] != daemon.ErrPhrasePolicyPrincipal {
		t.Fatalf("%s", out.String())
	}
	if got := errOut.String(); got != policyPrincipalWarning+"\n" || strings.Contains(got, "policies") && strings.Contains(got, "/") {
		t.Fatalf("%q", got)
	}
}

func TestDaemonStartWithoutPolicySetKeepsStatusShape(t *testing.T) {
	useDaemonPolicySet(t, false)
	root := daemonRoot()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"daemon", "start"})
	err := root.Execute()
	if exitCodeOf(t, err) != exitCodeDatabase || strings.Contains(out.String(), "startError") || strings.Contains(errOut.String(), "policy set") {
		t.Fatalf("%v %s %s", err, out.String(), errOut.String())
	}
}
