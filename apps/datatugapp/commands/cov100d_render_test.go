package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covDRenderWithDir(t *testing.T, dir string) {
	t.Helper()
	covDSetVar(t, &renderInitProject, func(v *renderCommand) error {
		v.ProjectDir = dir
		return nil
	})
}

// covDRenderCompactProject rewrites the project file of an initialised
// project in a compact form that a save would reformat, and returns its path.
func covDRenderCompactProject(t *testing.T, dir string) string {
	t.Helper()
	projectFile := filepath.Join(dir, "datatug-project.json")
	require.NoError(t, os.WriteFile(projectFile, []byte(`{"id":"render-p","access":"private","created":{"at":"2026-01-02T03:04:05Z"}}`), 0o600))
	return projectFile
}

func TestCovDRenderCommandActionReloadsAndSavesProject(t *testing.T) {
	dir := covDInitProject(t, "render-p")
	projectFile := covDRenderCompactProject(t, dir)
	require.NoError(t, os.Remove(filepath.Join(dir, "README.md")))
	covDRenderWithDir(t, dir) // absolute path, no chdir

	require.NoError(t, renderCommandAction(nil, nil))

	after, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	assert.NotEqual(t, `{"id":"render-p","access":"private","created":{"at":"2026-01-02T03:04:05Z"}}`, string(after), "project file must be rewritten by the save")
	assert.Contains(t, string(after), `"id": "render-p"`)
	assert.FileExists(t, filepath.Join(dir, "README.md"), "render must regenerate readme.md")
}

func TestCovDRenderCommandActionLoadFailure(t *testing.T) {
	covDRenderWithDir(t, t.TempDir()) // exists, but holds no project
	err := renderCommandAction(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load project")
}

func TestCovDRenderCommandActionSaveFailure(t *testing.T) {
	covDSkipIfRoot(t)
	dir := covDInitProject(t, "render-p")
	projectFile := covDRenderCompactProject(t, dir)
	covDRenderWithDir(t, dir)
	covDChmod(t, projectFile, 0o400) // readable, so the load works, but it cannot be rewritten
	err := renderCommandAction(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save datatug project")
}
