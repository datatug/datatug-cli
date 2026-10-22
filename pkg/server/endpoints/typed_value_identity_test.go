package endpoints

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestTypedFactIdentityDoesNotCoerceEqualTextAcrossTypes(t *testing.T) {
	integer := apicontract.NewIntegerValue("5")
	text := apicontract.NewStringValue("5")
	decimal := apicontract.NewDecimalValue("5")
	for _, value := range []apicontract.TypedValue{integer, text, decimal} {
		require.NoError(t, value.Validate())
		require.True(t, typedValueEqual(value, value))
	}
	require.False(t, typedValueEqual(integer, text))
	require.False(t, typedValueEqual(text, integer))
	require.False(t, typedValueEqual(integer, decimal))
	// These conflicting source facts must remain ambiguous, while duplicate
	// facts with exactly the same typed identity count only once.
	require.Equal(t, 3, distinctFactValues([]apicontract.Fact{
		{Value: integer}, {Value: text}, {Value: decimal}, {Value: integer},
	}))
}
