package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

const clickHouseDispatchSource = "env:DATATUG_CH_COMMAND_BOUNDARY"

func clickHouseDispatchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATATUG_CH_COMMAND_BOUNDARY", "clickhouse+http://127.0.0.1:18123/explore")
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
}

func stubClickHouseStructured(t *testing.T, fn func(context.Context, secureread.Session, string, dal.Query, map[string]any) (secureread.Result, error)) {
	t.Helper()
	old := runClickHouseStructured
	runClickHouseStructured = fn
	t.Cleanup(func() { runClickHouseStructured = old })
}

func TestClickHouseCommandDispatchPreservesScopeBoundsAndExactVariables(t *testing.T) {
	clickHouseDispatchEnv(t)
	calls := 0
	stubClickHouseStructured(t, func(ctx context.Context, s secureread.Session, source string, q dal.Query, vars map[string]any) (secureread.Result, error) {
		calls++
		require.NoError(t, ctx.Err())
		require.Equal(t, clickHouseDispatchSource, source)
		require.False(t, s.Unrestricted)
		require.NotEmpty(t, s.Policies)
		require.Equal(t, "alice", s.Principal.ID)
		require.Equal(t, []string{"reader"}, s.Principal.Roles)
		require.Equal(t, []string{"team"}, s.Principal.Groups)
		require.Equal(t, json.Number("18446744073709551615"), vars["id"])
		require.Equal(t, 100, q.Limit())
		require.Empty(t, q.(dal.StructuredQuery).OrderBy(), "no fabricated stable-key ordering")
		r := clickHouseCommandResult()
		r.Limitations = []secureread.Limitation{{Kind: secureread.LimitationPolicy, Note: "bounded policy read"}}
		return r, nil
	})
	out, stderr, code := runQuery(t, "", "--db", clickHouseDispatchSource, "--from", "events", "--var", "id=18446744073709551615", "--as", "alice", "--role", "reader", "--group", "team", "--policies-dir", globalPolicyDir(t), "--format", "json")
	require.Zero(t, code, stderr)
	require.Equal(t, 1, calls)
	require.Contains(t, stderr, "ClickHouse exploration default")
	require.Contains(t, stderr, "bounded policy read")
	require.Contains(t, out, "18446744073709551615")
	require.NotContains(t, out, "$key")
	_, stderr, code = runQuery(t, "", "--db", clickHouseDispatchSource, "--from", "events", "--var", "id=1e9999", "--no-policies")
	require.Equal(t, exitCodeUsage, code, stderr)
	require.Equal(t, 1, calls, "bad variables must not reach execution")
}

func TestClickHouseCommandKeepsExplicitLimitAndCancellation(t *testing.T) {
	clickHouseDispatchEnv(t)
	stubClickHouseStructured(t, func(ctx context.Context, s secureread.Session, _ string, q dal.Query, _ map[string]any) (secureread.Result, error) {
		require.True(t, s.Unrestricted)
		require.Equal(t, 2, q.Limit())
		if ctx.Err() != nil {
			return secureread.Result{}, ctx.Err()
		}
		return clickHouseCommandResult(), nil
	})
	cmd, out, stderr := covCRunCmd(t, map[string]string{queryDBFlag: clickHouseDispatchSource, queryFromFlag: "events", queryLimitFlag: "2", queryNoPoliciesFlag: "true", queryFormatFlag: "json"})
	require.NoError(t, queryRunCommandAction(cmd, nil))
	require.NotContains(t, stderr.String(), "exploration default")
	require.Contains(t, out.String(), "18446744073709551615")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)
	out.Reset()
	err := queryRunCommandAction(cmd, nil)
	require.Equal(t, exitCodeDatabase, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "cancelled")
	require.Empty(t, out.String())
	// Direct callers with no Cobra context still receive a live background scope.
	cmd = &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ref, err := dbcopy.Parse(clickHouseDispatchSource)
	require.NoError(t, err)
	require.NoError(t, runClickHouseQuery(cmd, queryOptions{from: "events", limit: 2, limitSet: true, noPolicies: true, format: "json", quiet: true}, ref, nil, "from: {name: events}", nil, nil, nil))
}

