package dbcopy

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A SQLite driver that is given a bare path reads everything after the first "?" in it as
// its own parameters, so it opens, and creates, the shorter name; the path of a source
// must reach it in a form that is read back as the same file whatever its characters.

func TestSQLiteFileURI(t *testing.T) {
	for path, want := range map[string]string{
		"/data/shop.db":             "file:///data/shop.db?mode=rw",
		"/data/what?mode=rw.db":     "file:///data/what%3Fmode=rw.db?mode=rw",
		"/data/what?/shop.db":       "file:///data/what%3F/shop.db?mode=rw",
		"/data/a#b.db":              "file:///data/a%23b.db?mode=rw",
		"/data/100%.db":             "file:///data/100%25.db?mode=rw",
		"/data/a%23b.db":            "file:///data/a%2523b.db?mode=rw",
		"/data/with space.db":       "file:///data/with%20space.db?mode=rw",
		"/data/a&b=c;d+e.db":        "file:///data/a&b=c;d+e.db?mode=rw",
		"/data/it's [1] (2).db":     "file:///data/it%27s%20%5B1%5D%20%282%29.db?mode=rw",
		"/data/données.db":          "file:///data/donn%C3%A9es.db?mode=rw",
		"/data/a:b.db":              "file:///data/a:b.db?mode=rw",
		"relative/shop.db":          "file:relative/shop.db?mode=rw",
		"./relative/what?.db":       "file:./relative/what%3F.db?mode=rw",
		"what?mode=rw.db":           "file:what%3Fmode=rw.db?mode=rw",
		"?":                         "file:%3F?mode=rw",
		"//server/share/shop.db":    "file:////server/share/shop.db?mode=rw",
		"/data/a%00b.db":            "file:///data/a%2500b.db?mode=rw",
		"/data/\u00a0nbsp\u2028.db": "file:///data/%C2%A0nbsp%E2%80%A8.db?mode=rw",
	} {
		assert.Equal(t, want, SQLiteFileURI(filepath.FromSlash(path)), path)
	}
}

// A path with a drive letter (which this machine may not know how to read) is the
// "file:" URI of the path after the slash, as SQLite documents it.
func TestSQLiteFileURI_DriveLetterAndNetworkPaths(t *testing.T) {
	assert.Equal(t, "file:///C:/Users/x/a%23b.db?mode=rw", sqliteFileURI("C:/Users/x/a#b.db", "C:"))
	assert.Equal(t, "file:////host/share/a%3F.db?mode=rw", sqliteFileURI("//host/share/a?.db", `\\host\share`))
	assert.Equal(t, "file:///C:/x.db?mode=rw", sqliteFileURI("/C:/x.db", "C:"), "a slash that is already there is not doubled")
}

// directoryNames lists the names under dir, folders included, relative to it.
func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		require.NoError(t, relErr)
		names = append(names, rel)
		return nil
	}))
	sort.Strings(names)
	return names
}

// moveSQLiteFile makes a database with one row, a marker that names the file, under a plain
// name, and moves it to path: the driver that makes the file reads a "?" in the name it is
// given as the start of its own parameters.
func moveSQLiteFile(t *testing.T, path, marker string) {
	t.Helper()
	plain := filepath.Join(t.TempDir(), "plain.db")
	raw, err := sql.Open("sqlite", plain)
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE marker (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)
	_, err = raw.Exec("INSERT INTO marker (id, name) VALUES (1, ?)", marker)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.Rename(plain, path))
}

// The marker of the file that was opened: the source opens the file the path names, and
// never creates another.
func openedMarker(t *testing.T, open func(context.Context) (dal.DB, error)) string {
	t.Helper()
	db, err := open(context.Background())
	require.NoError(t, err)
	if closer, ok := db.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("marker", ""))).SelectIntoRecord(nil)
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), query)
	require.NoError(t, err)
	record, err := reader.Next()
	require.NoError(t, err)
	return record.Data().(map[string]any)["name"].(string)
}

var awkwardSQLiteNames = []string{
	"what?mode=rw.db", "q?.db", "?", "a#b.db", "100%.db", "a%23b.db", "a%3Fb.db", "shop #1 50%.db",
	"with space.db", "a&b=c;d+e.db", "it's [1] (2).db", "données.db", "a:b.db", "-dash.db",
	"100%ab.db", "~tilde.db", "tab\there.db", "new\nline.db", "back\\slash.db", "all ?#% together.db",
}

