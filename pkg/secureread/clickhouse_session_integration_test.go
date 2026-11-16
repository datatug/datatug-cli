package secureread

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

func TestClickHouseExecutionUsesExplicitProfileAndRetainsLoadedPolicies(t *testing.T) {
	oldSource, oldNative := openSourceFn, openClickHouseNativeFn
	t.Cleanup(func() { openSourceFn, openClickHouseNativeFn = oldSource, oldNative })
	t.Setenv("DATATUG_CH_PROFILE_INTEGRATION", "clickhouse+http://127.0.0.1:1/explore")
	const source = "env:DATATUG_CH_PROFILE_INTEGRATION"
	query := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(1).SelectColumns(dal.Column{Expression: dal.Field("id")})
	for _, native := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			profile DatabaseProfile
			policy  string
			allowed bool
		}{
			{"policy free", DatabasePolicyFreeReads, "", true},
			{"governed without policy", DatabasePolicyGoverned, "", false},
			{"policy free retains denial", DatabasePolicyFreeReads, productsOnlyPolicy, false},
		} {
			t.Run(fmt.Sprintf("%s native=%t", tc.name, native), func(t *testing.T) {
				session := Session{DatabaseProfile: tc.profile, AllowOpaqueSQL: native}
				if tc.policy != "" {
					session = aliceSession(t, tc.policy)
					session.DatabaseProfile = tc.profile
					session.AllowOpaqueSQL = native
				}
				executor := NewExecutor(session)
				// The legacy field is an output echo, never execution authority.
				// Make it disagree after normalization to catch either CH path
				// accidentally reading that echo instead of the explicit profile.
				executor.session.Unrestricted = !tc.profile.PolicyFreeReads()
				reader := &clickHouseResultReader{set: clickHouseResultSet(t, []string{"id"}, []string{"UInt64"}, []any{uint64(7)})}
				db := &clickHouseNativeDB{clickHouseQueryDB: clickHouseQueryDatabase(t, reader)}
				openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
					return db, dbcopy.SourceCloser(db), nil
				}
				openClickHouseNativeFn = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return db, nil }
				var result Result
				var err error
				if native {
					result, err = executor.RunNativeSQL(context.Background(), source, "SELECT id FROM events")
				} else {
					result, err = executor.RunStructured(context.Background(), source, query, nil)
				}
				if tc.allowed {
					require.NoError(t, err)
					require.Len(t, result.AnonymousRecordset.Rows, 1)
					require.Equal(t, "7", result.AnonymousRecordset.Rows[0][0].Str)
				} else {
					require.Error(t, err)
					require.Nil(t, result.AnonymousRecordset)
					require.Empty(t, db.queries)
					require.Zero(t, db.nativeCalls)
					if tc.policy != "" {
						require.ErrorIs(t, err, ErrAccessDenied)
					}
				}
				require.Equal(t, 1, db.closes)
			})
		}
	}
}

func TestClickHouseInvalidSessionRefusesBeforeSourceResolutionOrOpen(t *testing.T) {
	oldSource, oldNative := openSourceFn, openClickHouseNativeFn
	t.Cleanup(func() { openSourceFn, openClickHouseNativeFn = oldSource, oldNative })
	opens := 0
	openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
		opens++
		t.Fatal("invalid session reached structured source opening")
		return nil, nil, nil
	}
	openClickHouseNativeFn = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		opens++
		t.Fatal("invalid session reached native source opening")
		return nil, nil
	}
	// Resolution would fail if reached, before a transport could be attempted.
	t.Setenv("DATATUG_CH_INVALID_SESSION_INTEGRATION", "")
	const source = "env:DATATUG_CH_INVALID_SESSION_INTEGRATION"
	document := []byte("from: {name: events}\nlimit: 1\n")
	query := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(1).SelectIntoRecordset()
	sticky, err := NormalizeSession(Session{DatabaseProfile: DatabaseProfile(255)})
	require.ErrorIs(t, err, ErrInvalidSession)
	sticky.DatabaseProfile, sticky.Unrestricted, sticky.AllowOpaqueSQL = DatabasePolicyFreeReads, true, true
	for _, session := range []Session{
		{DatabaseProfile: DatabasePolicyGoverned, Unrestricted: true, AllowOpaqueSQL: true},
		{DatabaseProfile: DatabaseProfile(255), AllowOpaqueSQL: true},
		sticky,
	} {
		executor := NewExecutor(session)
		ctx := context.Background()
		_, err = executor.RunStructured(ctx, source, query, nil)
		require.ErrorIs(t, err, ErrInvalidSession)
		_, err = executor.RunStructuredInsecureForTest(ctx, source, query, nil)
		require.ErrorIs(t, err, ErrInvalidSession)
		_, err = executor.RunTugQL(ctx, source, document, nil)
		require.ErrorIs(t, err, ErrInvalidSession)
		_, err = executor.RunNativeSQL(ctx, source, "SELECT id FROM events")
		require.ErrorIs(t, err, ErrInvalidSession)
		// Invalid-session priority also precedes ClickHouse's keyed refusal.
		_, err = executor.StreamFederatedTugQL(ctx, document, map[string]string{"analytics": "clickhouse+http://127.0.0.1:1/explore"}, nil)
		require.ErrorIs(t, err, ErrInvalidSession)
		_, err = executor.RunFederatedTugQL(ctx, document, map[string]string{"analytics": source}, nil)
		require.ErrorIs(t, err, ErrInvalidSession)
		require.NotContains(t, err.Error(), strings.TrimPrefix(source, "env:"))
	}
	require.Zero(t, opens)
}
