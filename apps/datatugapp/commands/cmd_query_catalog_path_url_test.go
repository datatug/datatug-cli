package commands

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The path of a catalog names a file or a directory: a URL written there is refused
// before it is joined to the project folder, and the message names the catalog and
// shows nothing of the path (joined, it would read as a relative path and be shown
// whole by a later "source file does not exist").
func TestQuerySourceURLFromCatalog_RefusesAURLInTheCatalogPath(t *testing.T) {
	for _, driver := range []string{"sqlite3", "ingitdb"} {
		catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "chinook-local"}},
			Driver:      driver, Path: "https://tok_Zk39xq@git.example/x",
		}}
		_, err := querySourceURLFromCatalog(catalog, t.TempDir())
		require.Error(t, err, driver)
		assert.ErrorContains(t, err, `catalog "chinook-local"`, driver)
		assert.ErrorContains(t, err, "is a URL", driver)
		assert.NotContains(t, err.Error(), "tok_Zk39xq", driver)
		assert.NotContains(t, err.Error(), "git.example", driver)
	}
}
