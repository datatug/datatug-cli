package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
)

// This file registers a database as a source of an environment of a project, without reading
// the database (`datatug db add`, spec/features/cli/db/add), and finds a registered source
// again for the commands that open one (`db schema --project --env --source`). The files it
// writes are the ones a scan writes for a catalog (see "Project layout written by a scan" in
// spec/features/cli/scan/README.md), less the metadata a scan reads: the catalog file has no
// database model, which is how a source that no scan has described is told (`notScanned`).

// ProjectSourceRefusal is the error of a registration that the project or the request does not
// allow: a name that is not a plain name, a name already taken by another database. The command
// exits 2 for it. Its text names ids and drivers, and never a path or a connection string.
type ProjectSourceRefusal struct{ message string }

func (e *ProjectSourceRefusal) Error() string { return e.message }

func refuseSource(format string, args ...any) error {
	return &ProjectSourceRefusal{message: fmt.Sprintf(format, args...)}
}

// ProjectSourceRequest is the database `db add` registers: exactly one of SQLitePath (the
// absolute path of a SQLite file) and DSNEnv (the name of the environment variable that holds
// a PostgreSQL connection URL).
type ProjectSourceRequest struct {
	Environment string // the environment of the project the source is added to
	ID          string // the id of the source (the catalog id), a plain name
	SQLitePath  string
	DSNEnv      string
	// Driver optionally distinguishes an env-only ClickHouse source. Empty
	// retains the existing PostgreSQL descriptor registration behavior.
	Driver string
}

// AddedSource is what AddProjectSource did: Added is false when the same database was already a
// source of the environment, under the id ID, and nothing was written.
type AddedSource struct {
	Added       bool
	ID          string
	Environment string
	Driver      string
}

// sourceIdentity is what makes two catalogs one database: for SQLite the file, for PostgreSQL the
// environment variable that holds the URL.
type sourceIdentity struct{ driver, key string }

// isSQLiteDriver is whether a catalog's driver names a SQLite file.
func isSQLiteDriver(driver string) bool {
	return driver == dbconnection.DriverSQLite3 || driver == "sqlite"
}

