package dbcopy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	dalgo2libsql "github.com/dal-go/dalgo2libsql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A libSQL ref opened through the shared Open/OpenProtected dispatcher (not
// openLibSQL directly) reaches the adapter with the endpoint, the per-request
// header callback and the loopback allowance, and its failure comes back through
// OpenFailure without the endpoint.
func TestLibSQLRefOpensThroughTheSharedDispatcher(t *testing.T) {
	t.Setenv(LibSQLBearerEnv, "")
	ref, err := Parse("libsql+http://127.0.0.1:8086/team/db")
	require.NoError(t, err)

	orig := newLibSQLDatabase
	t.Cleanup(func() { newLibSQLDatabase = orig })
	seam := errors.New("seam refusal")
	var got dalgo2libsql.Config
	newLibSQLDatabase = func(_ context.Context, cfg dalgo2libsql.Config) (dal.DB, error) {
		got = cfg
		return nil, seam
	}

	_, err = ref.OpenProtected(context.Background())
	require.ErrorIs(t, err, seam, "the adapter's own error stays reachable")
	assert.Equal(t, "http://127.0.0.1:8086/team/db", got.URL)
	assert.True(t, got.AllowLoopbackHTTP, "the loopback allowance the parser proved is passed on")
	require.NotNil(t, got.Headers, "the adapter refreshes the token per request")
	headers, headersErr := got.Headers(context.Background())
	require.NoError(t, headersErr)
	assert.Empty(t, headers, "no token configured means an anonymous request")
	assert.Contains(t, err.Error(), "open libsql+http source", "the failure keeps its fixed libSQL classification")
}

// ReadLibSQLDescriptor is the reader the source resolver uses in production: it
// applies the operator's dsnEnv rule with the process environment, reads the
// descriptor and never touches a credential.
func TestReadLibSQLDescriptorThroughTheProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"SHOP_LIBSQL_DESC"}`), 0o600))

	// "SHOP_" is not the operator's prefix, so only the allow-list can admit it.
	t.Setenv(DescriptorEnvAllowList, "")
	_, err := ReadLibSQLDescriptor(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), DescriptorEnvAllowList)

	t.Setenv(DescriptorEnvAllowList, "SHOP_LIBSQL_DESC")
	descriptor, err := ReadLibSQLDescriptor(path)
	require.NoError(t, err)
	assert.Equal(t, "env:SHOP_LIBSQL_DESC", descriptor.SourceURL())

	missing := filepath.Join(dir, "missing.json")
	_, err = ReadLibSQLDescriptor(missing)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
