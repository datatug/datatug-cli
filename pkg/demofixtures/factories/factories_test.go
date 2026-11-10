package factories

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/reconcileprofiles"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func generate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, Generate(context.Background(), dir, filestore.NewProjectStore("factories", dir)))
	return dir
}

func queryStrings(t *testing.T, path, query string) []string {
	t.Helper()
	db, err := sql.Open(sqliteDriver, path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(query)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var value string
		require.NoError(t, rows.Scan(&value))
		out = append(out, value)
	}
	return out
}

func TestGenerateWritesTheDocumentedDifferences(t *testing.T) {
	dir := generate(t)
	db := func(env string) string { return filepath.Join(dir, "data", env+".sqlite") }
	require.NotContains(t, queryStrings(t, db(MissingCurrencyIn), `SELECT code FROM currencies`), MissingCurrency)
	require.Contains(t, queryStrings(t, db("factory-a-prod"), `SELECT code FROM currencies`), MissingCurrency)
	require.Equal(t, []string{RenamedCountryName}, queryStrings(t, db(RenamedCountryIn), `SELECT name FROM countries WHERE code = 'IE'`))
	require.Equal(t, []string{"Ireland"}, queryStrings(t, db("factory-a-prod"), `SELECT name FROM countries WHERE code = 'IE'`))
	require.Equal(t, []string{describedSKUDescription}, queryStrings(t, db(DescribedSKUIn), `SELECT description FROM products WHERE sku = '`+DescribedSKU+`'`))
	require.Equal(t, []string{"13.75"}, queryStrings(t, db(PricedSKUIn), `SELECT CAST(price AS TEXT) FROM products WHERE sku = '`+PricedSKU+`'`))
	require.Equal(t, []string{testSettingValue}, queryStrings(t, db(TestSettingIn), `SELECT value FROM factory_settings WHERE setting_key = '`+TestSetting+`'`))
	require.Contains(t, queryStrings(t, db(DriftColumnIn), `SELECT name FROM pragma_table_info('products')`), DriftColumn)
	require.NotContains(t, queryStrings(t, db("factory-a-prod"), `SELECT name FROM pragma_table_info('products')`), DriftColumn)
	require.NotEqual(t, queryStrings(t, db("factory-a-prod"), `SELECT CAST(id AS TEXT) FROM products`), queryStrings(t, db("factory-b-prod"), `SELECT CAST(id AS TEXT) FROM products`),
		"surrogate ids never align across factories")

	store := filestore.NewProjectStore("factories", dir)
	envs, err := store.LoadEnvironments(context.Background())
	require.NoError(t, err)
	require.Len(t, envs, len(Instances))
	for _, instance := range Instances {
		env := envs.GetByID(instance.Environment)
		require.Equal(t, instance.Kind, env.Kind)
		catalogs, err := store.LoadEnvDbCatalogs(context.Background(), instance.Environment)
		require.NoError(t, err)
		require.Len(t, catalogs, 1)
		require.Equal(t, Family, catalogs[0].DbModel)
		require.Equal(t, []string{instance.Tenant}, catalogs[0].Tenants)
	}
	profiles, err := reconcileprofiles.Store{Dir: dir}.List()
	require.NoError(t, err)
	require.Len(t, profiles, 4)
	schema, err := api.GetDbModelSchema(dir, Family)
	require.NoError(t, err)
	require.Len(t, schema.Relations, len(modelTables))
	foreignKeys, err := filestore.LoadModelForeignKeys(dir, Family)
	require.NoError(t, err)
	require.Len(t, foreignKeys, 3)
	indexes, err := filestore.LoadModelIndexes(dir, Family)
	require.NoError(t, err)
	require.Len(t, indexes, 2)
	require.NoError(t, core.ValidateCheck(Check()))
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	require.Equal(t, README, string(readme))
	for id := range Queries {
		require.FileExists(t, filepath.Join(dir, "queries", id+".query.tugql.yaml"))
	}
}

func TestGenerateIsRepeatable(t *testing.T) {
	dir := generate(t)
	before := queryStrings(t, filepath.Join(dir, "data", "factory-c-prod.sqlite"), `SELECT sku || '|' || description || '|' || price || '|' || stock FROM products ORDER BY sku`)
	require.NoError(t, Generate(context.Background(), dir, filestore.NewProjectStore("factories", dir)))
	after := queryStrings(t, filepath.Join(dir, "data", "factory-c-prod.sqlite"), `SELECT sku || '|' || description || '|' || price || '|' || stock FROM products ORDER BY sku`)
	require.Equal(t, before, after)
}

type failingStore struct {
	datatug.ProjectStore
	fail  string
	calls *int
}

var errStore = errors.New("store failed")

func (s failingStore) LoadEnvironments(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
	if s.fail == "LoadEnvironments" {
		// The first read is AddProjectSource's own; fail Generate's.
		if *s.calls++; *s.calls > 1 {
			return nil, errStore
		}
	}
	return s.ProjectStore.LoadEnvironments(ctx, o...)
}

