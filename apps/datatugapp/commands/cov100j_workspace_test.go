package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

func TestSetupConversationAsk(t *testing.T) {
	t.Parallel()
	conv := setupConversation{}
	turn, err := conv.Ask(context.Background(), "prompt")
	require.NoError(t, err)
	require.Contains(t, turn.Text, "Chat is ready for setup")
}

func TestExplorePaneTitleAndEsc(t *testing.T) {
	t.Parallel()
	pane := &explorePane{menu: true, search: "find"}
	require.Equal(t, "Explorer [F6]", pane.Title())
	require.True(t, pane.CapturesEsc())

	pane.menu = false
	pane.search = ""
	require.Equal(t, "Explore", pane.Title())
	require.False(t, pane.CapturesEsc())
}

func TestExplorePaneViewsAndUpdates(t *testing.T) {
	t.Parallel()
	modules := []datatugui.Module{
		{ID: "projects", Text: "Projects", Root: func() nav.Page { return nav.Page{Content: nav.Static("p", "projects")} }},
		{ID: "settings", Text: "Settings", Root: func() nav.Page { return nav.Page{Content: nav.Static("s", "settings")} }},
	}
	state := &exploreWorkspace{
		app:     datatugui.New(modules, datatugui.Options{Start: "projects"}, nav.WithoutLogin()),
		modules: modules,
	}
	pane := &explorePane{workspace: state}

	// Init
	require.NotNil(t, pane.Init())

	// View with wide width (>= MinWidthForMenu)
	viewWide := pane.View(120, 30, true)
	require.Contains(t, viewWide, "Modules ·")

	// View with narrow width (< MinWidthForMenu)
	viewNarrow := pane.View(20, 10, true)
	require.Contains(t, viewNarrow, "Modules ·")

	// View with content focused
	pane.contentFocused = true
	viewFocused := pane.View(80, 20, true)
	require.Contains(t, viewFocused, "Page · Tab modules")
	pane.contentFocused = false

	// View with search filter that matches nothing
	pane.search = "nonexistent"
	viewEmpty := pane.View(80, 20, true)
	require.Contains(t, viewEmpty, "No matching modules")
	pane.search = "set"
	viewSearch := pane.View(80, 20, true)
	require.Contains(t, viewSearch, "Search: set")
	pane.search = ""

	step := func(msg tea.Msg) tea.Cmd {
		p, cmd := pane.Update(msg)
		pane = p.(*explorePane)
		return cmd
	}

	// Update tab / shift+tab toggles focus
	step(tea.KeyPressMsg{Code: tea.KeyTab})
	require.True(t, pane.contentFocused)
	step(tea.KeyPressMsg{Code: tea.KeyTab})
	require.False(t, pane.contentFocused)

	// Update navigation keys when !contentFocused
	// up, left
	step(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, 0, pane.index)
	step(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, 0, pane.index)

	// down, right
	step(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, 1, pane.index)
	step(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Equal(t, 1, pane.index)

	// enter opens module and focuses content
	cmd := step(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, pane.contentFocused)
	require.NotNil(t, cmd)

	// esc clears search
	pane.contentFocused = false
	pane.search = "hello"
	pane.index = 1
	step(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, "", pane.search)
	require.Equal(t, 0, pane.index)

	// backspace trims search
	pane.search = "abc"
	step(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Equal(t, "ab", pane.search)

	// printable text adds to search
	step(tea.KeyPressMsg{Text: "z"})
	require.Equal(t, "abz", pane.search)

	// other key (e.g. F1) does not change search or error
	step(tea.KeyPressMsg{Code: tea.KeyF1})

	// enter when visibleModules is empty does not panic or change focus
	pane.search = "nomatchanything"
	pane.contentFocused = false
	step(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, pane.contentFocused)

	// Non-key message delegates to app
	cmd = step(tea.WindowSizeMsg{Width: 80, Height: 24})
	require.NotNil(t, pane)
}

// Not parallel: mutates runChatProjectFunc and getChatSettings seams.
func TestRunUnifiedAppBranches(t *testing.T) {
	origRunChat := runChatProjectFunc
	origGetSettings := getChatSettings
	origLastChat := lastChatOptionsPath
	t.Cleanup(func() {
		runChatProjectFunc = origRunChat
		getChatSettings = origGetSettings
		lastChatOptionsPath = origLastChat
	})

	t.Run("happy path exit when next is empty", func(t *testing.T) {
		runChatProjectFunc = func(cmd *cobra.Command, opts chatOptions) (string, error) {
			return "", nil
		}
		err := runUnifiedApp(nil, datatugui.Options{})
		require.NoError(t, err)
	})

	t.Run("switch to new project in loop", func(t *testing.T) {
		calls := 0
		runChatProjectFunc = func(cmd *cobra.Command, opts chatOptions) (string, error) {
			calls++
			if calls == 1 {
				return "switched-project", nil
			}
			return "", nil
		}
		err := runUnifiedApp(nil, datatugui.Options{})
		require.NoError(t, err)
		require.Equal(t, 2, calls)
	})

	t.Run("resolve chat ai error falls back to empty ai", func(t *testing.T) {
		prefFile := filepath.Join(t.TempDir(), "chat-last.json")
		require.NoError(t, os.WriteFile(prefFile, []byte(`{"ai":"nonexistent"}`), 0600))
		lastChatOptionsPath = func() (string, error) { return prefFile, nil }

		runChatProjectFunc = func(cmd *cobra.Command, opts chatOptions) (string, error) {
			return "", nil
		}
		getChatSettings = func() (dtconfig.Settings, error) {
			return dtconfig.Settings{}, nil
		}
		err := runUnifiedApp(nil, datatugui.Options{})
		require.NoError(t, err)
	})

	t.Run("runChatProject returns error and UI run succeeds", func(t *testing.T) {
		prefFile := filepath.Join(t.TempDir(), "chat-last.json")
		require.NoError(t, os.WriteFile(prefFile, []byte(`{}`), 0600))
		lastChatOptionsPath = func() (string, error) { return prefFile, nil }

		runChatProjectFunc = func(cmd *cobra.Command, opts chatOptions) (string, error) {
			return "", errors.New("chat failed")
		}
		getChatSettings = func() (dtconfig.Settings, error) {
			return dtconfig.Settings{
				Projects: []*dtconfig.ProjectRef{
					{ID: "p1", Title: "Project 1", Path: "/path/1"},
					nil,                                      // exercises nil check
					{ID: "p2", Title: "Project 2", Path: ""}, // exercises empty path
				},
			}, nil
		}
		t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) {
			return nil, nil
		}))
		mods := []datatugui.Module{
			{ID: "p1", Text: "P1", Root: func() nav.Page { return nav.Page{Content: nav.Static("p", "p")} }},
		}
		err := runUnifiedApp(mods, datatugui.Options{Start: "p1"})
		require.NoError(t, err)
	})

	t.Run("runChatProject returns error and UI run fails", func(t *testing.T) {
		runChatProjectFunc = func(cmd *cobra.Command, opts chatOptions) (string, error) {
			return "", errors.New("chat failed")
		}
		injectedErr := errors.New("ui boom")
		t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) {
			return nil, injectedErr
		}))
		err := runUnifiedApp(nil, datatugui.Options{})
		require.ErrorIs(t, err, injectedErr)
	})
}
