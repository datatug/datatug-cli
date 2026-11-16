package commands

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/dal-go/dalgo2influxdb"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const influxNativePolicyNote = "access: native InfluxDB v3 mapped reads; the configured read token decides access; DataTug application policies are not applied"

type influxNativeConfig struct {
	Endpoint string                          `json:"endpoint"`
	Database string                          `json:"database"`
	TokenEnv string                          `json:"tokenEnv"`
	CAFile   string                          `json:"caFile,omitempty"`
	Tables   map[string]dalgo2influxdb.Table `json:"tables"`
}
type influxFilterJSON struct {
	Field    string          `json:"field"`
	Operator string          `json:"operator"`
	Value    json.RawMessage `json:"value"`
}
type influxNativeRequest struct {
	Operation  string                 `json:"operation"`
	Collection string                 `json:"collection"`
	Key        *string                `json:"key,omitempty"`
	Keys       []string               `json:"keys,omitempty"`
	Projection []string               `json:"projection,omitempty"`
	Filters    []influxFilterJSON     `json:"filters,omitempty"`
	Order      []dalgo2influxdb.Order `json:"order,omitempty"`
	Limit      int                    `json:"limit,omitempty"`
	StartAfter []json.RawMessage      `json:"startAfter,omitempty"`
}
type admittedInfluxRead struct {
	request influxNativeRequest
	query   dalgo2influxdb.Query
}
type influxReadClient interface {
	Get(context.Context, string, string) (*dalgo2influxdb.Row, error)
	Exists(context.Context, string, string) (bool, error)
	GetMany(context.Context, string, []string) ([]*dalgo2influxdb.Row, error)
	Query(context.Context, dalgo2influxdb.Query) (dalgo2influxdb.Page, error)
}

var _ influxReadClient = (*dalgo2influxdb.Client)(nil)

func queryInfluxDBCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "influxdb --config <mapping.json> --request <file|->",
		Short: "Read explicitly mapped existing InfluxDB v3 tables with a scoped token",
		Long: `Run one native get, exists, getMany or bounded mapped query.
The trusted config pins an HTTPS root, database, collection-to-existing-table
mapping and the NAME of an environment variable holding a read-only bearer token.
An optional caFile supplies a trusted PEM CA. No token value is accepted in flags
or JSON. No proxy, redirects, discovery, raw SQL, Flux, InfluxQL or write fallback.

` + influxNativePolicyNote + `.

--request is one strict JSON object up to 64 KiB, or '-' for stdin. Number values
use {"kind":"number","value":"9007199254740993.000"} in output, filters and cursor
positions. Strings, booleans and null remain native JSON scalars. Query requires
a positive limit; nextCursor uses typed keyset positions, not a server snapshot.
See docs/query/influxdb.md for mapping examples and supported SQL subset/limits.`,
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
			return runInfluxNativeCommand(cmd, configPath, requestPath, timeout)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted mapping JSON file; tokenEnv names an environment variable, never a token value")
	cmd.Flags().StringVar(&requestPath, "request", "", "Mapped read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1 ms to 2 m), including input, token and output preparation")
	return cmd
}

func influxCommandError(err error, usage bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		return Exit("InfluxDB read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("InfluxDB read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("InfluxDB configuration or request is invalid or unreadable", exitCodeUsage)
	default:
		return Exit("InfluxDB read failed; check endpoint, scoped token, mapping and limits", exitCodeDatabase)
	}
}

func runInfluxNativeCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	if configPath == "" || configPath == "-" || requestPath == "" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return influxCommandError(errDynamoInput, true)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	data, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return influxCommandError(err, true)
	}
	var config influxNativeConfig
	if err = decodeDynamoDocument(data, &config); err != nil {
		return influxCommandError(err, true)
	}
	if requestPath == "-" {
		data, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		data, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return influxCommandError(err, true)
	}
	var request influxNativeRequest
	if err = decodeDynamoDocument(data, &request); err != nil {
		return influxCommandError(err, true)
	}
	admitted, err := admitInfluxRead(config, request)
	if err != nil {
		return influxCommandError(err, true)
	}
	transport, err := influxTransport(ctx, config)
	if err != nil {
		return influxCommandError(err, true)
	}
	defer transport.CloseIdleConnections()
	client, err := newInfluxNativeClient(config, transport, timeout)
	if err != nil {
		return influxCommandError(err, true)
	}
	defer func() { _ = client.Close() }()
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), influxNativePolicyNote)
	return deliverInfluxRead(ctx, cmd.OutOrStdout(), client, admitted)
}

