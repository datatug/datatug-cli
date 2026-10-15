package commands

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

// Not parallel: mutates process environment via t.Setenv.
func TestChatRecentPreferencesDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATATUG_CHAT_PREFERENCES_DIR", dir)
	path, err := defaultLastChatOptionsPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "chat-last.json"), path)
}

func TestChatConsentTempFailure(t *testing.T) {
	t.Parallel()
	_, err := consentCreateTemp(filepath.Join(t.TempDir(), "nonexistent", "nested"))
	require.Error(t, err)
}

func TestProviderUnavailableConversationAskWithContext(t *testing.T) {
	t.Parallel()
	conv := providerUnavailableConversation{}
	turn, err := conv.AskWithContext(context.Background(), "question", "context")
	require.NoError(t, err)
	require.Contains(t, turn.Text, "Configure an AI provider")
}

// Not parallel: sets global RunTeaProgram seam in pkg/chat.
func TestRunChatProjectWithWorkspaceOption(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) {
		return nil, nil
	}))
	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{
		project:  dir,
		env:      "local",
		model:    defaultChatModel,
		thinking: "low",
		workspace: &workspaceStart{
			modules:        nil,
			options:        datatugui.Options{},
			programOptions: nil,
		},
	}
	next, err := runChatProject(cmd, options)
	require.NoError(t, err)
	require.Empty(t, next)
}

func TestCompareCommandValidationErrors(t *testing.T) {
	t.Parallel()
	t.Run("database specific flags without positional databases", func(t *testing.T) {
		t.Parallel()
		cmd := compareCommand()
		cmd.SetArgs([]string{"--left-schema", "public"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "database-specific schema")
	})

	t.Run("missing query and sides", func(t *testing.T) {
		t.Parallel()
		cmd := compareCommand()
		cmd.SetArgs([]string{"--query", "q"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "query comparison requires --query, --left, and --right")
	})
}

// Not parallel: mutates package-level seams openExportSource and runNativeExport.
func TestDBExportCommandBranches(t *testing.T) {
	origOpen := openExportSource
	origRun := runNativeExport
	t.Cleanup(func() {
		openExportSource = origOpen
		runNativeExport = origRun
	})

	t.Run("invalid from URL", func(t *testing.T) {
		cmd := dbExportCommand()
		cmd.SetArgs([]string{"--from", ":::invalid", "--to", "ingitdb://dir"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "--from:")
	})

	t.Run("invalid to URL", func(t *testing.T) {
		cmd := dbExportCommand()
		cmd.SetArgs([]string{"--from", "sqlite://file.db", "--to", ":::invalid"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "--to:")
	})

	t.Run("to scheme not ingitdb", func(t *testing.T) {
		cmd := dbExportCommand()
		cmd.SetArgs([]string{"--from", "sqlite://file.db", "--to", "sqlite://out.db"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "--to must be an ingitdb://")
	})

	t.Run("open source fails", func(t *testing.T) {
		openExportSource = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
			return nil, errors.New("open error")
		}
		cmd := dbExportCommand()
		cmd.SetArgs([]string{"--from", "sqlite://file.db", "--to", "ingitdb://dir"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "open --from:")
	})

	t.Run("source has no tables", func(t *testing.T) {
		openExportSource = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
			return nil, nil
		}
		runNativeExport = func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (map[string]int64, error) {
			return nil, dbcopy.ErrSourceHasNoTables
		}
		cmd := dbExportCommand()
		cmd.SetArgs([]string{"--from", "sqlite://file.db", "--to", "ingitdb://dir"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "source has no collections to export")
	})

	t.Run("export succeeds", func(t *testing.T) {
		openExportSource = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
			return nil, nil
		}
		runNativeExport = func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (map[string]int64, error) {
			return map[string]int64{"items": 5, "orders": 10}, nil
		}
		cmd := dbExportCommand()
		var errBuf bytes.Buffer
		cmd.SetErr(&errBuf)
		cmd.SetArgs([]string{"--from", "sqlite://file.db", "--to", "ingitdb://dir"})
		err := cmd.ExecuteContext(context.Background())
		require.NoError(t, err)
		require.Contains(t, errBuf.String(), "exported 2 collections and 15 rows")
	})
}
