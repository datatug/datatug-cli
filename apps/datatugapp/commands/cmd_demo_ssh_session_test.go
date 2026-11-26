//go:build unix

package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtdemo"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func stubSSHSession[T any](t *testing.T, target *T, val T) {
	t.Helper()
	orig := *target
	*target = val
	t.Cleanup(func() { *target = orig })
}

func TestDemoSSHSessionFlagsAndCommand(t *testing.T) {
	cmd := demoSSHSessionCommand()
	if cmd.Use != "ssh-session" || !cmd.Hidden {
		t.Fatalf("unexpected command metadata: Use=%q Hidden=%v", cmd.Use, cmd.Hidden)
	}
	requiredFlags := []string{
		"workspace", "template", "project-dir", "repo", "github-client-id",
		"save-branch", "session-id", "deadline",
		"rlimit-as", "rlimit-cpu", "rlimit-fsize", "rlimit-nofile", "rlimit-nproc",
		"cleanup", "prepare-template",
	}
	for _, name := range requiredFlags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing required flag --%s", name)
		}
	}
}

func TestDemoSSHSessionRlimitsLoweringOnlyAndFailClosed(t *testing.T) {
	t.Run("lowering only keeps lower hard/soft limits", func(t *testing.T) {
		current := map[int]unix.Rlimit{
			unix.RLIMIT_CORE:   {Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY},
			unix.RLIMIT_AS:     {Cur: 1024, Max: 1024}, // already lower than 2 GiB
			unix.RLIMIT_CPU:    {Cur: unix.RLIM_INFINITY, Max: 1000},
			unix.RLIMIT_FSIZE:  {Cur: 500, Max: 100000000}, // soft already lower than 64 MiB
			unix.RLIMIT_NOFILE: {Cur: 2048, Max: 4096},
			unix.RLIMIT_NPROC:  {Cur: 128, Max: 128},
		}
		applied := map[int]unix.Rlimit{}
		stubSSHSession(t, &demoSSHGetrlimit, func(res int, r *unix.Rlimit) error {
			*r = current[res]
			return nil
		})
		stubSSHSession(t, &demoSSHSetrlimit, func(res int, r *unix.Rlimit) error {
			applied[res] = *r
			return nil
		})

		err := applyDemoSSHRlimits(demoSSHRlimits{
			AS:     demoSSHDefaultRLimitAS,
			CPU:    900,
			FSize:  demoSSHDefaultRLimitFSize,
			NoFile: 512,
			NProc:  64,
		})
		if err != nil {
			t.Fatalf("applyDemoSSHRlimits: %v", err)
		}
		if applied[unix.RLIMIT_CORE] != (unix.Rlimit{Cur: 0, Max: 0}) {
			t.Fatalf("RLIMIT_CORE = %+v", applied[unix.RLIMIT_CORE])
		}
		if applied[unix.RLIMIT_AS] != (unix.Rlimit{Cur: 1024, Max: 1024}) {
			t.Fatalf("RLIMIT_AS must not be raised: %+v", applied[unix.RLIMIT_AS])
		}
		if applied[unix.RLIMIT_CPU] != (unix.Rlimit{Cur: 900, Max: 900}) {
			t.Fatalf("RLIMIT_CPU = %+v", applied[unix.RLIMIT_CPU])
		}
		if applied[unix.RLIMIT_FSIZE] != (unix.Rlimit{Cur: 500, Max: demoSSHDefaultRLimitFSize}) {
			t.Fatalf("RLIMIT_FSIZE must not raise soft: %+v", applied[unix.RLIMIT_FSIZE])
		}
		if applied[unix.RLIMIT_NOFILE] != (unix.Rlimit{Cur: 512, Max: 512}) {
			t.Fatalf("RLIMIT_NOFILE = %+v", applied[unix.RLIMIT_NOFILE])
		}
		if applied[unix.RLIMIT_NPROC] != (unix.Rlimit{Cur: 64, Max: 64}) {
			t.Fatalf("RLIMIT_NPROC = %+v", applied[unix.RLIMIT_NPROC])
		}
	})

	t.Run("cur clamped to max", func(t *testing.T) {
		got := computeLoweredRlimit(unix.Rlimit{Cur: 500, Max: 200}, 400)
		if got.Cur != 200 || got.Max != 200 {
			t.Fatalf("got = %+v", got)
		}
	})

	t.Run("zero configurable limit rejected", func(t *testing.T) {
		err := applyDemoSSHRlimits(demoSSHRlimits{AS: 0, CPU: 1, FSize: 1, NoFile: 1, NProc: 1})
		if err == nil || !strings.Contains(err.Error(), "must be > 0") {
			t.Fatalf("expected error for zero RLIMIT_AS, got %v", err)
		}
	})

	t.Run("getrlimit failure fails closed", func(t *testing.T) {
		stubSSHSession(t, &demoSSHGetrlimit, func(int, *unix.Rlimit) error { return errors.New("eperm") })
		err := applyDemoSSHRlimits(demoSSHRlimits{AS: 1, CPU: 1, FSize: 1, NoFile: 1, NProc: 1})
		if err == nil || !strings.Contains(err.Error(), "getrlimit") {
			t.Fatalf("expected getrlimit error, got %v", err)
		}
	})

	t.Run("setrlimit failure fails closed", func(t *testing.T) {
		stubSSHSession(t, &demoSSHGetrlimit, func(_ int, r *unix.Rlimit) error {
			*r = unix.Rlimit{Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY}
			return nil
		})
		stubSSHSession(t, &demoSSHSetrlimit, func(int, *unix.Rlimit) error { return errors.New("einval") })
		err := applyDemoSSHRlimits(demoSSHRlimits{AS: 1, CPU: 1, FSize: 1, NoFile: 1, NProc: 1})
		if err == nil || !strings.Contains(err.Error(), "setrlimit") {
			t.Fatalf("expected setrlimit error, got %v", err)
		}
	})
}

