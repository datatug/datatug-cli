package api

import (
	"bytes"
	"context"
	"log"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scan's log line names the target and never the user of a PostgreSQL
// connection URL, which is read out of the URL and can be a token. A driver whose
// user is a flag the operator typed still logs it (see
// TestUpdateDbSchema_LogLineNeverHoldsThePassword).
func TestUpdateDbSchema_LogLineNeverHoldsThePostgresUser(t *testing.T) {
	const token = "ghp_TOKENASUSERNAME1234"
	params, err := NewPostgresScanParams(func(string) (string, bool) {
		return "postgres://" + token + "@db.example.com:5433/shop", true
	}, "DATATUG_SHOP_PG_URL", "prod", "shop")
	require.NoError(t, err)
	var logged bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(saved)

	_, err = UpdateDbSchema(context.Background(), nil, "", "prod", DriverPostgres, "shop", params)
	assert.Error(t, err, "the empty project id is refused after the log line")
	assert.Contains(t, logged.String(), "server=db.example.com, port=5433")
	assert.NotContains(t, logged.String(), token)
	assert.NotContains(t, logged.String(), "user=")
}

// A scan flag is what the user typed, and a source string can be typed where an
// environment or a database name belongs. The refusal names a plain name and
// never anything else.
func TestNewPostgresScanParams_NamesOnlyAPlainEnvironmentAndDatabase(t *testing.T) {
	const typed = "postgres://alice:s3cretpw@db.example.com/shop"
	lookup := func(string) (string, bool) { return typed, true }
	_, err := NewPostgresScanParams(lookup, "DATATUG_SHOP_PG_URL", "my env", "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `environment "`+dbcopy.SourceIDNotShown+`" and database "shop" must each be a plain name`)

	_, err = NewPostgresScanParams(lookup, "DATATUG_SHOP_PG_URL", "prod", typed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `environment "prod" and database "`+dbcopy.SourceIDNotShown+`"`)
	assert.NotContains(t, err.Error(), "s3cretpw")
}
