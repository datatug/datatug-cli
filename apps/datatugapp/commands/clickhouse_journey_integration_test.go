//go:build clickhouseintegration

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

const clickHouseJourneyPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: clickhouse-reader
default: deny
scopes:
  - path: /events
    rules:
      - id: large-amounts
        effect: allow
        operations: [query]
        where:
          op: ">"
          left: {field: amount}
          right: {value: 2}
        fields: [id, amount]
  - opaqueQuery: true
    rules:
      - id: explicit-native
        effect: allow
        operations: [query]
`

// This build-tagged test MUST run with the owned fixture; absence is a failure,
// never a skip. It drives real Cobra commands and a separately built CLI binary.
func TestClickHouseCLIJourney(t *testing.T) {
	source := os.Getenv("DATATUG_TEST_CLICKHOUSE_URL")
	binary := os.Getenv("DATATUG_TEST_CLICKHOUSE_BINARY")
	require.NotEmpty(t, source, "run scripts/check-journey-clickhouse.sh in Linux")
	require.NotEmpty(t, binary)
	require.FileExists(t, binary)
	dir := addProject(t)
	t.Setenv("DATATUG_CH_JOURNEY", source)
	t.Setenv(dbcopy.ClickHouseNativeSourcesEnv, "DATATUG_CH_JOURNEY")
	t.Setenv(dbcopy.ClickHouseUsernameEnv, "fixture_reader")
	t.Setenv(dbcopy.ClickHousePasswordEnv, "synthetic-reader")
	t.Setenv(dbcopy.ClickHouseRelationsEnv, `{"events":[]}`)
	t.Setenv(dbcopy.ClickHouseNativeSQLEnv, "true")
	owned, err := dbcopy.Parse("env:DATATUG_CH_JOURNEY")
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(owned.ClickHouseAddress)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.NotNil(t, ip)
	require.True(t, ip.IsLoopback(), "the real journey may open only its owned literal loopback fixture")
	require.True(t, dbcopy.IsClickHouseScheme(owned.Scheme))
	previousOpen := api.OpenSchemaScanForTest()
	t.Cleanup(api.SetOpenSchemaScanForTest(func(ref dbcopy.BackendRef, ctx context.Context) (dbcopy.SchemaScanDB, error) {
		if ref.Raw == "env:DATATUG_CH_JOURNEY" && dbcopy.IsClickHouseScheme(ref.Scheme) && ref.Scheme == owned.Scheme && ref.ClickHouseAddress == owned.ClickHouseAddress && ref.ClickHouseDatabase == owned.ClickHouseDatabase && ref.Path == owned.Path {
			return ref.OpenSchemaScan(ctx)
		}
		return previousOpen(ref, ctx)
	}))
	require.Panics(t, func() {
		_, _ = api.OpenSchemaScanForTest()(dbcopy.BackendRef{Scheme: "sqlite", Raw: "env:DATATUG_NOT_THE_FIXTURE"}, context.Background())
	}, "the existing guard must still reject every unrelated source")
	write := func(relative, text string) {
		path := filepath.Join(dir, relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte(text), 0600))
	}
	decode := func(text string) struct {
		Recordset apicontract.Recordset      `json:"recordset"`
		Native    []secureread.OrdinalColumn `json:"ordinalColumns"`
	} {
		var out struct {
			Recordset apicontract.Recordset      `json:"recordset"`
			Native    []secureread.OrdinalColumn `json:"ordinalColumns"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &out))
		require.NotContains(t, text, "$key")
		return out
	}
	stdout, _, err := runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_CH_JOURNEY", "--env", "local", "--name", "warehouse")
	require.NoError(t, err)
	require.Contains(t, stdout, `"driver":"clickhouse"`)
	descriptor, err := os.ReadFile(filepath.Join(dir, "connections/local/warehouse.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"dsnEnv":"DATATUG_CH_JOURNEY"}`, string(descriptor))
	_, err = runScanCommand(t, "-d", dir, "-D", "clickhouse", "--dsn-env", "DATATUG_CH_JOURNEY", "--env", "local", "--db", "warehouse")
	require.NoError(t, err)
	schemaCommand := DatatugCommand()
	var schemaOut bytes.Buffer
	schemaCommand.SetOut(&schemaOut)
	schemaCommand.SetArgs([]string{"db", "schema", "--db", "datatug://shop/warehouse?env=local", "--columns", "--format", "json"})
	require.NoError(t, schemaCommand.Execute())
	require.Contains(t, schemaOut.String(), "UInt64")
	require.Contains(t, schemaOut.String(), "Decimal(38")
	require.NotContains(t, schemaOut.String(), "primaryKeyPosition")
	for name, text := range projectFilesOf(t, dir) {
		require.NotContains(t, text, "synthetic-reader", name)
		require.NotContains(t, text, source, name)
	}
	stdout, stderr, code := runQuery(t, "", "--db", "datatug://shop/warehouse?env=local", "--from", "events", "--limit", "3", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	all := decode(stdout)
	require.Len(t, all.Recordset.Rows, 3)
	require.Len(t, all.Native, 6)
	maxRows := 0
	for _, row := range all.Recordset.Rows {
		if row[0].Str == "18446744073709551615" {
			maxRows++
			require.Equal(t, "12345678901234567890.123456789", row[1].Str)
			require.Contains(t, row[2].Str, "18446744073709551615")
			require.Equal(t, apicontract.ValueTypeNull, row[3].Type)
			require.Equal(t, "2026-01-02T03:04:05.123456789Z", row[4].Str)
			require.Contains(t, row[5].Str, "18446744073709551615")
			require.Equal(t, "json-text", all.Native[2].Encoding)
			require.Equal(t, "json-text", all.Native[5].Encoding)
		}
	}
	require.Equal(t, 2, maxRows, "duplicate rows must survive")
	duplicate := `from: {name: events}
columns:
  - {field: id, as: same}
  - {field: id, as: same}
limit: 3
`
	stdout, stderr, code = runQuery(t, duplicate, "--db", "env:DATATUG_CH_JOURNEY", "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	dups := decode(stdout)
	require.Len(t, dups.Recordset.Columns, 2)
	require.Equal(t, "same", dups.Recordset.Columns[0].Name)
	require.Equal(t, "same", dups.Recordset.Columns[1].Name)
	for _, row := range dups.Recordset.Rows {
		require.Equal(t, row[0], row[1])
	}
	write("policies/reader.yaml", clickHouseJourneyPolicy)
	write("queries/warehouse/large.query.json", `{"id":"warehouse/large","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"warehouse"}]}`)
	write("queries/warehouse/large.query.tugql.yaml", `from: {name: events}
columns:
  - {field: id}
  - {field: amount}
limit: 3
`)
	stdout, stderr, code = runQuery(t, "", "--project", dir, "--query", "warehouse/large", "--env", "local", "--as", "alice", "--format", "json")
	require.Zero(t, code, stderr)
	secured := decode(stdout)
	require.Len(t, secured.Recordset.Rows, 2)
	require.Contains(t, stderr, "large-amounts")
	write("queries/warehouse/by-id.query.json", `{"id":"warehouse/by-id","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"warehouse"}],"parameters":[{"id":"id","type":"integer","isRequired":true}]}`)
	write("queries/warehouse/by-id.query.tugql.yaml", `from: {name: events}
columns: [{field: id}, {field: amount}]
where:
  op: "=="
  left: {field: id}
  right: {param: id}
limit: 3
`)
	stdout, stderr, code = runQuery(t, "", "--project", dir, "--query", "warehouse/by-id", "--env", "local", "--as", "alice", "--var", "id=18446744073709551615", "--format", "json")
	require.Zero(t, code, stderr)
	require.Len(t, decode(stdout).Recordset.Rows, 2)
	// A SELECT alias must not capture a row policy's physical amount predicate.
	// Field-restricted policies retain the shared alias refusal unchanged.
	write("alias-row-only.yaml", strings.Replace(clickHouseJourneyPolicy, "        fields: [id, amount]\n", "", 1))
	shadow := `from: {name: events}
columns:
  - {field: id, as: amount}
orderBy:
  - {field: amount}
limit: 3
`
	stdout, stderr, code = runQuery(t, shadow, "--db", "env:DATATUG_CH_JOURNEY", "-f", "-", "--policy", filepath.Join(dir, "alias-row-only.yaml"), "--as", "alice", "--format", "json")
	require.Zero(t, code, stderr)
	shadowed := decode(stdout)
	require.Len(t, shadowed.Recordset.Rows, 2)
	require.Equal(t, "18446744073709551615", shadowed.Recordset.Rows[0][0].Str)
	t.Run("private locator and typed reopen", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--project", dir, "--query", "warehouse/large", "--env", "local", "--as", "alice", "--result-id", "clickhouse-proof", "--format", "json")
		require.Zero(t, code, stderr)
		var locator struct {
			ResultFiles struct {
				Version int `json:"version"`
			} `json:"resultFiles"`
			RowsKept int  `json:"rowsKept"`
			HasMore  bool `json:"hasMore"`
			Columns  int  `json:"columns"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &locator))
		require.Equal(t, 1, locator.ResultFiles.Version)
		require.Equal(t, 2, locator.RowsKept)
		require.False(t, locator.HasMore)
		require.Equal(t, 2, locator.Columns)
		require.JSONEq(t, `{"resultFiles":{"version":1},"rowsKept":2,"hasMore":false,"columns":2}`, stdout)
		for _, private := range []string{"clickhouse-proof", dir, `"id"`, `"rows"`, "18446744073709551615", "12345678901234567890.123456789"} {
			require.NotContains(t, stdout, private, "the locator contains only ID-free, path-free result counts")
		}
		show := queryResultsShowCommand()
		var reopened bytes.Buffer
		show.SetOut(&reopened)
		show.SetArgs([]string{"--id", "clickhouse-proof"})
		require.NoError(t, show.Execute())
		var result struct {
			Anonymous bool                  `json:"anonymous"`
			Recordset apicontract.Recordset `json:"recordset"`
		}
		require.NoError(t, json.Unmarshal(reopened.Bytes(), &result))
		require.True(t, result.Anonymous)
		require.Len(t, result.Recordset.Rows, 2)
		require.NotContains(t, reopened.String(), "$key")
		for _, row := range result.Recordset.Rows {
			require.Len(t, row, 2)
			require.Equal(t, apicontract.NewIntegerValue("18446744073709551615"), row[0])
			require.Equal(t, apicontract.NewDecimalValue("12345678901234567890.123456789"), row[1])
		}
	})
	// Named values retain exact JSON numeric lexemes through saved sqlQueryArgs
	// and native driver bindings. There is no positional/name dropping fallback.
	write("queries/warehouse/exact.query.json", `{"id":"warehouse/exact","type":"SQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"warehouse"}],"parameters":[{"id":"id","type":"integer","isRequired":true},{"id":"amount","type":"number","isRequired":true}]}`)
	write("queries/warehouse/exact.query.sql", `SELECT id,amount FROM explore.events WHERE id={id:UInt64} AND amount={amount:Decimal(38,9)}`)
	stdout, stderr, code = runQuery(t, "", "--project", dir, "--query", "warehouse/exact", "--env", "local", "--as", "alice", "--allow-opaque-sql", "--var", "id=18446744073709551615", "--var", "amount=12345678901234567890.123456789", "--format", "json")
	require.Zero(t, code, stderr)
	require.Len(t, decode(stdout).Recordset.Rows, 2)
	require.Contains(t, stderr, "row/column policies not applied")
	_, stderr, code = runQuery(t, "", "--project", dir, "--query", "warehouse/exact", "--env", "local", "--as", "alice", "--var", "id=18446744073709551615", "--var", "amount=12345678901234567890.123456789")
	require.NotZero(t, code)
	require.Contains(t, stderr, "opaque")
	// Binary proof exercises this checkout's built CLI command routes.
	command := exec.Command(binary, "query", "run", "--db", "env:DATATUG_CH_JOURNEY", "--sql", "SELECT id AS same,id AS same FROM explore.events LIMIT 3", "--format", "json", "--no-policies")
	var out, errOut bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errOut
	require.NoError(t, command.Run(), errOut.String())
	native := decode(out.String())
	require.Len(t, native.Recordset.Rows, 3)
	require.Equal(t, "same", native.Recordset.Columns[0].Name)
	require.Equal(t, "same", native.Recordset.Columns[1].Name)
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	source = "env:DATATUG_CH_JOURNEY"
	for _, sql := range []string{"DROP TABLE explore.events", "SELECT 1 SETTINGS readonly=0", "SELECT value FROM forbidden.secret", "SELECT id AS same,amount AS same FROM explore.events"} {
		_, err := executor.RunNativeSQL(context.Background(), source, sql)
		require.Error(t, err)
		require.NotContains(t, err.Error(), sql)
		require.NotContains(t, err.Error(), "synthetic-reader")
	}
	_, err = executor.RunNativeSQL(context.Background(), source, "SELECT number FROM numbers(1001)")
	require.ErrorIs(t, err, dalgo2clickhouse.ErrLimit)
	bound, err := executor.RunNativeSQL(context.Background(), source, "SELECT {text:String}", dal.QueryArg{Name: "text", Value: "' OR 1=1 --"})
	require.NoError(t, err)
	require.Equal(t, "' OR 1=1 --", bound.AnonymousRecordset.Rows[0][0].Str)
	commandCtx, stopCommand := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, stderr, code = runQueryContext(t, commandCtx, "", "--db", "env:DATATUG_CH_JOURNEY", "--sql", "SELECT sleep(3)", "--no-policies")
	stopCommand()
	require.NotZero(t, code)
	require.Contains(t, stderr, "deadline")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = executor.RunNativeSQL(ctx, source, "SELECT sleep(3)")
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled), "cancellation identity: %v", err)
	t.Setenv(dbcopy.ClickHousePasswordEnv, "wrong-synthetic-password")
	_, err = executor.RunNativeSQL(context.Background(), source, "SELECT 1")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "wrong-synthetic-password")
	t.Setenv(dbcopy.ClickHousePasswordEnv, "synthetic-reader")
	final, err := executor.RunNativeSQL(context.Background(), source, "SELECT count() FROM explore.events")
	require.NoError(t, err)
	require.Equal(t, "3", final.AnonymousRecordset.Rows[0][0].Str)
}
