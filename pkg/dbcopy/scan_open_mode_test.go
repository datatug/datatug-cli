package dbcopy

import (
	"context"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo2postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DataTug reads a database that it did not create, whose tables have mixed-case names
// (PostgreSQL keeps the case of a quoted name), so it opens the adapter in exact identifier
// mode. The default of dalgo2postgres is to fold every name to lower case, which makes the
// reader list "Album" and then look for "album": a table is found under the name PostgreSQL
// reports, or the scan of a mixed-case database fails. This pins the mode, so that a later
// change of the options, or of the default, cannot flip it: the option the open passes is
// applied to a database and the mode it holds is read.
func TestOpenSchemaScan_PinsTheExactIdentifierMode(t *testing.T) {
	var options []dalgo2postgres.Option
	stubNewPostgresDatabase(t, func(_ string, given ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		options = given
		return &dalgo2postgres.Database{}, nil
	})
	ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/shop"}))
	require.NoError(t, err)

	_, err = ref.OpenSchemaScan(context.Background())
	require.NoError(t, err)

	require.Len(t, options, 1, "exactly the identifier mode, and no other option")
	modeOf := func(applied ...dalgo2postgres.Option) int64 {
		db := &dalgo2postgres.Database{}
		for _, option := range applied {
			option(db)
		}
		field := reflect.ValueOf(db).Elem().FieldByName("identifierMode")
		require.True(t, field.IsValid(), "dalgo2postgres.Database no longer has the field this test reads: pin the mode another way")
		return field.Int()
	}
	assert.Equal(t, int64(dalgo2postgres.IdentifierFoldLower), modeOf(), "the default of the adapter is to fold names to lower case")
	assert.Equal(t, int64(dalgo2postgres.IdentifierExact), modeOf(options...), "and DataTug opens it in exact mode")
}