func TestDemoSSHSessionCopyTemplateTree(t *testing.T) {
	t.Run("copies nested dirs and .git with 0755/0644 modes", func(t *testing.T) {
		src := t.TempDir()
		dst := filepath.Join(t.TempDir(), "repo")

		for _, rel := range []string{
			".git/HEAD",
			".git/objects/pack/test.idx",
			"demo-project-1/datatug-project.json",
			"demo-project-1/queries/top.sql",
		} {
			p := filepath.Join(src, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("content:"+rel), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		// Also include an executable script and verify its 0755 bit is preserved.
		scriptPath := filepath.Join(src, "scripts", "run.sh")
		if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(scriptPath, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := copyTemplateTree(src, dst); err != nil {
			t.Fatalf("copyTemplateTree: %v", err)
		}
		if sfi, err := os.Stat(filepath.Join(dst, "scripts", "run.sh")); err != nil || sfi.Mode().Perm() != 0o755 {
			t.Fatalf("copied script mode = %v / %v, want 0755", sfi, err)
		}
		for _, rel := range []string{".git/HEAD", ".git/objects/pack/test.idx", "demo-project-1/datatug-project.json", "demo-project-1/queries/top.sql"} {
			p := filepath.Join(dst, filepath.FromSlash(rel))
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("missing copied file %s: %v", rel, err)
			}
			if string(data) != "content:"+rel {
				t.Fatalf("content mismatch for %s: %q", rel, string(data))
			}
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o644 {
				t.Fatalf("file %s mode = %o, want 0644", rel, fi.Mode().Perm())
			}
		}
		dirInfo, err := os.Stat(filepath.Join(dst, "demo-project-1"))
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0o755 {
			t.Fatalf("dir mode = %o, want 0755", dirInfo.Mode().Perm())
		}
	})

	t.Run("refuses symlink in template", func(t *testing.T) {
		src := t.TempDir()
		dst := filepath.Join(t.TempDir(), "repo")
		if err := os.Symlink("/etc/passwd", filepath.Join(src, "evil")); err != nil {
			t.Fatal(err)
		}
		err := copyTemplateTree(src, dst)
		if err == nil || !strings.Contains(err.Error(), "refusing non-regular entry") {
			t.Fatalf("expected symlink refusal, got %v", err)
		}
	})

	t.Run("refuses nested symlink in template", func(t *testing.T) {
		src := t.TempDir()
		sub := filepath.Join(src, "sub")
		_ = os.MkdirAll(sub, 0o755)
		if err := os.Symlink("/etc/passwd", filepath.Join(sub, "evil")); err != nil {
			t.Fatal(err)
		}
		err := copyTemplateTree(src, filepath.Join(t.TempDir(), "repo"))
		if err == nil || !strings.Contains(err.Error(), "refusing non-regular entry") {
			t.Fatalf("expected nested symlink refusal, got %v", err)
		}
	})

	t.Run("rejects missing or non-dir template", func(t *testing.T) {
		if err := copyTemplateTree(filepath.Join(t.TempDir(), "missing"), t.TempDir()); err == nil {
			t.Fatal("expected error for missing template")
		}
		f := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(f, []byte("x"), 0o644)
		if err := copyTemplateTree(f, t.TempDir()); err == nil {
			t.Fatal("expected error for file template")
		}
	})

	t.Run("resolveSafeProjectDir rejects escape and absolute", func(t *testing.T) {
		if _, err := resolveSafeProjectDir("/ws/repo", "/etc"); err == nil {
			t.Fatal("expected error for absolute project-dir")
		}
		if _, err := resolveSafeProjectDir("/ws/repo", "../escape"); err == nil {
			t.Fatal("expected error for escaping project-dir")
		}
		got, err := resolveSafeProjectDir("/ws/repo", "demo-project-1")
		if err != nil || got != "/ws/repo/demo-project-1" {
			t.Fatalf("got=(%q, %v)", got, err)
		}
	})
}

func makeDemoTemplate(t *testing.T, title string, catalogs ...string) string {
	t.Helper()
	tpl := t.TempDir()
	projDir := filepath.Join(tpl, "demo-project-1")
	if err := os.MkdirAll(filepath.Join(tpl, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tpl, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "datatug-project.json"), []byte(`{"id":"demo-project-1","title":"`+title+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(catalogs) > 0 {
		envDir := filepath.Join(projDir, "environments", "local")
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			t.Fatal(err)
		}
		quoted := make([]string, len(catalogs))
		for i, cat := range catalogs {
			quoted[i] = `"` + cat + `"`
			catDir := filepath.Join(envDir, "catalogs", cat)
			if err := os.MkdirAll(catDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(catDir, cat+".db.json"), []byte(`{"id":"`+cat+`","driver":"sqlite3","path":"data/`+cat+`.sqlite"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		envJSON := `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":[` + strings.Join(quoted, ",") + `]}]}`
		if err := os.WriteFile(filepath.Join(envDir, "local.env.json"), []byte(envJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tpl
}

func TestDemoSSHSessionEndToEndWiringAndOptions(t *testing.T) {
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })

	t.Run("explore path copies template, writes settings, builds workspace options", func(t *testing.T) {
		ws := t.TempDir()
		if err := os.MkdirAll(filepath.Join(ws, "home"), 0o700); err != nil {
			t.Fatal(err)
		}
		tpl := makeDemoTemplate(t, "DataTug Demo Project", "chinook", "orders")
		// Put a bundled chinook.sqlite in the template to verify chinook-local seeding.
		chinookSrc := filepath.Join(tpl, "demo-project-1", ".demo-data", "chinook.sqlite")
		if err := os.MkdirAll(filepath.Dir(chinookSrc), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(chinookSrc, []byte("sqlite-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}

		order := []string{}
		var gotLimits demoSSHRlimits
		stubSSHSession(t, &demoSSHApplyRlimits, func(l demoSSHRlimits) error {
			order = append(order, "rlimits")
			gotLimits = l
			return nil
		})
		stubSSHSession(t, &demoSSHUseMemoryStore, func() {
			order = append(order, "memstore")
		})
		var gotWelcomeCfg dtdemo.Config
		stubSSHSession(t, &demoSSHRunWelcome, func(cfg dtdemo.Config, _ ...tea.ProgramOption) (dtdemo.WelcomeResult, error) {
			order = append(order, "welcome")
			gotWelcomeCfg = cfg
			return dtdemo.WelcomeResult{Outcome: dtdemo.OutcomeExplore}, nil
		})
		var captured chatOptions
		stubSSHSession(t, &demoSSHRunWorkspaceLoop, func(_ *cobra.Command, opts chatOptions) error {
			order = append(order, "workspace")
			captured = opts
			return nil
		})

		cmd := demoSSHSessionCommand()
		cmd.SetArgs([]string{
			"--workspace", ws,
			"--template", tpl,
			"--project-dir", "demo-project-1",
			"--repo", "https://github.com/datatug/datatug-demo-project.git",
			"--github-client-id", "cid-demo",
			"--save-branch", "datatug-demo",
			"--session-id", "sess-1",
			"--deadline", "2026-11-26T15:45:00Z",
			"--rlimit-as", "2147483648",
			"--rlimit-cpu", "900",
			"--rlimit-fsize", "67108864",
			"--rlimit-nofile", "512",
			"--rlimit-nproc", "64",
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		// Verify strict startup order: rlimits FIRST, then memory store, welcome, workspace.
		if strings.Join(order, ",") != "rlimits,memstore,welcome,workspace" {
			t.Fatalf("startup order = %v", order)
		}
		if gotLimits.AS != 2147483648 || gotLimits.CPU != 900 || gotLimits.FSize != 67108864 || gotLimits.NoFile != 512 || gotLimits.NProc != 64 {
			t.Fatalf("rlimits = %+v", gotLimits)
		}
		if gotWelcomeCfg.Canonical.FullName() != "datatug/datatug-demo-project" || gotWelcomeCfg.GitHubClientID != "cid-demo" {
			t.Fatalf("welcome cfg = %+v", gotWelcomeCfg)
		}

		// Settings written under <workspace>/home/.datatug.yaml.
		settingsPath := filepath.Join(ws, "home", dtconfig.ConfigFileName)
		dtconfig.SetConfigFilePath(settingsPath)
		settings, err := dtconfig.GetSettings()
		if err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
		proj := settings.GetProjectConfig("demo")
		if proj == nil || proj.Path != filepath.Join(ws, "repo", "demo-project-1") || proj.Title != "DataTug Demo Project" {
			t.Fatalf("registered project = %+v", proj)
		}

		// chinook-local seeded under <workspace>/home/datatug/dbs/chinook-local.sqlite.
		chinookLocal, err := os.ReadFile(filepath.Join(ws, "home", "datatug", "dbs", "chinook-local.sqlite"))
		if err != nil || string(chinookLocal) != "sqlite-bytes" {
			t.Fatalf("chinook-local = (%q, %v), want sqlite-bytes", string(chinookLocal), err)
		}

		// Workspace chatOptions verified: opens chinook / Invoice so notice is visible.
		if captured.project != "demo" || captured.env != "local" || captured.database != "chinook" || captured.openAtStart != "Invoice" {
			t.Fatalf("chatOptions project/env/db/openAtStart = %q/%q/%q/%q", captured.project, captured.env, captured.database, captured.openAtStart)
		}
		if len(captured.roles) != 1 || captured.roles[0] != "admin" {
			t.Fatalf("roles = %v", captured.roles)
		}
		if !strings.Contains(captured.notice, "Temporary demo workspace") || !strings.Contains(captured.notice, "15:45 UTC") || !strings.Contains(captured.notice, "Save to GitHub") {
			t.Fatalf("notice = %q", captured.notice)
		}
		if !captured.workspace.options.DisableBrowser || !captured.workspace.sessionWarningAt.Equal(time.Date(2026, 11, 26, 15, 44, 0, 0, time.UTC)) {
			t.Fatalf("demo browser setting = %t, warning time = %s", captured.workspace.options.DisableBrowser, captured.workspace.sessionWarningAt)
		}
		foundSaveMod := false
		for _, m := range captured.workspace.modules {
			if m.ID == dtdemo.ModuleID && m.Text == dtdemo.ModuleText {
				foundSaveMod = true
			}
		}
		if !foundSaveMod {
			t.Fatal("Save to GitHub module missing from workspace modules")
		}
	})

	t.Run("welcome quit exits 0 without copying or launching workspace", func(t *testing.T) {
		ws := t.TempDir()
		stubSSHSession(t, &demoSSHApplyRlimits, func(demoSSHRlimits) error { return nil })
		stubSSHSession(t, &demoSSHUseMemoryStore, func() {})
		stubSSHSession(t, &demoSSHRunWelcome, func(dtdemo.Config, ...tea.ProgramOption) (dtdemo.WelcomeResult, error) {
			return dtdemo.WelcomeResult{Outcome: dtdemo.OutcomeQuit}, nil
		})
		stubSSHSession(t, &demoSSHRunWorkspaceLoop, func(*cobra.Command, chatOptions) error {
			t.Fatal("workspace must not run on quit")
			return nil
		})
		err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{
			workspace:  ws,
			template:   t.TempDir(),
			projectDir: "demo-project-1",
			repo:       demoSSHDefaultRepo,
		})
		if err != nil {
			t.Fatalf("expected nil on quit, got %v", err)
		}
	})

	t.Run("opened fork skips template copy and falls back to first local catalog", func(t *testing.T) {
		ws := t.TempDir()
		if err := os.MkdirAll(filepath.Join(ws, "home"), 0o700); err != nil {
			t.Fatal(err)
		}
		// Pre-populate repo as if demogit.Client.Open ran, with only "alpha" and "beta" catalogs (no "orders").
		repoDir := filepath.Join(ws, "repo")
		tpl := makeDemoTemplate(t, "My Fork", "beta", "alpha")
		if err := copyTemplateTree(tpl, repoDir); err != nil {
			t.Fatal(err)
		}

		stubSSHSession(t, &demoSSHApplyRlimits, func(demoSSHRlimits) error { return nil })
		stubSSHSession(t, &demoSSHUseMemoryStore, func() {})
		stubSSHSession(t, &demoSSHRunWelcome, func(dtdemo.Config, ...tea.ProgramOption) (dtdemo.WelcomeResult, error) {
			return dtdemo.WelcomeResult{Outcome: dtdemo.OutcomeOpenedFork}, nil
		})
		var captured chatOptions
		stubSSHSession(t, &demoSSHRunWorkspaceLoop, func(_ *cobra.Command, opts chatOptions) error {
			captured = opts
			return nil
		})

		// Pass non-existent template to prove template copy is NOT called when OutcomeOpenedFork!
		err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{
			workspace:  ws,
			template:   filepath.Join(ws, "nonexistent-template"),
			projectDir: "demo-project-1",
			repo:       "datatug/datatug-demo-project",
		})
		if err != nil {
			t.Fatalf("runDemoSSHSession: %v", err)
		}
		if captured.database != "alpha" || captured.openAtStart != "" {
			t.Fatalf("expected first catalog alpha with empty openAtStart, got %q / %q", captured.database, captured.openAtStart)
		}
		if !strings.Contains(captured.notice, "Temporary demo workspace") || strings.Contains(captured.notice, "UTC") {
			t.Fatalf("notice without deadline = %q", captured.notice)
		}
	})

	t.Run("non-GitHub --repo disables GitHub features instead of failing", func(t *testing.T) {
		ws := t.TempDir()
		if err := os.MkdirAll(filepath.Join(ws, "home"), 0o700); err != nil {
			t.Fatal(err)
		}
		tpl := makeDemoTemplate(t, "Local Repo Demo", "orders")

		stubSSHSession(t, &demoSSHApplyRlimits, func(demoSSHRlimits) error { return nil })
		stubSSHSession(t, &demoSSHUseMemoryStore, func() {})
		var gotWelcomeCfg dtdemo.Config
		stubSSHSession(t, &demoSSHRunWelcome, func(cfg dtdemo.Config, _ ...tea.ProgramOption) (dtdemo.WelcomeResult, error) {
			gotWelcomeCfg = cfg
			return dtdemo.WelcomeResult{Outcome: dtdemo.OutcomeExplore}, nil
		})
		var captured chatOptions
		stubSSHSession(t, &demoSSHRunWorkspaceLoop, func(_ *cobra.Command, opts chatOptions) error {
			captured = opts
			return nil
		})

		err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{
			workspace:  ws,
			template:   tpl,
			projectDir: "demo-project-1",
			repo:       "file:///tmp/local-demo-repo",
		})
		if err != nil {
			t.Fatalf("non-GitHub repo should not fail session: %v", err)
		}
		if gotWelcomeCfg.GitHubEnabled() {
			t.Fatalf("expected GitHubEnabled=false, got %+v", gotWelcomeCfg.Canonical)
		}
		if captured.database != "orders" || captured.openAtStart != "" {
			t.Fatalf("expected orders fallback without chinook, got %q / %q", captured.database, captured.openAtStart)
		}
		if strings.Contains(captured.notice, "Save to GitHub") {
			t.Fatalf("notice should not advertise Save to GitHub when disabled: %q", captured.notice)
		}
	})

	t.Run("validation errors fail closed", func(t *testing.T) {
		stubSSHSession(t, &demoSSHUseMemoryStore, func() {})
		stubSSHSession(t, &demoSSHApplyRlimits, func(demoSSHRlimits) error { return errors.New("rlimit boom") })
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{}); err == nil || !strings.Contains(err.Error(), "rlimit boom") {
			t.Fatalf("expected rlimit failure, got %v", err)
		}

		stubSSHSession(t, &demoSSHApplyRlimits, func(demoSSHRlimits) error { return nil })
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{}); err == nil || !strings.Contains(err.Error(), "--workspace") {
			t.Fatalf("expected missing workspace error, got %v", err)
		}
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{workspace: "/tmp/ws"}); err == nil || !strings.Contains(err.Error(), "--template") {
			t.Fatalf("expected missing template error, got %v", err)
		}
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{workspace: "/tmp/ws", template: "/tmp/tpl", repo: demoSSHDefaultRepo, deadline: "not-a-time"}); err == nil || !strings.Contains(err.Error(), "--deadline") {
			t.Fatalf("expected invalid deadline error, got %v", err)
		}
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{workspace: "/tmp/ws", template: "/tmp/tpl", repo: demoSSHDefaultRepo, projectDir: "../bad"}); err == nil || !strings.Contains(err.Error(), "--project-dir") {
			t.Fatalf("expected invalid project-dir error, got %v", err)
		}
		stubSSHSession(t, &demoSSHRunWelcome, func(dtdemo.Config, ...tea.ProgramOption) (dtdemo.WelcomeResult, error) {
			return dtdemo.WelcomeResult{}, errors.New("welcome boom")
		})
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{workspace: "/tmp/ws", template: "/tmp/tpl", repo: demoSSHDefaultRepo, projectDir: "demo-project-1"}); err == nil || !strings.Contains(err.Error(), "welcome boom") {
			t.Fatalf("expected welcome error, got %v", err)
		}

		// Template copy failure on Explore fails closed.
		stubSSHSession(t, &demoSSHRunWelcome, func(dtdemo.Config, ...tea.ProgramOption) (dtdemo.WelcomeResult, error) {
			return dtdemo.WelcomeResult{Outcome: dtdemo.OutcomeExplore}, nil
		})
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{workspace: t.TempDir(), template: filepath.Join(t.TempDir(), "no-such-tpl"), repo: demoSSHDefaultRepo, projectDir: "demo-project-1"}); err == nil || !strings.Contains(err.Error(), "prepare demo workspace") {
			t.Fatalf("expected template copy failure, got %v", err)
		}

		// Register project failure when home is a regular file + nil ctx fallback.
		wsBad := t.TempDir()
		_ = os.WriteFile(filepath.Join(wsBad, "home"), []byte("not-a-dir"), 0o600)
		tplOK := makeDemoTemplate(t, "T")
		//nolint:staticcheck // intentionally testing nil context fallback
		if err := runDemoSSHSession(nil, demoSSHSessionFlags{workspace: wsBad, template: tplOK, repo: demoSSHDefaultRepo, projectDir: "demo-project-1"}); err == nil || !strings.Contains(err.Error(), "register demo project") {
			t.Fatalf("expected register project failure, got %v", err)
		}

		// Seeding chinook-local fails when home is a regular file and chinook.sqlite exists.
		tplWithChinook := makeDemoTemplate(t, "T")
		_ = os.MkdirAll(filepath.Join(tplWithChinook, "demo-project-1", ".demo-data"), 0o755)
		_ = os.WriteFile(filepath.Join(tplWithChinook, "demo-project-1", ".demo-data", "chinook.sqlite"), []byte("x"), 0o644)
		if err := runDemoSSHSession(context.Background(), demoSSHSessionFlags{workspace: wsBad, template: tplWithChinook, repo: demoSSHDefaultRepo, projectDir: "demo-project-1"}); err == nil || !strings.Contains(err.Error(), "seed chinook-local") {
			t.Fatalf("expected seed chinook-local failure, got %v", err)
		}

		// Corrupt settings file causes read settings error.
		wsCorrupt := t.TempDir()
		_ = os.MkdirAll(filepath.Join(wsCorrupt, "home"), 0o700)
		_ = os.WriteFile(filepath.Join(wsCorrupt, "home", dtconfig.ConfigFileName), []byte(":::bad-yaml:::\n\t["), 0o600)
		if err := registerDemoSSHProject(context.Background(), wsCorrupt, filepath.Join(tplOK, "demo-project-1")); err == nil || !strings.Contains(err.Error(), "read settings") {
			t.Fatalf("expected read settings error, got %v", err)
		}
	})
}

func TestDemoSSHSessionCleanupMode(t *testing.T) {
	validHex := "0123456789abcdef0123456789abcdef"

	t.Run("rejects relative path", func(t *testing.T) {
		if err := runDemoSSHCleanup("relative/" + validHex); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("expected relative path error, got %v", err)
		}
	})

	t.Run("rejects bad base names", func(t *testing.T) {
		for _, bad := range []string{"/tmp/short", "/tmp/0123456789ABCDEF0123456789ABCDEF", "/tmp/0123456789abcdef0123456789abcdeg"} {
			if err := runDemoSSHCleanup(bad); err == nil || !strings.Contains(err.Error(), "32 lowercase hex") {
				t.Fatalf("expected hex error for %q, got %v", bad, err)
			}
		}
	})

	t.Run("missing dir succeeds idempotently and lstat error fails", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), validHex)
		if err := runDemoSSHCleanup(missing); err != nil {
			t.Fatalf("missing dir should succeed: %v", err)
		}
		stubSSHSession(t, &demoSSHCleanupLstat, func(string) (os.FileInfo, error) {
			return nil, errors.New("eacces")
		})
		if err := runDemoSSHCleanup(missing); err == nil || !strings.Contains(err.Error(), "eacces") {
			t.Fatalf("expected lstat error, got %v", err)
		}
	})

	t.Run("fileOwnedByUID returns false for non-unix FileInfo", func(t *testing.T) {
		if fileOwnedByUID(nonUnixFileInfo{}, 1000) {
			t.Fatal("expected false for non-unix FileInfo")
		}
	})

	t.Run("rejects symlink or regular file at cleanup target", func(t *testing.T) {
		root := t.TempDir()
		realDir := filepath.Join(root, "real")
		_ = os.MkdirAll(realDir, 0o700)
		link := filepath.Join(root, validHex)
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		if err := runDemoSSHCleanup(link); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("expected non-dir error for symlink, got %v", err)
		}
	})

	t.Run("rejects directory owned by another uid via seam", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), validHex)
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		stubSSHSession(t, &demoSSHGeteuid, func() int { return os.Geteuid() + 999 })
		if err := runDemoSSHCleanup(target); err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Fatalf("expected ownership error, got %v", err)
		}
	})

	t.Run("removes contents without following symlinks and handles chmod 0500 dirs", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(root, "outside")
		if err := os.MkdirAll(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		outsideFile := filepath.Join(outside, "keep.txt")
		if err := os.WriteFile(outsideFile, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}

		target := filepath.Join(root, validHex)
		sub := filepath.Join(target, "repo", "locked")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("hi"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Symlink pointing outside target MUST NOT delete outside/keep.txt!
		if err := os.Symlink(outside, filepath.Join(target, "escape-link")); err != nil {
			t.Fatal(err)
		}
		// Lock subdir to test chmod recovery during cleanup.
		if err := os.Chmod(sub, 0o500); err != nil {
			t.Fatal(err)
		}

		cmd := demoSSHSessionCommand()
		cmd.SetArgs([]string{"--cleanup", target})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cleanup Execute: %v", err)
		}

		// Target directory itself remains (empty) for supervisor's os.Remove.
		entries, err := os.ReadDir(target)
		if err != nil {
			t.Fatalf("ReadDir target: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("target not empty after cleanup: %v", entries)
		}
		// Outside file is untouched!
		data, err := os.ReadFile(outsideFile)
		if err != nil || string(data) != "precious" {
			t.Fatalf("outside file was touched: err=%v data=%q", err, string(data))
		}
	})
}

