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

	convexsdk "github.com/dal-go/dalgo2convex"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const convexNativePolicyNote = "access: native Convex public-query reads; deployment function authorization decides access; DataTug application policies are not applied"

type convexCollectionConfig struct {
	GetFunction string          `json:"getFunction"`
	IDArgument  string          `json:"idArgument,omitempty"`
	IDField     string          `json:"idField,omitempty"`
	StaticArgs  json.RawMessage `json:"staticArgs,omitempty"`
	Fields      []string        `json:"fields,omitempty"`
}
type convexQueryConfig struct {
	Function      string          `json:"function"`
	Collection    string          `json:"collection"`
	ArgumentNames []string        `json:"argumentNames,omitempty"`
	StaticArgs    json.RawMessage `json:"staticArgs,omitempty"`
}
type convexNativeConfig struct {
	Endpoint    string                            `json:"endpoint"`
	TokenEnv    string                            `json:"tokenEnv,omitempty"`
	Anonymous   bool                              `json:"anonymous,omitempty"`
	CAFile      string                            `json:"caFile,omitempty"`
	Collections map[string]convexCollectionConfig `json:"collections"`
	Queries     map[string]convexQueryConfig      `json:"queries,omitempty"`
}
type convexNativeRequest struct {
	Operation  string          `json:"operation"`
	Collection string          `json:"collection,omitempty"`
	ID         string          `json:"id,omitempty"`
	IDs        []string        `json:"ids,omitempty"`
	Query      string          `json:"query,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	PageSize   int             `json:"pageSize,omitempty"`
	Pages      int             `json:"pages,omitempty"`
}
type admittedConvexRead struct {
	request convexNativeRequest
	args    convexsdk.Object
}
type convexReadClient interface {
	Get(context.Context, string, string) (*convexsdk.Row, error)
	Exists(context.Context, string, string) (bool, error)
	GetMany(context.Context, string, []string) ([]*convexsdk.Row, error)
	QueryFunction(context.Context, string, convexsdk.Object, convexsdk.PageOptions) (convexsdk.Page, error)
	Close() error
}

var _ convexReadClient = (*convexsdk.Client)(nil)

func queryConvexCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "convex --config <native.json> --request <file|->", Short: "Read existing Convex documents through trusted public query aliases", Long: `Run native get, exists, ordered getMany, configured query or testConnection.
Trusted configuration fixes an HTTPS root, collection/query function aliases,
static arguments and a named user-token ENV or explicit anonymous mode. No token
value belongs in argv/config. There is no admin/mutation/action/table-browse route.

` + convexNativePolicyNote + `.

Native args and document data use convex_encoded_json: int64 $integer, binary
$bytes, special IEEE float64 $float. Finite numbers are native float64 values,
not arbitrary-precision decimal lexemes. Query selects an alias, pageSize and
pages; pages*pageSize must be <=1000. Whole pages, including empty evaluated pages,
are traversed under one deadline without truncation. hasMore is advisory, not a
portable cursor. See docs/query/convex.md for exact contracts and proof limits.`, SilenceUsage: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		original := cmd.Context()
		parent := original
		if parent == nil {
			parent = context.Background()
		}
		ctx, _, stop := interruptContext(parent)
		cmd.SetContext(ctx)
		defer func() { cmd.SetContext(original); stop() }()
		return runConvexNativeCommand(cmd, configPath, requestPath, timeout)
	}}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted native public-query mapping JSON, without credential values")
	cmd.Flags().StringVar(&requestPath, "request", "", "Native read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1ms through2m), including input/auth/codec/output")
	return cmd
}
func convexCommandError(err error, usage bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		return Exit("Convex read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("Convex read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("Convex configuration or request is invalid or unreadable", exitCodeUsage)
	default:
		return Exit("Convex read failed; check trusted function mapping, user authorization and limits", exitCodeDatabase)
	}
}
func runConvexNativeCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	if configPath == "" || configPath == "-" || requestPath == "" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return convexCommandError(errDynamoInput, true)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	data, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return convexCommandError(err, true)
	}
	var config convexNativeConfig
	if err := decodeConvexConfig(data, &config); err != nil {
		return convexCommandError(err, true)
	}
	if requestPath == "-" {
		data, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		data, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return convexCommandError(err, true)
	}
	var request convexNativeRequest
	if err := decodeConvexRequest(data, &request); err != nil {
		return convexCommandError(err, true)
	}
	admitted, err := admitConvexRead(ctx, config, request)
	if err != nil {
		return convexCommandError(err, true)
	}
	options, err := convexClientOptions(ctx, config, timeout)
	if err != nil {
		return convexCommandError(err, true)
	}
	transport, err := influxTransport(ctx, influxNativeConfig{Endpoint: config.Endpoint, CAFile: config.CAFile})
	if err != nil {
		return convexCommandError(err, true)
	}
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client, err := newConvexNativeClient(config.Endpoint, options, transport)
	if err != nil {
		return convexCommandError(err, true)
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), convexNativePolicyNote)
	return deliverConvexRead(ctx, cmd.OutOrStdout(), client, admitted)
}
func convexClientOptions(ctx context.Context, c convexNativeConfig, timeout time.Duration) (convexsdk.Options, error) {
	o := convexsdk.Options{Anonymous: c.Anonymous, Timeout: timeout, MaxRows: 1000, MaxKeys: 100, MaxPages: 100, MaxResponseBytes: 1 << 20, MaxRequestBytes: 64 << 10, MaxTotalRequestBytes: 64 << 10, MaxWork: 10000, MaxConcurrent: 1, MaxCursors: 1, MaxCursorBytes: 1 << 20, MaxCursorNodes: 4096, Collections: make(map[string]convexsdk.Collection), Queries: make(map[string]convexsdk.Query)}
	if c.Anonymous {
		if c.TokenEnv != "" {
			return o, errDynamoInput
		}
	} else {
		if !dbcopy.ValidEnvName(c.TokenEnv) {
			return o, errDynamoInput
		}
		o.Token = func(ctx context.Context) (string, error) {
			if err := dynamoFresh(ctx); err != nil {
				return "", err
			}
			return os.Getenv(c.TokenEnv), nil
		}
	}
	for alias, cfg := range c.Collections {
		args, err := convexInputObject(ctx, cfg.StaticArgs)
		if err != nil {
			return o, err
		}
		o.Collections[alias] = convexsdk.Collection{GetFunction: cfg.GetFunction, IDArgument: cfg.IDArgument, IDField: cfg.IDField, StaticArgs: args, Fields: cfg.Fields}
	}
	for alias, cfg := range c.Queries {
		args, err := convexInputObject(ctx, cfg.StaticArgs)
		if err != nil {
			return o, err
		}
		o.Queries[alias] = convexsdk.Query{Function: cfg.Function, Collection: cfg.Collection, ArgumentNames: cfg.ArgumentNames, StaticArgs: args}
	}
	if err := dynamoFresh(ctx); err != nil {
		return o, err
	}
	return o, nil
}
func newConvexNativeClient(endpoint string, options convexsdk.Options, transport *http.Transport) (*convexsdk.Client, error) {
	options.RoundTripper = transport
	return convexsdk.New(endpoint, options)
}
func admitConvexRead(ctx context.Context, c convexNativeConfig, r convexNativeRequest) (admittedConvexRead, error) {
	a := admittedConvexRead{request: r}
	queryFields := r.Query != "" || r.Args != nil || r.PageSize != 0 || r.Pages != 0
	switch r.Operation {
	case "get", "exists", "testConnection":
		if r.Collection == "" || r.ID == "" || r.IDs != nil || queryFields {
			return a, errDynamoInput
		}
		if _, ok := c.Collections[r.Collection]; !ok {
			return a, errDynamoInput
		}
	case "getMany":
		if r.Collection == "" || r.ID != "" || r.IDs == nil || len(r.IDs) > 100 || queryFields {
			return a, errDynamoInput
		}
		if _, ok := c.Collections[r.Collection]; !ok {
			return a, errDynamoInput
		}
		a.request.IDs = append([]string(nil), r.IDs...)
	case "query":
		if r.Collection != "" || r.ID != "" || r.IDs != nil || r.Query == "" || r.PageSize < 1 || r.PageSize > 1000 || r.Pages < 1 || r.Pages > 100 || r.Pages > 1000/r.PageSize {
			return a, errDynamoInput
		}
		if _, ok := c.Queries[r.Query]; !ok {
			return a, errDynamoInput
		}
		args, err := convexInputObject(ctx, r.Args)
		if err != nil {
			return a, err
		}
		a.args = args
	default:
		return a, errDynamoInput
	}
	if err := dynamoFresh(ctx); err != nil {
		return a, err
	}
	return a, nil
}
func deliverConvexRead(ctx context.Context, output io.Writer, client convexReadClient, a admittedConvexRead) error {
	value, err := executeConvexRead(ctx, client, a)
	closed := client.Close()
	if final := dynamoFresh(ctx); final != nil {
		err = final
	}
	if err == nil {
		err = closed
	}
	if err != nil {
		return convexCommandError(err, false)
	}
	err = writeDynamoResult(ctx, output, value)
	if errors.Is(err, errDynamoResponse) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return convexCommandError(err, false)
	}
	return err
}
func executeConvexRead(ctx context.Context, client convexReadClient, a admittedConvexRead) (map[string]any, error) {
	r := a.request
	result := map[string]any{"operation": r.Operation, "format": "convex_encoded_json"}
	var rows []*convexsdk.Row
	switch r.Operation {
	case "get":
		row, err := client.Get(ctx, r.Collection, r.ID)
		if err != nil {
			return nil, err
		}
		if row != nil && row.ID != r.ID {
			return nil, errDynamoResponse
		}
		rows = []*convexsdk.Row{row}
	case "exists", "testConnection":
		exists, err := client.Exists(ctx, r.Collection, r.ID)
		if err != nil {
			return nil, err
		}
		result["exists"] = exists
		if r.Operation == "testConnection" {
			result["requestCompleted"] = true
		}
	case "getMany":
		if len(r.IDs) == 0 {
			rows = []*convexsdk.Row{}
		} else {
			var err error
			rows, err = client.GetMany(ctx, r.Collection, r.IDs)
			if err != nil {
				return nil, err
			}
			if len(rows) != len(r.IDs) {
				return nil, errDynamoResponse
			}
			for i, row := range rows {
				if row != nil && row.ID != r.IDs[i] {
					return nil, errDynamoResponse
				}
			}
		}
	case "query":
		rows = []*convexsdk.Row{}
		cursor := ""
		seen := map[string]bool{}
		for i := 0; i < r.Pages; i++ {
			if err := dynamoFresh(ctx); err != nil {
				return nil, err
			}
			page, err := client.QueryFunction(ctx, r.Query, a.args, convexsdk.PageOptions{Size: r.PageSize, Cursor: cursor})
			if err != nil {
				return nil, err
			}
			if len(page.Documents) > r.PageSize || len(rows) > 1000-len(page.Documents) || page.IsDone && page.NextCursor != "" || !page.IsDone && (page.NextCursor == "" || page.NextCursor == cursor) {
				return nil, errDynamoResponse
			}
			for _, doc := range page.Documents {
				if seen[doc.ID] {
					return nil, errDynamoResponse
				}
				seen[doc.ID] = true
				copy := doc
				rows = append(rows, &copy)
			}
			result["pages"] = i + 1
			result["hasMore"] = !page.IsDone
			if page.IsDone {
				break
			}
			cursor = page.NextCursor
		}
	}
	if r.Operation == "get" || r.Operation == "getMany" || r.Operation == "query" {
		wire, err := convexWireDocuments(ctx, rows)
		if err != nil {
			return nil, err
		}
		if r.Operation == "get" {
			result["document"] = wire[0]
		} else {
			result["documents"] = wire
		}
	}
	return result, nil
}
