package commands

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

type nativeSchemaTransaction interface {
	RunReadonlySchemaTransaction(context.Context, func(context.Context, dbschema.SchemaReader) error) error
}

type nativeSchemaCapabilities interface {
	dbschema.SchemaReader
	ListSchemas(context.Context) ([]string, error)
	ListSchemaCollections(context.Context, string) ([]dal.CollectionRef, error)
	ListViews(context.Context) ([]dal.CollectionRef, error)
	ListSchemaViews(context.Context, string) ([]dal.CollectionRef, error)
	ListSourceViews(context.Context) ([]dbschema.SourceViewDef, error)
	NonDeterministicTextColumns(context.Context, *dal.CollectionRef) ([]string, error)
}

// The guard's successful callback composes its same-Tx SHOW verification with
// real metadata queries. The independent observer proves backend/transaction
// continuity; pg_stat_activity does not reveal another transaction's GUC value.
func TestPostgresNativeReadonlySchemaUsesOneTransactionForAllMetadata(t *testing.T) {
	requireNativeReadonlyCI(t)
	server := newRealPgServer(t)
	require.Equal(t, os.Getenv(realPgMajorVar), strconv.Itoa(realPgServerMajor(t, server.scanURL)))
	parsed, err := url.Parse(server.scanURL)
	require.NoError(t, err)
	application := "dt_schema_" + randomHex(t, 6)
	query := parsed.Query()
	query.Set("application_name", application)
	parsed.RawQuery = query.Encode()
	ref, err := dbcopy.Parse(parsed.String())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	scanDB, err := ref.OpenSchemaScan(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, scanDB.Close()) }()
	transaction, ok := scanDB.(nativeSchemaTransaction)
	require.True(t, ok, "published PostgreSQL adapter must supply the native schema transaction")
	observer, err := sql.Open("pgx", server.scanURL)
	require.NoError(t, err)
	defer func() { require.NoError(t, observer.Close()) }()
	var retained nativeSchemaCapabilities
	var first nativeSchemaSession
	err = transaction.RunReadonlySchemaTransaction(ctx, func(ctx context.Context, reader dbschema.SchemaReader) error {
		var complete bool
		retained, complete = reader.(nativeSchemaCapabilities)
		require.True(t, complete, "the narrow reader must retain all 11 metadata methods")
		_, rawDB := reader.(dal.DB)
		require.False(t, rawDB, "the callback must not expose a DB")
		first = observeNativeSchemaSession(t, ctx, observer, server, application)
		require.Equal(t, "idle in transaction", first.state)
		require.True(t, first.started.Valid)
		require.Equal(t, "SHOW transaction_read_only", strings.TrimSpace(first.query), "the callback follows the native mode check")
		customer := dal.NewQualifiedRootCollectionRef("public", "customer", "")
		calls := []struct {
			name string
			read func() error
		}{
			{"ListCollections", func() error { refs, err := retained.ListCollections(ctx, nil); require.NotEmpty(t, refs); return err }},
			{"DescribeCollection", func() error {
				def, err := retained.DescribeCollection(ctx, &customer)
				require.NotNil(t, def)
				return err
			}},
			{"ListIndexes", func() error { _, err := retained.ListIndexes(ctx, &customer); return err }},
			{"ListConstraints", func() error { _, err := retained.ListConstraints(ctx, &customer); return err }},
			{"ListReferrers", func() error {
				refs, err := retained.ListReferrers(ctx, &customer)
				require.NotEmpty(t, refs)
				return err
			}},
			{"ListSchemas", func() error {
				schemas, err := retained.ListSchemas(ctx)
				require.Contains(t, schemas, "sales")
				return err
			}},
			{"ListSchemaCollections", func() error {
				refs, err := retained.ListSchemaCollections(ctx, "sales")
				require.NotEmpty(t, refs)
				return err
			}},
			{"ListViews", func() error { refs, err := retained.ListViews(ctx); require.NotEmpty(t, refs); return err }},
			{"ListSchemaViews", func() error {
				refs, err := retained.ListSchemaViews(ctx, "sales")
				require.NotEmpty(t, refs)
				return err
			}},
			{"ListSourceViews", func() error { views, err := retained.ListSourceViews(ctx); require.NotEmpty(t, views); return err }},
			{"NonDeterministicTextColumns", func() error { _, err := retained.NonDeterministicTextColumns(ctx, &customer); return err }},
		}
		for _, call := range calls {
			require.NoError(t, call.read(), call.name)
			current := observeNativeSchemaSession(t, ctx, observer, server, application)
			require.Equal(t, first.pid, current.pid, call.name)
			require.Equal(t, first.started, current.started, call.name)
			require.Equal(t, "idle in transaction", current.state, call.name)
		}
		return nil
	})
	require.NoError(t, err)
	_, err = retained.ListSchemas(context.Background())
	require.Error(t, err, "retained metadata capability must be closed, never pool-backed")
	ended := observeNativeSchemaSession(t, ctx, observer, server, application)
	require.Equal(t, first.pid, ended.pid)
	require.False(t, ended.started.Valid, "the schema transaction must finish before return")
	require.Equal(t, "idle", ended.state)
}

type nativeSchemaSession struct {
	pid     int
	started sql.NullTime
	state   string
	query   string
}

func observeNativeSchemaSession(t *testing.T, ctx context.Context, observer *sql.DB, server realPgServer, application string) nativeSchemaSession {
	t.Helper()
	rows, err := observer.QueryContext(ctx, `SELECT pid,xact_start,state,query FROM pg_stat_activity WHERE datname=$1 AND usename=$2 AND application_name=$3`, server.database, server.user, application)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next(), "the owned schema backend must be visible")
	var session nativeSchemaSession
	require.NoError(t, rows.Scan(&session.pid, &session.started, &session.state, &session.query))
	require.False(t, rows.Next(), "catalog reads must not open a second pooled backend")
	require.NoError(t, rows.Err())
	return session
}