func TestOpen_SQLiteFileWhoseNameHoldsAnyCharacterIsOpenedAndNothingIsCreated(t *testing.T) {
	for _, name := range awkwardSQLiteNames {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.ContainsAny(name, "?:\t\n\\") {
				t.Skip("a file name cannot have this character on Windows")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			moveSQLiteFile(t, path, name)
			before := directoryNames(t, dir)
			require.Equal(t, []string{name}, before)

			for opener, open := range map[string]func(BackendRef, context.Context) (dal.DB, error){
				"Open": BackendRef.Open, "OpenProtected": BackendRef.OpenProtected,
			} {
				ref := BackendRef{Scheme: "sqlite", Path: path}
				marker := openedMarker(t, func(ctx context.Context) (dal.DB, error) { return open(ref, ctx) })
				assert.Equal(t, name, marker, opener+" read the rows of another file")
				assert.Equal(t, before, directoryNames(t, dir), opener+" made another file beside it")
			}
		})
	}
}

// A character of a folder above the file is read back the same way.
func TestOpen_SQLiteFileInAFolderWhoseNameHoldsAnyCharacter(t *testing.T) {
	for _, folder := range []string{"what?", "what?mode=rw", "a#b", "100%", "my folder", "a%23b"} {
		t.Run(folder, func(t *testing.T) {
			if runtime.GOOS == "windows" && folder[len(folder)-1] == '?' {
				t.Skip("a folder name cannot have a ? on Windows")
			}
			root := t.TempDir()
			path := filepath.Join(root, folder, "shop.db")
			moveSQLiteFile(t, path, folder)
			before := directoryNames(t, root)

			marker := openedMarker(t, func(ctx context.Context) (dal.DB, error) { return BackendRef{Scheme: "sqlite", Path: path}.Open(ctx) })

			assert.Equal(t, folder, marker)
			assert.Equal(t, before, directoryNames(t, root), "another file or folder was made")
		})
	}
}

// A file that is not there is the missing-source answer, and no file is made for it,
// whatever its name holds.
func TestOpen_SQLiteFileThatIsNotThereIsNotCreated(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"missing.db", "what?mode=rwc.db", "a#b.db"} {
		if runtime.GOOS == "windows" && name == "what?mode=rwc.db" {
			continue
		}
		_, err := BackendRef{Scheme: "sqlite", Path: filepath.Join(dir, name)}.Open(context.Background())
		assert.ErrorIs(t, err, ErrSourceFileMissing, name)
	}
	assert.Empty(t, directoryNames(t, dir), "opening a file that is not there made a file")
}

// A file that disappears between the check and the open is not made again by the open:
// the open is a read-write open that does not create, so the open fails.
func TestOpen_SQLiteFileThatVanishesAfterTheCheckIsNotCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vanishing.db")
	moveSQLiteFile(t, path, "vanishing")
	original := newSQLiteDatabaseWithOptions
	t.Cleanup(func() { newSQLiteDatabaseWithOptions = original })
	newSQLiteDatabaseWithOptions = func(dsn string, schema dal.Schema, opts dalgo2sql.DbOptions) (*dalgo2sqlite.Database, error) {
		require.NoError(t, os.Remove(path), "the file goes between the check of the source and the open")
		return original(dsn, schema, opts)
	}

	_, err := BackendRef{Scheme: "sqlite", Path: path}.Open(context.Background())

	assert.Error(t, err)
	assert.Empty(t, directoryNames(t, dir), "the open made the file that had gone")
}

// protectedSQLiteFile makes a database file named name in a folder of its own, with one
// row (a marker that names it), and write-protects it. It skips the test when the file
// can be written anyway (a process that is run as root), which would prove nothing.
func protectedSQLiteFile(t *testing.T, name string) (dir, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a file mode is not what keeps a file from being written on Windows")
	}
	dir = t.TempDir()
	path = filepath.Join(dir, name)
	moveSQLiteFile(t, path, name)
	require.NoError(t, os.Chmod(path, 0o444))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Skip("the file can be written by this process (run as root?): the test would prove nothing")
	}
	return dir, path
}

// A database file that is write-protected is opened, as it always was: SQLite opens a file
// that it may not write read-only, whatever the open asked for when it can not create, and
// the rows of the file that was named are read, and no file is made beside it.
func TestOpen_AWriteProtectedSQLiteFileIsOpenedAndRead(t *testing.T) {
	for _, name := range []string{"shop.db", "what?mode=rw.db", "a#b.db", "with space.db"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(name, "?") {
				t.Skip("a file name cannot have a ? on Windows")
			}
			dir, path := protectedSQLiteFile(t, name)
			before := directoryNames(t, dir)

			for opener, open := range map[string]func(BackendRef, context.Context) (dal.DB, error){
				"Open": BackendRef.Open, "OpenProtected": BackendRef.OpenProtected,
			} {
				marker := openedMarker(t, func(ctx context.Context) (dal.DB, error) {
					return open(BackendRef{Scheme: "sqlite", Path: path}, ctx)
				})
				assert.Equal(t, name, marker, opener+" did not read the file that was named")
				assert.Equal(t, before, directoryNames(t, dir), opener+" made a file beside it")
			}
		})
	}
}
