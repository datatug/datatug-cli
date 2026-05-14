package dbcopy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse_SQLite_AbsolutePath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("sqlite:///tmp/foo.db")
	assert.NoError(t, err)
	assert.Equal(t, "sqlite", ref.Scheme)
	assert.Equal(t, "/tmp/foo.db", ref.Path)
	assert.Equal(t, "sqlite:///tmp/foo.db", ref.Raw)
}

func TestParse_SQLite_RelativePath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("sqlite://./rel/foo.db")
	assert.NoError(t, err)
	assert.Equal(t, "sqlite", ref.Scheme)
	// dburl preserves the "./" prefix; we accept whatever dburl emits as long
	// as the resulting path is a valid relative path pointing at rel/foo.db.
	assert.Equal(t, "./rel/foo.db", ref.Path)
}

func TestParse_InGitDB_LocalPath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("ingitdb://./project")
	assert.NoError(t, err)
	assert.Equal(t, "ingitdb", ref.Scheme)
	assert.Equal(t, "./project", ref.Path)
}

func TestParse_InGitDB_RemoteRejected(t *testing.T) {
	t.Parallel()
	_, err := Parse("ingitdb://github.com/owner/repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "local paths only")
}

func TestParse_Postgres_Recognized(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://user@host/db")
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.NotEmpty(t, ref.Path)
}

// TestParse_UnknownScheme verifies REQ:unknown-scheme-rejected: the error must
// name the unsupported scheme AND list all supported schemes.
func TestParse_UnknownScheme(t *testing.T) {
	t.Parallel()
	_, err := Parse("mongodb://host/db")
	assert.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "mongodb")
	assert.Contains(t, msg, "sqlite")
	assert.Contains(t, msg, "ingitdb")
	assert.Contains(t, msg, "postgres")
}

func TestOpen_Postgres_ReturnsErrPostgresNotWired(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://user@host/db")
	assert.NoError(t, err)
	db, openErr := ref.Open(context.Background())
	assert.Nil(t, db)
	assert.True(t, errors.Is(openErr, ErrPostgresNotWired),
		"expected ErrPostgresNotWired, got %v", openErr)
}

// TestOpen_SQLite_OpensChinookFixture exercises the sqlite Open path against
// the checked-in Chinook fixture. Verifies the returned dal.DB is non-nil,
// has an adapter, and advertises SupportsConcurrentConnections()==false
// (dalgo2sqlite embeds dal.NoConcurrency).
func TestOpen_SQLite_OpensChinookFixture(t *testing.T) {
	t.Parallel()
	absPath, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	_, err = os.Stat(absPath)
	assert.NoError(t, err, "chinook.db fixture missing at %s", absPath)

	ref, err := Parse("sqlite://" + absPath)
	assert.NoError(t, err)
	assert.Equal(t, "sqlite", ref.Scheme)

	db, err := ref.Open(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, db)
	if db != nil {
		assert.NotNil(t, db.Adapter())
		// dalgo2sqlite embeds dal.NoConcurrency.
		assert.False(t, db.SupportsConcurrentConnections(),
			"dalgo2sqlite must advertise NoConcurrency for the parallel-streams cap rule")
	}
}

// TestOpen_InGitDB_OpensEmptyProject exercises the ingitdb Open path against
// a freshly-created empty project directory. The dalgo2ingitdb constructor
// only validates the path; opening doesn't require a populated project.
// Asserts the driver advertises SupportsConcurrentConnections()==true
// (dalgo2ingitdb embeds dal.ConcurrencyAvailable).
func TestOpen_InGitDB_OpensEmptyProject(t *testing.T) {
	t.Parallel()
	projDir := t.TempDir()

	ref, err := Parse("ingitdb://" + projDir)
	assert.NoError(t, err)
	assert.Equal(t, "ingitdb", ref.Scheme)
	assert.Equal(t, projDir, ref.Path)

	db, err := ref.Open(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, db)
	if db != nil {
		assert.NotNil(t, db.Adapter())
		// dalgo2ingitdb embeds dal.ConcurrencyAvailable.
		assert.True(t, db.SupportsConcurrentConnections(),
			"dalgo2ingitdb must advertise ConcurrencyAvailable")
	}
}
