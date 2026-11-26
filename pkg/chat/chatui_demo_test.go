package chat

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestChatUIDemoBrowserActionsDisplayNotice(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.SetBrowserURL("http://example.test")
	u.openBrowser = func(string) error { t.Fatal("SSH demo opened a browser"); return nil }
	const notice = "Not available in the SSH demo"
	u.DisableBrowserHandoff(notice)
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for _, key := range []string{"f5", "ctrl+w"} {
		if key == "ctrl+w" {
			u.shell.FocusLeftPanel()
		}
		_, cmd := u.shell.Update(uitest.Key(key))
		if cmd != nil || !strings.Contains(u.shell.View().Content, notice) {
			t.Fatalf("%s did not show the demo notice", key)
		}
	}
}

func TestChatUIShowsScheduledWarning(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	const notice = "Your SSH demo session ends in one minute or less."
	warnAt := time.Now().Add(20 * time.Millisecond)
	u.SetSessionWarning(warnAt, notice)
	u.toolbarHidden = true
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	var program *tea.Program
	t.Cleanup(SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) {
		program = p
		return p.Run()
	}))
	var fired time.Time
	err := u.Run(tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(sessionWarningMsg); ok {
				fired = time.Now()
				go program.Send(tea.Quit())
			}
			return msg
		}))
	if err != nil || fired.IsZero() || fired.Before(warnAt) {
		t.Fatalf("warning fired at %s, expected at %s, error %v", fired, warnAt, err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(u.shell.View().Content, notice) {
		t.Fatal("the timer warning did not appear in the workspace")
	}
}
