package api

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// LibSQLScanParams are the connection parameters of a libSQL scan. The server
// base address is read from an environment variable named by --dsn-env; its value
// is never printed or written to a project. The project records only a connection
// descriptor that names that variable (connections/<env>/<catalog>.json), exactly
// as a PostgreSQL, CouchDB or PostgREST catalog does, so a saved project never
// holds an executable endpoint. The token itself is read from
// DATATUG_LIBSQL_BEARER for each request by the adapter, exactly as it is for
// `query run`; it is never part of the descriptor.
type LibSQLScanParams struct {
	dsnEnv         string
	catalog        string
	ref            dbcopy.BackendRef
	host           string
	port           int
	descriptorPath string
}

var _ dbconnection.Params = (*LibSQLScanParams)(nil)

// NewLibSQLScanParams reads the libSQL base address held by the environment
// variable dsnEnv and names the catalog of environment that the scan will write.
// It performs no request and holds no credential.
func NewLibSQLScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*LibSQLScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if !dbcopy.IsLibSQLScheme(ref.Scheme) {
		return nil, fmt.Errorf("environment variable %s must hold a libsql://, libsql+https:// or libsql+http:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		return nil, fmt.Errorf("environment %q and database %q must each be a plain name to be recorded in a project", dbcopy.SourceIDDisplay(environment), dbcopy.SourceIDDisplay(catalog))
	}
	host, port := libSQLScanTarget(ref)
	return &LibSQLScanParams{
		dsnEnv:         dsnEnv,
		catalog:        catalog,
		ref:            ref,
		host:           host,
		port:           port,
		descriptorPath: ConnectionDescriptorPath(environment, catalog),
	}, nil
}

// libSQLScanTarget is the host and the port of the base address, exposed as
// non-secret metadata through dbconnection.Params. The project itself records
// only the connection descriptor, never this endpoint.
func libSQLScanTarget(ref dbcopy.BackendRef) (host string, port int) {
	host, portText, err := net.SplitHostPort(ref.LibSQLHost)
	if err == nil {
		port, _ = strconv.Atoi(portText)
		return host, port
	}
	return strings.TrimSuffix(strings.TrimPrefix(ref.LibSQLHost, "["), "]"), 0
}

// Driver implements dbconnection.Params.
func (*LibSQLScanParams) Driver() string { return DriverLibSQL }

// Mode implements dbconnection.Params: a scan only reads.
func (*LibSQLScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }

// Server implements dbconnection.Params: the host of the base address.
func (p *LibSQLScanParams) Server() string { return p.host }

// Port implements dbconnection.Params: the port of the base address, 0 when it
// names none.
func (p *LibSQLScanParams) Port() int { return p.port }

// Catalog implements dbconnection.Params: the catalog ID in the project.
func (p *LibSQLScanParams) Catalog() string { return p.catalog }

// User implements dbconnection.Params: libSQL credentials are not part of the
// endpoint, so a project never records a user here.
func (*LibSQLScanParams) User() string { return "" }

// ConnectionString implements dbconnection.Params: the "env:NAME" source, never
// the base address's value and never a credential.
func (p *LibSQLScanParams) ConnectionString() string { return p.ref.Raw }

// String implements dbconnection.Params with the same text as ConnectionString,
// so formatting the parameters cannot print an endpoint.
func (p *LibSQLScanParams) String() string { return p.ref.Raw }

// GoString makes %#v print the same text as String.
func (p *LibSQLScanParams) GoString() string { return p.String() }

// LogTarget is what a scan logs: where the base address is read from, never a
// part of it.
func (p *LibSQLScanParams) LogTarget() string {
	return "the libSQL base address is read from the environment variable " + p.dsnEnv
}

// Path is the project-relative path of the connection descriptor the scan
// writes, which UpdateDbSchema records as the catalog's path: only the name of
// the environment variable, never the endpoint or a credential.
func (p *LibSQLScanParams) Path() string { return p.descriptorPath }

// WriteDescriptor writes the libSQL connection descriptor into projectDir: a
// file that names the environment variable and nothing else, using the same
// plain-file rules as a PostgreSQL descriptor (see
// PostgresScanParams.WriteDescriptor). It replaces a descriptor already there,
// and returns the function that takes the write back for a failed save.
func (p *LibSQLScanParams) WriteDescriptor(projectDir string) (undo func() error, err error) {
	return writeDescriptorFile(projectDir, p.descriptorPath, p.dsnEnv)
}

// SourceRef is the source the scan opens: an "env:NAME" ref whose Path holds the
// real base address. Only the open path reads Path.
func (p *LibSQLScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

// libSQLScanSource is what scanLibSQLCatalog needs of its parameters.
type libSQLScanSource interface {
	SourceRef() dbcopy.BackendRef
}

// scanLibSQLCatalog reads a libSQL database through the generic
// dbschema.SchemaReader seam: its tables and views, their native SQLite column
// types, primary keys, indexes, constraints and referrers. A libSQL database is a
// SQLite database served over HTTP, so the metadata is SQLite's own and the
// adapter reports it from the live catalog rather than inventing an "id" key. It
// opens the source through pkg/dbcopy and classifies a failure the same way every
// other scan does.
func scanLibSQLCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	params, ok := connectionParams.(libSQLScanSource)
	if !ok {
		return nil, fmt.Errorf("a libSQL scan needs connection parameters built from an environment variable (--dsn-env NAME)")
	}
	source := params.SourceRef()
	scanDB, err := openSchemaScan(source, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open libSQL: %w", source.OpenFailure(err))
	}
	defer func() { _ = scanDB.Close() }()

	catalogID := connectionParams.Catalog()
	// libSQL is SQLite over HTTP: its one implicit database is "main", not the
	// fixed "default" schema of a MySQL/Oracle/SQL Server catalog. Recording the
	// tables under "main" is what lets a scanned "main.items" reference resolve
	// and lets a bare "/items" policy match (see DefaultSchemaForDriver and
	// PolicyCollectionName).
	provider := dalgoschema.NewSchemaProvider(scanDB, nil, catalogID, DefaultSchemaForDriver(DriverLibSQL))
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", catalogReadFailure(source, err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverLibSQL
	return dbCatalog, nil
}