func TestClickHouseCommandSealsFailuresAndSavesTypedAdhocResults(t *testing.T) {
	clickHouseDispatchEnv(t)
	resultsHome(t)
	stubClickHouseStructured(t, func(context.Context, secureread.Session, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{}, access.ErrAccessDenied
	})
	_, stderr, code := runQuery(t, "", "--db", clickHouseDispatchSource, "--from", "events", "--no-policies")
	require.Equal(t, exitCodeAccessDenied, code, stderr)
	require.Contains(t, stderr, "ClickHouse read denied")
	stubClickHouseStructured(t, func(context.Context, secureread.Session, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{}, errors.New("secret select * from private")
	})
	_, stderr, code = runQuery(t, "", "--db", clickHouseDispatchSource, "--from", "events", "--no-policies")
	require.Equal(t, exitCodeDatabase, code, stderr)
	require.NotContains(t, stderr, "secret")
	stubClickHouseStructured(t, func(context.Context, secureread.Session, string, dal.Query, map[string]any) (secureread.Result, error) {
		return clickHouseCommandResult(), nil
	})
	for _, from := range []bool{false, true} {
		id := "adhoc-document"
		args := []string{"--db", clickHouseDispatchSource, "--no-policies", "--var", "id=18446744073709551615", "--result-rows", "1"}
		if from {
			id = "adhoc-from"
			args = append(args, "--from", "events")
		} else {
			args = append(args, "-f", "-")
		}
		args = append(args, "--result-id", id)
		out, stderr, code := runQuery(t, "from: {name: events}\nlimit: 2\n", args...)
		require.Zero(t, code, stderr)
		require.JSONEq(t, `{"resultFiles":{"version":1},"rowsKept":1,"hasMore":true,"columns":2}`, out)
		store, err := openResultsStore()
		require.NoError(t, err)
		loaded, err := store.Load(id)
		require.NoError(t, err)
		require.Equal(t, "clickhouse", loaded.Meta.DB.Driver)
		require.Equal(t, apicontract.ScalarValue(apicontract.NewIntegerValue("18446744073709551615")), loaded.Meta.Query.Parameters["id"])
		if from {
			require.Equal(t, "from", loaded.Meta.Query.Kind)
			require.Equal(t, "events", loaded.Meta.Query.From)
		} else {
			require.Equal(t, "tugql", loaded.Meta.Query.Kind)
		}
	}
}

func TestClickHouseNativeCommandPassesSQLAndSortedExactNamedBindings(t *testing.T) {
	clickHouseDispatchEnv(t)
	old := runNativeSQLCLI
	t.Cleanup(func() { runNativeSQLCLI = old })
	const text = "SELECT {z:UInt64}, {amount:Decimal(38,9)}"
	calls := 0
	runNativeSQLCLI = func(e *secureread.Executor, ctx context.Context, source, sql string, args ...dal.QueryArg) (secureread.Result, error) {
		calls++
		require.NotNil(t, e)
		require.NoError(t, ctx.Err())
		require.Equal(t, clickHouseDispatchSource, source)
		require.Equal(t, text, sql)
		require.Equal(t, []dal.QueryArg{{Name: "amount", Value: json.Number("12345678901234567890.123456789")}, {Name: "z", Value: json.Number("18446744073709551615")}}, args)
		return clickHouseCommandResult(), nil
	}
	out, stderr, code := runQuery(t, "", "--db", clickHouseDispatchSource, "--sql", text, "--no-policies", "--var", "z=18446744073709551615", "--var", "amount=12345678901234567890.123456789", "--format", "json")
	require.Zero(t, code, stderr)
	require.Equal(t, 1, calls)
	require.Contains(t, out, "12345678901234567890.123456789")
	_, _, code = runQuery(t, "", "--db", clickHouseDispatchSource, "--sql", text, "--no-policies", "--var", "z=1e9999")
	require.Equal(t, exitCodeUsage, code)
	require.Equal(t, 1, calls)
}

func clickHouseSavedProject(t *testing.T, lookup bool) string {
	t.Helper()
	clickHouseDispatchEnv(t)
	files := map[string]string{
		"connections/local/warehouse.json":                        `{"dsnEnv":"DATATUG_CH_COMMAND_BOUNDARY"}`,
		"environments/local/local.env.json":                       `{"id":"local","dbServers":[{"driver":"clickhouse","catalogs":["warehouse"]}]}`,
		"environments/local/catalogs/warehouse/warehouse.db.json": `{"driver":"clickhouse","path":"connections/local/warehouse.json"}`,
	}
	extra := `,"targets":[{"catalog":"warehouse"}],"parameters":[{"id":"id","type":"integer","isRequired":true}]`
	if lookup {
		extra += `,"federation":{"ovdbBaseUrl":"http://127.0.0.1:1","lookups":[{"database":"extra","collection":"Country","fromColumn":"id","fields":[{"source":"region","target":"region"}]}]}`
	}
	return covCWriteProject(t, covCMerge(files, covCQueryFiles("f", "exact", "TugQL", "tugql", "from: {name: events}\nlimit: 2\n", extra)))
}

