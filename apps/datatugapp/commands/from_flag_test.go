package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func fromRoot() *cobra.Command {
	root := &cobra.Command{Use: "datatug"}
	addFromFlag(root)
	noop := func(*cobra.Command, []string) {}
	for _, name := range []string{"show", "serve", "version"} {
		root.AddCommand(&cobra.Command{Use: name, Run: noop})
	}
	root.AddCommand(daemonCommand())
	for _, c := range root.Commands() {
		c.InitDefaultHelpFlag()
	}
	return root
}

func ensure(t *testing.T, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	EnsureDaemonForCaller(fromRoot(), args, &stderr)
	return stderr.String()
}

func TestEnsureDaemonForCaller(t *testing.T) {
	t.Setenv(FromEnv, "")
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
		if got := ensure(t, "show", "--from=claude-mod"); got != "" || f.spawned != 1 {
			t.Fatalf("%q %d", got, f.spawned)
		}
		// Second call reuses it.
		ensure(t, "--from", "claude-mod", "show")
		if f.spawned != 1 {
			t.Fatal("must not spawn twice")
		}
	})
	t.Run("env fallback and flag precedence", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		t.Setenv(FromEnv, "codex-mod")
		if got := ensure(t, "show"); got != "" || f.spawned != 1 {
			t.Fatalf("%q %d", got, f.spawned)
		}
		if got := ensure(t, "show", "--from", "other"); !strings.Contains(got, `unknown --from value "other"`) {
			t.Fatalf("flag must win over env: %q", got)
		}
	})
	t.Run("unknown caller warns once and starts nothing", func(t *testing.T) {
		f := &daemonFake{}
		useDaemonFake(t, f)
		got := ensure(t, "show", "--from", "someone")
		if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "datatug: warning: ") || f.spawned != 0 {
			t.Fatalf("%q %d", got, f.spawned)
		}
	})
	t.Run("failure is one warning line naming the log", func(t *testing.T) {
		useDaemonFake(t, &daemonFake{spawnErr: assertErr("boom")})
		got := ensure(t, "show", "--from", "claude-mod")
		if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "datatug: warning: DataTug daemon is not running and could not be started (cannot launch the server)") ||
			!strings.Contains(got, "daemon.log") || strings.Contains(got, "boom") {
			t.Fatalf("%q", got)
		}
	})
	for _, args := range [][]string{
		{"daemon", "stop", "--from", "claude-mod"},
		{"serve", "--from", "claude-mod"},
		{"version", "--from", "claude-mod"},
		{"show", "--help", "--from", "claude-mod"},
		{"show", "--bogus", "--from", "claude-mod"},
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
	t.Setenv(FromEnv, "claude-mod")
	if got := callerOf(&cobra.Command{Use: "bare"}); got != "claude-mod" {
		t.Fatal(got)
	}
}