// canonicalFile is a file path as two paths to one file compare: cleaned, and with the links
// resolved when the file is there.
func canonicalFile(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// catalogIdentity is the database a catalog of the project records, and whether the project
// says: a catalog whose path or descriptor cannot be read, or whose driver is another, has none.
func catalogIdentity(projectDir string, catalog *datatug.DbCatalog) (sourceIdentity, bool) {
	switch {
	case isSQLiteDriver(catalog.Driver):
		path, err := ResolveCatalogPath(projectDir, catalog.Path)
		if err != nil {
			return sourceIdentity{}, false
		}
		return sourceIdentity{dbconnection.DriverSQLite3, canonicalFile(path)}, true
	case IsServerDriver(catalog.Driver) || catalog.Driver == DriverClickHouse:
		file, err := ResolveDescriptorPath(projectDir, catalog.Path)
		if err != nil {
			return sourceIdentity{}, false
		}
		descriptor, err := dbcopy.ReadPostgresDescriptor(file)
		if err != nil {
			return sourceIdentity{}, false
		}
		return sourceIdentity{catalog.Driver, descriptor.DSNEnv}, true
	}
	return sourceIdentity{}, false
}

// AddProjectSource registers the database of req as a source of req.Environment of the project
// at projectDir, through store, and does not open it. The files are those of a scan of the
// catalog: the catalog file (the driver and the path, and no database model), the environment
// file that lists the catalog on a server of the driver, and, for PostgreSQL, the connection
// descriptor connections/<env>/<id>.json that names the environment variable and nothing else.
// A SQLite path is stored as a scan stores it (see sqliteCatalogPath).
//
// When the same database (the same SQLite file, or the same environment variable) is already a
// source of the environment, under any id, nothing is written and the existing id is returned
// with Added false. A source of the environment that has the id of req and is another database
// is refused, naming the ids and the drivers. The environment is made when the project has none
// of that id. A write that fails takes back what this call wrote before it.
func AddProjectSource(ctx context.Context, store datatug.ProjectStore, projectDir string, req ProjectSourceRequest) (AddedSource, error) {
	driver, want := dbconnection.DriverSQLite3, sourceIdentity{dbconnection.DriverSQLite3, canonicalFile(req.SQLitePath)}
	if req.DSNEnv != "" {
		driver, want = DriverPostgres, sourceIdentity{DriverPostgres, req.DSNEnv}
		if req.Driver != "" {
			if req.Driver != DriverPostgres && req.Driver != DriverClickHouse {
				return AddedSource{}, refuseSource("unsupported descriptor driver")
			}
			driver = req.Driver
			want = sourceIdentity{driver, req.DSNEnv}
		}
	}
	for _, name := range []struct{ flag, value string }{{"--env", req.Environment}, {"--name", req.ID}} {
		if err := CheckScanName(name.flag, name.value); err != nil {
			return AddedSource{}, refuseSource("%s", err.Error())
		}
	}
	if err := CheckScanNamesAgainstProject(projectDir, req.Environment, req.ID, ""); err != nil {
		return AddedSource{}, refuseSource("%s", err.Error())
	}

	envs, err := store.LoadEnvironments(ctx)
	if err != nil {
		return AddedSource{}, errors.New("the environments of the project cannot be read")
	}
	var catalogs datatug.DbCatalogs
	if envs.GetByID(req.Environment) != nil {
		if catalogs, err = store.LoadEnvDbCatalogs(ctx, req.Environment); err != nil {
			return AddedSource{}, LookupError("the catalogs of environment %q cannot be read", nil, req.Environment)
		}
	}
	var taken *datatug.DbCatalog
	takenKnown := false
	for _, catalog := range catalogs {
		identity, known := catalogIdentity(projectDir, catalog)
		if known && identity == want {
			return AddedSource{ID: catalog.ID, Environment: req.Environment, Driver: driver}, nil
		}
		if catalog.ID == req.ID {
			taken, takenKnown = catalog, known
		}
	}
	if taken != nil {
		return AddedSource{}, nameTaken(req, taken, takenKnown)
	}

	catalog := &datatug.DbCatalog{
		DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: req.ID}},
			Driver:      driver,
		},
		Schemas: datatug.DbSchemas{},
	}
	undo := func() error { return nil }
	if req.DSNEnv != "" {
		catalog.Path = PostgresDescriptorPath(req.Environment, req.ID)
		if undo, err = WritePostgresDescriptor(projectDir, req.Environment, req.ID, req.DSNEnv); err != nil {
			return AddedSource{}, err
		}
	} else if catalog.Path, err = sqliteCatalogPath(projectDir, req.SQLitePath); err != nil {
		return AddedSource{}, fmt.Errorf("failed to record the path of the SQLite database %q in the project: %w", req.ID, err)
	}
	server := datatug.ServerRef{Driver: driver}
	addCatalogToEnvironments(&envs, req.Environment, server, req.ID)

	if err = store.SaveEnvDbCatalog(ctx, req.Environment, server.GetID(), req.ID, catalog); err != nil {
		return AddedSource{}, errors.Join(fmt.Errorf("failed to save the catalog file of %q: %w", req.ID, err), undo())
	}
	if err = store.SaveEnvironment(ctx, envs.GetByID(req.Environment)); err != nil {
		failure := fmt.Errorf("failed to save the environment file of %q: %w", req.Environment, err)
		return AddedSource{}, errors.Join(failure, store.DeleteEnvDbCatalog(ctx, req.Environment, server.GetID(), req.ID), undo())
	}
	return AddedSource{Added: true, ID: req.ID, Environment: req.Environment, Driver: driver}, nil
}

// nameTaken is the refusal of a source whose id the environment already has for another database.
// It names the id, the environment and the recorded driver (only when it is a name), and says
// that the database is another one, or, when the project does not say which database the
// recorded source is, that it cannot be told apart.
func nameTaken(req ProjectSourceRequest, taken *datatug.DbCatalog, known bool) error {
	recorded := "another driver"
	if dbcopy.IsPlainSourceID(taken.Driver) {
		recorded = fmt.Sprintf("%q", taken.Driver)
	}
	if !known {
		return refuseSource("--name %q is already a source of environment %q in this project (driver: %s), and the project does not say which database it is, so it cannot be told from the one given: use another --name",
			req.ID, req.Environment, recorded)
	}
	return refuseSource("--name %q is already a %s source of environment %q in this project, and it is not the database given: use another --name",
		req.ID, recorded, req.Environment)
}