func TestClickHouseSavedQueryDispatchAndTypedFileMetadata(t *testing.T) {
	resultsHome(t)
	dir := clickHouseSavedProject(t, false)
	old := runSavedTugQLCLI
	t.Cleanup(func() { runSavedTugQLCLI = old })
	calls := 0
	runSavedTugQLCLI = func(e *secureread.Executor, ctx context.Context, source string, doc []byte, vars map[string]any) (secureread.Result, error) {
		calls++
		require.NotNil(t, e)
		require.NoError(t, ctx.Err())
		require.Equal(t, clickHouseDispatchSource, source)
		require.Equal(t, "from: {name: events}\nlimit: 2\n", string(doc))
		require.Equal(t, json.Number("18446744073709551615"), vars["id"])
		return clickHouseCommandResult(), nil
	}
	base := []string{"--project", dir, "--query", "f/exact", "--env", "local", "--as", "alice", "--var", "id=18446744073709551615"}
	out, stderr, code := runQuery(t, "", append(append([]string{}, base...), "--format", "csv")...)
	require.Zero(t, code, stderr)
	require.Contains(t, out, "same,same")
	require.Equal(t, 1, calls)
	out, stderr, code = runQuery(t, "", append(append([]string{}, base...), "--result-id", "saved-exact", "--result-rows", "1")...)
	require.Zero(t, code, stderr)
	require.NotContains(t, out, "saved-exact")
	store, err := openResultsStore()
	require.NoError(t, err)
	loaded, err := store.Load("saved-exact")
	require.NoError(t, err)
	require.True(t, loaded.Anonymous)
	require.Equal(t, "f/exact", loaded.Meta.Query.SavedQueryID)
	require.Equal(t, apicontract.ScalarValue(apicontract.NewIntegerValue("18446744073709551615")), loaded.Meta.Query.Parameters["id"])
	_, stderr, code = runQuery(t, "", "--project", dir, "--query", "f/exact", "--env", "local", "--as", "alice", "--var", "id=1e9999")
	require.Equal(t, exitCodeUsage, code, stderr)
	require.Equal(t, 2, calls)
	// A source descriptor mutation fails before the saved leaf is dispatched.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections/local/warehouse.json"), []byte(`{"dsnEnv":"DATATUG_CH_COMMAND_BOUNDARY","password":"private"}`), 0600))
	_, _, code = runQuery(t, "", base...)
	require.NotZero(t, code)
	require.Equal(t, 2, calls)
}

func TestClickHouseSavedKeyedLookupsRefuseBeforeLeafDispatch(t *testing.T) {
	dir := clickHouseSavedProject(t, true)
	old := runSavedTugQLCLI
	t.Cleanup(func() { runSavedTugQLCLI = old })
	runSavedTugQLCLI = func(*secureread.Executor, context.Context, string, []byte, map[string]any) (secureread.Result, error) {
		t.Fatal("anonymous lookup reached leaf query")
		return secureread.Result{}, nil
	}
	_, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/exact", "--env", "local", "--as", "alice", "--var", "id=1")
	require.NotZero(t, code)
	require.Contains(t, stderr, "anonymous recordsets do not support keyed lookup")
}

func TestClickHouseCommandOutputFailurePropagates(t *testing.T) {
	clickHouseDispatchEnv(t)
	stubClickHouseStructured(t, func(context.Context, secureread.Session, string, dal.Query, map[string]any) (secureread.Result, error) {
		return clickHouseCommandResult(), nil
	})
	cmd := &cobra.Command{}
	broken := errors.New("destination closed")
	cmd.SetOut(&clickHouseOutputFailure{err: broken})
	cmd.SetErr(&bytes.Buffer{})
	ref, err := dbcopy.Parse(clickHouseDispatchSource)
	require.NoError(t, err)
	err = runClickHouseQuery(cmd, queryOptions{format: "json", noPolicies: true}, ref, nil, "", nil, nil, nil)
	require.ErrorIs(t, err, broken)
}

