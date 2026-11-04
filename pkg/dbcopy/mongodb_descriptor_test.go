package dbcopy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mongoDescriptorSecret = "mongo-s3cret-password"

func writeMongoDescriptor(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestReadMongoDBDescriptor_NamesOnlyTheAllowedVariable(t *testing.T) {
	t.Setenv(DescriptorEnvAllowList, "")
	dir := t.TempDir()

	t.Setenv("DATATUG_MONGO_URL", "mongodb://alice:"+mongoDescriptorSecret+"@db.example.com/shop")
	descriptor, err := ReadMongoDBDescriptor(writeMongoDescriptor(t, dir, "datatug.json", `{"dsnEnv":"DATATUG_MONGO_URL"}`))
	require.NoError(t, err)
	assert.Equal(t, ConnectionDescriptor{DSNEnv: "DATATUG_MONGO_URL"}, descriptor)
	assert.Equal(t, "env:DATATUG_MONGO_URL", descriptor.SourceURL())
	assert.NotContains(t, descriptor.SourceURL(), mongoDescriptorSecret, "the reader returns the name and never the URL")

	_, err = ReadMongoDBDescriptor(writeMongoDescriptor(t, dir, "foreign.json", `{"dsnEnv":"MONGO_PROD_URL"}`))
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, "MONGO_PROD_URL")
		assert.ErrorContains(t, err, DescriptorEnvPrefix)
		assert.ErrorContains(t, err, DescriptorEnvAllowList)
		assert.NotContains(t, err.Error(), mongoDescriptorSecret)
	}

	t.Setenv(DescriptorEnvAllowList, "OPS_MONGO_URL")
	descriptor, err = ReadMongoDBDescriptor(writeMongoDescriptor(t, dir, "listed.json", `{"dsnEnv":"OPS_MONGO_URL"}`))
	require.NoError(t, err)
	assert.Equal(t, ConnectionDescriptor{DSNEnv: "OPS_MONGO_URL"}, descriptor)
}

func TestReadMongoDBDescriptor_FailsClosed(t *testing.T) {
	t.Setenv(DescriptorEnvAllowList, "")
	dir := t.TempDir()
	tests := []struct {
		name string
		body string
	}{
		{"null", `null`},
		{"duplicate key", `{"dsnEnv":"DATATUG_MONGO_URL","dsnEnv":"DATATUG_OTHER_URL"}`},
		{"wrong case field", `{"dsnenv":"DATATUG_MONGO_URL"}`},
		{"unknown secret field", `{"dsnEnv":"DATATUG_MONGO_URL","password":"` + mongoDescriptorSecret + `"}`},
		{"trailing object", `{"dsnEnv":"DATATUG_MONGO_URL"} {}`},
		{"invalid env name", `{"dsnEnv":"mongodb://alice:` + mongoDescriptorSecret + `@db.example.com/shop"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := ReadMongoDBDescriptor(writeMongoDescriptor(t, dir, tc.name+".json", tc.body))
			assert.Equal(t, ConnectionDescriptor{}, descriptor, "a failed read returns no descriptor")
			if assert.Error(t, err) {
				assert.ErrorContains(t, err, "MongoDB")
				assert.NotContains(t, err.Error(), mongoDescriptorSecret)
			}
		})
	}

	_, err := ReadMongoDBDescriptor(filepath.Join(dir, "missing.json"))
	assert.ErrorContains(t, err, "open MongoDB connection descriptor")

	big := writeMongoDescriptor(t, dir, "big.json", strings.Repeat(" ", maxPostgresDescriptorBytes+1))
	_, err = ReadMongoDBDescriptor(big)
	assert.ErrorContains(t, err, "invalid MongoDB connection descriptor size")
}
