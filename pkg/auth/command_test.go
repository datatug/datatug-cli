package auth

import (
	"testing"
)

func TestCommand(t *testing.T) {
	cmd := Command()
	if cmd == nil {
		t.Fatal("Command() returned nil")
	}
	if cmd.Name != "auth" {
		t.Errorf("expected Name %q, got %q", "auth", cmd.Name)
	}
	if len(cmd.Commands) != 1 {
		t.Fatalf("expected 1 subcommand, got %d", len(cmd.Commands))
	}
	if cmd.Commands[0].Name != "google" {
		t.Errorf("expected subcommand Name %q, got %q", "google", cmd.Commands[0].Name)
	}
}