func (s failingStore) SaveEnvironment(ctx context.Context, env *datatug.Environment) error {
	if s.fail == "SaveEnvironment" {
		// The first write is AddProjectSource's own; fail Generate's.
		if *s.calls++; *s.calls > 1 {
			return errStore
		}
	}
	return s.ProjectStore.SaveEnvironment(ctx, env)
}

func (s failingStore) LoadEnvDbCatalog(ctx context.Context, env, server, catalog string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
	if s.fail == "LoadEnvDbCatalog" {
		return datatug.DbCatalog{}, errStore
	}
	return s.ProjectStore.LoadEnvDbCatalog(ctx, env, server, catalog, o...)
}

func (s failingStore) SaveEnvDbCatalog(ctx context.Context, env, server, id string, catalog *datatug.DbCatalog) error {
	if s.fail == "SaveEnvDbCatalog" {
		// The first write is AddProjectSource's own; fail Generate's.
		if *s.calls++; *s.calls > 1 {
			return errStore
		}
	}
	return s.ProjectStore.SaveEnvDbCatalog(ctx, env, server, id, catalog)
}

type modelStore struct {
	failingStore
	datatug.DbModelsStore
}

func (s modelStore) SaveDbModel(ctx context.Context, model *datatug.DbModel) error {
	if s.fail == "SaveDbModel" {
		return errStore
	}
	return s.DbModelsStore.SaveDbModel(ctx, model)
}

func TestGenerateReportsEveryFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	restore := func() {
		sqlOpen, removeAll, mkdirAll, writeFile = sql.Open, os.Remove, os.MkdirAll, os.WriteFile
		addProjectSource, saveModelForeignKeys, saveModelIndexes = api.AddProjectSource, filestore.SaveModelForeignKeys, filestore.SaveModelIndexes
	}
	t.Cleanup(restore)
	run := func(store func(dir string) datatug.ProjectStore) error {
		dir := t.TempDir()
		if store == nil {
			store = func(dir string) datatug.ProjectStore {
				real := filestore.NewProjectStore("factories", dir)
				return modelStore{failingStore: failingStore{ProjectStore: real}, DbModelsStore: real.(datatug.DbModelsStore)}
			}
		}
		return Generate(ctx, dir, store(dir))
	}
	failOn := func(fail string) func(string) datatug.ProjectStore {
		return func(dir string) datatug.ProjectStore {
			real := filestore.NewProjectStore("factories", dir)
			return modelStore{failingStore: failingStore{ProjectStore: real, fail: fail, calls: new(int)}, DbModelsStore: real.(datatug.DbModelsStore)}
		}
	}
	for _, fail := range []string{"LoadEnvironments", "SaveEnvironment", "LoadEnvDbCatalog", "SaveEnvDbCatalog", "SaveDbModel"} {
		require.ErrorIs(t, run(failOn(fail)), errStore, fail)
	}
	require.ErrorContains(t, run(func(dir string) datatug.ProjectStore {
		return failingStore{ProjectStore: filestore.NewProjectStore("factories", dir)}
	}), "cannot save database models")

	mkdirAll = func(string, fs.FileMode) error { return boom }
	require.ErrorIs(t, run(nil), boom)
	restore()
	removeAll = func(string) error { return boom }
	require.ErrorIs(t, run(nil), boom)
	restore()
	sqlOpen = func(string, string) (*sql.DB, error) { return nil, boom }
	require.ErrorIs(t, run(nil), boom)
	restore()
	sqlOpen = func(driver, path string) (*sql.DB, error) {
		return sql.Open(driver, filepath.Join(path, "missing", "x.sqlite"))
	}
	require.Error(t, run(nil), "a statement that fails stops the database")
	restore()
	addProjectSource = func(context.Context, datatug.ProjectStore, string, api.ProjectSourceRequest) (api.AddedSource, error) {
		return api.AddedSource{}, boom
	}
	require.ErrorIs(t, run(nil), boom)
	restore()
	saveModelForeignKeys = func(string, string, []datatug.TableForeignKeys) error { return boom }
	require.ErrorIs(t, run(nil), boom)
	restore()
	saveModelIndexes = func(string, string, []datatug.TableIndexes) error { return boom }
	require.ErrorIs(t, run(nil), boom)
	restore()
	for _, suffix := range []string{".columns.json", ".query.json", ".query.tugql.yaml", CheckID + ".json", "README.md"} {
		writeFile = func(path string, data []byte, mode fs.FileMode) error {
			if strings.HasSuffix(path, suffix) {
				return boom
			}
			return os.WriteFile(path, data, mode)
		}
		require.ErrorIs(t, run(nil), boom, suffix)
	}
	restore()
	mkdirAll = func(path string, mode fs.FileMode) error {
		if strings.HasSuffix(path, "checks") {
			return boom
		}
		return os.MkdirAll(path, mode)
	}
	require.ErrorIs(t, run(nil), boom)
	restore()
	require.Error(t, run(func(dir string) datatug.ProjectStore {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "reconciliations"), nil, 0o644))
		real := filestore.NewProjectStore("factories", dir)
		return modelStore{failingStore: failingStore{ProjectStore: real}, DbModelsStore: real.(datatug.DbModelsStore)}
	}), "a profile that cannot be saved stops the demo")
}
