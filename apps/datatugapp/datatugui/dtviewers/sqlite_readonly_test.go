package dtviewers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

func TestSQLiteViewerRefusesNativeWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned ?#%.sqlite")
	writeViewerDB(t, dbcopy.SQLiteFileURIMode(path, "rwc"), "CREATE TABLE sample(id INTEGER PRIMARY KEY, name TEXT)",
		"INSERT INTO sample VALUES(1, 'Ada')", "INSERT INTO sample VALUES(2, 'Grace')")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	viewer := GetSQLiteDbContext(path)
	db, err := viewer.GetSqlDB(context.Background(), "sqlite")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var name string
	require.NoError(t, db.QueryRow("SELECT name FROM sample WHERE id = 1").Scan(&name))
	require.Equal(t, "Ada", name)
	_, err = db.Exec("INSERT INTO sample VALUES(3, 'Refused')")
	var native interface{ Code() int }
	require.ErrorAs(t, err, &native)
	require.Equal(t, 8, native.Code()&0xff, "must be native SQLITE_READONLY")
	require.NoError(t, db.Close())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
	observer, err := sql.Open("sqlite", dbcopy.SQLiteFileURIMode(path, dbcopy.SQLiteReadOnly))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	var count int
	require.NoError(t, observer.QueryRow("SELECT COUNT(*) FROM sample").Scan(&count))
	require.Equal(t, 2, count)
}
