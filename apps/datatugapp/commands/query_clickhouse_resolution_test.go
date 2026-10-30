package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/require"
)

type clickHouseSnapshotStore struct {
	datatug.ProjectStore
	catalogReads     int
	changeAfterFirst bool
}

func (s *clickHouseSnapshotStore) LoadEnvDbCatalog(ctx context.Context, env, server, catalog string, options ...datatug.StoreOption) (datatug.DbCatalog, error) {
	s.catalogReads++
	if s.changeAfterFirst && s.catalogReads > 1 {
		// A regression that resolves again would silently change providers.
		catalog := datatug.DbCatalog{}
		catalog.Driver, catalog.Path = "sqlite3", "/tmp/synthetic-second-source.sqlite"
		return catalog, nil
	}
	return s.ProjectStore.LoadEnvDbCatalog(ctx, env, server, catalog, options...)
}

func TestClickHouseSavedCommandUsesOneValidatedSourceSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, format       string
		sql, lookup, saved bool
	}{
		{name: "TugQL JSON", format: "json"},
		{name: "TugQL CSV", format: "csv"},
		{name: "TugQL JSONL", format: "jsonl"},
		{name: "native SQL", format: "json", sql: true},
		{name: "saved TugQL result", format: "json", saved: true},
		{name: "saved native result", format: "json", sql: true, saved: true},
		{name: "keyed lookup refusal", format: "json", lookup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempDatatugHome(t)
			dir := clickHouseSavedProject(t, tc.lookup)
			if tc.sql {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "queries/f/exact.query.json"), []byte(`{"id":"exact","type":"SQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"warehouse"}],"parameters":[{"id":"id","type":"integer","isRequired":true}]}`), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "queries/f/exact.query.sql"), []byte("SELECT {id:UInt64}"), 0600))
			}
			registerProjectForTest(t, "snapshot", dir)
			store := &clickHouseSnapshotStore{ProjectStore: covCFileStore(dir), changeAfterFirst: true}
			oldStore := newProjectsStore
			newProjectsStore = func(string, map[string]string) (storage.Store, error) { return cov100fStore{proj: store}, nil }
			t.Cleanup(func() { newProjectsStore = oldStore })
			oldTugQL, oldNative := runSavedTugQLCLI, runNativeSQLCLI
			t.Cleanup(func() { runSavedTugQLCLI = oldTugQL; runNativeSQLCLI = oldNative })
			calls := 0
			runSavedTugQLCLI = func(_ *secureread.Executor, ctx context.Context, source string, document []byte, vars map[string]any) (secureread.Result, error) {
				calls++
				require.False(t, tc.sql)
				require.False(t, tc.lookup)
				require.NoError(t, ctx.Err())
				require.Equal(t, clickHouseDispatchSource, source)
				require.Equal(t, "from: {name: events}\nlimit: 2\n", string(document))
				require.Equal(t, json.Number("18446744073709551615"), vars["id"])
				return clickHouseCommandResult(), nil
			}
			runNativeSQLCLI = func(_ *secureread.Executor, ctx context.Context, source, text string, args ...dal.QueryArg) (secureread.Result, error) {
				calls++
				require.True(t, tc.sql)
				require.False(t, tc.lookup)
				require.NoError(t, ctx.Err())
				require.Equal(t, clickHouseDispatchSource, source)
				require.Equal(t, "SELECT {id:UInt64}", text)
				require.Equal(t, []dal.QueryArg{{Name: "id", Value: json.Number("18446744073709551615")}}, args)
				return clickHouseCommandResult(), nil
			}
			args := []string{"--project", "snapshot", "--query", "f/exact", "--env", "local", "--as", "alice", "--allow-opaque-sql", "--var", "id=18446744073709551615", "--format", tc.format}
			if tc.saved {
				args = append(args, "--result-id", "source-snapshot")
			}
			out, stderr, code := runQuery(t, "", args...)
			if tc.lookup {
				require.NotZero(t, code)
				require.Contains(t, stderr, "anonymous recordsets do not support keyed lookup")
				require.Zero(t, calls)
			} else {
				require.Zero(t, code, stderr)
				require.Equal(t, 1, calls)
				if tc.saved {
					require.NotContains(t, out, "source-snapshot")
					require.NotContains(t, out, "18446744073709551615")
					files, err := openResultsStore()
					require.NoError(t, err)
					loaded, err := files.Load("source-snapshot")
					require.NoError(t, err)
					require.True(t, loaded.Anonymous)
					require.Equal(t, api.DriverClickHouse, loaded.Meta.DB.Driver)
					require.NotNil(t, loaded.Meta.DB.Project)
					require.Equal(t, "snapshot", *loaded.Meta.DB.Project)
					require.NotNil(t, loaded.Meta.DB.Env)
					require.Equal(t, "local", *loaded.Meta.DB.Env)
					require.NotNil(t, loaded.Meta.DB.Source)
					require.Equal(t, "warehouse", *loaded.Meta.DB.Source)
					require.NotNil(t, loaded.Meta.DB.Ref)
					require.Equal(t, "datatug://snapshot/warehouse?env=local", *loaded.Meta.DB.Ref)
					require.Equal(t, "f/exact", loaded.Meta.Query.SavedQueryID)
				} else {
					require.Contains(t, out, "18446744073709551615")
				}
			}
			require.Equal(t, 1, store.catalogReads, "classification, refusal and execution must share the first catalog resolution")
		})
	}
}

