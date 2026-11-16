package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/dal-go/dalgo2cosmosdb"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const cosmosNativePolicyNote = "access: native Cosmos DB for NoSQL reads; scoped resource/AAD authority decides access; DataTug application policies are not applied"

type cosmosNativeConfig struct {
	Endpoint       string `json:"endpoint"`
	Database       string `json:"database"`
	Container      string `json:"container"`
	PartitionField string `json:"partitionField"`
	AuthScopeID    string `json:"authScopeId"`
	AuthType       string `json:"authType"`
	TokenEnv       string `json:"tokenEnv"`
	CAFile         string `json:"caFile,omitempty"`
}
type cosmosNativeKey struct {
	ID        string `json:"id"`
	Partition string `json:"partition"`
}
type cosmosParameter struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}
type cosmosNativeRequest struct {
	Operation  string            `json:"operation"`
	Key        *cosmosNativeKey  `json:"key,omitempty"`
	Keys       []cosmosNativeKey `json:"keys,omitempty"`
	Partition  string            `json:"partition,omitempty"`
	SQL        string            `json:"sql,omitempty"`
	Parameters []cosmosParameter `json:"parameters,omitempty"`
	Limit      int               `json:"limit,omitempty"`
}
type admittedCosmosRead struct {
	request cosmosNativeRequest
	keys    []dalgo2cosmosdb.NativeKey
	query   dalgo2cosmosdb.Query
}
type cosmosReadClient interface {
	Get(context.Context, dalgo2cosmosdb.NativeKey, ...dalgo2cosmosdb.ReadOptions) (*dalgo2cosmosdb.Document, error)
	Exists(context.Context, dalgo2cosmosdb.NativeKey, ...dalgo2cosmosdb.ReadOptions) (bool, error)
	GetMany(context.Context, []dalgo2cosmosdb.NativeKey) ([]*dalgo2cosmosdb.Document, error)
	Query(context.Context, dalgo2cosmosdb.Query) (dalgo2cosmosdb.Page, error)
	Close() error
}

var _ cosmosReadClient = (*dalgo2cosmosdb.Client)(nil)

func queryCosmosDBCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "cosmosdb --config <native.json> --request <file|->",
		Short: "Read existing Cosmos DB for NoSQL documents in explicit string partitions",
		Long: `Run one native get, exists, getMany, parameterized query or testConnection.
The trusted config fixes an HTTPS root, database, existing container, top-level
partition field, nonsecret authScopeId and authType resource or aad. tokenEnv is
the NAME of an environment variable, never a token value. Optional caFile adds
an owned PEM trust root; certificate verification remains enabled.

` + cosmosNativePolicyNote + `.

Input is strict JSON up to 64 KiB. Every native key has id and a nonempty string
partition. Query supplies SQL, separate named parameters, partition and positive
limit. Output numbers use {"kind":"number","value":"9007199254740993.000"}.
hasMore is not a portable cursor: no continuation/session token is emitted.
testConnection requires a native key and performs an authenticated point read;
it does not enumerate accounts, databases or containers. No writes, cross-partition
query, discovery, admin/master-key fallback, retry or consistency override.
See docs/query/cosmosdb.md for examples and the exact qualification boundaries.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			original := cmd.Context()
			parent := original
			if parent == nil {
				parent = context.Background()
			}
			ctx, _, stop := interruptContext(parent)
			cmd.SetContext(ctx)
			defer func() { cmd.SetContext(original); stop() }()
			return runCosmosNativeCommand(cmd, configPath, requestPath, timeout)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted native configuration JSON; contains no credential values")
	cmd.Flags().StringVar(&requestPath, "request", "", "Native read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1 ms to 2 m), including input, authorization and output preparation")
	return cmd
}

func cosmosCommandError(err error, usage bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		return Exit("Cosmos DB read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("Cosmos DB read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("Cosmos DB configuration or request is invalid or unreadable", exitCodeUsage)
	default:
		return Exit("Cosmos DB read failed; check endpoint, scoped authorization, native partition and limits", exitCodeDatabase)
	}
}

func runCosmosNativeCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	if configPath == "" || configPath == "-" || requestPath == "" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return cosmosCommandError(errDynamoInput, true)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	data, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return cosmosCommandError(err, true)
	}
	var config cosmosNativeConfig
	if err = decodeDynamoDocument(data, &config); err != nil {
		return cosmosCommandError(err, true)
	}
	if requestPath == "-" {
		data, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		data, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return cosmosCommandError(err, true)
	}
	var request cosmosNativeRequest
	if err = decodeDynamoDocument(data, &request); err != nil {
		return cosmosCommandError(err, true)
	}
	admitted, err := admitCosmosRead(config, request)
	if err != nil {
		return cosmosCommandError(err, true)
	}
	// This shared transport constructor validates an HTTPS root and bounded CA
	// input. Cosmos owns a clone and additionally disables connection replay.
	transport, err := influxTransport(ctx, influxNativeConfig{Endpoint: config.Endpoint, CAFile: config.CAFile})
	if err != nil {
		return cosmosCommandError(err, true)
	}
	defer transport.CloseIdleConnections()
	client, err := newCosmosNativeClient(config, transport, timeout)
	if err != nil {
		return cosmosCommandError(err, true)
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), cosmosNativePolicyNote)
	return deliverCosmosRead(ctx, cmd.OutOrStdout(), client, admitted)
}

func cosmosAuthorization(config cosmosNativeConfig) dalgo2cosmosdb.AuthorizationProvider {
	return func(ctx context.Context, _ dalgo2cosmosdb.AuthRequest) (dalgo2cosmosdb.Credential, error) {
		if err := dynamoFresh(ctx); err != nil {
			return dalgo2cosmosdb.Credential{}, err
		}
		// The pinned provider rechecks cancellation immediately after this
		// callback, before authorization headers or network dispatch.
		token := os.Getenv(config.TokenEnv)
		if config.AuthType == "resource" {
			return dalgo2cosmosdb.ResourceToken(token)
		}
		return dalgo2cosmosdb.AADToken(token)
	}
}
func newCosmosNativeClient(config cosmosNativeConfig, transport *http.Transport, timeout time.Duration) (*dalgo2cosmosdb.Client, error) {
	return dalgo2cosmosdb.New(config.Endpoint, dalgo2cosmosdb.Options{
		Database: config.Database, Container: config.Container, PartitionField: config.PartitionField, AuthScopeID: config.AuthScopeID,
		Authorization: cosmosAuthorization(config), Transport: transport,
		Limits: dalgo2cosmosdb.Limits{Timeout: timeout, MaxRows: 1000, MaxKeys: 100, MaxPages: 100, MaxRequests: 100, MaxResponseBytes: 1 << 20, MaxRequestBytes: 64 << 10, MaxConcurrent: 1, PageSize: 100},
	})
}

func admitCosmosRead(config cosmosNativeConfig, r cosmosNativeRequest) (admittedCosmosRead, error) {
	a := admittedCosmosRead{request: r}
	if !dbcopy.ValidEnvName(config.TokenEnv) || (config.AuthType != "resource" && config.AuthType != "aad") {
		return a, errDynamoInput
	}
	queryFields := r.SQL != "" || r.Partition != "" || r.Parameters != nil || r.Limit != 0
	switch r.Operation {
	case "get", "exists", "testConnection":
		if r.Key == nil || r.Keys != nil || queryFields {
			return a, errDynamoInput
		}
	case "getMany":
		if r.Key != nil || r.Keys == nil || len(r.Keys) > 100 || queryFields {
			return a, errDynamoInput
		}
		a.keys = make([]dalgo2cosmosdb.NativeKey, len(r.Keys))
		for i, key := range r.Keys {
			a.keys[i] = dalgo2cosmosdb.NativeKey{ID: key.ID, Partition: key.Partition}
		}
	case "query":
		if r.Key != nil || r.Keys != nil || r.SQL == "" || r.Partition == "" || r.Limit < 1 || r.Limit > 1000 || len(r.Parameters) > 100 {
			return a, errDynamoInput
		}
		a.query = dalgo2cosmosdb.Query{SQL: r.SQL, Partition: r.Partition, Limit: r.Limit}
		for _, parameter := range r.Parameters {
			value, err := cosmosInputValue(parameter.Value)
			if err != nil {
				return a, err
			}
			a.query.Parameters = append(a.query.Parameters, dalgo2cosmosdb.Parameter{Name: parameter.Name, Value: value})
		}
	default:
		return a, errDynamoInput
	}
	return a, nil
}

func deliverCosmosRead(ctx context.Context, output io.Writer, client cosmosReadClient, a admittedCosmosRead) error {
	value, err := executeCosmosRead(ctx, client, a)
	closeErr := client.Close()
	if cancelled := dynamoFresh(ctx); cancelled != nil {
		err = cancelled
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return cosmosCommandError(err, false)
	}
	err = writeDynamoResult(ctx, output, value)
	if errors.Is(err, errDynamoResponse) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return cosmosCommandError(err, false)
	}
	return err
}
func executeCosmosRead(ctx context.Context, client cosmosReadClient, a admittedCosmosRead) (map[string]any, error) {
	output := map[string]any{"operation": a.request.Operation}
	var documents []*dalgo2cosmosdb.Document
	switch a.request.Operation {
	case "get":
		document, err := client.Get(ctx, dalgo2cosmosdb.NativeKey{ID: a.request.Key.ID, Partition: a.request.Key.Partition})
		if err != nil {
			return nil, err
		}
		documents = []*dalgo2cosmosdb.Document{document}
	case "exists", "testConnection":
		exists, err := client.Exists(ctx, dalgo2cosmosdb.NativeKey{ID: a.request.Key.ID, Partition: a.request.Key.Partition})
		if err != nil {
			return nil, err
		}
		output["exists"] = exists
		if a.request.Operation == "testConnection" {
			output["requestCompleted"] = true
		}
	case "getMany":
		var err error
		documents, err = client.GetMany(ctx, a.keys)
		if err != nil {
			return nil, err
		}
		if len(documents) != len(a.keys) {
			return nil, errDynamoResponse
		}
	case "query":
		page, err := client.Query(ctx, a.query)
		if err != nil {
			return nil, err
		}
		documents = page.Documents
		output["hasMore"], output["pages"] = page.Next != nil, page.Pages
	}
	if a.request.Operation == "get" || a.request.Operation == "getMany" || a.request.Operation == "query" {
		wire, err := cosmosWireDocuments(ctx, documents)
		if err != nil {
			return nil, err
		}
		if a.request.Operation == "get" {
			output["document"] = wire[0]
		} else {
			output["documents"] = wire
		}
	}
	return output, nil
}
