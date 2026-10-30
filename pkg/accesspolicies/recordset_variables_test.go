package accesspolicies

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/stretchr/testify/require"
)

func TestNativeRecordsetVariablesRetainMandatoryGuardsAndLegacyRefusal(t *testing.T) {
	query := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(3).WhereField("id", dal.Equal, dal.NewParam("id")).SelectIntoRecordset()
	options := Options{Unrestricted: true, Variables: map[string]any{"id": json.Number("18446744073709551615")}}
	_, err := PrepareRecordset(context.Background(), query, options, func(dal.Query) error { return nil })
	require.ErrorIs(t, err, ErrInvalidQuery, "generic comparison domain must retain its exact-range refusal")
	visits := 0
	guard := func(q dal.Query) error {
		visits++
		if visits > 1 {
			where := q.(dal.StructuredQuery).Where().(dal.Comparison)
			require.Equal(t, json.Number("18446744073709551615"), where.Right.(dal.Constant).Value)
		}
		return nil
	}
	normalize := func(values map[string]any) (map[string]any, error) {
		require.Equal(t, 1, visits, "original guard must precede scalar normalization")
		return map[string]any{"id": values["id"]}, nil
	}
	prepared, err := PrepareRecordsetWithVariables(context.Background(), query, options, guard, normalize)
	require.NoError(t, err)
	require.NotNil(t, prepared.Query)
	require.GreaterOrEqual(t, visits, 2)
	refused := errors.New("original refused")
	_, err = PrepareRecordsetWithVariables(context.Background(), query, options, func(dal.Query) error { return refused }, func(map[string]any) (map[string]any, error) {
		t.Fatal("normalizer ran before original guard")
		return nil, nil
	})
	require.ErrorIs(t, err, refused)
	_, err = PrepareRecordsetWithVariables(context.Background(), query, options, guard, nil)
	require.ErrorIs(t, err, ErrInvalidQuery)
}
