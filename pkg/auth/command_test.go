package auth

import (
	"testing"
)

func TestCommand(t *testing.T) {
	cmd := Command()
	if cmd == nil {
		t.Fatal("Command() returned nil")
	}
	if cmd.Name() != "auth" {
		t.Errorf("expected Name %q, got %q", "auth", cmd.Name())
	}
	want := map[string]bool{"login": false, "product": false, "status": false, "logout": false, "google": false}
	for _, child := range cmd.Commands() {
		if _, ok := want[child.Name()]; ok {
			want[child.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing %q subcommand", name)
		}
	}
}

func TestWhoamiCommand(t *testing.T) {
	cmd := WhoamiCommand()
	if cmd == nil {
		t.Fatal("WhoamiCommand() returned nil")
	}
	if cmd.Name() != "whoami" {
		t.Errorf("expected Name %q, got %q", "whoami", cmd.Name())
	}
}