func TestSavedTugQLAtSourceRejectsInvalidBodyBeforeLeaf(t *testing.T) {
	old := runSavedTugQLCLI
	runSavedTugQLCLI = func(*secureread.Executor, context.Context, string, []byte, map[string]any) (secureread.Result, error) {
		t.Fatal("invalid body reached executor")
		return secureread.Result{}, nil
	}
	t.Cleanup(func() { runSavedTugQLCLI = old })
	_, err := runTugQLSavedQueryAtSource(context.Background(), nil, clickHouseDispatchSource, &datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: "from: ["}, nil)
	require.Error(t, err)
}

func TestSavedTugQLResolvingWrapperPreservesLeafContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "anonymous result"},
		{name: "access denial", err: secureread.ErrAccessDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := clickHouseSavedProject(t, false)
			store := &clickHouseSnapshotStore{ProjectStore: covCFileStore(dir), changeAfterFirst: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			query, err := store.LoadQuery(ctx, "f/exact")
			require.NoError(t, err)
			executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
			variables := map[string]any{"id": json.Number("18446744073709551615")}
			want := clickHouseCommandResult()
			if tc.err != nil {
				want = secureread.Result{}
			}
			old := runSavedTugQLCLI
			t.Cleanup(func() { runSavedTugQLCLI = old })
			calls := 0
			runSavedTugQLCLI = func(gotExecutor *secureread.Executor, gotCtx context.Context, source string, document []byte, gotVariables map[string]any) (secureread.Result, error) {
				calls++
				require.Same(t, executor, gotExecutor)
				require.Same(t, ctx, gotCtx)
				require.Equal(t, clickHouseDispatchSource, source)
				require.Equal(t, "from: {name: events}\nlimit: 2\n", string(document))
				require.Equal(t, variables, gotVariables)
				return want, tc.err
			}
			got, err := runTugQLSavedQuery(ctx, executor, store, dir, "local", query, variables)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, want, got)
			require.Equal(t, 1, calls)
			require.Equal(t, 1, store.catalogReads)
		})
	}
}

func TestSavedTugQLStreamingDefersSingleSourceWithoutIO(t *testing.T) {
	for _, format := range []string{"csv", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			var out, progress bytes.Buffer
			query := &datatug.QueryDef{Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: "from: {name: events}\nlimit: 2\n"}
			// A single-source query belongs to ordinary execution. It needs no
			// store or executor here, and must emit nothing before that fallback.
			streamed, err := runStreamedSavedTugQL(context.Background(), &out, &progress, queryOptions{format: format}, nil, nil, "", query, nil)
			require.NoError(t, err)
			require.False(t, streamed)
			require.Empty(t, out.String())
			require.Empty(t, progress.String())
		})
	}
}

func TestClickHouseSavedSourceSnapshotComesFromValidatedCatalog(t *testing.T) {
	dir := clickHouseSavedProject(t, false)
	store := &clickHouseSnapshotStore{ProjectStore: covCFileStore(dir)}
	query, err := store.LoadQuery(context.Background(), "f/exact")
	require.NoError(t, err)
	source, err := savedQuerySourceForClickHouse(context.Background(), store, dir, "local", query)
	require.NoError(t, err)
	require.Equal(t, resolvedQuerySource{URL: clickHouseDispatchSource, Driver: api.DriverClickHouse, Environment: "local", Catalog: "warehouse"}, source)
	require.Equal(t, 1, store.catalogReads, "one validated catalog snapshot suffices for classification")
	// A ClickHouse label cannot classify a descriptor whose actual source is
	// another engine. No partial URL/driver snapshot may escape on failure.
	t.Setenv("DATATUG_CH_COMMAND_BOUNDARY", "sqlite:///tmp/synthetic.sqlite")
	source, err = savedQuerySourceForClickHouse(context.Background(), store, dir, "local", query)
	require.ErrorContains(t, err, "descriptor must name a ClickHouse source")
	require.Empty(t, source)
	t.Setenv("DATATUG_CH_COMMAND_BOUNDARY", "not-a-supported-source")
	source, err = savedQuerySourceForClickHouse(context.Background(), store, dir, "local", query)
	require.Error(t, err)
	require.Empty(t, source)
	clickHouseDispatchEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections/local/warehouse.json"), []byte(`{"dsnEnv":"DATATUG_CH_COMMAND_BOUNDARY","password":"private"}`), 0600))
	source, err = savedQuerySourceForClickHouse(context.Background(), store, dir, "local", query)
	require.Error(t, err)
	require.Empty(t, source)
	require.NotContains(t, err.Error(), "private")
}

func TestClickHouseSavedSourceSnapshotPreservesAmbiguityAndFederationRefusal(t *testing.T) {
	ctx := context.Background()
	dir := covCWriteProject(t, covCEnvFiles(map[string]string{"one": "/tmp/one.sqlite", "two": "/tmp/two.sqlite"}))
	source, err := savedQuerySourceForClickHouse(ctx, covCFileStore(dir), dir, "local", &datatug.QueryDef{Type: datatug.QueryTypeSQL})
	require.ErrorContains(t, err, "does not declare which one")
	require.Empty(t, source)
	dir = clickHouseSavedProject(t, false)
	store := covCFileStore(dir)
	query := &datatug.QueryDef{Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: "from: {database: warehouse, name: events}\nlimit: 2\n"}
	source, err = savedQuerySourceForClickHouse(ctx, store, dir, "local", query)
	require.NoError(t, err)
	require.Empty(t, source, "named-database federation must not be classified as a single-source query")
	// The actual executor retains the independent refusal before opening any
	// ClickHouse leaf; classification does not create a RecordsReader fallback.
	_, err = runTugQLSavedQuery(ctx, secureread.NewExecutor(secureread.Session{Unrestricted: true}), store, dir, "local", query, nil)
	require.ErrorIs(t, err, dal.ErrNotSupported)
	require.ErrorContains(t, err, "ClickHouse")
}
