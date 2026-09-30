package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covDRunScan(args ...string) error {
	cmd := scanCommandArgs()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return cmd.Execute()
}

func TestCovDScanCommandAction(t *testing.T) {
	t.Run("project is required", func(t *testing.T) {
		require.Error(t, covDRunScan("--db", "d", "--env", "local"))
	})
	t.Run("project directory must exist", func(t *testing.T) {
		err := covDRunScan("-d", filepath.Join(t.TempDir(), "absent"), "--db", "d", "--env", "local")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
	t.Run("connection params error", func(t *testing.T) {
		err := covDRunScan("-d", t.TempDir(), "-D", "sqlite3", "--db", "d", "--env", "local") // no --path
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--path")
	})
	t.Run("scan failure is returned", func(t *testing.T) {
		err := covDRunScan("-d", t.TempDir(), "-D", "sqlite3", "--path", filepath.Join(t.TempDir(), "absent.db"), "--db", "d", "--env", "local")
		require.Error(t, err)
	})
	t.Run("saves the scanned project", func(t *testing.T) {
		dir := t.TempDir()
		covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
			return &datatug.Project{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "scanned"}, Access: "private"},
				Created:     &datatug.ProjectCreated{At: time.Now()},
			}, nil
		})
		require.NoError(t, covDRunScan("-d", dir, "-D", "sqlite3", "--path", "x.db", "--db", "chinook", "--env", "local"))
		saved, err := os.ReadFile(filepath.Join(dir, "datatug-project.json"))
		require.NoError(t, err)
		assert.Contains(t, string(saved), `"id": "scanned"`)
	})
	t.Run("save failure is reported", func(t *testing.T) {
		dir := t.TempDir()
		// The scanner hands back a project that fails save-time validation.
		covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
			return &datatug.Project{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "invalid"}}}, nil
		})
		err := covDRunScan("-d", dir, "-D", "sqlite3", "--path", "x.db", "--db", "chinook", "--env", "local")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to save datatug project [invalid]")
		assert.Contains(t, err.Error(), "project validation failed")
	})
	t.Run("scanner error is returned unchanged", func(t *testing.T) {
		boom := errors.New("scan boom")
		covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
			return nil, boom
		})
		err := covDRunScan("-d", t.TempDir(), "-D", "sqlite3", "--path", "x.db", "--db", "chinook", "--env", "local")
		require.ErrorIs(t, err, boom)
	})
}

func TestCovDScanConnectionParamsInvalidConnectionString(t *testing.T) {
	covDSetVar(t, &scanNewConnectionString, func(string, string, string, string, string, ...string) (dbconnection.GeneralParams, error) {
		return dbconnection.GeneralParams{}, errors.New("conn boom")
	})
	v := &scanDbCommand{Driver: "sqlserver", Host: "h", Database: "d"}
	params, err := v.connectionParams()
	require.Error(t, err)
	assert.Nil(t, params)
	assert.Contains(t, err.Error(), "invalid connection string")
}