// ResolveProjectSourceURL is the source of the catalog sourceID of the project at projectDir, as
// `db schema`, `db table`, `db column` and `query run` open it: for SQLite the sqlite:// URL of
// the file its path resolves to (ResolveCatalogPath), for PostgreSQL env:<VARIABLE>, never the
// URL the variable holds. environment may be "" when exactly one environment of the project
// lists sourceID; with several the error is an AmbiguousSourceError that lists them. A catalog
// that is not there is a ProjectSourceNotFoundError; one that cannot be resolved is a
// SourceUnresolvedError.
func ResolveProjectSourceURL(ctx context.Context, store datatug.ProjectStore, projectDir, environment, sourceID string) (string, error) {
	notFound := func() error {
		if environment == "" {
			return &ProjectSourceNotFoundError{message: LookupError("source %q not found in any environment of the project", nil, sourceID).Error()}
		}
		return &ProjectSourceNotFoundError{message: LookupError("source %q not found in environment %q", nil, sourceID, environment).Error()}
	}
	// The ids are joined into paths of the project: only a plain name can be one of its folders.
	if (environment != "" && !dbcopy.IsPlainSourceID(environment)) || !dbcopy.IsPlainSourceID(sourceID) {
		return "", notFound()
	}
	if environment == "" {
		envs, err := store.LoadEnvironments(ctx)
		if err != nil {
			return "", errors.New("the environments of the project cannot be read")
		}
		holding := environmentsListing(ctx, store, envs, sourceID)
		switch len(holding) {
		case 0:
			return "", notFound()
		case 1:
			environment = holding[0]
		default:
			return "", &AmbiguousSourceError{Environments: holding}
		}
	}
	catalog, err := store.LoadEnvDbCatalog(ctx, environment, "", sourceID)
	if errors.Is(err, fs.ErrNotExist) {
		return "", notFound()
	}
	if err != nil {
		return "", LookupError("source %q of environment %q cannot be read", nil, sourceID, environment)
	}
	var source string
	if IsServerDriver(catalog.Driver) {
		source, err = postgresDescriptorSource(catalog, projectDir)
	} else {
		source, err = sourceURLFromCatalog(catalog, projectDir)
	}
	if err != nil {
		return "", &SourceUnresolvedError{Driver: catalog.Driver, cause: err}
	}
	return source, nil
}

// environmentsListing is the ids of the environments of envs that list the source sourceID, in order.
func environmentsListing(ctx context.Context, store datatug.ProjectStore, envs datatug.Environments, sourceID string) (holding []string) {
	for _, env := range envs {
		if _, err := store.LoadEnvDbCatalog(ctx, env.GetID(), "", sourceID); err == nil {
			holding = append(holding, env.GetID())
		}
	}
	return holding
}

// EnvironmentsListingSource is the ids of the environments of the project that list a source with the
// id sourceID, in order: more than one means a reference to the source needs ?env=.
func EnvironmentsListingSource(ctx context.Context, store datatug.ProjectStore, sourceID string) ([]string, error) {
	envs, err := store.LoadEnvironments(ctx)
	if err != nil {
		return nil, errors.New("the environments of the project cannot be read")
	}
	return environmentsListing(ctx, store, envs, sourceID), nil
}

// AmbiguousSourceError is the failure of a source id that several environments list when no
// environment was named. Environments are their ids, in order; the text names them and nothing else.
type AmbiguousSourceError struct{ Environments []string }

func (e *AmbiguousSourceError) Error() string {
	shown := make([]string, len(e.Environments))
	for i, env := range e.Environments {
		shown[i] = dbcopy.SourceIDDisplay(env)
	}
	return "the source is listed in several environments (" + strings.Join(shown, ", ") + "): add ?env=<ENV> to the reference"
}

// ProjectSourceNotFoundError is the failure of a lookup of a source the project does not have. Its
// text shows each id only when it is a plain name.
type ProjectSourceNotFoundError struct{ message string }

func (e *ProjectSourceNotFoundError) Error() string { return e.message }

// SourceUnresolvedError is the failure to turn a catalog into a source: its driver is not one
// that is opened, or its path or descriptor does not resolve. The cause quotes a path of the
// project, so Error is a fixed sentence and the cause is only reachable through errors.Is/As.
type SourceUnresolvedError struct {
	Driver string
	cause  error
}

func (e *SourceUnresolvedError) Error() string {
	if isSQLiteDriver(e.Driver) || IsServerDriver(e.Driver) || e.Driver == DriverCouchDB || e.Driver == DriverPostgREST || e.Driver == DriverLibSQL || e.Driver == DriverTypesense || e.Driver == DriverClickHouse {
		return "its catalog path or connection descriptor cannot be resolved"
	}
	return "its driver is not one that can be opened (sqlite3, ingitdb, openvaultdb, postgres, mysql, oracle, sqlserver, firestore, couchdb, postgrest, libsql or typesense)"
}

func (e *SourceUnresolvedError) Unwrap() error { return e.cause }
