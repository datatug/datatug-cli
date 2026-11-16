package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dal-go/dalgo/dal"
	snowsdk "github.com/dal-go/dalgo2snowflake"
	"github.com/spf13/cobra"
)

const snowflakePolicyNote = "access: native Snowflake SQL API reads; configured role and read token govern access; DataTug application policies are not applied"
const snowflakeWireFormat = "snowflake_jsonv2_string_null_v1"

var errSnowflakeCleanup = errors.New("snowflake owned work did not become locally quiescent")

type snowflakeNativeConfig struct {
	Endpoint  string                   `json:"endpoint"`
	Database  string                   `json:"database"`
	Schema    string                   `json:"schema"`
	Warehouse string                   `json:"warehouse,omitempty"`
	Role      string                   `json:"role,omitempty"`
	Tables    map[string]snowsdk.Table `json:"tables"`
	AuthKind  string                   `json:"authKind"`
	TokenEnv  string                   `json:"tokenEnv"`
	CAFile    string                   `json:"caFile,omitempty"`
}
type snowflakeReadClient interface {
	Get(context.Context, string, string) (*snowsdk.Document, error)
	Exists(context.Context, string, string) (bool, error)
	GetMany(context.Context, string, []string) ([]*snowsdk.Document, error)
	Query(context.Context, dal.Query) (*snowsdk.Result, error)
	Shutdown(context.Context) error
}

var _ snowflakeReadClient = (*snowsdk.Client)(nil)

func querySnowflakeCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "snowflake --config <native.json> --request <file|->", Short: "Read existing mapped Snowflake tables through a closed SELECT interface", Long: `Run native get, exists, ordered getMany, structured query, supplied-ID
testConnection or local configuration metadata. Trusted configuration fixes a
Snowflake HTTPS account, database/schema/table mappings, named token ENV and
optional verified CA. There is no raw SQL, signing key, admin/anonymous fallback,
discovery or write route. Use a least-privilege existing Snowflake role.

` + snowflakePolicyNote + `.

Output format snowflake_jsonv2_string_null_v1 preserves original JSONv2 STRING
and NULL cells, including numeric, temporal, binary and VARIANT text. Column
int64 descriptors use {kind:int64,value:decimalString}. All result partitions
are returned or the whole command fails; offsets do not establish a snapshot.
Owned SDK work is drained before stdout with an independent maximum2s cleanup
scope. Cancellation preserves the original error and zero stdout; cleanup may
extend wall time by up to2s and is not proof of hosted termination or credits.
See docs/query/snowflake.md for exact request and qualification boundaries.`, SilenceUsage: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		original := cmd.Context()
		parent := original
		if parent == nil {
			parent = context.Background()
		}
		ctx, _, stop := interruptContext(parent)
		cmd.SetContext(ctx)
		defer func() { cmd.SetContext(original); stop() }()
		return runSnowflakeCommand(cmd, configPath, requestPath, timeout)
	}}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted existing-table mapping JSON, without credential values")
	cmd.Flags().StringVar(&requestPath, "request", "", "Closed native read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1ms through2m); independent cleanup may add at most2s")
	return cmd
}
func snowflakeCommandError(err error, usage bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		return Exit("Snowflake read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("Snowflake read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("Snowflake configuration or request is invalid or unreadable", exitCodeUsage)
	case errors.Is(err, errSnowflakeCleanup):
		return Exit("Snowflake cleanup did not become locally quiescent within its bounded scope", exitCodeDatabase)
	default:
		return Exit("Snowflake read failed; check trusted table mapping, read-token authority and limits", exitCodeDatabase)
	}
}
func runSnowflakeCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	if configPath == "" || configPath == "-" || requestPath == "" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return snowflakeCommandError(errDynamoInput, true)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	raw, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return snowflakeCommandError(err, true)
	}
	var config snowflakeNativeConfig
	if err = decodeSnowflakeConfig(raw, &config); err != nil {
		return snowflakeCommandError(err, true)
	}
	if requestPath == "-" {
		raw, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		raw, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return snowflakeCommandError(err, true)
	}
	request, err := admitSnowflakeRequest(ctx, config, raw)
	if err != nil {
		return snowflakeCommandError(err, true)
	}
	if request.operation == "metadata" {
		return deliverSnowflakeMetadata(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), config)
	}
	client, closeTransport, err := openSnowflakeClient(ctx, config, timeout)
	if err != nil {
		return snowflakeCommandError(err, true)
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), snowflakePolicyNote)
	return deliverSnowflakeRead(ctx, cmd.OutOrStdout(), client, closeTransport, request)
}
func deliverSnowflakeMetadata(ctx context.Context, output, diagnostic io.Writer, config snowflakeNativeConfig) error {
	value, err := snowflakeMetadataResult(ctx, config)
	if err != nil {
		return snowflakeCommandError(err, false)
	}
	_, _ = fmt.Fprintln(diagnostic, snowflakePolicyNote)
	return finishSnowflakeOutput(ctx, output, value)
}

// Resource construction is independently testable using real SDK validation;
// the command's trusted config admission is still completed before this call.
func openSnowflakeClient(ctx context.Context, config snowflakeNativeConfig, timeout time.Duration) (*snowsdk.Client, func(), error) {
	options, err := snowflakeClientOptions(ctx, config, timeout)
	if err != nil {
		return nil, nil, err
	}
	transport, err := influxTransport(ctx, influxNativeConfig{Endpoint: config.Endpoint, CAFile: config.CAFile})
	if err != nil {
		return nil, nil, err
	}
	client, err := newSnowflakeNativeClient(config.Endpoint, options, transport)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	return client, transport.CloseIdleConnections, nil
}

