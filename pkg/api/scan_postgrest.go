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

// PostgRESTScanParams are the connection parameters of a PostgREST scan. The API
// root (host, port and API path) is read from an environment variable named by
// --dsn-env; its value is never printed or written to a project. The project
// records only a connection descriptor that names that variable
// (connections/<env>/<catalog>.json), exactly as a PostgreSQL or CouchDB catalog
// does, so a saved project never holds an executable endpoint. Credentials
// themselves are read from DATATUG_POSTGREST_BEARER and DATATUG_POSTGREST_API_KEY
// for each request by the adapter, exactly as they are for `query run`.
type PostgRESTScanParams struct {
	dsnEnv         string
	catalog        string
	ref            dbcopy.BackendRef
	host           string
	port           int
	descriptorPath string
}

var _ dbconnection.Params = (*PostgRESTScanParams)(nil)

// NewPostgRESTScanParams reads the PostgREST API root held by the environment
// variable dsnEnv and names the catalog of environment that the scan will write.
// It performs no request and holds no credential.
func NewPostgRESTScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*PostgRESTScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if !dbcopy.IsPostgRESTScheme(ref.Scheme) {
		return nil, fmt.Errorf("environment variable %s must hold a postgrest://, postgrest+https:// or postgrest+http:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		return nil, fmt.Errorf("environment %q and database %q must each be a plain name to be recorded in a project", dbcopy.SourceIDDisplay(environment), dbcopy.SourceIDDisplay(catalog))
	}
	host, port := postgrestScanTarget(ref)
	return &PostgRESTScanParams{
		dsnEnv:         dsnEnv,
		catalog:        catalog,
		ref:            ref,
		host:           host,
		port:           port,
		descriptorPath: ConnectionDescriptorPath(environment, catalog),
	}, nil
}

// postgrestScanTarget is the host and the port of the API root, exposed as
// non-secret metadata through dbconnection.Params. The project itself records
// only the connection descriptor, never this endpoint.
func postgrestScanTarget(ref dbcopy.BackendRef) (host string, port int) {
	host, portText, err := net.SplitHostPort(ref.PostgRESTHost)
	if err == nil {
		port, _ = strconv.Atoi(portText)
		return host, port
	}
	return strings.TrimSuffix(strings.TrimPrefix(ref.PostgRESTHost, "["), "]"), 0
}

// Driver implements dbconnection.Params.
func (*PostgRESTScanParams) Driver() string { return DriverPostgREST }

// Mode implements dbconnection.Params: a scan only reads.
func (*PostgRESTScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }

// Server implements dbconnection.Params: the host of the API root.
func (p *PostgRESTScanParams) Server() string { return p.host }

// Port implements dbconnection.Params: the port of the API root, 0 when it names
// none.
func (p *PostgRESTScanParams) Port() int { return p.port }

// Catalog implements dbconnection.Params: the catalog ID in the project.
func (p *PostgRESTScanParams) Catalog() string { return p.catalog }

// User implements dbconnection.Params: PostgREST credentials are not part of the
// endpoint, so a project never records a user here.
func (*PostgRESTScanParams) User() string { return "" }

// ConnectionString implements dbconnection.Params: the "env:NAME" source, never
// the endpoint's value and never a credential.
func (p *PostgRESTScanParams) ConnectionString() string { return p.ref.Raw }

// String implements dbconnection.Params with the same text as ConnectionString,
// so formatting the parameters cannot print an endpoint.
func (p *PostgRESTScanParams) String() string { return p.ref.Raw }

// GoString makes %#v print the same text as String.
func (p *PostgRESTScanParams) GoString() string { return p.String() }

// LogTarget is what a scan logs: where the API root is read from, never a part of
// it.
func (p *PostgRESTScanParams) LogTarget() string {
	return "the PostgREST API root is read from the environment variable " + p.dsnEnv
}

// Path is the project-relative path of the connection descriptor the scan
// writes, which UpdateDbSchema records as the catalog's path: only the name of
// the environment variable, never the endpoint or a credential.
func (p *PostgRESTScanParams) Path() string { return p.descriptorPath }

// WriteDescriptor writes the PostgREST connection descriptor into projectDir: a
// file that names the environment variable and nothing else, using the same
// plain-file rules as a PostgreSQL descriptor (see
// PostgresScanParams.WriteDescriptor). It replaces a descriptor already there,
// and returns the function that takes the write back for a failed save.
func (p *PostgRESTScanParams) WriteDescriptor(projectDir string) (undo func() error, err error) {
	return writeDescriptorFile(projectDir, p.descriptorPath, p.dsnEnv)
}

// SourceRef is the source the scan opens: an "env:NAME" ref whose Path holds the
// real API root. Only the open path reads Path.
func (p *PostgRESTScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

// postgrestScanSource is what scanPostgRESTCatalog needs of its parameters.
type postgrestScanSource interface {
	SourceRef() dbcopy.BackendRef
}

// scanPostgRESTCatalog reads a PostgREST API root through the generic
// dbschema.SchemaReader seam: the relations the role can see, their fields, and a
// primary key only where the OpenAPI document declares one. PostgREST exposes no
// index or constraint metadata, so the scan reports none instead of inventing
// any. It opens the source through pkg/dbcopy and classifies a failure the same
// way every other scan does.
func scanPostgRESTCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	params, ok := connectionParams.(postgrestScanSource)
	if !ok {
		return nil, fmt.Errorf("a PostgREST scan needs connection parameters built from an environment variable (--dsn-env NAME)")
	}
	source := params.SourceRef()
	scanDB, err := openSchemaScan(source, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open PostgREST: %w", source.OpenFailure(err))
	}
	defer func() { _ = scanDB.Close() }()

	catalogID := connectionParams.Catalog()
	provider := dalgoschema.NewSchemaProvider(scanDB, nil, catalogID, ServerDefaultSchema)
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", catalogReadFailure(source, err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverPostgREST
	return dbCatalog, nil
}
