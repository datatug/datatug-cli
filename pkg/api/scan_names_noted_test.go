package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordCatalog writes the catalog file of catalog id of environment env, on model, in the
// nested place.
func recordCatalog(t *testing.T, projectDir, env, id, model string) {
	t.Helper()
	path := filepath.Join(projectDir, "environments", env, "catalogs", id, id+".db.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"`+id+`","driver":"sqlite3","path":"/data/`+id+`.db","dbModel":"`+model+`"}`), 0o600))
}

// A scan that is put on a model that the database id did not give, by what the other
// environments record, says so in one line: nobody who named no model expects one.
func TestResolveScanDbModelNoted(t *testing.T) {
	t.Run("the other environments record a model of another name: one line, naming it and where it is from", func(t *testing.T) {
		projectDir := t.TempDir()
		recordCatalog(t, projectDir, "local", "shop", "retail")

		model, note, err := ResolveScanDbModelNoted(projectDir, "dev", "shop", "")

		require.NoError(t, err)
		assert.Equal(t, "retail", model)
		assert.Equal(t, `note: database "shop" is on database model "retail" in environment "local", so this scan of environment "dev" puts it on that model too; --dbmodel chooses another`, note)
	})

	t.Run("two environments that agree are both named", func(t *testing.T) {
		projectDir := t.TempDir()
		recordCatalog(t, projectDir, "local", "shop", "retail")
		recordCatalog(t, projectDir, "prod", "shop", "retail")

		model, note, err := ResolveScanDbModelNoted(projectDir, "dev", "shop", "")

		require.NoError(t, err)
		assert.Equal(t, "retail", model)
		assert.Equal(t, `note: database "shop" is on database model "retail" in environments "local", "prod", so this scan of environment "dev" puts it on that model too; --dbmodel chooses another`, note)
	})

	t.Run("a model that is the database id is what no flag gives anyway: nothing to say", func(t *testing.T) {
		projectDir := t.TempDir()
		recordCatalog(t, projectDir, "local", "shop", "shop")

		model, note, err := ResolveScanDbModelNoted(projectDir, "dev", "shop", "")

		require.NoError(t, err)
		assert.Equal(t, "shop", model)
		assert.Empty(t, note)
	})

	t.Run("every other answer says nothing", func(t *testing.T) {
		projectDir := t.TempDir()
		recordCatalog(t, projectDir, "local", "shop", "retail")
		recordCatalog(t, projectDir, "dev", "shop", "retail")
		for name, c := range map[string]struct{ env, flag, want string }{
			"the model this environment records": {"dev", "", "retail"},
			"a flag that settles it":             {"prod", "other", "other"},
			"a flag that names the recorded one": {"dev", "retail", "retail"},
		} {
			model, note, err := ResolveScanDbModelNoted(projectDir, c.env, "shop", c.flag)
			require.NoError(t, err, name)
			assert.Equal(t, c.want, model, name)
			assert.Empty(t, note, name)
		}
		model, note, err := ResolveScanDbModelNoted(t.TempDir(), "dev", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "shop", model, "no environment records the database: it is on the model of its own name")
		assert.Empty(t, note)
	})

	t.Run("an error is passed on, with no model and no line", func(t *testing.T) {
		projectDir := t.TempDir()
		recordCatalog(t, projectDir, "local", "shop", "retail")
		recordCatalog(t, projectDir, "dev", "shop", "sales")

		model, note, err := ResolveScanDbModelNoted(projectDir, "prod", "shop", "")

		assert.ErrorContains(t, err, "more than one database model")
		assert.Empty(t, model)
		assert.Empty(t, note)
	})
}
