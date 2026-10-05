package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The descriptor of a scan that fails to save is taken back: the one the scan made is
// removed with the folders it made, and one that was there before is put back as it was.

func descriptorFile(dir string) string {
	return filepath.Join(dir, "connections", "prod", "shop.json")
}

func TestWriteDescriptor_UndoRemovesTheDescriptorAndTheFoldersItMade(t *testing.T) {
	params := newShopParams(t)
	dir := t.TempDir()

	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	assert.FileExists(t, descriptorFile(dir))
	require.NoError(t, undo())

	assert.NoDirExists(t, filepath.Join(dir, "connections"), "the folders the write made go with the descriptor")
	assert.DirExists(t, dir, "the project folder was there before, and stays")
	require.NoError(t, undo(), "taking back what is already gone is not an error")
}

func TestWriteDescriptor_UndoLeavesTheFoldersThatWereThereAndWhatIsInThem(t *testing.T) {
	params := newShopParams(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections", "README.md"), []byte("mine"), 0o600))

	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	// Somebody put a file where the scan made a folder, after it did.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections", "prod", "notes.md"), []byte("theirs"), 0o600))
	require.NoError(t, undo())

	assert.NoFileExists(t, descriptorFile(dir))
	assert.FileExists(t, filepath.Join(dir, "connections", "prod", "notes.md"), "a folder that holds something is not the scan's to remove")
	assert.FileExists(t, filepath.Join(dir, "connections", "README.md"))
}

func TestWriteDescriptor_UndoPutsBackADescriptorThatWasThere(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_OLD_PG_URL"}`)
	before, err := os.ReadFile(descriptorFile(dir))
	require.NoError(t, err)

	undo, err := newShopParams(t).WriteDescriptor(dir)
	require.NoError(t, err)
	changed, err := os.ReadFile(descriptorFile(dir))
	require.NoError(t, err)
	assert.JSONEq(t, `{"dsnEnv":"SHOP_PG_URL"}`, string(changed), "a rescan with another variable updates the descriptor")
	require.NoError(t, undo())

	after, err := os.ReadFile(descriptorFile(dir))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a descriptor that was there is left as it was")
}

func TestWriteDescriptor_ADescriptorAsItWouldBeWrittenIsNotWrittenAgain(t *testing.T) {
	params := newShopParams(t)
	dir := t.TempDir()
	_, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	stamp := fs.FileMode(0o640)
	require.NoError(t, os.Chmod(descriptorFile(dir), stamp))
	var writes int
	useOps(t, func(ops *plainfs.Ops) {
		open := ops.OpenFile
		ops.OpenFile = func(name string, flag int, perm fs.FileMode) (plainfs.File, error) {
			if flag&os.O_WRONLY != 0 {
				writes++
			}
			return open(name, flag, perm)
		}
	})

	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	require.NoError(t, undo())

	info, err := os.Stat(descriptorFile(dir))
	require.NoError(t, err)
	assert.Equal(t, stamp, info.Mode().Perm(), "the file was not touched")
	assert.Zero(t, writes, "and the undo of a write that did nothing puts nothing back")
	assert.FileExists(t, descriptorFile(dir), "or removes anything")
}

func TestWriteDescriptor_UndoReportsWhatItCannotTakeBack(t *testing.T) {
	boom := errors.New("disk on fire")
	t.Run("the descriptor cannot be removed", func(t *testing.T) {
		undo, err := newShopParams(t).WriteDescriptor(t.TempDir())
		require.NoError(t, err)
		useOps(t, func(ops *plainfs.Ops) { ops.Remove = func(string) error { return boom } })
		err = undo()
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "remove the connection descriptor")
	})
	t.Run("a folder it made cannot be removed", func(t *testing.T) {
		undo, err := newShopParams(t).WriteDescriptor(t.TempDir())
		require.NoError(t, err)
		useOps(t, func(ops *plainfs.Ops) {
			remove := ops.Remove
			ops.Remove = func(name string) error {
				if strings.HasSuffix(name, ".json") {
					return remove(name)
				}
				return boom
			}
		})
		err = undo()
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "remove the folder the connection descriptor was written in")
	})
	t.Run("the descriptor that was there cannot be put back", func(t *testing.T) {
		dir := t.TempDir()
		writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_OLD_PG_URL"}`)
		undo, err := newShopParams(t).WriteDescriptor(dir)
		require.NoError(t, err)
		useOps(t, func(ops *plainfs.Ops) {
			ops.OpenFile = func(string, int, fs.FileMode) (plainfs.File, error) { return nil, boom }
		})
		err = undo()
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "put back the connection descriptor")
	})
}

func TestWriteDescriptor_AFolderThatCannotBeMadeLeavesNoneBehind(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("no space")
	useOps(t, func(ops *plainfs.Ops) {
		mkdir := ops.Mkdir
		ops.Mkdir = func(name string, perm fs.FileMode) error {
			if strings.HasSuffix(name, "prod") {
				return boom
			}
			return mkdir(name, perm)
		}
	})

	undo, err := newShopParams(t).WriteDescriptor(dir)

	assert.Nil(t, undo)
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "create the connection descriptor folder")
	assert.NoDirExists(t, filepath.Join(dir, "connections"), "connections/ was made on the way, and is removed again")
}

