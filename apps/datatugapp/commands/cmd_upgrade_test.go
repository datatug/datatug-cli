package commands

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	cliinstallcobracmd "github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
	selfupdatecobracmd "github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

// --- command shape ---

func TestUpgradeCommand_Shape(t *testing.T) {
	t.Parallel()

	cmd := UpgradeCommand("1.2.3")
	if cmd.Name() != "upgrade" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "upgrade")
	}
	// No "update" alias anywhere, ever (cli-install#req:update-alias-policy).
	if cmd.HasAlias("update") {
		t.Error(`upgrade must not alias "update" (cli-install#req:update-alias-policy)`)
	}
	for _, name := range []string{"all", "check", "yes", "dry-run", "format"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	// upgrade never takes a caller-chosen destination (cli-install#req:
	// upgrade-per-target-policy: "always acts on the copy status-probing
	// already located").
	if cmd.Flags().Lookup("dir") != nil {
		t.Error("unexpected --dir flag; upgrade has no --dir")
	}
	if f := cmd.Flags().Lookup("yes"); f.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want y", f.Shorthand)
	}
	if cmd.Short == "" {
		t.Error("Short is empty, want a description")
	}
}

// --- upgradeErrors: reuses installErrors' Failure, adds UpgradesAvailable ---

// Failure must behave exactly like installErrors.Failure for every kind,
// since upgradeErrors embeds it unchanged.
func TestUpgradeErrors_Failure_MatchesInstallErrors(t *testing.T) {
	t.Parallel()

	cases := []error{
		&cliinstallcobracmd.UsageError{Err: errors.New("invalid --format")},
		&selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: errors.New("nosuchcli: not a known install target")},
		&selfupdate.Failure{Kind: selfupdate.KindNoInstallDir, Err: errors.New("no per-user bin directory on PATH")},
		&selfupdate.Failure{Kind: selfupdate.KindDestinationExists, Err: errors.New("destination already exists")},
		&selfupdate.Failure{Kind: selfupdate.KindAmbiguous, Err: errors.New("ambiguous")},
		&selfupdate.Failure{Kind: selfupdate.KindChecksum, Err: errors.New("checksum mismatch")},
		errors.New("plain error"),
	}
	for _, err := range cases {
		got := (upgradeErrors{}).Failure(err)
		want := (installErrors{}).Failure(err)
		var gotEC, wantEC ExitCoder
		if !errors.As(got, &gotEC) || !errors.As(want, &wantEC) {
			t.Fatalf("Failure(%v): got=%v (%T) want=%v (%T), both must be ExitCoder", err, got, got, want, want)
		}
		if gotEC.ExitCode() != wantEC.ExitCode() {
			t.Errorf("Failure(%v) exit code = %d, want %d (same as installErrors)", err, gotEC.ExitCode(), wantEC.ExitCode())
		}
	}
}

// UpgradesAvailable must return nil (exit 0): datatug reserves no dedicated
// exit code for "an upgrade is available", mirroring selfUpdateErrors.
// UpdateAvailable (plan task-15 exit mapping).
func TestUpgradeErrors_UpgradesAvailable_ReturnsNil(t *testing.T) {
	t.Parallel()

	cases := [][]cliinstall.UpgradeResult{
		nil,
		{{Target: "datatug", Verdict: selfupdate.UpdateAvailable, Current: "1.0.0", Latest: "1.1.0"}},
		{{Target: "ovdb", Verdict: selfupdate.Undetermined}},
	}
	for _, res := range cases {
		if err := (upgradeErrors{}).UpgradesAvailable(res); err != nil {
			t.Errorf("UpgradesAvailable(%+v) = %v, want nil", res, err)
		}
	}
}

// --- self-update ≡ upgrade datatug: same Config by construction ---

// TestDatatugSelfUpdateConfig_SameForBothCommands proves SelfUpdateCommand
// and UpgradeCommand build their selfupdate.Config from the exact same
// datatugSelfUpdateConfig helper, structurally: calling it twice with the
// same version yields an equal Config either way
// (cli-install#req:self-update-equals-upgrade-self).
func TestDatatugSelfUpdateConfig_SameForBothCommands(t *testing.T) {
	t.Parallel()

	a := datatugSelfUpdateConfig("1.2.3")
	b := datatugSelfUpdateConfig("1.2.3")
	if !reflect.DeepEqual(a, b) {
		t.Errorf("datatugSelfUpdateConfig(\"1.2.3\") is not stable/shared: %+v != %+v", a, b)
	}
}

// --- end-to-end exit-code contract, fully offline ---

// `upgrade nosuchcli` never reaches a status probe or a release lookup:
// cliinstall.CheckUpgrades/PlanUpgrade validate every name against the
// catalog before probing anything, exactly like install's own
// unknown-target path, so this is inherently offline
// (REQ: no-network-in-tests).
func TestUpgradeCommand_NoSuchTarget_ExitCodeContract(t *testing.T) {
	t.Parallel()

	cmd := UpgradeCommand("1.2.3")
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"nosuchcli"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil error for an unknown upgrade target")
	}
	var ec ExitCoder
	if !errors.As(err, &ec) {
		t.Fatalf("Execute() error = %v (%T), want an ExitCoder", err, err)
	}
	if ec.ExitCode() != 2 {
		t.Errorf("Execute() exit code = %d, want 2 (invalid arguments)", ec.ExitCode())
	}
	if !strings.Contains(err.Error(), "nosuchcli") {
		t.Errorf("error %q does not name the unknown target", err.Error())
	}
}

