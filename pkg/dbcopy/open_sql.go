package dbcopy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	_ "modernc.org/sqlite"             // registers the "sqlite" database/sql driver
)

// SQLEngine reports the faithful-copy engine of this source: "sqlite", "postgres" or "" for a
// scheme the faithful SQLite/PostgreSQL copy does not handle.
func (r BackendRef) SQLEngine() string {
	switch r.Scheme {
	case "sqlite", "postgres":
		return r.Scheme
	}
	return ""
}

// OpenSQL opens the database/sql handle that the faithful SQLite/PostgreSQL copy reads or writes
// through. A SQLite file that is written is created when it is missing (its folder too); one that
// is read must exist. A PostgreSQL URL gets the same session defaults and refusals as every other
// open (see postgresConnectionString), and the preview switch is asked first. The error is safe
// to show (see OpenFailure).
func (r BackendRef) OpenSQL(ctx context.Context, forWrite bool) (*sql.DB, error) {
	db, err := r.openSQL(ctx, forWrite)
	if err != nil {
		return nil, r.OpenFailure(err)
	}
	return db, nil
}

// scheme-like start of a path ("HTTP:/host/..."): what is left of a URL that was pasted after "sqlite://".
var urlLikePath = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+:/`)

// prepareNewSQLiteFile makes the folder of a SQLite file that is about to be written, and refuses a
// path that is a URL or holds a query string: creating a file there would write whatever the person
// pasted (a password in a query) into a file name on disk. A file that already exists is left alone.
func (r BackendRef) prepareNewSQLiteFile() error {
	if _, err := os.Stat(r.Path); err == nil {
		return nil
	}
	if strings.ContainsAny(r.Path, "?#") || urlLikePath.MatchString(filepath.ToSlash(r.Path)) {
		return &refusedError{"refusing to create a SQLite file at a path that looks like a URL; write a local path (a relative one as ./path)"}
	}
	if dir := filepath.Dir(r.Path); dir != "" && dir != "." {
		return os.MkdirAll(dir, 0o755)
	}
	return nil
}

func (r BackendRef) openSQL(ctx context.Context, forWrite bool) (*sql.DB, error) {
	switch r.Scheme {
	case "sqlite":
		mode := SQLiteReadOnly
		if forWrite {
			mode = "rwc"
			if err := r.prepareNewSQLiteFile(); err != nil {
				return nil, err
			}
		} else if err := r.CheckFile(); err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", SQLiteFileURIMode(r.Path, mode))
		if err != nil {
			return nil, err
		}
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	case "postgres":
		if err := CheckPostgresPreview(); err != nil {
			return nil, err
		}
		connection, err := postgresConnectionString(r.Path, forWrite)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("pgx", connection)
		if err != nil {
			return nil, errPostgresOpenFailed
		}
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
	return nil, fmt.Errorf("%w: %s", errUnsupportedBackend, r.Scheme)
}