// ResolveDescriptorPath reads the path of a catalog as the project file says it, and does
// not trust it: the descriptor is a file inside the project folder or it is not read.
func TestResolveDescriptorPath(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)

	file, err := ResolveDescriptorPath(dir, "connections/prod/shop.json")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "connections", "prod", "shop.json"), file)

	file, err = ResolveDescriptorPath(dir, "connections/prod/not-there-yet.json")
	require.NoError(t, err, "a file that is not there is not a link to anywhere: reading it says it is not there")
	assert.Equal(t, filepath.Join(dir, "connections", "prod", "not-there-yet.json"), file)

	for _, outside := range []string{"", "/etc/shop.json", "../shop.json", "connections/../../shop.json", "~/shop.json", "~", "$HOME/shop.json", "${HOME}/shop.json"} {
		_, err = ResolveDescriptorPath(dir, outside)
		assert.ErrorIs(t, err, errDescriptorOutsideProject, outside)
	}
	for _, url := range []string{"https://token@example.com/shop.json", "./https://u:p@example.com/shop.json"} {
		_, err = ResolveDescriptorPath(dir, url)
		if assert.Error(t, err, url) {
			assert.ErrorContains(t, err, "is a URL", url)
			assert.NotContains(t, err.Error(), "token", url)
		}
	}
	_, err = ResolveDescriptorPath("", "connections/prod/shop.json")
	assert.ErrorContains(t, err, "no project directory")
}

func TestResolveDescriptorPath_RefusesALinkThatLeadsOutOfTheProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege on Windows")
	}
	outside := t.TempDir()
	writeDescriptor(t, outside, "shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/own.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	require.NoError(t, os.Symlink(filepath.Join(outside, "shop.json"), filepath.Join(dir, "connections", "prod", "shop.json")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "linked")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "connections", "prod", "own.json"), filepath.Join(dir, "connections", "prod", "alias.json")))

	for _, path := range []string{"connections/prod/shop.json", "linked/shop.json"} {
		_, err := ResolveDescriptorPath(dir, path)
		assert.ErrorIs(t, err, errDescriptorOutsideProject, path)
	}
	_, err := ResolveDescriptorPath(dir, "connections/prod/alias.json")
	assert.NoError(t, err, "a link to a file of the project is a file of the project")
}

func TestResolveDescriptorPath_RefusesAProjectFolderThatCannotBeLookedAt(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	original := scanEvalSymlinks
	t.Cleanup(func() { scanEvalSymlinks = original })
	scanEvalSymlinks = func(path string) (string, error) {
		if path == dir {
			return "", errors.New("cannot look")
		}
		return original(path)
	}

	_, err := ResolveDescriptorPath(dir, "connections/prod/shop.json")

	assert.ErrorIs(t, err, errDescriptorOutsideProject)
}

// The scan says what it connects to as the display function of the sources does: the scheme,
// the host, the port and the database, and no user, no password, no query string.
func TestPostgresScanParams_DisplayNamesTheServerWithoutTheCredentials(t *testing.T) {
	params := newShopParams(t)

	assert.Equal(t, "postgres://db.example.com:5433/shop", params.Display())
	for _, hidden := range []string{pgSecret, "alice", "sslmode", "require"} {
		assert.NotContains(t, params.Display(), hidden)
	}
	assert.Equal(t, "connecting to postgres://db.example.com:5433/shop", loggedTarget(params))
	sqlServer, err := dbconnection.NewConnectionString("sqlserver", "db.example.com", "sa", "pw-never-shown", "shop", "port=1434")
	require.NoError(t, err)
	assert.Equal(t, "server=db.example.com, port=1434, user=sa", loggedTarget(sqlServer), "a flag the operator typed is named as it was")
}

func TestScanDbCatalog_PostgresClassifiesAnOpenThatFailsInTheDriversWords(t *testing.T) {
	cause := errors.New("dalgo2postgres: PingContext(" + shopEnv()["SHOP_PG_URL"] + "): refused")
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return nil, cause })

	_, err := scanDbCatalog(shopServer(), newShopParams(t))

	require.Error(t, err)
	assert.ErrorIs(t, err, cause, "the driver's own error is kept for errors.Is, never printed")
	for _, shown := range []string{pgSecret, "alice", "db.example.com", "PingContext"} {
		assert.NotContains(t, err.Error(), shown)
	}
	assert.ErrorContains(t, err, "the PostgreSQL connection string is read from the environment variable SHOP_PG_URL")
}

// shopServer is the server of a PostgreSQL scan as a project records it: the driver alone.
func shopServer() datatug.ServerRef { return datatug.ServerRef{Driver: DriverPostgres} }

// countingScanDB is a fakeScanDB whose server runs COUNT(*) natively, so the scan counts the
// records of its tables, and that can tell views from tables. It counts the counts.
type countingScanDB struct {
	*fakeScanDB
	rows   int
	err    error
	views  []string
	counts atomic.Int32
}

func (c *countingScanDB) QueryCapabilities() dal.QueryCapabilities {
	return dal.QueryCapabilities{Aggregate: dal.AggregateCapabilities{Count: true}}
}

func (c *countingScanDB) ListViews(context.Context) ([]dal.CollectionRef, error) {
	var refs []dal.CollectionRef
	for _, view := range c.views {
		refs = append(refs, dal.NewRootCollectionRef(view, ""))
	}
	return refs, nil
}

func (c *countingScanDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	c.counts.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return &oneCountReader{n: c.rows}, nil
}

func (c *countingScanDB) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}

type oneCountReader struct {
	n    int
	done bool
}

func (r *oneCountReader) Cursor() (string, error) { return "", dal.ErrNotSupported }
func (r *oneCountReader) Close() error            { return nil }
func (r *oneCountReader) Next() (record.Record, error) {
	if r.done {
		return nil, dal.ErrNoMoreRecords
	}
	r.done = true
	return record.NewRecordWithData(record.NewKeyWithID("Order", "0"), map[string]any{"n": r.n}), nil
}
