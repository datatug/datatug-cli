// Package factories generates the five-factory reconciliation demo: one
// application (database family "factory") run as seven SQLite databases,
// five production factories and two test copies, with a few deliberate,
// documented differences for `datatug reconcile` to find. Everything is
// deterministic and offline.
package factories

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/reconcileprofiles"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	_ "modernc.org/sqlite" // pure-Go driver: writing the fixtures needs no cgo
)

// Family is the database family (DbModel) every factory database belongs to.
const Family = "factory"

// Catalog is the catalog id of the factory database in each environment.
const Catalog = "factory"

// CheckID is the demo Check that reconciles the reference data.
const CheckID = "reference-data-agrees"

// Profile ids of the demo.
const (
	ProfileCountries  = "reference-countries"
	ProfileCurrencies = "reference-currencies"
	ProfileProducts   = "products"
	ProfileSettings   = "factory-settings"
)

// Instance is one factory database in one environment.
type Instance struct {
	Environment string
	Tenant      string
	Kind        datatug.EnvironmentKind
	// idBase offsets every surrogate id, so ids never align across factories.
	idBase int
}

// DBPath is the database file of the instance, relative to the project.
func (i Instance) DBPath() string { return filepath.Join("data", i.Environment+".sqlite") }

// Instances are the seven demo databases, in environment order.
var Instances = []Instance{
	{Environment: "factory-a-prod", Tenant: "factory-a", Kind: datatug.EnvironmentKindProduction, idBase: 1000},
	{Environment: "factory-a-test", Tenant: "factory-a", Kind: datatug.EnvironmentKindTest, idBase: 6000},
	{Environment: "factory-b-prod", Tenant: "factory-b", Kind: datatug.EnvironmentKindProduction, idBase: 2000},
	{Environment: "factory-b-test", Tenant: "factory-b", Kind: datatug.EnvironmentKindTest, idBase: 7000},
	{Environment: "factory-c-prod", Tenant: "factory-c", Kind: datatug.EnvironmentKindProduction, idBase: 3000},
	{Environment: "factory-d-prod", Tenant: "factory-d", Kind: datatug.EnvironmentKindProduction, idBase: 4000},
	{Environment: "factory-e-prod", Tenant: "factory-e", Kind: datatug.EnvironmentKindProduction, idBase: 5000},
}

// The deliberate differences, documented in the generated README.
const (
	MissingCurrency         = "CHF"            // factory-d-prod lacks it
	MissingCurrencyIn       = "factory-d-prod" //
	RenamedCountry          = "IE"             // factory-e-prod names it "Eire"
	RenamedCountryIn        = "factory-e-prod" //
	RenamedCountryName      = "Eire"           //
	DescribedSKU            = "BEARING-608"    // factory-c-prod describes it differently
	DescribedSKUIn          = "factory-c-prod" //
	PricedSKU               = "GEAR-20T"       // factory-b-prod prices it differently (permitted)
	PricedSKUIn             = "factory-b-prod" //
	TestSetting             = "smtp_host"      // factory-a-test differs from factory-a-prod
	TestSettingIn           = "factory-a-test" //
	DriftColumn             = "local_notes"    // factory-e-prod has this extra products column
	DriftColumnIn           = "factory-e-prod" //
	describedSKUDescription = "Ball bearing 608, sealed"
	pricedSKUPrice          = 13.75
	testSettingValue        = "smtp.test.factory-a.example"
	demoActor               = "datatug-demo"
	queryCreatedAt          = "2026-11-07T00:00:00Z"
	modelSchema             = "main"
	sqliteDriver            = "sqlite"
)

var demoTime = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

type currency struct{ code, name string }
type country struct{ code, name, currency string }
type product struct {
	sku, description string
	price            float64
}

var currencies = []currency{
	{"CHF", "Swiss Franc"}, {"EUR", "Euro"}, {"GBP", "Pound Sterling"}, {"PLN", "Polish Zloty"}, {"USD", "US Dollar"},
}