func TestDemoSSHSessionParseGitHubRepoNoticeAndSeeding(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://github.com/datatug/datatug-demo-project", "datatug/datatug-demo-project"},
		{"https://github.com/datatug/datatug-demo-project.git", "datatug/datatug-demo-project"},
		{"http://github.com/datatug/datatug-demo-project/", "datatug/datatug-demo-project"},
		{"github.com/datatug/datatug-demo-project/", "datatug/datatug-demo-project"},
		{"datatug/datatug-demo-project", "datatug/datatug-demo-project"},
	}
	for _, tc := range cases {
		r, ok := parseGitHubRepo(tc.raw)
		if !ok || r.FullName() != tc.want {
			t.Errorf("parseGitHubRepo(%q) = (%q, %v), want (%q, true)", tc.raw, r.FullName(), ok, tc.want)
		}
	}
	for _, nonGH := range []string{
		"", "onlyone", "https://github.com/a/b/c", "://bad-url",
		"file:///tmp/repo", "/tmp/local/repo", "./local/repo", "~/repo",
		"https://gitlab.com/a/b", "a/..", "a/b c",
	} {
		if r, ok := parseGitHubRepo(nonGH); ok {
			t.Errorf("parseGitHubRepo(%q) = (%+v, true), want false", nonGH, r)
		}
	}

	if got := demoSSHNotice(time.Date(2026, 11, 26, 9, 5, 0, 0, time.UTC), true); !strings.Contains(got, "09:05 UTC") || !strings.Contains(got, "Save to GitHub") {
		t.Fatalf("notice with github = %q", got)
	}
	if got := demoSSHNotice(time.Time{}, true); strings.Contains(got, "UTC") || !strings.Contains(got, "Save to GitHub") {
		t.Fatalf("zero deadline notice = %q", got)
	}
	if got := demoSSHNotice(time.Date(2026, 11, 26, 9, 5, 0, 0, time.UTC), false); !strings.Contains(got, "09:05 UTC") || strings.Contains(got, "Save to GitHub") {
		t.Fatalf("notice without github = %q", got)
	}

	// Registering twice updates the existing demo entry instead of duplicating.
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, "home"), 0o700)
	tpl1 := makeDemoTemplate(t, "First Title")
	tpl2 := makeDemoTemplate(t, "Second Title")
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	if err := registerDemoSSHProject(context.Background(), ws, filepath.Join(tpl1, "demo-project-1")); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := registerDemoSSHProject(context.Background(), ws, filepath.Join(tpl2, "demo-project-1")); err != nil {
		t.Fatalf("second register: %v", err)
	}
	settings, err := dtconfig.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if len(settings.Projects) != 1 || settings.Projects[0].Title != "Second Title" {
		t.Fatalf("expected updated single project, got %+v", settings.Projects)
	}
	// Empty catalog list falls back to preferred database with empty openAtStart.
	if db, tbl := resolveDemoSSHStartTarget(context.Background(), filepath.Join(tpl1, "demo-project-1"), "local"); db != demoSSHPreferredDB || tbl != "" {
		t.Fatalf("fallback db/tbl = (%q, %q), want (%q, \"\")", db, tbl, demoSSHPreferredDB)
	}

	// seedDemoSSHChinookLocal edge cases: directory at chinook path is skipped; stat error returned.
	dirChinookProj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dirChinookProj, ".demo-data", "chinook.sqlite"), 0o755)
	if err := seedDemoSSHChinookLocal(ws, dirChinookProj); err != nil {
		t.Fatalf("dir at chinook.sqlite should skip: %v", err)
	}
	fileAsDemoData := t.TempDir()
	_ = os.WriteFile(filepath.Join(fileAsDemoData, ".demo-data"), []byte("not-a-dir"), 0o600)
	if err := seedDemoSSHChinookLocal(ws, fileAsDemoData); err == nil {
		t.Fatal("expected stat error when .demo-data is a regular file")
	}
}

