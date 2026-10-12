package commands

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests that drive the command actions replace the *InitProject seams with
// fakes, so the production default of each seam is never the function under
// test. These tests call the untouched defaults directly.

// TestDefaultInitProjectSeamsRequireAProject: with no project name or
// directory, every default seam must fail the same way the command does.
func TestDefaultInitProjectSeamsRequireAProject(t *testing.T) {
	tempDatatugHome(t)
	for name, call := range map[string]func() error{
		"dataset":     func() error { return datasetInitProject(&datasetCommand{}) },
		"dataset def": func() error { return datasetDefInitProject(&datasetDefCommand{}) },
		"datasets":    func() error { return datasetsInitProject(&datasetsCommand{}) },
		"render":      func() error { return renderInitProject(&renderCommand{}) },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, call())
		})
	}
}

// TestDefaultLastChatCreateTemp: the default creates a real temp file in the
// given directory and returns it as a lastChatTempFile.
func TestDefaultLastChatCreateTemp(t *testing.T) {
	dir := t.TempDir()
	f, err := lastChatCreateTemp(dir)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, statErr := os.Stat(f.Name())
	assert.NoError(t, statErr)
}

// TestDefaultLastChatCreateTempMissingDir: an unusable directory surfaces the
// os error and returns no file.
func TestDefaultLastChatCreateTempMissingDir(t *testing.T) {
	f, err := lastChatCreateTemp(t.TempDir() + "/does/not/exist")
	assert.Error(t, err)
	assert.Nil(t, f)
}
