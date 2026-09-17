package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- command shape ---

func TestInstallCommand_Shape(t *testing.T) {
	t.Parallel()

	cmd := InstallCommand()
	if cmd.Name() != "install" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "install")
	}
	for _, name := range []string{"all", "yes", "dry-run", "dir", "format"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	if f := cmd.Flags().Lookup("yes"); f.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want y", f.Shorthand)
	}
	if cmd.Short == "" {
		t.Error("Short is empty, want a description")
	}
}

// --- installErrors: datatug's own exit-code contract for install ---

// Failure must map every kind of install failure — usage errors, all three
// cli-install-only kinds explicitly, every self-update-shared kind, and any
// plain error — onto exit code 1
// (cli-install#req:host-owned-exit-codes).
func TestInstallErrors_Failure_MapsToExitOne(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{"usage error", &cobracmd.UsageError{Err: errors.New("invalid --format")}},
		{"unknown target", &selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: errors.New("nosuchcli: not a known install target; valid ids: ingitdb, ovdb, specscore")}},
		{"no install dir", &selfupdate.Failure{Kind: selfupdate.KindNoInstallDir, Err: errors.New("no per-user bin directory on PATH")}},
		{"destination exists", &selfupdate.Failure{Kind: selfupdate.KindDestinationExists, Path: "/home/alex/.local/bin/ovdb", Err: errors.New("destination already exists")}},
		{"self-update-shared kind", &selfupdate.Failure{Kind: selfupdate.KindChecksum, Err: errors.New("checksum mismatch")}},
		{"plain error", errors.New("plain error")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := (installErrors{}).Failure(c.err)
			var ec ExitCoder
			if !errors.As(got, &ec) {
				t.Fatalf("Failure(%v) = %v (%T), want an ExitCoder", c.err, got, got)
			}
			if ec.ExitCode() != 1 {
				t.Errorf("Failure(%v) exit code = %d, want 1", c.err, ec.ExitCode())
			}
		})
	}
}

// TestInstallErrors_UnknownTarget_MessageListsValidIDs proves the "usage
// message" datatug reports for an unknown target is the underlying
// cliinstall error itself, which already names the unknown target and
// lists valid catalog ids (cli-install#req:unknown-target-refused) — no
// extra wrapping is needed or performed.
func TestInstallErrors_UnknownTarget_MessageListsValidIDs(t *testing.T) {
	t.Parallel()

	err := &selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: errors.New("nosuchcli: not a known install target; valid ids: chatwright, codegrapher, cover100, datatug, ingitdb, ovdb, specscore, synchestra, wb")}
	got := (installErrors{}).Failure(err)
	if !strings.Contains(got.Error(), "valid ids") {
		t.Errorf("Failure error = %q, want it to list valid ids", got.Error())
	}
}

// TestInstallErrors_Failure_NilReturnsNil proves the defensive nil guard:
// cliinstall/cobracmd v0.19.0's runInstall calls mapFailure(opts,
// plan.Failure()) and mapFailure(opts, result.Failure()) unconditionally,
// and BatchResult.Failure() returns nil for a fully successful batch
// (including a successful --dry-run), so opts.Errors.Failure(nil) is a
// real, reachable call on the ordinary success path — see the doc comment
// on installErrors.Failure for how this was found (a manual `datatug
// install ovdb --dry-run` smoke run panicked before this guard was added).
func TestInstallErrors_Failure_NilReturnsNil(t *testing.T) {
	t.Parallel()

	if got := (installErrors{}).Failure(nil); got != nil {
		t.Errorf("Failure(nil) = %v, want nil", got)
	}
}

// --- end-to-end exit-code contract, fully offline ---
//
// `install nosuchcli` never reaches a status probe, a release lookup, or
// any write: cliinstall.Plan validates every name against the catalog
// BEFORE probing anything (cli-install#req:unknown-target-refused: "MUST
// fail before any confirmation, network request or write"), so exercising
// the real InstallCommand() — built against the real DefaultInstallEnv(),
// exactly as main.go wires it — against an unknown name is inherently
// offline; no env/network seam is needed to keep this test safe to run in
// CI (REQ: no-network-in-tests).
func TestInstallCommand_NoSuchTarget_ExitCodeContract(t *testing.T) {
	t.Parallel()

	cmd := InstallCommand()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"nosuchcli"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil error for an unknown install target")
	}
	var ec ExitCoder
	if !errors.As(err, &ec) {
		t.Fatalf("Execute() error = %v (%T), want an ExitCoder", err, err)
	}
	if ec.ExitCode() != 1 {
		t.Errorf("Execute() exit code = %d, want 1", ec.ExitCode())
	}
	if !strings.Contains(err.Error(), "nosuchcli") {
		t.Errorf("error %q does not name the unknown target", err.Error())
	}
}

// TestInstallCommand_InvalidFormat_IsUsageError proves the usage-error path
// through installErrors.Failure is reachable end-to-end, the same way
// TestInstallCommand_NoSuchTarget_ExitCodeContract proves the
// KindUnknownTarget path. commands.Exit (like selfUpdateErrors' own
// mapping) carries the message forward as a plain exitError rather than
// preserving *cobracmd.UsageError in the chain, matching datatug's existing
// self-update convention, so this only asserts the exit code and message.
func TestInstallCommand_InvalidFormat_IsUsageError(t *testing.T) {
	t.Parallel()

	cmd := InstallCommand()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--format", "yaml"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil error for --format yaml")
	}
	var ec ExitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Errorf("Execute() exit code = %v, want 1", err)
	}
	if !strings.Contains(err.Error(), "--format") {
		t.Errorf("error %q does not mention --format", err.Error())
	}
}
