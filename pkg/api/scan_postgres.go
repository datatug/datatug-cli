package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// DriverPostgres is the driver name a PostgreSQL server and catalog carry in a
// project.
const DriverPostgres = "postgres"

// postgresDescriptorFolder is the project folder that holds connection
// descriptors: a PostgreSQL catalog's path points at one of them.
const postgresDescriptorFolder = "connections"

// recordableHost is a host a project can record under its server's file name.
var recordableHost = regexp.MustCompile(`^[A-Za-z0-9:][A-Za-z0-9._:-]*$`)

// PostgresScanParams are the connection parameters of a PostgreSQL scan. The
// connection is an environment variable that holds the whole URL, so these
// parameters print as the variable's name, never as the URL: no verb of fmt shows
// the password or the rest of the URL, for the parameters or a pointer to them
// (String and GoString). Their accessors do carry the host, port and user of the
// URL, which name the server in the project and the log line of a scan; the
// password is the only part of the URL that is never shown.
type PostgresScanParams struct {
	dsnEnv         string
	catalog        string
	ref            dbcopy.BackendRef
	target         dbcopy.PostgresTarget
	descriptorPath string
}

var _ dbconnection.Params = (*PostgresScanParams)(nil)

// NewPostgresScanParams resolves the environment variable dsnEnv, which must
// hold a postgres:// URL, and names the catalog of environment that the scan
// will write. lookupEnv reads the environment.
//
// The environment and the catalog each name a file under the project's
// connections folder, so each must be a plain name (dbcopy.IsPlainSourceID, the one
// definition: the routes that read the project apply the same). The host of the URL names
// the server in the project, so it must be one a project can record.
func NewPostgresScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*PostgresScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if ref.Scheme != "postgres" {
		return nil, fmt.Errorf("environment variable %s must hold a postgres:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	target, err := dbcopy.ParsePostgresTarget(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("environment variable %s: %w", dsnEnv, err)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("the URL in environment variable %s names no host (a unix-socket connection cannot be scanned yet)", dsnEnv)
	}
	if !recordableHost.MatchString(target.Host) {
		return nil, fmt.Errorf("the host in environment variable %s cannot be recorded in a project: use a host name or an address", dsnEnv)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		// The flags are what the user typed, and a source string can be typed where
		// a name belongs: only a plain name is echoed.
		return nil, fmt.Errorf("environment %q and database %q must each be a plain name (letters, digits, '.', '_' and '-', at most 128 characters, starting with a letter or a digit) to name the connection descriptor under %s/", dbcopy.SourceIDDisplay(environment), dbcopy.SourceIDDisplay(catalog), postgresDescriptorFolder)
	}
	return &PostgresScanParams{
		dsnEnv:         dsnEnv,
		catalog:        catalog,
		ref:            ref,
		target:         target,
		descriptorPath: path.Join(postgresDescriptorFolder, environment, catalog+".json"),
	}, nil
}

// Driver implements dbconnection.Params.
func (*PostgresScanParams) Driver() string { return DriverPostgres }

// Mode implements dbconnection.Params: a scan only reads.
func (*PostgresScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }

// Server implements dbconnection.Params: the host of the URL.
func (p *PostgresScanParams) Server() string { return p.target.Host }

// Port implements dbconnection.Params: the port of the URL, 0 when it names none.
func (p *PostgresScanParams) Port() int { return p.target.Port }

// Catalog implements dbconnection.Params: the catalog ID in the project.
func (p *PostgresScanParams) Catalog() string { return p.catalog }

// User implements dbconnection.Params: the user of the URL.
func (p *PostgresScanParams) User() string { return p.target.User }

// ConnectionString implements dbconnection.Params. It is the "env:NAME" source
// and never the URL: nothing that formats these parameters can print a password.
func (p *PostgresScanParams) ConnectionString() string { return p.ref.Raw }

// String implements dbconnection.Params, with the same text as ConnectionString.
// It has a value receiver so that the parameters print the same through a
// pointer and as a value: fmt cannot call a method on the unexported fields that
// hold the URL, and would print them as they are.
func (p PostgresScanParams) String() string { return p.ref.Raw }

// GoString makes %#v print the same text as String, not the fields.
func (p PostgresScanParams) GoString() string { return p.String() }