func snowflakeClientOptions(ctx context.Context, c snowflakeNativeConfig, timeout time.Duration) (snowsdk.Options, error) {
	tables := make(map[string]snowsdk.Table, len(c.Tables))
	for k, v := range c.Tables {
		tables[k] = v
	}
	token := influxTokenProvider(c.TokenEnv)
	o := snowsdk.Options{Database: c.Database, Schema: c.Schema, Warehouse: c.Warehouse, Role: c.Role, Tables: tables, Timeout: timeout, MaxConcurrent: 1, MaxKeys: 100, MaxRows: 1000, MaxRequestBytes: 64 << 10, MaxResponseBytes: 4 << 20, MaxPageBytes: 1 << 20, MaxRequests: 256, MaxPartitions: 128, MaxColumns: 128, MaxNodes: 65536, MaxPolls: 120, PollInterval: min(250*time.Millisecond, timeout)}
	o.Token = func(ctx context.Context) (snowsdk.Credential, error) {
		value, err := token(ctx)
		if err != nil {
			return snowsdk.Credential{}, err
		}
		if c.AuthKind == "keypairJwt" {
			return snowsdk.KeyPairJWT(value), nil
		}
		return snowsdk.OAuthToken(value), nil
	}
	if err := dynamoFresh(ctx); err != nil {
		return o, err
	}
	return o, nil
}
func newSnowflakeNativeClient(endpoint string, options snowsdk.Options, transport *http.Transport) (*snowsdk.Client, error) {
	options.Transport = transport
	return snowsdk.New(endpoint, options)
}

func deliverSnowflakeRead(ctx context.Context, output io.Writer, client snowflakeReadClient, closeTransport func(), request admittedSnowflakeRead) error {
	value, err := executeSnowflakeRead(ctx, client, request)
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Second)
	cleanupErr := client.Shutdown(cleanup)
	cancelCleanup()
	closeTransport()
	// Caller cancellation is sticky and takes priority over a later deadline;
	// cleanup errors cannot replace an original read failure.
	if final := dynamoFresh(ctx); final != nil && !errors.Is(err, context.Canceled) {
		if errors.Is(final, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
			err = final
		}
	}
	if err == nil && cleanupErr != nil {
		err = errSnowflakeCleanup
	}
	if err != nil {
		return snowflakeCommandError(err, false)
	}
	return finishSnowflakeOutput(ctx, output, value)
}
func finishSnowflakeOutput(ctx context.Context, output io.Writer, value any) error {
	err := writeDynamoResult(ctx, output, value)
	if errors.Is(err, errDynamoResponse) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return snowflakeCommandError(err, false)
	}
	return err
}
func executeSnowflakeRead(ctx context.Context, client snowflakeReadClient, r admittedSnowflakeRead) (map[string]any, error) {
	budget := snowflakeWireBudget{}
	budget.object(ctx, "operation", "format", "collection")
	budget.text(ctx, r.operation)
	budget.text(ctx, snowflakeWireFormat)
	budget.text(ctx, r.collection)
	if budget.err != nil {
		return nil, budget.err
	}
	result := map[string]any{"operation": r.operation, "format": snowflakeWireFormat, "collection": r.collection}
	switch r.operation {
	case "get":
		document, err := client.Get(ctx, r.collection, r.id)
		if err != nil {
			return nil, err
		}
		budget.key(ctx, "document")
		wire, err := budget.document(ctx, document, r.table.IDColumn, r.id)
		if err != nil {
			return nil, err
		}
		result["document"] = wire
	case "exists", "testConnection":
		exists, err := client.Exists(ctx, r.collection, r.id)
		if err != nil {
			return nil, err
		}
		budget.key(ctx, "exists")
		budget.literal(ctx, 5)
		result["exists"] = exists
		if r.operation == "testConnection" {
			budget.key(ctx, "requestCompleted")
			budget.literal(ctx, 4)
			result["requestCompleted"] = true
		}
	case "getMany":
		documents, err := client.GetMany(ctx, r.collection, append([]string{}, r.ids...))
		if err != nil {
			return nil, err
		}
		if len(documents) != len(r.ids) {
			return nil, errDynamoResponse
		}
		budget.key(ctx, "documents")
		budget.array(ctx, len(documents))
		if budget.err != nil {
			return nil, budget.err
		}
		wire := make([]any, len(documents))
		for i, document := range documents {
			wire[i], err = budget.document(ctx, document, r.table.IDColumn, r.ids[i])
			if err != nil {
				return nil, err
			}
		}
		result["documents"] = wire
	case "query":
		page, err := client.Query(ctx, r.query)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.Rows) > r.limit {
			return nil, errDynamoResponse
		}
		budget.key(ctx, "columns")
		budget.key(ctx, "rows")
		budget.array(ctx, len(page.Rows))
		cols, err := budget.columns(ctx, page.Columns)
		if err != nil {
			return nil, err
		}
		rows := make([]any, len(page.Rows))
		for i, row := range page.Rows {
			budget.array(ctx, len(page.Columns))
			if err = budget.admitRow(ctx, row, page.Columns); err != nil {
				return nil, err
			}
			values := make([]any, len(page.Columns))
			for j, col := range page.Columns {
				if value := row[col.Name]; value != nil {
					values[j] = *value
				}
			}
			rows[i] = values
		}
		result["columns"] = cols
		result["rows"] = rows
	default:
		return nil, errDynamoInput
	}
	if budget.err != nil {
		return nil, budget.err
	}
	return snowflakeWireResult(ctx, result)
}