type nonUnixFileInfo struct{ os.FileInfo }

func (nonUnixFileInfo) Sys() any { return nil }

func TestRaiseDemoSSHOOMScore(t *testing.T) {
	old := demoSSHOOMScoreAdjPath
	t.Cleanup(func() { demoSSHOOMScoreAdjPath = old })
	demoSSHOOMScoreAdjPath = filepath.Join(t.TempDir(), "oom_score_adj")
	if err := os.WriteFile(demoSSHOOMScoreAdjPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := raiseDemoSSHOOMScore(); err != nil {
		t.Fatalf("raiseDemoSSHOOMScore: %v", err)
	}
	got, _ := os.ReadFile(demoSSHOOMScoreAdjPath)
	if string(got) != "900" {
		t.Fatalf("oom_score_adj = %q, want 900", got)
	}
}

func TestDemoSSHSessionIsWithinDir(t *testing.T) {
	for _, tc := range []struct {
		root, target string
		want         bool
	}{
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b", false},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a/b/../c", false},
		{"/", "/x", true},
		{"/", "/", false},
	} {
		if got := isWithinDir(tc.root, tc.target); got != tc.want {
			t.Errorf("isWithinDir(%q, %q) = %v, want %v", tc.root, tc.target, got, tc.want)
		}
	}
}

