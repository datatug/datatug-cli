package chat

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/apimonitor"
)

func TestUnifiedWorkspaceBridgeMonitorStartupFailures(t *testing.T) {
	previous, random := apimonitor.Default, readRandom
	t.Cleanup(func() { apimonitor.Default, readRandom = previous, random })
	apimonitor.Default = &apimonitor.Registry{}
	for i := 0; i < apimonitor.MaxServers; i++ {
		apimonitor.Default.NewServer("occupied")
	}
	readRandom = func([]byte) (int, error) { t.Fatal("admission must precede capability generation"); return 0, nil }
	if _, err := StartBrowserBridge(nil); !errors.Is(err, apimonitor.ErrRegistryFull) {
		t.Fatalf("admission: %v", err)
	}
	apimonitor.Default = &apimonitor.Registry{}
	readRandom = func([]byte) (int, error) { return 0, errors.New("private entropy failure") }
	if _, err := StartBrowserBridge(nil); err == nil {
		t.Fatal("expected entropy error")
	}
	if got := apimonitor.Default.Snapshot(); got.State != "error" || got.Error == "private entropy failure" {
		t.Fatalf("failed startup: %+v", got)
	}
}
