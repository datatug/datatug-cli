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

// CouchDBScanParams are the connection parameters of a CouchDB scan. The endpoint
// (host, port and one database) is read from an environment variable named by
// --dsn-env; its value is never printed or written to a project. The catalog
// records the endpoint URL without credentials. Credentials themselves are read
// from DATATUG_COUCHDB_USERNAME/PASSWORD or DATATUG_COUCHDB_BEARER for each
// request by the adapter, exactly as they are for `query run`.
type CouchDBScanParams struct {
	dsnEnv  string
	catalog string
	ref     dbcopy.BackendRef
	host    string
	port    int
}

var _ dbconnection.Params = (*CouchDBScanParams)(nil)

// NewCouchDBScanParams reads the CouchDB endpoint held by the environment
// variable dsnEnv and names the catalog of environment that the scan will write.
// It performs no request and holds no credential.
func NewCouchDBScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*CouchDBScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if !dbcopy.IsCouchDBScheme(ref.Scheme) {
		return nil, fmt.Errorf("environment variable %s must hold a couchdb://, couchdb+https:// or couchdb+http:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		return nil, fmt.Errorf("environment %q and database %q must each be a plain name to be recorded in a project", dbcopy.SourceIDDisplay(environment), dbcopy.SourceIDDisplay(catalog))
	}
	host, port := couchDBScanTarget(ref)
	return &CouchDBScanParams{dsnEnv: dsnEnv, catalog: catalog, ref: ref, host: host, port: port}, nil
}

// couchDBScanTarget is the host and the port a project records for the endpoint.
func couchDBScanTarget(ref dbcopy.BackendRef) (host string, port int) {
	host, portText, err := net.SplitHostPort(ref.CouchDBHost)
	if err == nil {
		port, _ = strconv.Atoi(portText)
		return host, port
	}
	return strings.TrimSuffix(strings.TrimPrefix(ref.CouchDBHost, "["), "]"), 0
}

// Driver implements dbconnection.Params.
func (*CouchDBScanParams) Driver() string { return DriverCouchDB }

// Mode implements dbconnection.Params: a scan only reads.
func (*CouchDBScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }

// Server implements dbconnection.Params: the host of the endpoint.
func (p *CouchDBScanParams) Server() string { return p.host }

// Port implements dbconnection.Params: the port of the endpoint, 0 when it names none.
func (p *CouchDBScanParams) Port() int { return p.port }

// Catalog implements dbconnection.Params: the catalog ID in the project.
func (p *CouchDBScanParams) Catalog() string { return p.catalog }

// User implements dbconnection.Params: CouchDB credentials are not part of the
// endpoint, so a project never records a user here.
func (*CouchDBScanParams) User() string { return "" }

// ConnectionString implements dbconnection.Params: the "env:NAME" source, never
// the endpoint's value and never a credential.
func (p *CouchDBScanParams) ConnectionString() string { return p.ref.Raw }

// String implements dbconnection.Params with the same text as ConnectionString,
// so formatting the parameters cannot print an endpoint.
func (p *CouchDBScanParams) String() string { return p.ref.Raw }

// GoString makes %#v print the same text as String.
func (p *CouchDBScanParams) GoString() string { return p.String() }

// LogTarget is what a scan logs: where the endpoint is read from, never a part
// of it.
func (p *CouchDBScanParams) LogTarget() string {
	return "the CouchDB endpoint is read from the environment variable " + p.dsnEnv
}

// Path is the catalog path the scan records: the endpoint URL, without
// credentials, a query or a fragment.
func (p *CouchDBScanParams) Path() string { return p.ref.CouchDBSourceURL() }

// SourceRef is the source the scan opens: an "env:NAME" ref whose Path holds the
// real endpoint. Only the open path reads Path.
func (p *CouchDBScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

// couchDBScanSource is what scanCouchDBCatalog needs of its parameters.
type couchDBScanSource interface {
	SourceRef() dbcopy.BackendRef
}

// scanCouchDBCatalog reads a CouchDB database through the generic
// dbschema.SchemaReader seam: the one configured collection, its native _id
// primary key and its real Mango indexes. CouchDB declares no field schema, no
// SQL constraints and no foreign keys, so the scan reports the collection with no
// columns and no keys instead of inventing any. It opens the source through
// pkg/dbcopy and classifies a failure the same way every other scan does.
func scanCouchDBCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	params, ok := connectionParams.(couchDBScanSource)
	if !ok {
		return nil, fmt.Errorf("a CouchDB scan needs connection parameters built from an environment variable (--dsn-env NAME)")
	}
	source := params.SourceRef()
	scanDB, err := openSchemaScan(source, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open CouchDB: %w", source.OpenFailure(err))
	}
	defer func() { _ = scanDB.Close() }()

	catalogID := connectionParams.Catalog()
	provider := dalgoschema.NewSchemaProvider(scanDB, nil, catalogID, ServerDefaultSchema)
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", catalogReadFailure(source, err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverCouchDB
	return dbCatalog, nil
}
