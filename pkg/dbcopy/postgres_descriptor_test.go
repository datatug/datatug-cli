package dbcopy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecodePostgresDescriptor_Valid(t *testing.T) {
	t.Parallel()
	descriptor, err := DecodePostgresDescriptor([]byte(`{"dsnEnv":"SHOP_PG_URL"}`))
	assert.NoError(t, err)
	assert.Equal(t, "SHOP_PG_URL", descriptor.DSNEnv)
	assert.Equal(t, "env:SHOP_PG_URL", descriptor.SourceURL())
	ref, err := parseSource(descriptor.SourceURL(), fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://u:p@h/db"}))
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
}

func TestDecodePostgresDescriptor_FailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"empty input", ``, "invalid PostgreSQL connection descriptor"},
		{"not JSON", `dsnEnv: SHOP_PG_URL`, "invalid PostgreSQL connection descriptor"},
		{"array", `["SHOP_PG_URL"]`, "invalid PostgreSQL connection descriptor"},
		{"null", `null`, "invalid PostgreSQL connection descriptor"},
		{"trailing value", `{"dsnEnv":"SHOP_PG_URL"} {}`, "invalid PostgreSQL connection descriptor"},
		{"duplicate key", `{"dsnEnv":"SHOP_PG_URL","dsnEnv":"OTHER_PG_URL"}`, "invalid PostgreSQL connection descriptor"},
		{"empty object", `{}`, "requires dsnEnv"},
		{"empty name", `{"dsnEnv":""}`, "requires dsnEnv"},
		{"wrong type", `{"dsnEnv":42}`, "invalid PostgreSQL connection descriptor"},
		{"lower-case name", `{"dsnEnv":"shop_pg_url"}`, "^[A-Z][A-Z0-9_]*$"},
		{"a URL instead of a name", `{"dsnEnv":"postgres://alice:s3cret@h/db"}`, "^[A-Z][A-Z0-9_]*$"},
		{"a literal dsn field", `{"dsnEnv":"SHOP_PG_URL","dsn":"postgres://alice:s3cret@h/db"}`, `unknown PostgreSQL connection field "dsn"`},
		{"a literal password field", `{"dsnEnv":"SHOP_PG_URL","password":"s3cret"}`, `unknown PostgreSQL connection field "password"`},
		{"a field name that is itself a secret", `{"dsnEnv":"SHOP_PG_URL","postgres://alice:s3cret@h/db":true}`, "unknown PostgreSQL connection field"},
		{"an overlong field name", `{"dsnEnv":"SHOP_PG_URL","` + strings.Repeat("a", 65) + `":true}`, "unknown PostgreSQL connection field"},
		{"an empty field name", `{"dsnEnv":"SHOP_PG_URL","":true}`, "unknown PostgreSQL connection field"},
		{"a field name starting with a digit", `{"dsnEnv":"SHOP_PG_URL","1x":true}`, "unknown PostgreSQL connection field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			descriptor, err := DecodePostgresDescriptor([]byte(tc.body))
			assert.Error(t, err)
			assert.Equal(t, PostgresDescriptor{}, descriptor)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "alice")
		})
	}
}

func TestDecodePostgresDescriptor_AdvertisesTheEnvironmentVariableRoute(t *testing.T) {
	t.Parallel()
	_, err := DecodePostgresDescriptor([]byte(`{"dsnEnv":"SHOP_PG_URL","dsn":"x"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dsnEnv")
	assert.Contains(t, err.Error(), "environment variable")
}

func TestReadPostgresDescriptor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	assert.NoError(t, os.WriteFile(good, []byte(`{"dsnEnv":"DATATUG_SHOP_PG_URL"}`), 0o600))
	descriptor, err := ReadPostgresDescriptor(good)
	assert.NoError(t, err)
	assert.Equal(t, "DATATUG_SHOP_PG_URL", descriptor.DSNEnv)

	bad := filepath.Join(dir, "bad.json")
	assert.NoError(t, os.WriteFile(bad, []byte(`{"dsnEnv":"SHOP_PG_URL","password":"s3cret"}`), 0o600))
	_, err = ReadPostgresDescriptor(bad)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")

	big := filepath.Join(dir, "big.json")
	assert.NoError(t, os.WriteFile(big, []byte(strings.Repeat(" ", maxPostgresDescriptorBytes+1)), 0o600))
	_, err = ReadPostgresDescriptor(big)
	assert.ErrorContains(t, err, "invalid PostgreSQL connection descriptor size")

	_, err = ReadPostgresDescriptor(filepath.Join(dir, "missing.json"))
	assert.ErrorContains(t, err, "open PostgreSQL connection descriptor")

	// A directory opens fine on Unix but fails to read.
	_, err = ReadPostgresDescriptor(dir)
	assert.ErrorContains(t, err, "read PostgreSQL connection descriptor")
}