func influxTransport(ctx context.Context, config influxNativeConfig) (*http.Transport, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, errDynamoInput
	}
	var roots *x509.CertPool
	if config.CAFile != "" {
		pem, err := readDynamoFile(ctx, config.CAFile)
		if err != nil {
			return nil, err
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errDynamoInput
		}
	}
	return &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DisableCompression: true, MaxConnsPerHost: 1, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: 10 * time.Second}, nil
}

func influxTokenProvider(name string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if err := dynamoFresh(ctx); err != nil {
			return "", err
		}
		token := os.Getenv(name)
		if !dynamoCredentialText(token) {
			return "", errDynamoInput
		}
		if err := dynamoFresh(ctx); err != nil {
			return "", err
		}
		return token, nil
	}
}
func newInfluxNativeClient(config influxNativeConfig, transport *http.Transport, timeout time.Duration) (*dalgo2influxdb.Client, error) {
	return dalgo2influxdb.New(config.Endpoint, dalgo2influxdb.Options{
		Database: config.Database, Tables: config.Tables, TokenProvider: influxTokenProvider(config.TokenEnv), Transport: transport,
		Timeout: timeout, MaxRows: 1000, MaxResponseBytes: 4 << 20, MaxRequestBytes: 64 << 10, MaxConcurrent: 1,
	})
}

func admitInfluxRead(config influxNativeConfig, r influxNativeRequest) (admittedInfluxRead, error) {
	a := admittedInfluxRead{request: r}
	if !dbcopy.ValidEnvName(config.TokenEnv) {
		return a, errDynamoInput
	}
	if _, ok := config.Tables[r.Collection]; !ok {
		return a, errDynamoInput
	}
	queryFields := r.Projection != nil || r.Filters != nil || r.Order != nil || r.Limit != 0 || r.StartAfter != nil
	switch r.Operation {
	case "get", "exists":
		if r.Key == nil || r.Keys != nil || queryFields {
			return a, errDynamoInput
		}
	case "getMany":
		if r.Key != nil || r.Keys == nil || len(r.Keys) > 1000 || queryFields {
			return a, errDynamoInput
		}
	case "query":
		if r.Key != nil || r.Keys != nil || r.Limit < 1 || r.Limit > 1000 || len(r.Filters) > 64 || len(r.Order) > 16 || len(r.Projection) > 128 || len(r.StartAfter) > 17 {
			return a, errDynamoInput
		}
		a.query = dalgo2influxdb.Query{Collection: r.Collection, Projection: r.Projection, Order: r.Order, Limit: r.Limit}
		for _, f := range r.Filters {
			value, err := influxInputScalar(f.Value)
			if err != nil {
				return a, err
			}
			a.query.Filters = append(a.query.Filters, dalgo2influxdb.Filter{Field: f.Field, Operator: f.Operator, Value: value})
		}
		for _, raw := range r.StartAfter {
			value, err := influxInputScalar(raw)
			if err != nil {
				return a, err
			}
			a.query.StartAfter = append(a.query.StartAfter, value)
		}
	default:
		return a, errDynamoInput
	}
	return a, nil
}

func deliverInfluxRead(ctx context.Context, output io.Writer, client influxReadClient, a admittedInfluxRead) error {
	value, err := executeInfluxRead(ctx, client, a)
	if err != nil {
		return influxCommandError(err, false)
	}
	err = writeDynamoResult(ctx, output, value)
	if errors.Is(err, errDynamoResponse) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return influxCommandError(err, false)
	}
	return err
}

func executeInfluxRead(ctx context.Context, client influxReadClient, a admittedInfluxRead) (any, error) {
	switch a.request.Operation {
	case "get":
		row, err := client.Get(ctx, a.request.Collection, *a.request.Key)
		if err != nil {
			return nil, err
		}
		return influxWireRow(row)
	case "exists":
		return client.Exists(ctx, a.request.Collection, *a.request.Key)
	case "getMany":
		rows, err := client.GetMany(ctx, a.request.Collection, a.request.Keys)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(a.request.Keys) {
			return nil, errDynamoResponse
		}
		result := make([]any, len(rows))
		for i, row := range rows {
			result[i], err = influxWireRow(row)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case "query":
		page, err := client.Query(ctx, a.query)
		if err != nil {
			return nil, err
		}
		result := influxWirePage{Rows: make([]any, len(page.Rows))}
		for i := range page.Rows {
			result.Rows[i], err = influxWireRow(&page.Rows[i])
			if err != nil {
				return nil, err
			}
		}
		if len(page.NextCursor) > 0 {
			result.NextCursor = make([]any, len(page.NextCursor))
			for i, value := range page.NextCursor {
				result.NextCursor[i], err = influxWireScalar(value)
				if err != nil {
					return nil, err
				}
			}
		}
		return result, nil
	default:
		return nil, errDynamoInput
	}
}
