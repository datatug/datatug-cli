package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDbModelSchemaReadsTheStoredTables(t *testing.T) {
	dir := t.TempDir()
	schema, err := GetDbModelSchema(dir, "factory")
	require.NoError(t, err)
	require.Empty(t, schema.Relations)

	folder := filepath.Join(dir, "dbmodels", "factory", "main", "tables", "countries")
	require.NoError(t, os.MkdirAll(folder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "countries.columns.json"), []byte(`{"columns":[{"name":"code","dbType":"TEXT","pkPosition":1}]}`), 0o644))
	schema, err = GetDbModelSchema(dir, "factory")
	require.NoError(t, err)
	require.Equal(t, []CatalogRelation{{Schema: "main", Name: "countries", DbType: "BASE TABLE", Columns: []CatalogColumn{{Name: "code", DbType: "TEXT", PrimaryKeyPosition: 1}}, folder: ""}}, schema.Relations)

	require.NoError(t, os.WriteFile(filepath.Join(folder, "countries.columns.json"), []byte("{"), 0o644))
	_, err = GetDbModelSchema(dir, "factory")
	require.Error(t, err)
}