var countries = []country{
	{"CH", "Switzerland", "CHF"}, {"DE", "Germany", "EUR"}, {"FR", "France", "EUR"}, {"GB", "United Kingdom", "GBP"},
	{"IE", "Ireland", "EUR"}, {"PL", "Poland", "PLN"}, {"US", "United States", "USD"},
}

var products = []product{
	{"BEARING-608", "Ball bearing 608", 2.40}, {"BOLT-M8", "Hex bolt M8x40", 0.35}, {"GEAR-20T", "Spur gear 20 teeth", 12.50},
	{"NUT-M8", "Hex nut M8", 0.12}, {"WASHER-M8", "Flat washer M8", 0.05},
}

var settings = []struct{ key, value string }{
	{"currency", "EUR"}, {"shift_pattern", "3x8"}, {"smtp_host", "smtp.factory-a.example"}, {"timezone", "Europe/Dublin"},
}

// Seams for failure tests.
var (
	sqlOpen   = sql.Open
	removeAll = os.Remove
	mkdirAll  = os.MkdirAll
	writeFile = os.WriteFile

	addProjectSource     = api.AddProjectSource
	saveModelForeignKeys = filestore.SaveModelForeignKeys
	saveModelIndexes     = filestore.SaveModelIndexes
)

// Generate writes the demo databases and project definitions into the
// project at dir through store: environments with their kinds, one catalog
// each with its tenant, the factory DbModel with its keys, saved queries,
// reconciliation profiles, the reference-data-agrees Check and a README.
// It overwrites what it wrote before.
func Generate(ctx context.Context, dir string, store datatug.ProjectStore) error {
	if err := mkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		return err
	}
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	for _, instance := range Instances {
		if err := writeDatabase(filepath.Join(dir, instance.DBPath()), instance); err != nil {
			return fmt.Errorf("write %s: %w", instance.DBPath(), err)
		}
		if _, err := addProjectSource(ctx, store, dir, api.ProjectSourceRequest{Environment: instance.Environment, ID: Catalog, SQLitePath: filepath.Join(dir, instance.DBPath())}); err != nil {
			return fmt.Errorf("add source of %s: %w", instance.Environment, err)
		}
		envs, err := store.LoadEnvironments(ctx)
		if err != nil {
			return err
		}
		env := envs.GetByID(instance.Environment)
		env.Kind = instance.Kind
		if err := store.SaveEnvironment(ctx, env); err != nil {
			return err
		}
		catalog, err := store.LoadEnvDbCatalog(ctx, instance.Environment, serverID, Catalog)
		if err != nil {
			return err
		}
		catalog.DbModel = Family
		catalog.Tenants = []string{instance.Tenant}
		if err := store.SaveEnvDbCatalog(ctx, instance.Environment, serverID, Catalog, &catalog); err != nil {
			return err
		}
	}
	if err := writeModel(ctx, dir, store); err != nil {
		return err
	}
	if err := writeQueries(dir); err != nil {
		return err
	}
	for _, profile := range Profiles() {
		if err := (reconcileprofiles.Store{Dir: dir}).Save(profile); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(dir, "checks", CheckID+".json"), Check()); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "README.md"), []byte(README), 0o644)
}

func writeJSON(path string, v any) error {
	body, _ := json.MarshalIndent(v, "", "  ") // plain values
	if err := mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFile(path, append(body, '\n'), 0o644)
}

