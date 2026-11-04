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
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/dal-go/dalgo2dynamodb"
	"github.com/spf13/cobra"
)

const dynamoNativePolicyNote = "access: native DynamoDB reads; scoped AWS IAM decides access; DataTug application policies are not applied"

var dynamoRegion = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type dynamoNativeConfig struct {
	Endpoint string                          `json:"endpoint"`
	Region   string                          `json:"region"`
	CAFile   string                          `json:"caFile,omitempty"`
	Tables   map[string]dalgo2dynamodb.Table `json:"tables"`
}
type dynamoSortJSON struct {
	Operator string                       `json:"operator"`
	Values   []map[string]json.RawMessage `json:"values"`
}
type dynamoNativeRequest struct {
	Operation  string                     `json:"operation"`
	Collection string                     `json:"collection"`
	Key        dynamoKeyJSON              `json:"key,omitempty"`
	Keys       []dynamoKeyJSON            `json:"keys,omitempty"`
	Partition  map[string]json.RawMessage `json:"partition,omitempty"`
	Sort       *dynamoSortJSON            `json:"sort,omitempty"`
	Limit      int                        `json:"limit,omitempty"`
	Descending bool                       `json:"descending,omitempty"`
}
type admittedDynamoRead struct {
	request dynamoNativeRequest
	key     dalgo2dynamodb.Item
	keys    []dalgo2dynamodb.Item
	query   dalgo2dynamodb.QueryRequest
}

// Keep the consumer's decoded-native boundary independently fail-closed even
// if a provider returns an unknown future AWS union member. The production
// constructor remains the pinned public provider over the official SDK.
type dynamoReadClient interface {
	Get(context.Context, string, dalgo2dynamodb.Item) (dalgo2dynamodb.Item, error)
	Exists(context.Context, string, dalgo2dynamodb.Item) (bool, error)
	GetMany(context.Context, string, []dalgo2dynamodb.Item) ([]dalgo2dynamodb.Item, error)
	QueryPartition(context.Context, dalgo2dynamodb.QueryRequest) (dalgo2dynamodb.QueryPage, error)
}

var _ dynamoReadClient = (*dalgo2dynamodb.Client)(nil)

func queryDynamoDBCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "dynamodb --config <mapping.json> --request <file|->",
		Short: "Read explicitly mapped existing DynamoDB tables with scoped IAM",
		Long: `Run one native get, exists, getMany or bounded partition query.
The trusted config pins an HTTPS endpoint, region and collection-to-existing-table
key mapping; optional caFile supplies a trusted PEM CA. Credentials are read only
from AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and optional AWS_SESSION_TOKEN.
No profile, IMDS, account discovery, Scan, SQL, writes or transaction fallback.

` + dynamoNativePolicyNote + `.

--request is one strict JSON object up to 64 KiB, or '-' for stdin. Native keys use
AWS one-of tagged S/N/B values; output preserves all AWS attribute tags, decimal
number strings, base64 binary, sets, nested values and null. Query requires a
literal partition and positive limit, optional sort condition and direction.
Output hasMore reports continuation availability; no resumable cursor is emitted
because provider cursors belong to the current in-process client only.
See docs/query/dynamodb.md for config/request examples and exact limits.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// First SIGINT/SIGTERM follows the normal result/cleanup path.
			// Restore the caller's context before this command is reused.
			original := cmd.Context()
			parent := original
			if parent == nil {
				parent = context.Background()
			}
			ctx, _, stop := interruptContext(parent)
			cmd.SetContext(ctx)
			defer func() { cmd.SetContext(original); stop() }()
			return runDynamoNativeCommand(cmd, configPath, requestPath, timeout)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted JSON mapping file; contains no credential values")
	cmd.Flags().StringVar(&requestPath, "request", "", "Native read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1 ms to 2 m), including input, credentials and output preparation")
	return cmd
}

func dynamoCommandError(err error, usage bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		return Exit("DynamoDB read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("DynamoDB read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("DynamoDB configuration or request is invalid or unreadable", exitCodeUsage)
	default:
		return Exit("DynamoDB read failed; check endpoint, scoped credentials, mapping and limits", exitCodeDatabase)
	}
}

func runDynamoNativeCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	if configPath == "" || requestPath == "" || configPath == "-" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return dynamoCommandError(errDynamoInput, true)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	data, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return dynamoCommandError(err, true)
	}
	var config dynamoNativeConfig
	if err = decodeDynamoDocument(data, &config); err != nil {
		return dynamoCommandError(err, true)
	}
	if requestPath == "-" {
		data, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		data, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return dynamoCommandError(err, true)
	}
	var request dynamoNativeRequest
	if err = decodeDynamoDocument(data, &request); err != nil {
		return dynamoCommandError(err, true)
	}
	admitted, err := admitDynamoRead(config, request)
	if err != nil {
		return dynamoCommandError(err, true)
	}
	transport, err := dynamoTransport(ctx, config)
	if err != nil {
		return dynamoCommandError(err, true)
	}
	defer transport.CloseIdleConnections()
	client, err := newDynamoNativeClient(config, transport, timeout)
	if err != nil {
		return dynamoCommandError(err, true)
	}
	defer func() { _ = client.Close() }()
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), dynamoNativePolicyNote)
	return deliverDynamoRead(ctx, cmd.OutOrStdout(), client, admitted)
}
func deliverDynamoRead(ctx context.Context, output io.Writer, client dynamoReadClient, admitted admittedDynamoRead) error {
	value, err := executeDynamoRead(ctx, client, admitted)
	if err != nil {
		return dynamoCommandError(err, false)
	}
	return writeDynamoResult(ctx, output, value)
}

func dynamoTransport(ctx context.Context, config dynamoNativeConfig) (*http.Transport, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || !dynamoRegion.MatchString(config.Region) {
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

type dynamoEnvCredentials struct{}

func (dynamoEnvCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	if err := dynamoFresh(ctx); err != nil {
		return aws.Credentials{}, err
	}
	access, secret, token := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")
	if !dynamoCredentialText(access) || !dynamoCredentialText(secret) || (token != "" && !dynamoCredentialText(token)) {
		return aws.Credentials{}, errDynamoInput
	}
	if err := dynamoFresh(ctx); err != nil {
		return aws.Credentials{}, err
	}
	return aws.Credentials{AccessKeyID: access, SecretAccessKey: secret, SessionToken: token, Source: "DataTug explicit environment"}, nil
}
func dynamoCredentialText(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, c := range []byte(value) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func newDynamoNativeClient(config dynamoNativeConfig, transport *http.Transport, timeout time.Duration) (*dalgo2dynamodb.Client, error) {
	httpClient := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sdk := dynamodb.NewFromConfig(aws.Config{Region: config.Region, Credentials: dynamoEnvCredentials{}, HTTPClient: httpClient}, func(o *dynamodb.Options) { o.BaseEndpoint = &config.Endpoint; o.RetryMaxAttempts = 1 })
	return dalgo2dynamodb.New(sdk, dalgo2dynamodb.Options{Tables: config.Tables, Timeout: timeout, MaxConcurrent: 1, MaxRecords: 1000, MaxParsedBytes: 1 << 20, MaxPages: 100, MaxEvaluated: 10_000, PageSize: 100})
}

func admitDynamoRead(config dynamoNativeConfig, request dynamoNativeRequest) (admittedDynamoRead, error) {
	result := admittedDynamoRead{request: request}
	table, ok := config.Tables[request.Collection]
	if !ok {
		return result, errDynamoInput
	}
	var err error
	switch request.Operation {
	case "get", "exists":
		if request.Keys != nil || request.Partition != nil || request.Sort != nil || request.Limit != 0 || request.Descending {
			return result, errDynamoInput
		}
		result.key, err = decodeDynamoKey(request.Key, table)
	case "getMany":
		if request.Key != nil || request.Keys == nil || len(request.Keys) > 1000 || request.Partition != nil || request.Sort != nil || request.Limit != 0 || request.Descending {
			return result, errDynamoInput
		}
		result.keys = make([]dalgo2dynamodb.Item, len(request.Keys))
		for i, key := range request.Keys {
			result.keys[i], err = decodeDynamoKey(key, table)
			if err != nil {
				return result, err
			}
		}
	case "query":
		if request.Key != nil || request.Keys != nil || request.Limit < 1 || request.Limit > 1000 {
			return result, errDynamoInput
		}
		var partition types.AttributeValue
		partition, err = decodeDynamoScalar(request.Partition)
		if err != nil {
			return result, err
		}
		result.query = dalgo2dynamodb.QueryRequest{Collection: request.Collection, Partition: partition, Limit: request.Limit, Descending: request.Descending}
		if request.Sort != nil {
			if table.SortKey == "" {
				return result, errDynamoInput
			}
			count := 1
			switch request.Sort.Operator {
			case "=", "<", "<=", ">", ">=", "begins_with":
			case "between":
				count = 2
			default:
				return result, errDynamoInput
			}
			if len(request.Sort.Values) != count {
				return result, errDynamoInput
			}
			condition := &dalgo2dynamodb.SortCondition{Operator: request.Sort.Operator, Values: make([]types.AttributeValue, count)}
			for i, value := range request.Sort.Values {
				condition.Values[i], err = decodeDynamoScalar(value)
				if err != nil {
					return result, err
				}
			}
			if condition.Operator == "begins_with" {
				switch condition.Values[0].(type) {
				case *types.AttributeValueMemberS, *types.AttributeValueMemberB:
				default:
					return result, errDynamoInput
				}
			}
			if condition.Operator == "between" {
				cmp, ok := dynamoScalarOrder(condition.Values[0], condition.Values[1])
				if !ok || cmp > 0 {
					return result, errDynamoInput
				}
			}
			result.query.Sort = condition
		}
	default:
		return result, errDynamoInput
	}
	return result, err
}

func executeDynamoRead(ctx context.Context, client dynamoReadClient, admitted admittedDynamoRead) (map[string]any, error) {
	r := admitted.request
	output := map[string]any{"operation": r.Operation}
	switch r.Operation {
	case "get":
		item, err := client.Get(ctx, r.Collection, admitted.key)
		if err != nil {
			return nil, err
		}
		wire, err := dynamoWireItem(item)
		if err != nil {
			return nil, err
		}
		output["item"] = wire
	case "exists":
		exists, err := client.Exists(ctx, r.Collection, admitted.key)
		if err != nil {
			return nil, err
		}
		output["exists"] = exists
	case "getMany":
		items, err := client.GetMany(ctx, r.Collection, admitted.keys)
		if err != nil {
			return nil, err
		}
		wire, err := dynamoWireItems(items)
		if err != nil {
			return nil, err
		}
		output["items"] = wire
	case "query":
		page, err := client.QueryPartition(ctx, admitted.query)
		if err != nil {
			return nil, err
		}
		wire, err := dynamoWireItems(page.Items)
		if err != nil {
			return nil, err
		}
		output["items"] = wire
		output["hasMore"] = page.Next != nil
		output["pages"] = page.Pages
		output["evaluated"] = page.Evaluated
	}
	return output, nil
}
func dynamoWireItems(items []dalgo2dynamodb.Item) ([]map[string]any, error) {
	result := make([]map[string]any, len(items))
	for i, item := range items {
		wire, err := dynamoWireItem(item)
		if err != nil {
			return nil, err
		}
		result[i] = wire
	}
	return result, nil
}