func TestUpgradeCommand_InvalidFormat_IsUsageError(t *testing.T) {
	t.Parallel()

	cmd := UpgradeCommand("1.2.3")
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--format", "yaml"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil error for --format yaml")
	}
	var ec ExitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 2 {
		t.Errorf("Execute() exit code = %v, want 2 (invalid arguments)", err)
	}
	if !strings.Contains(err.Error(), "--format") {
		t.Errorf("error %q does not mention --format", err.Error())
	}
}

// --- self-update-equals-upgrade-self, against a fake releases server ---

// fakeInstallEnv is a fully hermetic cliinstall.InstallEnv: no real PATH
// scan, no real filesystem, no real process execution
// (REQ: no-network-in-tests). Only the host row is exercised in the tests
// below, and the host's classification/version come from HostConfig, never
// from this Env (cli-install#req:host-target-is-running-binary), but
// resolveUpgradeCandidates still calls Probe over every named entry for
// diagnostics, so every field must be set to avoid a nil-func panic.
func fakeInstallEnv() cliinstall.InstallEnv {
	return cliinstall.InstallEnv{
		Env: cliinstall.Env{
			PathDirs:     func() []string { return nil },
			HostDir:      func() (string, error) { return "", errors.New("no host dir in test") },
			IsExecutable: func(string) bool { return false },
			EvalSymlinks: func(p string) (string, error) { return p, nil },
			Run: func(context.Context, string, []string) ([]byte, error) {
				return nil, errors.New("process execution disabled in test")
			},
		},
		UserHomeDir: func() (string, error) { return "", errors.New("disabled in test") },
		Getenv:      func(string) string { return "" },
		MkdirAll:    func(string, fs.FileMode) error { return errors.New("disabled in test") },
	}
}

// TestSelfUpdateEqualsUpgradeSelf_CheckContract proves
// `datatug self-update --check` and `datatug upgrade datatug --check`
// reach the same verdict for the same fake releases server, built from the
// exact same Config (selfUpdateConfigForTest, cmd_self_update_test.go's own
// seam), for both an up-to-date and an update-available scenario
// (cli-install#req:self-update-equals-upgrade-self,
// cli-install#req:upgrade-check: "--check with an update available maps
// like self-update --check").
func TestSelfUpdateEqualsUpgradeSelf_CheckContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ver  string
		body string
	}{
		{name: "up to date", ver: "1.0.0", body: `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`},
		{name: "update available", ver: "1.0.0", body: `[{"tag_name":"v1.1.0","prerelease":false,"draft":false}]`},
		{name: "undetermined dev build", ver: "dev", body: `[{"tag_name":"v1.1.0","prerelease":false,"draft":false}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := releasesServer(t, c.body, 200)
			cfg := selfUpdateConfigForTest(t, c.ver, srv.URL, srv.Client())

			selfCmd := selfupdatecobracmd.New(cfg, selfupdatecobracmd.CommandOptions{Errors: selfUpdateErrors{}})
			selfCmd.SetOut(&strings.Builder{})
			selfCmd.SetErr(&strings.Builder{})
			selfCmd.SetArgs([]string{"--check"})
			selfErr := selfCmd.Execute()

			upCmd := cliinstallcobracmd.NewUpgrade(cliinstallcobracmd.UpgradeCommandOptions{
				HostID:     "datatug",
				Errors:     upgradeErrors{},
				HostConfig: cfg,
				Env:        fakeInstallEnv(),
			})
			upCmd.SetOut(&strings.Builder{})
			upCmd.SetErr(&strings.Builder{})
			upCmd.SetArgs([]string{"datatug", "--check"})
			upErr := upCmd.Execute()

			if (selfErr == nil) != (upErr == nil) {
				t.Fatalf("self-update --check err=%v, upgrade datatug --check err=%v; want the same verdict (cli-install#req:self-update-equals-upgrade-self)", selfErr, upErr)
			}
		})
	}

	t.Run("release lookup failure fails both the same way", func(t *testing.T) {
		t.Parallel()
		srv := releasesServer(t, `not json`, 500)
		cfg := selfUpdateConfigForTest(t, "1.0.0", srv.URL, srv.Client())

		selfCmd := selfupdatecobracmd.New(cfg, selfupdatecobracmd.CommandOptions{Errors: selfUpdateErrors{}})
		selfCmd.SetOut(&strings.Builder{})
		selfCmd.SetErr(&strings.Builder{})
		selfCmd.SetArgs([]string{"--check"})
		selfErr := selfCmd.Execute()
		if selfErr == nil {
			t.Fatal("expected self-update --check to fail on a release-lookup error")
		}

		upCmd := cliinstallcobracmd.NewUpgrade(cliinstallcobracmd.UpgradeCommandOptions{
			HostID:     "datatug",
			Errors:     upgradeErrors{},
			HostConfig: cfg,
			Env:        fakeInstallEnv(),
		})
		upCmd.SetOut(&strings.Builder{})
		upCmd.SetErr(&strings.Builder{})
		upCmd.SetArgs([]string{"datatug", "--check"})
		upErr := upCmd.Execute()
		if upErr == nil {
			t.Fatal("expected upgrade datatug --check to fail the same way")
		}

		var selfEC, upEC ExitCoder
		if !errors.As(selfErr, &selfEC) || !errors.As(upErr, &upEC) {
			t.Fatalf("want both errors to be ExitCoder: self=%v (%T) up=%v (%T)", selfErr, selfErr, upErr, upErr)
		}
		if selfEC.ExitCode() != upEC.ExitCode() {
			t.Errorf("exit codes differ: self-update=%d upgrade=%d", selfEC.ExitCode(), upEC.ExitCode())
		}
	})
}