// writeDatabase creates one factory database from scratch.
func writeDatabase(path string, instance Instance) error {
	if err := removeAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := sqlOpen(sqliteDriver, path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	productColumns := "id INTEGER PRIMARY KEY, sku TEXT NOT NULL UNIQUE, description TEXT NOT NULL, price REAL NOT NULL, stock INTEGER NOT NULL"
	if instance.Environment == DriftColumnIn {
		productColumns += ", " + DriftColumn + " TEXT"
	}
	statements := []string{
		`CREATE TABLE currencies (code TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT NOT NULL, currency TEXT NOT NULL REFERENCES currencies(code))`,
		`CREATE TABLE products (` + productColumns + `)`,
		`CREATE TABLE factory_settings (setting_key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, order_no TEXT NOT NULL UNIQUE, product_sku TEXT NOT NULL REFERENCES products(sku), quantity INTEGER NOT NULL, ordered_at TEXT NOT NULL)`,
		`CREATE TABLE inventory_movements (id INTEGER PRIMARY KEY, product_sku TEXT NOT NULL REFERENCES products(sku), order_id INTEGER REFERENCES orders(id), delta INTEGER NOT NULL, moved_at TEXT NOT NULL)`,
		`CREATE TABLE audit_events (id INTEGER PRIMARY KEY, event TEXT NOT NULL, occurred_at TEXT NOT NULL)`,
	}
	var args [][]any
	insert := func(statement string, values ...any) {
		statements = append(statements, statement)
		args = append(args, values)
	}
	for _, c := range currencies {
		if c.code == MissingCurrency && instance.Environment == MissingCurrencyIn {
			continue
		}
		insert(`INSERT INTO currencies (code, name) VALUES (?, ?)`, c.code, c.name)
	}
	for _, c := range countries {
		name := c.name
		if c.code == RenamedCountry && instance.Environment == RenamedCountryIn {
			name = RenamedCountryName
		}
		insert(`INSERT INTO countries (code, name, currency) VALUES (?, ?, ?)`, c.code, name, c.currency)
	}
	for i, p := range products {
		description, price := p.description, p.price
		if p.sku == DescribedSKU && instance.Environment == DescribedSKUIn {
			description = describedSKUDescription
		}
		if p.sku == PricedSKU && instance.Environment == PricedSKUIn {
			price = pricedSKUPrice
		}
		stock := 100 + (instance.idBase/1000)*10 + i
		if instance.Environment == DriftColumnIn {
			insert(`INSERT INTO products (id, sku, description, price, stock, `+DriftColumn+`) VALUES (?, ?, ?, ?, ?, ?)`, instance.idBase+i+1, p.sku, description, price, stock, "kept in hall 2")
		} else {
			insert(`INSERT INTO products (id, sku, description, price, stock) VALUES (?, ?, ?, ?, ?)`, instance.idBase+i+1, p.sku, description, price, stock)
		}
	}
	for _, s := range settings {
		value := s.value
		if s.key == TestSetting && instance.Environment == TestSettingIn {
			value = testSettingValue
		}
		insert(`INSERT INTO factory_settings (setting_key, value) VALUES (?, ?)`, s.key, value)
	}
	letter := strings.ToUpper(instance.Tenant[len(instance.Tenant)-1:])
	for i := 1; i <= 3; i++ {
		sku := products[i%len(products)].sku
		at := demoTime.AddDate(0, 0, -i).Format(time.RFC3339)
		insert(`INSERT INTO orders (id, order_no, product_sku, quantity, ordered_at) VALUES (?, ?, ?, ?, ?)`, instance.idBase+i, fmt.Sprintf("%s-%04d", letter, i), sku, i*5, at)
		insert(`INSERT INTO inventory_movements (id, product_sku, order_id, delta, moved_at) VALUES (?, ?, ?, ?, ?)`, instance.idBase+i, sku, instance.idBase+i, -i*5, at)
		insert(`INSERT INTO audit_events (id, event, occurred_at) VALUES (?, ?, ?)`, instance.idBase+i, "order "+fmt.Sprintf("%s-%04d", letter, i)+" shipped", at)
	}
	for i, statement := range statements {
		var values []any
		if j := i - (len(statements) - len(args)); j >= 0 {
			values = args[j]
		}
		if _, err := db.Exec(statement, values...); err != nil {
			return err
		}
	}
	return nil
}

type modelTable struct {
	name    string
	columns []api.CatalogColumn
}

func column(name, dbType string, pk int, nullable bool) api.CatalogColumn {
	return api.CatalogColumn{Name: name, DbType: dbType, PrimaryKeyPosition: pk, IsNullable: nullable}
}

var modelTables = []modelTable{
	{"currencies", []api.CatalogColumn{column("code", "TEXT", 1, false), column("name", "TEXT", 0, false)}},
	{"countries", []api.CatalogColumn{column("code", "TEXT", 1, false), column("name", "TEXT", 0, false), column("currency", "TEXT", 0, false)}},
	{"products", []api.CatalogColumn{column("id", "INTEGER", 1, false), column("sku", "TEXT", 0, false), column("description", "TEXT", 0, false), column("price", "REAL", 0, false), column("stock", "INTEGER", 0, false)}},
	{"factory_settings", []api.CatalogColumn{column("setting_key", "TEXT", 1, false), column("value", "TEXT", 0, false)}},
	{"orders", []api.CatalogColumn{column("id", "INTEGER", 1, false), column("order_no", "TEXT", 0, false), column("product_sku", "TEXT", 0, false), column("quantity", "INTEGER", 0, false), column("ordered_at", "TEXT", 0, false)}},
	{"inventory_movements", []api.CatalogColumn{column("id", "INTEGER", 1, false), column("product_sku", "TEXT", 0, false), column("order_id", "INTEGER", 0, true), column("delta", "INTEGER", 0, false), column("moved_at", "TEXT", 0, false)}},
	{"audit_events", []api.CatalogColumn{column("id", "INTEGER", 1, false), column("event", "TEXT", 0, false), column("occurred_at", "TEXT", 0, false)}},
}

func tableKey(name string) datatug.DBCollectionKey {
	return datatug.NewTableKey(name, modelSchema, "", nil)
}

// writeModel stores the factory DbModel with its foreign keys and unique
// indexes, the schema-only facts `datatug db roles suggest` reads.
func writeModel(ctx context.Context, dir string, store datatug.ProjectStore) error {
	model := &datatug.DbModel{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: Family, Title: "Factory application"}}}
	models, ok := store.(datatug.DbModelsStore)
	if !ok {
		return errors.New("the project store cannot save database models")
	}
	if err := models.SaveDbModel(ctx, model); err != nil {
		return err
	}
	// The tables are stored as a scan stores them: one columns file per table.
	for _, t := range modelTables {
		path := filepath.Join(dir, storage.DbModelsFolder, Family, modelSchema, "tables", t.name, t.name+"."+storage.ColumnsFileSuffix+".json")
		if err := writeJSON(path, map[string]any{"columns": t.columns}); err != nil {
			return err
		}
	}
	fk := func(table, name, columnName, refTable, refColumn string) datatug.TableForeignKeys {
		return datatug.TableForeignKeys{Table: tableKey(table), ForeignKeys: datatug.ForeignKeys{{Name: name, Columns: []string{columnName}, RefTable: tableKey(refTable), RefColumns: []string{refColumn}}}}
	}
	movements := fk("inventory_movements", "fk_movements_product", "product_sku", "products", "sku")
	movements.ForeignKeys = append(movements.ForeignKeys, &datatug.ForeignKey{Name: "fk_movements_order", Columns: []string{"order_id"}, RefTable: tableKey("orders"), RefColumns: []string{"id"}})
	if err := saveModelForeignKeys(dir, Family, []datatug.TableForeignKeys{
		fk("countries", "fk_countries_currency", "currency", "currencies", "code"),
		movements,
		fk("orders", "fk_orders_product", "product_sku", "products", "sku"),
	}); err != nil {
		return err
	}
	unique := func(table, name, columnName string) datatug.TableIndexes {
		return datatug.TableIndexes{Table: tableKey(table), Indexes: []*datatug.Index{{Name: name, Type: "BTREE", IsUnique: true, IsUniqueConstraint: true, Columns: []*datatug.IndexColumn{{Name: columnName}}}}}
	}
	return saveModelIndexes(dir, Family, []datatug.TableIndexes{
		unique("orders", "orders_order_no", "order_no"),
		unique("products", "products_sku", "sku"),
	})
}