func TestClickHouseProjectReferenceIsRetainedInAnonymousMetadata(t *testing.T) {
	addProject(t)
	clickHouseDispatchEnv(t)
	_, stderr, err := runDbAdd(t, "--project", "shop", "--db", clickHouseDispatchSource, "--name", "warehouse", "--env", "local")
	require.NoError(t, err, stderr)
	stubClickHouseStructured(t, func(_ context.Context, _ secureread.Session, source string, _ dal.Query, _ map[string]any) (secureread.Result, error) {
		require.Equal(t, clickHouseDispatchSource, source)
		return clickHouseCommandResult(), nil
	})
	out, stderr, code := runQuery(t, "", "--db", "datatug://shop/warehouse?env=local", "--from", "events", "--no-policies", "--result-id", "project-ordinal")
	require.Zero(t, code, stderr)
	require.NotContains(t, out, "shop")
	store, err := openResultsStore()
	require.NoError(t, err)
	loaded, err := store.Load("project-ordinal")
	require.NoError(t, err)
	require.Equal(t, "clickhouse", loaded.Meta.DB.Driver)
	require.NotNil(t, loaded.Meta.DB.Project)
	require.Equal(t, "shop", *loaded.Meta.DB.Project)
	require.NotNil(t, loaded.Meta.DB.Source)
	require.Equal(t, "warehouse", *loaded.Meta.DB.Source)
	require.NotNil(t, loaded.Meta.DB.Env)
	require.Equal(t, "local", *loaded.Meta.DB.Env)
	require.NotNil(t, loaded.Meta.DB.Ref)
	require.Equal(t, "datatug://shop/warehouse?env=local", *loaded.Meta.DB.Ref)
}

func TestClickHouseSavedSQLRetainsTextAndDeclaredBindingOrder(t *testing.T) {
	tempDatatugHome(t)
	dir := clickHouseSavedProject(t, false)
	const text = "SELECT {id:UInt64}, {amount:Decimal(38,9)}"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries/f/exact.query.json"), []byte(`{"id":"exact","type":"SQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"warehouse"}],"parameters":[{"id":"id","type":"integer","isRequired":true},{"id":"amount","type":"number","isRequired":true}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries/f/exact.query.sql"), []byte(text), 0600))
	old := runNativeSQLCLI
	t.Cleanup(func() { runNativeSQLCLI = old })
	calls := 0
	runNativeSQLCLI = func(e *secureread.Executor, ctx context.Context, source, sql string, args ...dal.QueryArg) (secureread.Result, error) {
		calls++
		require.NotNil(t, e)
		require.NoError(t, ctx.Err())
		require.Equal(t, clickHouseDispatchSource, source)
		require.Equal(t, text, sql)
		require.Equal(t, []dal.QueryArg{{Name: "id", Value: json.Number("18446744073709551615")}, {Name: "amount", Value: json.Number("12345678901234567890.123456789")}}, args)
		return clickHouseCommandResult(), nil
	}
	out, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/exact", "--as", "alice", "--allow-opaque-sql", "--var", "amount=12345678901234567890.123456789", "--var", "id=18446744073709551615", "--format", "json")
	require.Zero(t, code, stderr)
	require.Contains(t, out, "12345678901234567890.123456789")
	require.Equal(t, 1, calls)
	_, _, code = runQuery(t, "", "--project", dir, "--query", "f/exact", "--as", "alice", "--var", "id=1")
	require.NotZero(t, code)
	require.Equal(t, 1, calls, "missing declared binding must be refused before execution")
}

func TestClickHouseCommandDefaultExecutionRefusesMissingOperatorConfig(t *testing.T) {
	// Exercise the real default seam. Config validation precedes a transport
	// read, and no operator identity may be inferred from the URL or machine.
	clickHouseDispatchEnv(t)
	t.Setenv(dbcopy.ClickHouseUsernameEnv, "")
	out, stderr, code := runQuery(t, "", "--db", clickHouseDispatchSource, "--from", "events", "--no-policies")
	require.Equal(t, exitCodeDatabase, code, stderr)
	require.Empty(t, out)
	require.Contains(t, stderr, "ClickHouse read failed:")
	require.NotContains(t, stderr, "127.0.0.1")
}

func TestClickHouseSavedStreamingRejectsInvalidDocumentBeforeExecution(t *testing.T) {
	streamed, err := runStreamedSavedTugQL(context.Background(), io.Discard, io.Discard, queryOptions{format: "csv"}, nil, &covCStore{}, "", &datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: "from: ["}, nil)
	require.True(t, streamed)
	require.Error(t, err)
}
