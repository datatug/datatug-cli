package dbcopy

import (
	"context"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedPostgresURLs are the shapes that net/url and the driver read differently from how they were
// written, with the markers where the password, the user name and a query parameter would be: each is
// refused once, in Parse, so every way in is covered. want is a word of the sentence that says what to
// fix.
var refusedPostgresURLs = map[string]struct{ url, want string }{
	"a password that holds a slash after digits": {"postgres://" + markerUser + ":42/" + markerPassword + "@db.example.com/shop?x=" + markerQuery, "percent-encode"},
	"a second URL inside the source":             {"postgres://postgres://" + markerUser + ":" + markerPassword + "@db.example.com/shop?x=" + markerQuery, "percent-encode"},
	"a literal at sign after the host":           {"postgres://" + markerUser + ":pw@db.example.com/shop@" + markerPassword + "?x=" + markerQuery, "percent-encode"},
	"credentials hidden in an encoded user":      {"postgres://" + markerUser + "%3A" + markerPassword + "@db.example.com/shop?x=" + markerQuery, "literal colon"},
	"a colon in the user query parameter":        {"postgres://db.example.com/shop?user=" + markerUser + ":" + markerPassword + "&x=" + markerQuery, "literal colon"},
	"a doubly encoded colon in the user":         {"postgres://" + markerUser + "%253A" + markerPassword + "@db.example.com/shop?x=" + markerQuery, "percent sign"},
	"a percent sign in the user query parameter": {"postgres://db.example.com/shop?user=" + markerUser + "%25" + markerPassword + "&x=" + markerQuery, "percent sign"},
	"a port that is not a number":                {"postgres://" + markerUser + ":" + markerPassword + "@db.example.com/shop?port=" + markerQuery, "cannot be read"},
}

func TestParse_RefusesTheShapesThatAreReadDifferentlyFromHowTheyWereWritten(t *testing.T) {
	t.Parallel()
	for name, tc := range refusedPostgresURLs {
		for _, scheme := range []string{"postgres", "postgresql"} {
			typed := scheme + strings.TrimPrefix(tc.url, "postgres")

			ref, err := Parse(typed)
			assert.Equal(t, BackendRef{}, ref, name)
			if assert.Error(t, err, name) {
				assert.ErrorContains(t, err, tc.want, name)
				assertNoMarkers(t, name, err)
				for _, shown := range []string{"alice", "pw@"} {
					assert.NotContains(t, err.Error(), shown, name)
				}
			}
		}
	}
}

// An env source is refused the same way, and the message names the variable and the shape, never the
// value of the variable.
func TestParseWithEnv_RefusesTheSameShapesAndNamesTheVariable(t *testing.T) {
	t.Parallel()
	for name, tc := range refusedPostgresURLs {
		ref, err := ParseWithEnv("env:DATATUG_SHOP_PG_URL", fakeEnv(map[string]string{"DATATUG_SHOP_PG_URL": tc.url}))
		assert.Equal(t, BackendRef{}, ref, name)
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "environment variable DATATUG_SHOP_PG_URL does not hold a usable PostgreSQL URL", name)
			assert.ErrorContains(t, err, tc.want, name)
			assertNoMarkers(t, name, err)
		}
	}
}

// The source the person typed reaches no opener: Open and OpenProtected have no BackendRef to open.
// The scope of a chat session that names such a source is the one of an unresolved source.
func TestParse_ARefusedURLNeverReachesTheOpener(t *testing.T) {
	previewOn(t)
	stubPostgresOpener(t, func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		t.Fatal("a refused URL must never reach the opener")
		return nil, nil
	})
	for name, tc := range refusedPostgresURLs {
		ref, err := Parse(tc.url)
		require.Error(t, err, name)
		// What Parse refused cannot be opened through the zero ref either.
		_, openErr := ref.Open(context.Background())
		assert.Error(t, openErr, name)
		assertNoMarkers(t, name, openErr)
	}
}

// What the tests above do not say is what stays accepted: a password with an escaped separator, a
// colon in the password, an empty password, and a percent sign in the password (never in the user).
func TestParse_AcceptsWhatIsReadAsItWasWritten(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"postgres://alice:p%40ss%2Fw%3Fx%23y@db.example.com/shop",
		"postgres://alice:pa:ss@db.example.com/shop",
		"postgres://alice:@db.example.com/shop",
		"postgres://alice:100%25@db.example.com/shop",
		"postgres://db.example.com/shop?sslmode=require",
		"postgresql://alice@db.example.com:5433/shop?application_name=billing",
	} {
		ref, err := Parse(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, "postgres", ref.Scheme, raw)
		assert.Equal(t, raw[strings.Index(raw, "://"):], ref.Path[strings.Index(ref.Path, "://"):], raw+": the URL is carried as it was written")
	}
}

func TestPostgresURLRefusal_OnlyTheRefusalsOfTheParserAreRecognised(t *testing.T) {
	t.Parallel()
	for _, refusal := range postgresURLRefusals {
		assert.Same(t, refusal, postgresURLRefusal(refusal))
	}
	assert.Nil(t, postgresURLRefusal(ErrPostgresPreview))
	assert.Nil(t, postgresURLRefusal(nil))
}
