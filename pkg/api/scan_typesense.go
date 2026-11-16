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

// TypesenseScanParams are the connection parameters of a Typesense scan. The
// server origin and the one collection are read from an environment variable
// named by --dsn-env; its value is never printed or written to a project. The
// project records only a connection descriptor that names that variable
// (connections/<env>/<catalog>.json), exactly as a PostgreSQL, CouchDB, PostgREST
// or libSQL catalog does, so a saved project never holds an executable endpoint.
// The scoped API key itself is read from DATATUG_TYPESENSE_API_KEY for each request
// by the adapter, exactly as it is for `query run`; it is never part of the
// descriptor.
type TypesenseScanParams struct {
	dsnEnv         string
	catalog        string
	ref            dbcopy.BackendRef
	host           string
	port           int
	descriptorPath string
}

var _ dbconnection.Params = (*TypesenseScanParams)(nil)

// NewTypesenseScanParams reads the Typesense source held by the environment
// variable dsnEnv and names the catalog of environment that the scan will write.
// It performs no request and holds no credential.
func NewTypesenseScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*TypesenseScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if !dbcopy.IsTypesenseScheme(ref.Scheme) {
		return nil, fmt.Errorf("environment variable %s must hold a typesense://, typesense+https:// or typesense+http:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		return nil, fmt.Errorf("environment %q and database %q must each be a plain name to be recorded in a project", dbcopy.SourceIDDisplay(environment), dbcopy.SourceIDDisplay(catalog))
	}
	host, port := typesenseScanTarget(ref)
	return &TypesenseScanParams{
		dsnEnv:         dsnEnv,
		catalog:        catalog,
		ref:            ref,
		host:           host,
		port:           port,
		descriptorPath: ConnectionDescriptorPath(environment, catalog),
	}, nil
}

// typesenseScanTarget is the host and the port of the origin, exposed as
// non-secret metadata through dbconnection.Params. The project itself records only
// the connection descriptor, never this endpoint.
func typesenseScanTarget(ref dbcopy.BackendRef) (host string, port int) {
	host, portText, err := net.SplitHostPort(ref.TypesenseHost)
	if err == nil {
		port, _ = strconv.Atoi(portText)
		return host, port
	}
	return strings.TrimSuffix(strings.TrimPrefix(ref.TypesenseHost, "["), "]"), 0
}

// Driver implements dbconnection.Params.
func (*TypesenseScanParams) Driver() string { return DriverTypesense }

// Mode implements dbconnection.Params: a scan only reads.
func (*TypesenseScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }

// Server implements dbconnection.Params: the host of the origin.
func (p *TypesenseScanParams) Server() string { return p.host }

// Port implements dbconnection.Params: the port of the origin, 0 when it names
// none.
func (p *TypesenseScanParams) Port() int { return p.port }

// Catalog implements dbconnection.Params: the catalog ID in the project.
func (p *TypesenseScanParams) Catalog() string { return p.catalog }

// User implements dbconnection.Params: Typesense credentials are not part of the
// endpoint, so a project never records a user here.
func (*TypesenseScanParams) User() string { return "" }

// ConnectionString implements dbconnection.Params: the "env:NAME" source, never
// the origin's value and never a credential.
func (p *TypesenseScanParams) ConnectionString() string { return p.ref.Raw }

// String implements dbconnection.Params with the same text as ConnectionString,
// so formatting the parameters cannot print an endpoint.
func (p *TypesenseScanParams) String() string { return p.ref.Raw }

// GoString makes %#v print the same text as String.
func (p *TypesenseScanParams) GoString() string { return p.String() }

// LogTarget is what a scan logs: where the origin is read from, never a part of
// it.
func (p *TypesenseScanParams) LogTarget() string {
	return "the Typesense source is read from the environment variable " + p.dsnEnv
}

// Path is the project-relative path of the connection descriptor the scan
// writes, which UpdateDbSchema records as the catalog's path: only the name of
// the environment variable, never the origin or a credential.
func (p *TypesenseScanParams) Path() string { return p.descriptorPath }

// WriteDescriptor writes the Typesense connection descriptor into projectDir: a
// file that names the environment variable and nothing else, using the same
// plain-file rules as a PostgreSQL descriptor (see
// PostgresScanParams.WriteDescriptor). It replaces a descriptor already there,
// and returns the function that takes the write back for a failed save.
func (p *TypesenseScanParams) WriteDescriptor(projectDir string) (undo func() error, err error) {
	return writeDescriptorFile(projectDir, p.descriptorPath, p.dsnEnv)
}

// SourceRef is the source the scan opens: an "env:NAME" ref whose Path holds the
// real origin. Only the open path reads Path.
func (p *TypesenseScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

// typesenseScanSource is what scanTypesenseCatalog needs of its parameters.
type typesenseScanSource interface {
	SourceRef() dbcopy.BackendRef
}

// scanTypesenseCatalog reads one Typesense collection through the generic
// dbschema.SchemaReader seam: the collection's real declared field types, the
// implicit non-null string id primary key, and no invented indexes, constraints or
// foreign keys (the adapter reports those as unsupported, which the scanner
// records as none). It opens the source through pkg/dbcopy and classifies a
// failure the same way every other scan does.
//
// Options.SourceDefinitions keeps each field's native declared type, so a
// Typesense "string[]" stays a native array in the project instead of being
// flattened into a scalar portable type. The implicit id is reported by the
// adapter as a non-nullable portable string and as the primary key.
//
// The collection is recorded under the logical ServerDefaultSchema catalog
// grouping, exactly as a CouchDB collection is. Typesense has no SQL namespace:
// the grouping exists only so one catalog can hold the collection, and
// DefaultSchemaForDriver/PolicyCollectionName resolve "default.<collection>" and a
// bare "<collection>" to the same policy name.
func scanTypesenseCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	params, ok := connectionParams.(typesenseScanSource)
	if !ok {
		return nil, fmt.Errorf("a Typesense scan needs connection parameters built from an environment variable (--dsn-env NAME)")
	}
	source := params.SourceRef()
	scanDB, err := openSchemaScan(source, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open Typesense: %w", source.OpenFailure(err))
	}
	defer func() { _ = scanDB.Close() }()

	catalogID := connectionParams.Catalog()
	provider := dalgoschema.NewSchemaProviderWithOptions(scanDB, nil, catalogID, DefaultSchemaForDriver(DriverTypesense), dalgoschema.Options{
		SourceDefinitions: true,
	})
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		if dbcopy.IsTypesenseMetadataForbidden(err) {
			return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %s: %w", typesenseMetadataForbiddenText, err)
		}
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", catalogReadFailure(source, err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverTypesense
	return dbCatalog, nil
}

// typesenseMetadataForbiddenText is the fixed sentence a scan shows when the
// credential may search documents but may not read collection metadata. It names
// no endpoint, request or key.
const typesenseMetadataForbiddenText = "the Typesense credential is not permitted to read collection metadata; a scan needs a key with the documents:search, collections:get and collections:list actions on the collection, while a search-only key can still run structured and native reads"
