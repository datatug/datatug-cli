package commands

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCovDDefaultLastChatOptionsPath(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HOME", cfg)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	p, err := defaultLastChatOptionsPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("datatug", "chat-last.json"), p[len(p)-len(filepath.Join("datatug", "chat-last.json")):])

	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	_, err = defaultLastChatOptionsPath()
	require.Error(t, err)
}

func covDPathErr(t *testing.T) {
	t.Helper()
	covDSetVar(t, &lastChatOptionsPath, func() (string, error) { return "", errors.New("no config dir") })
}

func TestCovDApplyLastChatOptionsErrors(t *testing.T) {
	t.Run("path error", func(t *testing.T) {
		covDPathErr(t)
		require.Error(t, applyLastChatOptions(chatCommand(), &chatOptions{}))
	})
	t.Run("missing file is not an error", func(t *testing.T) {
		useTestLastChatOptionsPath(t)
		options := chatOptions{project: "keep"}
		require.NoError(t, applyLastChatOptions(chatCommand(), &options))
		assert.Equal(t, "keep", options.project)
	})
	t.Run("unreadable path", func(t *testing.T) {
		path := useTestLastChatOptionsPath(t)
		require.NoError(t, os.Mkdir(path, 0o755)) // a directory cannot be read as a file
		require.Error(t, applyLastChatOptions(chatCommand(), &chatOptions{}))
	})
	t.Run("flag set fails", func(t *testing.T) {
		path := useTestLastChatOptionsPath(t)
		data, _ := json.Marshal(lastChatOptions{Model: "remembered"})
		require.NoError(t, os.WriteFile(path, data, 0o600))
		bare := &cobra.Command{} // no "model" flag registered
		require.Error(t, applyLastChatOptions(bare, &chatOptions{}))
	})
}

func TestCovDApplyLastChatOptionsRestoresBaseURLAndThinking(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	data, _ := json.Marshal(lastChatOptions{BaseURL: "https://byok.example/v1", Thinking: "high"})
	require.NoError(t, os.WriteFile(path, data, 0o600))
	cmd := chatCommand()
	options := chatOptions{}
	require.NoError(t, applyLastChatOptions(cmd, &options))
	assert.Equal(t, "https://byok.example/v1", options.baseURL)
	assert.Equal(t, "high", options.thinking)
}

// covDTempFile is a fake temp file failing at a chosen step.
type covDTempFile struct {
	name                       string
	writeErr, syncErr, closeEr error
}

func (f *covDTempFile) Name() string { return f.name }
func (f *covDTempFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}
func (f *covDTempFile) Sync() error  { return f.syncErr }
func (f *covDTempFile) Close() error { return f.closeEr }

func TestCovDSaveLastChatOptions(t *testing.T) {
	t.Run("path error", func(t *testing.T) {
		covDPathErr(t)
		require.Error(t, saveLastChatOptions(chatCommand(), chatOptions{}))
	})
	t.Run("remembers base url and thinking overrides", func(t *testing.T) {
		path := useTestLastChatOptionsPath(t)
		cmd := chatCommand()
		require.NoError(t, cmd.Flags().Set("base-url", "https://byok.example/v1"))
		require.NoError(t, cmd.Flags().Set("thinking", "high"))
		require.NoError(t, saveLastChatOptions(cmd, chatOptions{baseURL: "https://byok.example/v1", thinking: "high"}))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var stored lastChatOptions
		require.NoError(t, json.Unmarshal(data, &stored))
		assert.Equal(t, "https://byok.example/v1", stored.BaseURL)
		assert.Equal(t, "high", stored.Thinking)
	})
	t.Run("marshal error", func(t *testing.T) {
		useTestLastChatOptionsPath(t)
		covDSetVar(t, &lastChatMarshal, func(any, string, string) ([]byte, error) { return nil, errors.New("marshal boom") })
		require.Error(t, saveLastChatOptions(chatCommand(), chatOptions{}))
	})
	t.Run("create temp error", func(t *testing.T) {
		useTestLastChatOptionsPath(t)
		covDSetVar(t, &lastChatCreateTemp, func(string) (lastChatTempFile, error) { return nil, errors.New("temp boom") })
		require.Error(t, saveLastChatOptions(chatCommand(), chatOptions{}))
	})
	for name, f := range map[string]*covDTempFile{
		"write error": {writeErr: errors.New("write boom")},
		"sync error":  {syncErr: errors.New("sync boom")},
		"close error": {closeEr: errors.New("close boom")},
	} {
		t.Run(name, func(t *testing.T) {
			path := useTestLastChatOptionsPath(t)
			f.name = filepath.Join(filepath.Dir(path), "fake-temp")
			covDSetVar(t, &lastChatCreateTemp, func(string) (lastChatTempFile, error) { return f, nil })
			require.Error(t, saveLastChatOptions(chatCommand(), chatOptions{}))
		})
	}
}
