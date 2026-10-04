package api

import (
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A relative project directory whose first segment holds an "@" (my@proj) is
// valid on disk; the ingitdb:// source URL built from it must still parse,
// because dbcopy.Parse refuses "ingitdb://my@proj/..." as credentials.
func TestIngitdbSourceURLs_ProjectDirectoryWithAnAtSignStillParses(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"my@proj", "@acme/proj", "plain-proj"} {
		semantic := semanticIngitdbPath(dir)
		ref, err := dbcopy.Parse(semantic)
		require.NoError(t, err, semantic)
		assert.Equal(t, "ingitdb", ref.Scheme)
		assert.Contains(t, ref.Path, filepath.Join(dir, storage.DataFolder, "ingitdb"))

		fromCatalog, err := sourceURLFromCatalog(datatug.DbCatalog{Driver: "ingitdb", Path: "data"}, dir)
		require.NoError(t, err)
		ref, err = dbcopy.Parse(fromCatalog)
		require.NoError(t, err, fromCatalog)
		assert.Contains(t, ref.Path, filepath.Join(dir, "data"))
	}
}
