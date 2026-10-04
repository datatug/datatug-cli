package dbcopy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckDescriptorEnvName_PrefixIsAlwaysAllowed(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"DATATUG_SHOP_PG_URL", "DATATUG_X", "DATATUG_"} {
		assert.NoError(t, CheckDescriptorEnvName(name, fakeEnv(nil)), name)
	}
}

func TestCheckDescriptorEnvName_OperatorAllowList(t *testing.T) {
	t.Parallel()
	lookup := fakeEnv(map[string]string{DescriptorEnvAllowList: "SHOP_PG_URL, OTHER_PG_URL\n\tTHIRD_PG_URL,,"})
	for _, name := range []string{"SHOP_PG_URL", "OTHER_PG_URL", "THIRD_PG_URL"} {
		assert.NoError(t, CheckDescriptorEnvName(name, lookup), name)
	}
	for _, name := range []string{"SHOP_PG", "SHOP_PG_URL2", "DATABASE_URL", "AWS_SECRET_ACCESS_KEY", "PGPASSWORD"} {
		assert.Error(t, CheckDescriptorEnvName(name, lookup), name)
	}
}

func TestCheckDescriptorEnvName_RefusesEverythingElseAndNamesTheRule(t *testing.T) {
	t.Parallel()
	for name, lookup := range map[string]func(string) (string, bool){
		"no allow-list variable": fakeEnv(nil),
		"an empty allow-list":    fakeEnv(map[string]string{DescriptorEnvAllowList: " , "}),
	} {
		err := CheckDescriptorEnvName("DATABASE_URL", lookup)
		if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), "DATABASE_URL", name)
			assert.Contains(t, err.Error(), DescriptorEnvPrefix, name)
			assert.Contains(t, err.Error(), DescriptorEnvAllowList, name)
		}
	}
}

func TestReadPostgresDescriptor_AppliesTheEnvNameRule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		assert.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}
	prefixed := write("prefixed.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	listed := write("listed.json", `{"dsnEnv":"SHOP_PG_URL"}`)
	foreign := write("foreign.json", `{"dsnEnv":"PROD_DATABASE_URL"}`)
	allow := fakeEnv(map[string]string{DescriptorEnvAllowList: "SHOP_PG_URL"})

	descriptor, err := readPostgresDescriptor(prefixed, fakeEnv(nil))
	assert.NoError(t, err)
	assert.Equal(t, "DATATUG_SHOP_PG_URL", descriptor.DSNEnv)

	descriptor, err = readPostgresDescriptor(listed, allow)
	assert.NoError(t, err)
	assert.Equal(t, "SHOP_PG_URL", descriptor.DSNEnv)

	_, err = readPostgresDescriptor(listed, fakeEnv(nil))
	assert.ErrorContains(t, err, DescriptorEnvAllowList)

	// A cloned project that names another variable of the operator's environment.
	descriptor, err = readPostgresDescriptor(foreign, allow)
	assert.Equal(t, PostgresDescriptor{}, descriptor)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "PROD_DATABASE_URL")
		assert.Contains(t, err.Error(), DescriptorEnvPrefix)
		assert.Contains(t, err.Error(), DescriptorEnvAllowList)
	}
}

func TestReadPostgresDescriptor_ReadsTheProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "descriptor.json")
	assert.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"DT03_SHOP_PG_URL"}`), 0o600))

	_, err := ReadPostgresDescriptor(path)
	assert.ErrorContains(t, err, DescriptorEnvAllowList)

	t.Setenv(DescriptorEnvAllowList, "DT03_SHOP_PG_URL")
	descriptor, err := ReadPostgresDescriptor(path)
	assert.NoError(t, err)
	assert.Equal(t, "env:DT03_SHOP_PG_URL", descriptor.SourceURL())
}
