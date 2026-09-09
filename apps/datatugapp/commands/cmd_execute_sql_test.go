package commands

import (
	"bytes"
	"strings"
	"testing"
)

// TestUpdateUrlConfigCommandArgs_HelpDoesNotPanic is the repro/regression
// test for the audit's "`datatug updateUrlConfig --help` panics" finding:
// updateUrlConfigCommandArgs registered `flags.StringP("host", "h", ...)`,
// and cobra auto-registers its own `-h, --help` flag on first Execute -
// colliding on the "h" shorthand and panicking
// ("unable to redefine 'h' shorthand"). Every other command in this package
// leaves --help alone, so --host is the one that has to move.
func TestUpdateUrlConfigCommandArgs_HelpDoesNotPanic(t *testing.T) {
	cmd := updateUrlConfigCommandArgs()
	cmd.SetArgs([]string{"--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(--help): %v", err)
	}
	if !strings.Contains(out.String(), "--host") {
		t.Errorf("help output missing --host flag: %s", out.String())
	}
}

func TestUpdateUrlConfigCommandArgs_HostFlagStillParses(t *testing.T) {
	cmd := updateUrlConfigCommandArgs()
	if err := cmd.Flags().Set("host", "example.com"); err != nil {
		t.Fatalf("Set(host): %v", err)
	}
	got, err := cmd.Flags().GetString("host")
	if err != nil {
		t.Fatalf("GetString(host): %v", err)
	}
	if got != "example.com" {
		t.Errorf("host = %q, want %q", got, "example.com")
	}
}
