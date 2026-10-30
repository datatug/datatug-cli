package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/stretchr/testify/require"
)

func TestClickHouseMalformedGrantDocumentsFailBeforeConstruction(t *testing.T) {
	old := newClickHouseDatabase
	newClickHouseDatabase = func(context.Context, dalgo2clickhouse.Config) (dal.DB, error) {
		t.Fatal("invalid scope reached constructor")
		return nil, nil
	}
	t.Cleanup(func() { newClickHouseDatabase = old })
	ref, err := Parse("clickhouse+http://127.0.0.1:1/explore")
	require.NoError(t, err)
	for _, document := range []string{`{true:[]}`, `{"events":[],"unterminated`, `{"events":[]`, `{"events":[]]`, `{"bad-name":[]}`} {
		t.Run(document, func(t *testing.T) {
			_, err := ref.ClickHouseConfig(clickHouseEnv(map[string]string{ClickHouseUsernameEnv: "reader", ClickHouseRelationsEnv: document}), false)
			require.Error(t, err)
			require.NotContains(t, err.Error(), document)
		})
	}
	entries := make([]string, 257)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"table%d":[]`, i)
	}
	_, err = ref.ClickHouseConfig(clickHouseEnv(map[string]string{ClickHouseUsernameEnv: "reader", ClickHouseRelationsEnv: "{" + strings.Join(entries, ",") + "}"}), false)
	require.Error(t, err)
	for _, bad := range []BackendRef{{Scheme: "sqlite"}, {Scheme: ClickHouseScheme}} {
		_, err := bad.ClickHouseConfig(clickHouseEnv(nil), false)
		require.Error(t, err)
	}
	for _, tc := range []struct{ name, raw, allow string }{
		{"direct endpoint", ref.Raw, "DATATUG_CH_BOUNDARY"},
		{"invalid env name", "env:bad-name", "DATATUG_CH_BOUNDARY"},
		{"oversize source grant", "env:DATATUG_CH_BOUNDARY", strings.Repeat("X", 4097)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ref
			r.Raw = tc.raw
			_, err := r.ClickHouseConfig(clickHouseEnv(map[string]string{ClickHouseUsernameEnv: "reader", ClickHouseNativeSQLEnv: "true", ClickHouseNativeSourcesEnv: tc.allow}), true)
			require.Error(t, err)
		})
	}
}

func TestClickHouseOpenSchemaAndErrorsKeepTheirCapabilityAndOwnership(t *testing.T) {
	t.Setenv("DATATUG_CH_BOUNDARY", "clickhouse+http://127.0.0.1:1/explore")
	t.Setenv(ClickHouseUsernameEnv, "reader")
	t.Setenv(ClickHousePasswordEnv, "synthetic-only")
	t.Setenv(ClickHouseRelationsEnv, `{"events":["id","amount"]}`)
	t.Setenv(ClickHouseNativeSQLEnv, "false")
	ref, err := Parse("env:DATATUG_CH_BOUNDARY")
	require.NoError(t, err)
	old := newClickHouseDatabase
	calls := 0
	newClickHouseDatabase = func(ctx context.Context, cfg dalgo2clickhouse.Config) (dal.DB, error) {
		calls++
		require.False(t, cfg.NativeSQL)
		require.Equal(t, []string{"id", "amount"}, cfg.Tables["events"])
		// The real SDK constructor is pure; no query or metadata call is made.
		return old(ctx, cfg)
	}
	t.Cleanup(func() { newClickHouseDatabase = old })
	db, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	require.NotNil(t, db)
	CloseSource(db)
	scan, err := ref.OpenSchemaScan(context.Background())
	require.NoError(t, err)
	require.NotNil(t, scan)
	require.NoError(t, scan.Close())
	require.Equal(t, 2, calls)
	marker := errors.New("untrusted SQL credential endpoint")
	newClickHouseDatabase = func(context.Context, dalgo2clickhouse.Config) (dal.DB, error) { return nil, marker }
	_, err = ref.OpenClickHouse(context.Background(), false)
	require.ErrorIs(t, err, marker)
	require.NotContains(t, err.Error(), marker.Error())
	_, err = ref.OpenSchemaScan(context.Background())
	require.ErrorIs(t, err, marker)
	require.NotContains(t, err.Error(), marker.Error())
	wrapped := ref.OpenFailure(marker)
	require.ErrorIs(t, wrapped, marker)
	require.Contains(t, wrapped.Error(), "ClickHouse")
	require.NotContains(t, wrapped.Error(), marker.Error())
	t.Setenv(ClickHouseUsernameEnv, "")
	_, err = ref.OpenClickHouse(context.Background(), false)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-only")
	file := filepath.Join(t.TempDir(), "connection.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"dsnEnv":"DATATUG_CH_BOUNDARY"}`), 0600))
	descriptor, err := ReadClickHouseDescriptor(file)
	require.NoError(t, err)
	require.Equal(t, "env:DATATUG_CH_BOUNDARY", descriptor.SourceURL())
}
