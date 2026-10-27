package dbcopy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodePostgRESTDescriptor(t *testing.T) {
	t.Parallel()
	descriptor, err := DecodePostgRESTDescriptor([]byte(`{"dsnEnv":"DATATUG_SHOP_REST"}`))
	require.NoError(t, err)
	assert.Equal(t, "DATATUG_SHOP_REST", descriptor.DSNEnv)
	assert.Equal(t, "env:DATATUG_SHOP_REST", descriptor.SourceURL())

	for _, tc := range []struct{ name, body, want string }{
		{"an endpoint field", `{"dsnEnv":"DATATUG_SHOP_REST","url":"postgrest://api.example.com/rest/v1"}`, "unknown PostgREST connection field"},
		{"a token field", `{"dsnEnv":"DATATUG_SHOP_REST","token":"s3cret"}`, "unknown PostgREST connection field"},
		{"a URL instead of a name", `{"dsnEnv":"postgrest://api.example.com/rest/v1"}`, "^[A-Z][A-Z0-9_]*$"},
		{"empty", `{}`, "requires dsnEnv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodePostgRESTDescriptor([]byte(tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "PostgREST")
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "api.example.com")
		})
	}
}

func TestReadPostgRESTDescriptorAppliesTheOperatorVariableRule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "api.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"SHOP_REST_URL"}`), 0o600))

	_, err := readPostgRESTDescriptor(path, fakeEnv(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), DescriptorEnvAllowList)

	descriptor, err := readPostgRESTDescriptor(path, fakeEnv(map[string]string{DescriptorEnvAllowList: "SHOP_REST_URL"}))
	require.NoError(t, err)
	assert.Equal(t, "env:SHOP_REST_URL", descriptor.SourceURL())
}

func TestReadPostgRESTDescriptorReadsTheProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"DT_POSTGREST_URL"}`), 0o600))

	t.Setenv(DescriptorEnvAllowList, "")
	_, err := ReadPostgRESTDescriptor(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), DescriptorEnvAllowList)

	t.Setenv(DescriptorEnvAllowList, "DT_POSTGREST_URL")
	descriptor, err := ReadPostgRESTDescriptor(path)
	require.NoError(t, err)
	assert.Equal(t, "env:DT_POSTGREST_URL", descriptor.SourceURL())
}
