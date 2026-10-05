package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scan's log line names the variable that holds the PostgreSQL URL and nothing
// inside the URL, including a user that may be a token. A driver whose user is
// a flag the operator typed still logs it (see TestUpdateDbSchema_LogLineNeverHoldsThePassword).
func TestUpdateDbSchema_LogLineNeverHoldsThePostgresUser(t *testing.T) {
	const token = "ghp_TOKENASUSERNAME1234"
	params, err := NewPostgresScanParams(func(string) (string, bool) {
		return "postgres://" + token + "@db.example.com:5433/shop?sslmode=require&password=QUERYSECRET", true
	}, "DATATUG_SHOP_PG_URL", "prod", "shop")
	require.NoError(t, err)
	// The scan is refused for its empty project id before it opens anything; if that order ever
	// changes, the test stops here and does not reach a host.
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		t.Error("the empty project id is refused before the source is opened")
		return nil, errors.New("not a source of this test")
	})
	var logged bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(saved)

	_, err = UpdateDbSchema(context.Background(), nil, "", "prod", DriverPostgres, "shop", params)
	assert.Error(t, err, "the empty project id is refused after the log line")
	assert.Contains(t, logged.String(), "connecting; the PostgreSQL connection string is read from the environment variable DATATUG_SHOP_PG_URL\n")
	for _, hidden := range []string{token, "user=", "server=", "port=", "sslmode", "QUERYSECRET", "db.example.com", "5433", "shop?"} {
		assert.NotContains(t, logged.String(), hidden)
	}
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

// The environment and the catalog of a PostgreSQL scan name folders in the project,
// and a plain name is what dbcopy.IsPlainSourceID says it is: the scan has no
// definition of its own, so an ID that the catalog route accepts is one the scan can
// write, and the other way round.
func TestNewPostgresScanParams_UsesTheOneDefinitionOfAPlainName(t *testing.T) {
	ids := []string{"prod", "données", "Chinook1", "a.b_c-d", "1st", strings.Repeat("a", 128), strings.Repeat("a", 129), "_x", "my env", dbcopy.SourceIDNotShown}
	for _, unsafe := range sourcecases.UnsafeIdentifiers() {
		ids = append(ids, unsafe.ID)
	}
	accepted := 0
	for _, id := range ids {
		want := dbcopy.IsPlainSourceID(id)
		_, errEnvironment := NewPostgresScanParams(envOf(shopEnv()), "SHOP_PG_URL", id, "shop")
		_, errCatalog := NewPostgresScanParams(envOf(shopEnv()), "SHOP_PG_URL", "prod", id)
		if (errEnvironment == nil) != want || (errCatalog == nil) != want {
			t.Errorf("%q: IsPlainSourceID = %v, the scan took it as an environment: %v, as a database: %v", id, want, errEnvironment == nil, errCatalog == nil)
		}
		if want {
			accepted++
		}
	}
	if accepted < 5 {
		t.Fatalf("only %d of the IDs are plain names: the test does not reach the accepting side", accepted)
	}
}
