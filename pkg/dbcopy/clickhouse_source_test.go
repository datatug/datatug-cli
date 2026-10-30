package dbcopy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/stretchr/testify/require"
)

func clickHouseEnv(values map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
}

func TestClickHouseSourceStrictURLAndEnvironment(t *testing.T) {
	for _, source := range []string{"clickhouse://db.example:9440/explore", "clickhouse+https://db.example:8443/explore", "clickhouse+http://127.0.0.1:8123/explore", "clickhouse+http://[::1]:8123/explore"} {
		ref, err := Parse(source)
		require.NoError(t, err)
		require.True(t, IsClickHouseScheme(ref.Scheme))
		require.Equal(t, "explore", ref.ClickHouseDatabase)
	}
	for _, source := range []string{"clickhouse://db/explore", "clickhouse://db:0/explore", "clickhouse://db:65536/explore", "clickhouse://user:secret@db:9440/explore", "clickhouse://db:9440/explore?readonly=0", "clickhouse://db:9440/explore#secret", "clickhouse://db:9440/explore/other", "clickhouse://db:9440/%65xplore", "clickhouse+http://localhost:8123/explore", "clickhouse+http://db.example:8123/explore"} {
		_, err := Parse(source)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
	env := map[string]string{ClickHouseUsernameEnv: "reader", ClickHousePasswordEnv: "secret", ClickHouseRelationsEnv: `{"events":["id","amount"]}`}
	ref, err := Parse("clickhouse+http://127.0.0.1:8123/explore")
	require.NoError(t, err)
	cfg, err := ref.ClickHouseConfig(clickHouseEnv(env), false)
	require.NoError(t, err)
	require.False(t, cfg.NativeSQL)
	require.Equal(t, []string{"id", "amount"}, cfg.Tables["events"])
	require.Equal(t, 1000, cfg.MaxRows)
	require.True(t, cfg.HTTP)
	require.True(t, cfg.PlaintextLoopback)
	_, err = ref.ClickHouseConfig(clickHouseEnv(env), true)
	require.Error(t, err)
	env[ClickHouseNativeSQLEnv] = "true"
	delete(env, ClickHouseRelationsEnv)
	ref.Raw = "env:DATATUG_CLICKHOUSE_TEST"
	env[ClickHouseNativeSourcesEnv] = "DATATUG_CLICKHOUSE_TEST"
	_, err = ref.ClickHouseConfig(clickHouseEnv(env), false)
	require.Error(t, err, "native opt-in cannot authorize structured wildcard discovery")
	cfg, err = ref.ClickHouseConfig(clickHouseEnv(env), true)
	require.NoError(t, err)
	require.True(t, cfg.NativeSQL)
	require.Empty(t, cfg.Tables)
	for _, sources := range []string{"DATATUG_OTHER", "DATATUG_CLICKHOUSE_TEST,DATATUG_CLICKHOUSE_TEST", "invalid-name", ""} {
		env[ClickHouseNativeSourcesEnv] = sources
		_, err = ref.ClickHouseConfig(clickHouseEnv(env), true)
		require.Error(t, err)
	}
	env[ClickHouseNativeSourcesEnv] = "DATATUG_CLICKHOUSE_TEST"
	for _, relations := range []string{`[]`, `null`, `{"events":null}`, `{"events":[] } {}`, `{"events":[],"events":["id"]}`, `{"events":["id","id"]}`, `{"events":["id;DROP"]}`, `{"events":{}}`, strings.Repeat(" ", 65537)} {
		env[ClickHouseRelationsEnv] = relations
		_, err = ref.ClickHouseConfig(clickHouseEnv(env), true)
		require.Error(t, err, relations)
		require.NotContains(t, err.Error(), "secret")
	}
	env[ClickHouseRelationsEnv] = `{"events":[]}`
	for _, opt := range []string{"1", "TRUE", "yes"} {
		env[ClickHouseNativeSQLEnv] = opt
		_, err = ref.ClickHouseConfig(clickHouseEnv(env), false)
		require.Error(t, err)
	}
	env[ClickHouseNativeSQLEnv] = "false"
	env[ClickHousePasswordEnv] = "unsafe\nsecret"
	_, err = ref.ClickHouseConfig(clickHouseEnv(env), false)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}

func TestClickHouseWriteRefusedBeforeConstructor(t *testing.T) {
	old := newClickHouseDatabase
	newClickHouseDatabase = func(context.Context, dalgo2clickhouse.Config) (dal.DB, error) {
		t.Fatal("write must not construct a source")
		return nil, nil
	}
	t.Cleanup(func() { newClickHouseDatabase = old })
	ref, err := Parse("clickhouse://db.example:9440/explore")
	require.NoError(t, err)
	_, err = openClickHouse(context.Background(), ref, openMode{forWrite: true})
	require.Error(t, err)
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, dalgo2clickhouse.ErrLimit, dal.ErrNotSupported, errors.New("secret SQL endpoint")} {
		require.NotContains(t, ClickHouseFailureReason(err), "secret")
	}
}
