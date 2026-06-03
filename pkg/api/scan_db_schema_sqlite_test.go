package api

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" database/sql driver (CGO)

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScanDbCatalog_SQLite3 proves the sqlite3 scan path is wired end-to-end:
// scanDbCatalog opens the file, dispatches to the sqlite schemer, and returns
// a catalog containing the table that was created.
func TestScanDbCatalog_SQLite3(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	params := dbconnection.NewSQLite3ConnectionParams(dbPath, "main", dbconnection.ModeReadOnly)
	server := datatug.ServerRef{Driver: dbconnection.DriverSQLite3, Host: "localhost"}

	catalog, err := scanDbCatalog(server, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)

	var widgets *datatug.CollectionInfo
	for _, sch := range catalog.Schemas {
		for _, tbl := range sch.Tables {
			if tbl.Name() == "widgets" {
				widgets = tbl
			}
		}
	}
	require.NotNil(t, widgets, "scanned sqlite catalog must include the created table")

	var colNames []string
	for _, col := range widgets.Columns {
		colNames = append(colNames, col.Name)
	}
	assert.Contains(t, colNames, "id", "scanned table must include its columns")
	assert.Contains(t, colNames, "name", "scanned table must include its columns")
}
