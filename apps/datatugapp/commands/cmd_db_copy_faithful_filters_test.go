package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --where and --limit between two SQLite files keep working as they did before the faithful engine.
func TestDBCopyFaithful_WhereAndLimitBetweenSQLiteFiles(t *testing.T) {
	source := sqliteFixture(t,
		`CREATE TABLE Artist (ArtistId INTEGER PRIMARY KEY, Name TEXT)`,
		`CREATE TABLE Album (AlbumId INTEGER PRIMARY KEY, ArtistId INTEGER REFERENCES Artist(ArtistId), Title TEXT)`,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 20) INSERT INTO Artist SELECT x, 'artist ' || x FROM c`,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 20) INSERT INTO Album SELECT x, x, 'album ' || x FROM c`)
	targetPath := filepath.Join(t.TempDir(), "t.sqlite")
	stdout, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+targetPath,
		"--where", "Artist:ArtistId:<:10", "--limit", "Album:5", "--json")
	require.NoError(t, err)
	db := openSQLiteForTest(t, targetPath)
	var artists, albums int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM Artist`).Scan(&artists))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM Album`).Scan(&albums))
	assert.Equal(t, 9, artists)
	assert.Equal(t, 5, albums)
	assert.Contains(t, stdout.String(), `"rows": 14`, "the report counts the rows that were written")

	// A filter in a file is the same filter.
	cfg := filepath.Join(t.TempDir(), "filter.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("where:\n  Artist:\n    - field: ArtistId\n      op: \"<\"\n      value: \"4\"\nlimit:\n  Album: 2\n"), 0o644))
	filePath := filepath.Join(t.TempDir(), "u.sqlite")
	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filePath, "--filter-config", cfg)
	require.NoError(t, err)
	db = openSQLiteForTest(t, filePath)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM Artist`).Scan(&artists))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM Album`).Scan(&albums))
	assert.Equal(t, 3, artists)
	assert.Equal(t, 2, albums)
}

func TestDBCopyFaithful_ReloadEmptiesAndLoadsAgain(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO p VALUES (1, 'one'), (2, 'two')`)
	targetPath := filepath.Join(t.TempDir(), "t.sqlite")
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+targetPath)
	require.NoError(t, err)
	db := openSQLiteForTest(t, targetPath)
	_, err = db.Exec(`INSERT INTO p VALUES (3, 'stale')`)
	require.NoError(t, err)

	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+targetPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--overwrite=reload", "a conflict names both ways out")

	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+targetPath, "--overwrite", "reload")
	require.NoError(t, err)
	var ids string
	require.NoError(t, db.QueryRow(`SELECT group_concat(id) FROM p`).Scan(&ids))
	assert.Equal(t, "1,2", ids)

	// A target table that does not fit the source is refused (exit 1), and nothing changes.
	_, err = db.Exec(`ALTER TABLE p DROP COLUMN v`)
	require.NoError(t, err)
	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+targetPath, "--overwrite", "reload")
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), `column "v" is missing`)
}

func TestDBCopyFaithful_ParallelStreamsAreCappedWithAWarning(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	_, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "a.sqlite"), "--parallel-streams", "8")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "ignoring --parallel-streams=8, using 1")
	_, stderr, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "b.sqlite"), "--parallel-streams", "1")
	require.NoError(t, err)
	assert.NotContains(t, stderr.String(), "parallel-streams")
}

func TestDBCopyFaithful_ProgressLines(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1), (2)`)
	_, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "a.sqlite"), "--progress")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "copying p (est. 2 rows)…")
	assert.Contains(t, stderr.String(), "copied p: 2 rows in ")
	_, stderr, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "b.sqlite"))
	require.NoError(t, err)
	assert.False(t, strings.Contains(stderr.String(), "copying p"), "without --progress the copy is silent")
}

// A failed copy removes the file it created and the folders it made for it, and nothing else.
func TestDBCopyFaithful_AFailedCopyRemovesTheFoldersItMade(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	existing := t.TempDir()
	target := filepath.Join(existing, "made", "deeper", "t.sqlite")
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+target, "--include", "nope")
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(existing, "made"))
	assert.True(t, os.IsNotExist(statErr), "the folders the copy made are removed")
	_, statErr = os.Stat(existing)
	assert.NoError(t, statErr, "the folder that was there is kept")

	// A folder that gained something of its own is kept.
	target = filepath.Join(existing, "keep", "t.sqlite")
	require.NoError(t, os.MkdirAll(filepath.Join(existing, "keep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "keep", "mine.txt"), []byte("x"), 0o644))
	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+target, "--include", "nope")
	require.Error(t, err)
	_, statErr = os.Stat(filepath.Join(existing, "keep", "mine.txt"))
	assert.NoError(t, statErr)
}
