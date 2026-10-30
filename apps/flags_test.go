package apps

import (
	"testing"
)

func TestFlags(t *testing.T) {
	t.Parallel()
	if TUIFlagName != "tui" {
		t.Errorf("expected tui, got %q", TUIFlagName)
	}
	if TUIFlagShorthand != "t" {
		t.Errorf("expected t, got %q", TUIFlagShorthand)
	}
	if TUIFlagUsage == "" {
		t.Error("expected non-empty usage")
	}
}
