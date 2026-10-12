package api

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A scan reads the project file and the database at the same time. When one of the two fails its
// error is what the person reads, as it is: the count of workers that the runner puts before it
// ("failed 1 out of 2 workers, 1st error: ") says nothing a person can act on. When both fail the
// scan says how many.
func TestUpdateDbSchema_AFailureOfOneWorkerIsReportedAsItIs(t *testing.T) {
	scanFails := errors.New("failed to open PostgreSQL: the server could not be reached")
	loadFails := errors.New("disk on fire")
	failingScan := func(context.Context, datatug.ServerRef, dbconnection.Params) (*datatug.DbCatalog, error) {
		return nil, scanFails
	}
	failingLoad := mockProjectStore{loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) {
		return datatug.ProjectFile{}, loadFails
	}}

	t.Run("the scan of the database fails", func(t *testing.T) {
		setSeam(t, &scanDbCatalogSeam, failingScan)

		_, err := UpdateDbSchema(context.Background(), newProjectLoader(), "my-shop", "dev", "sqlite3", "shop", scanTestDB(t))

		require.Error(t, err)
		assert.EqualError(t, err, scanFails.Error())
		assert.ErrorIs(t, err, scanFails)
	})

	t.Run("the project file cannot be read", func(t *testing.T) {
		setSeam(t, &scanDbCatalogSeam, func(context.Context, datatug.ServerRef, dbconnection.Params) (*datatug.DbCatalog, error) {
			return &datatug.DbCatalog{}, nil
		})

		_, err := UpdateDbSchema(context.Background(), failingLoad, "my-shop", "dev", "sqlite3", "shop", scanTestDB(t))

		require.Error(t, err)
		assert.EqualError(t, err, "failed to load project summary: disk on fire")
		assert.ErrorIs(t, err, loadFails)
	})

	t.Run("both fail", func(t *testing.T) {
		setSeam(t, &scanDbCatalogSeam, failingScan)

		_, err := UpdateDbSchema(context.Background(), failingLoad, "my-shop", "dev", "sqlite3", "shop", scanTestDB(t))

		require.Error(t, err)
		assert.ErrorContains(t, err, "failed 2 out of 2 workers")
	})
}
