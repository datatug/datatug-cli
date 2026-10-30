package api

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// ClickHouseScanParams persists only a strict dsnEnv descriptor. The operator
// chooses the endpoint, credentials and permitted relations outside the project.
type ClickHouseScanParams struct {
	dsnEnv, catalog, descriptorPath string
	ref                             dbcopy.BackendRef
}

var _ dbconnection.Params = (*ClickHouseScanParams)(nil)

func NewClickHouseScanParams(lookup func(string) (string, bool), dsnEnv, environment, catalog string) (*ClickHouseScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookup)
	if err != nil {
		return nil, err
	}
	if !dbcopy.IsClickHouseScheme(ref.Scheme) {
		return nil, fmt.Errorf("environment variable %s must hold a ClickHouse URL", dsnEnv)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		return nil, fmt.Errorf("environment and database must be plain project names")
	}
	return &ClickHouseScanParams{dsnEnv: dsnEnv, catalog: catalog, ref: ref, descriptorPath: ConnectionDescriptorPath(environment, catalog)}, nil
}
func (*ClickHouseScanParams) Driver() string          { return DriverClickHouse }
func (*ClickHouseScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }
func (p *ClickHouseScanParams) Server() string {
	h, _, _ := net.SplitHostPort(p.ref.ClickHouseAddress)
	return h
}
func (p *ClickHouseScanParams) Port() int {
	_, s, _ := net.SplitHostPort(p.ref.ClickHouseAddress)
	n, _ := strconv.Atoi(s)
	return n
}
func (p *ClickHouseScanParams) Catalog() string          { return p.catalog }
func (*ClickHouseScanParams) User() string               { return "" }
func (p *ClickHouseScanParams) ConnectionString() string { return p.ref.Raw }
func (p *ClickHouseScanParams) String() string           { return p.ref.Raw }
func (p *ClickHouseScanParams) GoString() string         { return p.String() }
func (p *ClickHouseScanParams) LogTarget() string {
	return "ClickHouse source from environment variable " + p.dsnEnv
}
func (p *ClickHouseScanParams) Path() string { return p.descriptorPath }
func (p *ClickHouseScanParams) WriteDescriptor(projectDir string) (func() error, error) {
	return writeDescriptorFile(projectDir, p.descriptorPath, p.dsnEnv)
}
func (p *ClickHouseScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

func scanClickHouseCatalog(ctx context.Context, connection dbconnection.Params) (*datatug.DbCatalog, error) {
	p, ok := connection.(interface{ SourceRef() dbcopy.BackendRef })
	if !ok {
		return nil, fmt.Errorf("ClickHouse scan requires --dsn-env")
	}
	ref := p.SourceRef()
	db, err := openSchemaScan(ref, ctx)
	if err != nil {
		return nil, fmt.Errorf("ClickHouse scan: %s", dbcopy.ClickHouseFailureReason(err))
	}
	defer func() { _ = db.Close() }()
	provider := dalgoschema.NewSchemaProvider(db, nil, connection.Catalog(), ref.ClickHouseDatabase)
	catalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, connection.Catalog())
	if err != nil {
		return nil, fmt.Errorf("ClickHouse catalog: %s", dbcopy.ClickHouseFailureReason(err))
	}
	catalog.ID = connection.Catalog()
	catalog.Driver = DriverClickHouse
	return catalog, nil
}
