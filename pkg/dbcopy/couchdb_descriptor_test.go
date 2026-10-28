package dbcopy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeCouchDBDescriptor(t *testing.T) {
	t.Parallel()
	descriptor, err := DecodeCouchDBDescriptor([]byte(`{"dsnEnv":"DATATUG_SHOP_COUCH"}`))
	require.NoError(t, err)
	assert.Equal(t, "DATATUG_SHOP_COUCH", descriptor.DSNEnv)
	assert.Equal(t, "env:DATATUG_SHOP_COUCH", descriptor.SourceURL())

	for _, tc := range []struct{ name, body, want string }{
		{"an endpoint field", `{"dsnEnv":"DATATUG_SHOP_COUCH","url":"couchdb://alice:s3cret@h/orders"}`, "unknown CouchDB connection field"},
		{"a password field", `{"dsnEnv":"DATATUG_SHOP_COUCH","password":"s3cret"}`, "unknown CouchDB connection field"},
		{"a URL instead of a name", `{"dsnEnv":"couchdb://alice:s3cret@h/orders"}`, "^[A-Z][A-Z0-9_]*$"},
		{"empty", `{}`, "requires dsnEnv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeCouchDBDescriptor([]byte(tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "CouchDB")
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "alice")
		})
	}
}

func TestReadCouchDBDescriptorAppliesTheOperatorVariableRule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"SHOP_COUCH_URL"}`), 0o600))

	_, err := readCouchDBDescriptor(path, fakeEnv(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), DescriptorEnvAllowList)

	descriptor, err := readCouchDBDescriptor(path, fakeEnv(map[string]string{DescriptorEnvAllowList: "SHOP_COUCH_URL"}))
	require.NoError(t, err)
	assert.Equal(t, "env:SHOP_COUCH_URL", descriptor.SourceURL())
}

func TestReadCouchDBDescriptorReadsTheProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"DT_POSTGREST_COUCH_URL"}`), 0o600))

	t.Setenv(DescriptorEnvAllowList, "")
	_, err := ReadCouchDBDescriptor(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), DescriptorEnvAllowList)

	t.Setenv(DescriptorEnvAllowList, "DT_POSTGREST_COUCH_URL")
	descriptor, err := ReadCouchDBDescriptor(path)
	require.NoError(t, err)
	assert.Equal(t, "env:DT_POSTGREST_COUCH_URL", descriptor.SourceURL())
}
