package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func clientRoot() *cobra.Command {
	root := &cobra.Command{Use: "datatug"}
	addClientFlag(root)
	noop := func(*cobra.Command, []string) {}
	for _, name := range []string{"show", "serve", "version"} {
		root.AddCommand(&cobra.Command{Use: name, Run: noop})
	}
	root.AddCommand(daemonCommand())
	root.Flags().Bool("version", false, "")
	for _, c := range root.Commands() {
		c.InitDefaultHelpFlag()
	}
	return root
}

func ensure(t *testing.T, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	EnsureDaemonForCaller(clientRoot(), args, &stderr)
	return stderr.String()
}

func TestEnsureDaemonForCaller(t *testing.T) {
	t.Setenv(ClientEnv, "")
	t.Run("no caller does nothing", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		if got := ensure(t, "show"); got != "" || f.spawned != 0 {
			t.Fatalf("%q %d", got, f.spawned)
		}
	})
	t.Run("known caller starts it silently", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		if got := ensure(t, "show", "--client=claude-mod"); got != "" || f.spawned != 1 {
			t.Fatalf("%q %d", got, f.spawned)
		}
		// Second call reuses it.
		ensure(t, "--client", "claude-mod", "show")
		if f.spawned != 1 {
			t.Fatal("must not spawn twice")
		}
	})
	t.Run("env fallback and flag precedence", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		t.Setenv(ClientEnv, "codex-mod")
		if got := ensure(t, "show"); got != "" || f.spawned != 1 {
			t.Fatalf("%q %d", got, f.spawned)
		}
		if got := ensure(t, "show", "--client", "other"); !strings.Contains(got, "unknown --client value ignored") || strings.Contains(got, "other") {
			t.Fatalf("flag must win over env: %q", got)
		}
	})
	t.Run("unknown caller warns once and starts nothing", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		got := ensure(t, "show", "--client", "someone")
		if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "datatug: warning: ") || f.spawned != 0 {
			t.Fatalf("%q %d", got, f.spawned)
		}
	})
	t.Run("failure is one warning line naming the log", func(t *testing.T) {
		useDaemonFake(t, &daemonFake{spawnErr: assertErr("boom")})
		got := ensure(t, "show", "--client", "claude-mod")
		if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "datatug: warning: DataTug daemon is not running and could not be started (cannot launch the server)") ||
			!strings.Contains(got, "daemon.log") || strings.Contains(got, "boom") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("policy set without a principal is one clear warning", func(t *testing.T) {
		useDaemonPolicySet(t, true)
		got := ensure(t, "show", "--client", "claude-mod")
		if got != policyPrincipalWarning+"\n" || strings.Contains(got, "daemon.log") {
			t.Fatalf("%q", got)
		}
	})
	for _, args := range [][]string{
		{"daemon", "stop", "--client", "claude-mod"},
		{"serve", "--client", "claude-mod"},
		{"version", "--client", "claude-mod"},
		{"show", "--help", "--client", "claude-mod"},
		{"show", "--bogus", "--client", "claude-mod"},
		{"--version", "--client", "claude-mod"},
	} {
		t.Run("skips "+strings.Join(args, " "), func(t *testing.T) {
			f := &daemonFake{}
			useDaemonFake(t, f)
			if got := ensure(t, args...); got != "" || f.spawned != 0 {
				t.Fatalf("%q %d", got, f.spawned)
			}
		})
	}
	t.Run("unresolvable command", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		root := &cobra.Command{Use: "x"}
		root.AddCommand(&cobra.Command{Use: "a", Run: func(*cobra.Command, []string) {}})
		var stderr bytes.Buffer
		EnsureDaemonForCaller(root, []string{"bogus"}, &stderr)
		if stderr.Len() != 0 || f.spawned != 0 {
			t.Fatal("nothing ran, nothing to ensure")
		}
	})
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestCallerOfWithoutFlag(t *testing.T) {
	t.Setenv(ClientEnv, "claude-mod")
	if got := callerOf(&cobra.Command{Use: "bare"}); got != "claude-mod" {
		t.Fatal(got)
	}
}

// TestClientFlagCollidesWithNoCommand pins why the caller flag is --client and not --from
// (spec daemon#decisions): no command of the real tree may declare a local flag of the
// same name, which would shadow the persistent root flag; and the commands that own
// --from keep it.
func TestClientFlagCollidesWithNoCommand(t *testing.T) {
	root := DatatugCommand()
	if root.PersistentFlags().Lookup(ClientFlag) == nil {
		t.Fatal("root must carry the persistent --client flag")
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.LocalNonPersistentFlags().Lookup(ClientFlag) != nil || (c != root && c.PersistentFlags().Lookup(ClientFlag) != nil) {
			t.Errorf("%s declares its own --%s", c.CommandPath(), ClientFlag)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	for _, path := range [][]string{{"query", "run"}, {"db", "copy"}, {"db", "export"}} {
		cmd, _, err := root.Find(path)
		if err != nil || cmd.Flags().Lookup("from") == nil {
			t.Errorf("%v must keep its own --from flag (err %v)", path, err)
		}
	}
}

// A command's own --from (a table, a source URL) is never read as the caller, and no
// flag value is ever echoed into a warning.
func TestCommandOwnFromFlagIsNotTheCaller(t *testing.T) {
	const secretURL = "postgres://u:SECRETPW@127.0.0.1:1/db"
	t.Run("db copy --from <credentialed URL>", func(t *testing.T) {
		t.Setenv(ClientEnv, "")
		f := &daemonFake{}
		useDaemonFake(t, f)
		var stderr bytes.Buffer
		EnsureDaemonForCaller(DatatugCommand(), []string{"db", "copy", "--from", secretURL, "--to", "sqlite:///tmp/x.db"}, &stderr)
		if strings.Contains(stderr.String(), "SECRETPW") || stderr.Len() != 0 || f.spawned != 0 {
			t.Fatalf("stderr %q spawned %d", stderr.String(), f.spawned)
		}
	})
	t.Run("a credentialed URL as the unknown --client is not echoed", func(t *testing.T) {
		useDaemonFake(t, &daemonFake{})
		var stderr bytes.Buffer
		EnsureDaemonForCaller(DatatugCommand(), []string{"show", "--client", secretURL}, &stderr)
		if strings.Contains(stderr.String(), "SECRETPW") || !strings.Contains(stderr.String(), "unknown --client value ignored") {
			t.Fatalf("stderr %q", stderr.String())
		}
	})
	t.Run("query run --from T still ensures the daemon for DATATUG_CLIENT", func(t *testing.T) {
		t.Setenv(ClientEnv, "claude-mod")
		f := &daemonFake{}
		useDaemonFake(t, f)
		var stderr bytes.Buffer
		EnsureDaemonForCaller(DatatugCommand(), []string{"query", "run", "--db", "sqlite:///tmp/x.db", "--from", "customers"}, &stderr)
		if stderr.Len() != 0 || f.spawned != 1 {
			t.Fatalf("stderr %q spawned %d", stderr.String(), f.spawned)
		}
	})
}
