package commands

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/dtentity"
	"github.com/stretchr/testify/require"
)

func TestRenderEntityShowPreservesCanonicalTableMetadata(t *testing.T) {
	entity, err := dtentity.UnmarshalEntity([]byte(`{"id":"Route","tables":[{"name":"routes","type":"view","schema":"logical","catalog":"catalog","database":"shipping","refSchema":"physical"},{"name":"requests","parent":[{"collection":"databases","id":"18446744073709551615","idType":"uint64"}]}]}`))
	require.NoError(t, err)
	output, err := renderEntityShow(entity)
	require.NoError(t, err)
	for _, metadata := range []string{"database: shipping", "refSchema: physical", "schema: logical", "catalog: catalog", "type: view", "18446744073709551615", "idType: uint64"} {
		require.Contains(t, output, metadata)
	}
}
