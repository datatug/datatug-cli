package dtentity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

const canonicalEntity = `{"id":"Route","title":"Routes","tables":[{"name":"routes","type":"view","schema":"logical","catalog":"catalog","database":"shipping","refSchema":"physical"},{"name":"requests","type":"table","schema":"logical","catalog":"catalog","parent":[{"collection":"spaces","id":"S1"},{"collection":"databases","id":"18446744073709551615","idType":"uint64"}]},{"name":"legacy"}]}`

func TestCanonicalNamedTablesLibraryRoundTrip(t *testing.T) {
	var expected datatug.Entity
	require.NoError(t, json.Unmarshal([]byte(canonicalEntity), &expected))
	expectedTables, err := json.Marshal(expected.Tables)
	require.NoError(t, err)
	parsed, err := UnmarshalEntity([]byte(canonicalEntity))
	require.NoError(t, err)
	actualTables, err := json.Marshal(parsed.Tables)
	require.NoError(t, err)
	require.JSONEq(t, string(expectedTables), string(actualTables))
	encoded, err := MarshalEntity(&expected)
	require.NoError(t, err)
	require.JSONEq(t, canonicalEntity, string(encoded))
	for _, malformed := range []string{`{"tables":[{"name":5}]}`, `{"tables":[{"name":"routes","unexpected":true}]}`, `{"tables":[{"name":"routes","parent":[{"collection":"db","id":"18446744073709551616","idType":"uint64"}]}]}`} {
		_, err := UnmarshalEntity([]byte(malformed))
		require.Error(t, err)
	}
}

func TestCanonicalNamedTablesActualFileStoreReadModifySave(t *testing.T) {
	for _, layout := range []string{"flat", "nested"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "entities", "Route.entity.json")
			if layout == "nested" {
				path = filepath.Join(root, "entities", "Route", "Route.entity.json")
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(canonicalEntity), 0600))
			store := filestore.NewProjectStore("training", root)
			entity, err := store.LoadEntity(context.Background(), "Route")
			require.NoError(t, err)
			expected, err := json.Marshal(entity.Tables)
			require.NoError(t, err)
			entity.Title = "Edited route title"
			encoded, err := MarshalEntity(entity)
			require.NoError(t, err)
			edited, err := UnmarshalEntity(encoded)
			require.NoError(t, err)
			require.NoError(t, store.SaveEntity(context.Background(), edited))
			reloaded, err := store.LoadEntity(context.Background(), "Route")
			require.NoError(t, err)
			require.Equal(t, "Edited route title", reloaded.Title)
			actual, err := json.Marshal(reloaded.Tables)
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(actual))
			require.Equal(t, uint64(18446744073709551615), reloaded.Tables[1].Ref.Parent().ID)
		})
	}
}