func TestDemoSSHSessionCopyTemplateTreeErrors(t *testing.T) {
	newSrc := func(t *testing.T) string {
		src := t.TempDir()
		if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return src
	}
	fail := errors.New("injected")
	t.Run("destination under a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(blocker, nil, 0o644)
		if err := copyTemplateTree(newSrc(t), filepath.Join(blocker, "repo")); err == nil || !strings.Contains(err.Error(), "create repo dir") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unreadable source dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any directory")
		}
		src := newSrc(t)
		_ = os.Chmod(filepath.Join(src, "sub"), 0o000)
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "sub"), 0o755) })
		if err := copyTemplateTree(src, filepath.Join(t.TempDir(), "repo")); err == nil || !strings.Contains(err.Error(), "read template dir") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("lstat failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHTemplateLstat, func(string) (os.FileInfo, error) { return nil, fail })
		if err := copyTemplateTree(newSrc(t), filepath.Join(t.TempDir(), "repo")); err == nil || !strings.Contains(err.Error(), "lstat") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("destination subdir is a file", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(dst, 0o755)
		_ = os.WriteFile(filepath.Join(dst, "sub"), nil, 0o644)
		if err := copyTemplateTree(newSrc(t), dst); err == nil || !strings.Contains(err.Error(), "mkdir") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("dir chmod failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHTemplateChmod, func(string, os.FileMode) error { return fail })
		if err := copyTemplateTree(newSrc(t), filepath.Join(t.TempDir(), "repo")); err == nil || !strings.Contains(err.Error(), "chmod dir") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unreadable source file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any file")
		}
		src := newSrc(t)
		_ = os.Chmod(filepath.Join(src, "sub", "f.txt"), 0o000)
		if err := copyTemplateTree(src, filepath.Join(t.TempDir(), "repo")); err == nil || !strings.Contains(err.Error(), "open") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("destination file is a directory", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(filepath.Join(dst, "sub", "f.txt"), 0o755)
		if err := copyTemplateTree(newSrc(t), dst); err == nil || !strings.Contains(err.Error(), "create") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("copy failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHTemplateCopy, func(io.Writer, io.Reader) (int64, error) { return 0, fail })
		if err := copyTemplateTree(newSrc(t), filepath.Join(t.TempDir(), "repo")); err == nil || !strings.Contains(err.Error(), "copy") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("file chmod failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHTemplateChmod, func(p string, m os.FileMode) error {
			if strings.HasSuffix(p, "f.txt") {
				return fail
			}
			return os.Chmod(p, m)
		})
		if err := copyTemplateTree(newSrc(t), filepath.Join(t.TempDir(), "repo")); err == nil || !strings.Contains(err.Error(), "chmod \"") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDemoSSHSessionRemoveDirContentsErrors(t *testing.T) {
	fail := errors.New("injected")
	tree := func(t *testing.T) string {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, "sub", "f"), nil, 0o644)
		return dir
	}
	t.Run("not a directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "f")
		_ = os.WriteFile(f, nil, 0o644)
		if err := removeDirContentsNoFollow(f); err == nil || !strings.Contains(err.Error(), "read dir") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("entry vanished", func(t *testing.T) {
		stubSSHSession(t, &demoSSHCleanupLstat, func(string) (os.FileInfo, error) { return nil, os.ErrNotExist })
		if err := removeDirContentsNoFollow(tree(t)); err != nil {
			t.Fatalf("vanished entries are skipped, got %v", err)
		}
	})
	t.Run("lstat failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHCleanupLstat, func(string) (os.FileInfo, error) { return nil, fail })
		if err := removeDirContentsNoFollow(tree(t)); err == nil || !strings.Contains(err.Error(), "lstat") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nested failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHCleanupLstat, func(p string) (os.FileInfo, error) {
			if strings.HasSuffix(p, string(filepath.Separator)+"f") {
				return nil, fail
			}
			return os.Lstat(p)
		})
		if err := removeDirContentsNoFollow(tree(t)); err == nil || !strings.Contains(err.Error(), "lstat") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("remove failure", func(t *testing.T) {
		stubSSHSession(t, &demoSSHCleanupRemove, func(string) error { return fail })
		if err := removeDirContentsNoFollow(tree(t)); err == nil || !strings.Contains(err.Error(), "remove") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDemoSSHSessionRegisterProjectSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into read-only directories")
	}
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	ws := t.TempDir()
	home := filepath.Join(ws, "home")
	// No settings file yet (read succeeds as empty), but home is read-only.
	if err := os.MkdirAll(home, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o755) })
	err := registerDemoSSHProject(context.Background(), ws, filepath.Join(t.TempDir(), "demo-project-1"))
	if err == nil || !strings.Contains(err.Error(), "save settings") {
		t.Fatalf("err = %v, want save settings error", err)
	}
}
