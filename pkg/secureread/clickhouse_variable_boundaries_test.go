package secureread

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestClickHousePolicyVariablesAdmitFiniteScalarsWithoutNumericCoercion(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	values := map[string]any{"null": nil, "bool": true, "small": int8(-7), "large": uint64(math.MaxUint64), "float32": float32(1.25), "float64": float64(2.5), "text": "safe", "day": day}
	got, err := clickHouseVariables(values)
	require.NoError(t, err)
	require.Equal(t, values, got)
	for _, value := range []any{float32(math.NaN()), float32(math.Inf(1)), float32(1 << 25), decimal.New(1, -77), decimal.NewFromBigInt(new(big.Int).Lsh(big.NewInt(1), 256), 0)} {
		_, err := clickHouseVariables(map[string]any{"value": value})
		require.ErrorIs(t, err, accesspolicies.ErrInvalidQuery)
	}
	tooMany := map[string]any{}
	for i := 0; i < 257; i++ {
		tooMany[fmt.Sprintf("v%d", i)] = i
	}
	_, err = clickHouseVariables(tooMany)
	require.ErrorIs(t, err, accesspolicies.ErrInvalidQuery)
}

func TestClickHouseKeyedFederationRefusesBeforeOpeningAnySource(t *testing.T) {
	old := openSourceFn
	t.Cleanup(func() { openSourceFn = old })
	openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
		t.Fatal("anonymous ClickHouse source reached keyed federation leaf")
		return nil, nil, nil
	}
	_, err := NewExecutor(Session{Unrestricted: true}).StreamFederatedTugQL(context.Background(), []byte("from: {database: analytics, name: events}\nlimit: 1\n"), map[string]string{"analytics": "clickhouse+http://127.0.0.1:1/explore"}, nil)
	require.ErrorIs(t, err, dal.ErrNotSupported)
}