// Queries maps each saved query id to the table it reads.
var Queries = map[string]string{
	"countries": "countries", "currencies": "currencies", "products": "products", "factory-settings": "factory_settings",
	"orders": "orders", "inventory-movements": "inventory_movements", "audit-events": "audit_events",
}

func writeQueries(dir string) error {
	for id, table := range Queries {
		definition := map[string]any{"createdAt": queryCreatedAt, "id": id, "title": "All " + strings.ReplaceAll(table, "_", " "), "type": string(datatug.QueryTypeTugQL)}
		if err := writeJSON(filepath.Join(dir, "queries", id+".query.json"), definition); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dir, "queries", id+".query.tugql.yaml"), []byte("from:\n  name: "+table+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Profiles are the demo's reconciliation profiles.
func Profiles() []reconcile.Profile {
	production := reconcile.InstanceSelector{Family: Family, Kind: datatug.EnvironmentKindProduction}
	return []reconcile.Profile{
		{
			ID: ProfileCountries, Title: "Countries agree across production factories", QueryID: "countries", Instances: production,
			Rules: reconcile.Rules{Key: []string{"code"}, Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceRequired},
		},
		{
			ID: ProfileCurrencies, Title: "Currencies agree across production factories", QueryID: "currencies", Instances: production,
			Rules: reconcile.Rules{Key: []string{"code"}, Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceRequired},
		},
		{
			ID: ProfileProducts, Title: "Product master data: shared descriptions, local prices and stock", QueryID: "products", Instances: production,
			Rules: reconcile.Rules{Key: []string{"sku"}, Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceAny, Fields: map[string]reconcile.FieldPolicy{
				"id": reconcile.FieldIgnored, "price": reconcile.FieldInstanceSpecific, "stock": reconcile.FieldIgnored,
			}},
		},
		{
			ID: ProfileSettings, Title: "Factory A settings: test against production", QueryID: "factory-settings",
			Instances: reconcile.InstanceSelector{Family: Family, Tenants: []string{"factory-a"}},
			Rules: reconcile.Rules{Key: []string{"setting_key"}, Mode: reconcile.ModeBaseline, Baseline: "factory-a-prod", Presence: reconcile.PresenceRequired,
				Fields: map[string]reconcile.FieldPolicy{"value": reconcile.FieldInstanceSpecific}},
		},
	}
}

// Check is the demo Check: its steps reconcile the reference data.
func Check() core.Check {
	return core.Check{
		ID: CheckID, Title: "Reference data agrees across factories", Version: 1, Inputs: []core.Input{},
		Description: "Countries and currencies must be the same in every production factory.",
		Steps: []core.Step{
			{ID: "countries", Kind: "reconcile", Profile: ProfileCountries},
			{ID: "currencies", Kind: "reconcile", Profile: ProfileCurrencies},
		},
		UpdatedAt: demoTime, UpdatedBy: demoActor, CreatedAt: queryCreatedAt,
	}
}

// README documents the deliberate differences and the expected verdicts.
const README = `# Five-factory reconciliation demo

One application, database family ` + "`factory`" + `, runs as seven SQLite databases: five production
factories (` + "`factory-a-prod`" + ` … ` + "`factory-e-prod`" + `, kind production) and two test copies
(` + "`factory-a-test`" + `, ` + "`factory-b-test`" + `, kind test). Each environment has one catalog, ` + "`factory`" + `,
labelled with its tenant (` + "`factory-a`" + ` … ` + "`factory-e`" + `). Surrogate ids differ in every factory, so
rows align only by their semantic key.

## Deliberate differences

| Where | What | Kind of difference |
|---|---|---|
| factory-d-prod | currency ` + "`CHF`" + ` is missing | missing reference record |
| factory-e-prod | country ` + "`IE`" + ` is named "Eire" (others: "Ireland") | incorrect reference value |
| factory-c-prod | product ` + "`BEARING-608`" + ` has description "Ball bearing 608, sealed" | unexpected master-data value |
| factory-b-prod | product ` + "`GEAR-20T`" + ` costs 13.75 (others: 12.5) | permitted: price is instance-specific |
| factory-a-test | setting ` + "`smtp_host`" + ` is ` + "`smtp.test.factory-a.example`" + ` | environment-specific |
| factory-e-prod | ` + "`products`" + ` has an extra column ` + "`local_notes`" + ` | schema drift |

Stock levels differ in every factory; the products profile does not compare them (ignored), so the one permitted price difference stands out.

## Profiles and expected verdicts

| Profile | Command | Expected |
|---|---|---|
| reference-countries | ` + "`datatug reconcile --profile reference-countries`" + ` | discrepancies: IE unexpected, groups {a,b,c,d: Ireland} {e: Eire}, factory-e-prod deviating, no group authoritative (exit 1) |
| reference-currencies | ` + "`datatug reconcile --profile reference-currencies`" + ` | discrepancies: CHF missing in factory-d-prod (exit 1) |
| products | ` + "`datatug reconcile --profile products`" + ` | discrepancies: BEARING-608 description unexpected (factory-c-prod deviating); GEAR-20T price expected, factory-b-prod stays consistent; column ` + "`local_notes`" + ` missing in a–d (exit 1) |
| factory-settings | ` + "`datatug reconcile --profile factory-settings`" + ` | consistent: smtp_host differs as expected, baseline factory-a-prod (exit 0) |

The Check ` + "`reference-data-agrees`" + ` runs reference-countries and reference-currencies as reconcile
steps; it fails on the discrepancies above (` + "`datatug checks run reference-data-agrees`" + `, exit 1).
A factory that cannot be read makes its step inconclusive, never a pass.

Ad hoc: ` + "`datatug reconcile --query countries --key code --family factory --kind production`" + `.

## Data roles

` + "`datatug db roles suggest factory`" + ` classifies the tables from schema alone (countries and currencies
reference, products master, factory_settings configuration, orders and inventory_movements
transactional, audit_events event). Confirm one (` + "`datatug db roles confirm factory/countries`" + `) and
` + "`datatug reconcile --query countries --family factory --kind production`" + ` uses its rule without ` + "`--key`" + `.

## Running

Serve the project to an agent, then reconcile:

    datatug serve --project <this folder> --allow-writes
    datatug reconcile --profile reference-countries

--allow-writes is needed only to record Check runs (` + "`datatug checks run`" + `). The project has no access
policies, so the agent reads unrestricted; add a ` + "`policies/`" + ` folder and serve with --as/--role to
reconcile under Perimeter.

Reconcile is read-only: it never writes to a factory database.

## Refusing one factory

A local policy rule may name an ` + "`instance`" + ` (` + "`<environment>/<catalog>`" + `) or an ` + "`environment`" + `
kind. Put this in ` + "`policies/factories.yaml`" + ` and serve with ` + "`--as analyst`" + `:

    apiVersion: dalgo.io/access/v1
    kind: AccessPolicy
    metadata: {name: factories}
    default: deny
    scopes:
      - path: /countries
        rules:
          - {id: never-factory-e, effect: deny, operations: [query], instance: factory-e-prod/factory}
          - {id: read-countries, effect: allow, operations: [query]}

` + "`datatug reconcile --profile reference-countries`" + ` then reports factory-e-prod as permission_denied and
the result as inconclusive, never consistent. The deny sits at the same scope as the allow so it wins;
a rule for another instance is left out of a read as if it were not there.
`