// DSNEnv is the name of the environment variable that holds the URL.
func (p *PostgresScanParams) DSNEnv() string { return p.dsnEnv }

// Path is the project-relative path of the connection descriptor the scan
// writes, which UpdateDbSchema records as the catalog's path.
func (p *PostgresScanParams) Path() string { return p.descriptorPath }

// SourceRef is the source the scan opens: its Raw is "env:NAME", its Path the
// real URL, which only the open path may use.
func (p *PostgresScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

// WriteDescriptor writes the connection descriptor into projectDir: a file that
// names the environment variable and nothing else, so the descriptor holds no
// password, user or host. (The project's server entry records the host and port
// of the URL at scan time, never the user or the password.) It replaces a
// descriptor already there.
func (p *PostgresScanParams) WriteDescriptor(projectDir string) error {
	// A struct of one string cannot fail to marshal.
	data, _ := json.MarshalIndent(dbcopy.PostgresDescriptor{DSNEnv: p.dsnEnv}, "", "  ")
	file := filepath.Join(projectDir, filepath.FromSlash(p.descriptorPath))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("create the connection descriptor folder: %w", err)
	}
	if err := os.WriteFile(file, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write the connection descriptor: %w", err)
	}
	return nil
}

// postgresScanSource is what scanPostgresCatalog needs of its parameters: the
// source to open.
type postgresScanSource interface {
	SourceRef() dbcopy.BackendRef
}

// openSchemaScan opens a source for a schema scan, a seam so tests scan without
// a server. Always dbcopy.BackendRef.OpenSchemaScan in production.
var openSchemaScan = dbcopy.BackendRef.OpenSchemaScan

// validatePostgresServer is the project model's check of a server entry, a seam
// so a test can stand in for a datatug-core that records postgres servers.
// Always datatug.ServerRef.Validate in production.
//
// A scan ends by recording its server in the project, and datatug-core does not
// accept a postgres server yet, so every scan would fail at its end, after it had
// connected and read the whole schema. scanDbCatalog asks first and says so. The
// refusal lifts itself on the day datatug-core accepts the driver.
var validatePostgresServer = datatug.ServerRef.Validate

// postgresScanUnavailable is what a PostgreSQL scan answers while the project
// model cannot record the server it scanned.
const postgresScanUnavailable = "scanning PostgreSQL is not available in this release: a DataTug project cannot record a postgres server yet"

// checkPostgresServer is nil when the project model can record server, and the
// answer that a PostgreSQL scan is not available, with the model's own reason,
// when it cannot.
func checkPostgresServer(server datatug.ServerRef) error {
	if err := validatePostgresServer(server); err != nil {
		return fmt.Errorf("%s: %w", postgresScanUnavailable, err)
	}
	return nil
}

// CheckPostgresScanAvailable is nil when this release can scan PostgreSQL and
// otherwise says that it cannot. It asks the project model about a postgres
// server that names nothing of the operator's database, so a caller can answer
// before it reads a flag, an environment variable or a connection URL: a user
// told that the scan does not exist must not first be asked to put a password
// in a variable for it. scanDbCatalog asks again, about the real server.
func CheckPostgresScanAvailable() error {
	return checkPostgresServer(datatug.ServerRef{Driver: DriverPostgres, Host: "localhost"})
}

// scanPostgresCatalog scans a PostgreSQL database through DALgo's schema reader.
// It opens the source through dbcopy, so a driver error comes back classified
// (see BackendRef.OpenFailure), never as the driver wrote it; what the scan
// itself reads is reported the same way.
func scanPostgresCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	params, ok := connectionParams.(postgresScanSource)
	if !ok {
		return nil, errors.New("a PostgreSQL scan needs connection parameters built from an environment variable (--dsn-env NAME)")
	}
	source := params.SourceRef()
	scanDB, err := openSchemaScan(source, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open PostgreSQL: %w", err)
	}
	defer func() { _ = scanDB.Close() }()

	catalogID := connectionParams.Catalog()
	provider := dalgoschema.NewSchemaProvider(scanDB, dalgoschema.NewNativeCounter(scanDB), catalogID, dbcopy.PostgresDefaultSchema)
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", source.OpenFailure(err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverPostgres
	return dbCatalog, nil
}
