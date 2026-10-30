package bigqueryread

import (
	"context"
	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSessionSplitBigQueryFingerprintTracksLoadedPoliciesInFreeMode(t *testing.T) {
	original := bigqueryCompile
	t.Cleanup(func() { bigqueryCompile = original })
	bigqueryCompile = func(bigquery.SourceProfile, dal.Query) (bigquery.ReadPlan, error) {
		return bigquery.ReadPlan{Version: 1}, nil
	}
	input := Input{Profile: bigquery.SourceProfile{SourceProject: "proj", DatasetID: "ds", TableID: "t"}, Query: QueryShape{From: From{Name: "table"}, Columns: []Column{{Field: "c"}}, Limit: 10}}
	fingerprints := []string{}
	for _, fields := range []string{"c", "c, other"} {
		loaded, err := accesspolicies.DecodeLoaded([]byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: table}\ndefault: deny\nscopes:\n  - path: /table\n    rules:\n      - id: read\n        effect: allow\n        operations: [query]\n        fields: ["+fields+"]\n"), access.YAMLCodec{}, "table.yaml")
		require.NoError(t, err)
		preparer := Preparer{Input: input, Options: func() (accesspolicies.Options, error) {
			return accesspolicies.Options{Policies: []accesspolicies.Loaded{loaded}, Unrestricted: true}, nil
		}}
		_, fingerprint, lines, err := preparer.PrepareWithReport(context.Background())
		require.NoError(t, err)
		require.NotEmpty(t, lines)
		fingerprints = append(fingerprints, fingerprint)
	}
	require.NotEqual(t, fingerprints[0], fingerprints[1], "a policy change must invalidate the BigQuery plan fingerprint")
}
