package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	kv "github.com/dal-go/dalgo2cloudflarekv"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const cloudflareKVPolicyNote = "access: native Cloudflare Workers KV REST reads; token/account/namespace and literal prefixes govern access; DataTug application policies are not applied"

type cloudflareKVConfig struct {
	Endpoint             string   `json:"endpoint,omitempty"`
	AccountID            string   `json:"accountId"`
	NamespaceID          string   `json:"namespaceId"`
	CollectionAlias      string   `json:"collectionAlias,omitempty"`
	AllowedPrefixes      []string `json:"allowedPrefixes"`
	AllowNamespaceAccess bool     `json:"allowNamespaceAccess,omitempty"`
	TokenEnv             string   `json:"tokenEnv"`
	CAFile               string   `json:"caFile,omitempty"`
}

func (c cloudflareKVConfig) origin() string {
	if c.Endpoint == "" {
		return "https://api.cloudflare.com"
	}
	return c.Endpoint
}
func (c cloudflareKVConfig) alias() string {
	if c.CollectionAlias == "" {
		return "kv"
	}
	return c.CollectionAlias
}
func (c cloudflareKVConfig) scoped(value string) bool {
	for _, p := range c.AllowedPrefixes {
		if p == "" && !c.AllowNamespaceAccess {
			continue
		}
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	return false
}

type cloudflareKVRequest struct {
	Operation  string   `json:"operation"`
	Collection string   `json:"collection"`
	Key        string   `json:"key,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Prefix     *string  `json:"prefix,omitempty"`
	PageSize   int      `json:"pageSize,omitempty"`
	Pages      int      `json:"pages,omitempty"`
}
type cloudflareKVReadClient interface {
	Get(context.Context, string) (kv.Entry, error)
	Exists(context.Context, string) (bool, error)
	GetMany(context.Context, []string) ([]kv.Entry, error)
	ListKeys(context.Context, kv.ListOptions) (kv.ListPage, error)
	Close() error
}

var _ cloudflareKVReadClient = (*kv.Client)(nil)

func queryCloudflareKVCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "cloudflare-kv --config <native.json> --request <file|->", Short: "Read original keys and list metadata in an existing Cloudflare KV namespace", Long: `Run native get, exists, ordered getMany, metadata-only list or supplied-key testConnection.
Trusted configuration fixes an HTTPS origin, existing account/namespace,
collection alias, literal prefix grants and a named read-token ENV. No token
value belongs in argv/config; there is no anonymous/admin/global-key fallback.

` + cloudflareKVPolicyNote + `.

Output format cloudflare_kv_raw_v1 preserves raw bytes as {kind:bytes,base64},
expiration as {kind:uint64,value:decimalString}, and metadata as
{kind:json,value:ORIGINAL_JSON_TEXT}; wide JSON metadata number lexemes survive
ordinary outer JSON parsing. ALL REST404s remain errors, never false absence.
List traverses whole pages with fixed pageSize10..1000 and pages1..100 under
one deadline; pages*pageSize<=1000. It returns key metadata, not values or a
portable cursor. testConnection reads a supplied authorized key without
enumerating accounts/namespaces. No SQL, writes or snapshot/atomic transaction.
See docs/query/cloudflare-kv.md for examples, budgets and proof boundaries.`, SilenceUsage: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		original := cmd.Context()
		parent := original
		if parent == nil {
			parent = context.Background()
		}
		ctx, _, stop := interruptContext(parent)
		cmd.SetContext(ctx)
		defer func() { cmd.SetContext(original); stop() }()
		return runCloudflareKVCommand(cmd, configPath, requestPath, timeout)
	}}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted existing namespace mapping JSON, without credential values")
	cmd.Flags().StringVar(&requestPath, "request", "", "Native read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1ms through2m), including input/auth/encoding/output")
	return cmd
}
func cloudflareKVCommandError(err error, usage bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		return Exit("Cloudflare KV read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("Cloudflare KV read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("Cloudflare KV configuration or request is invalid or unreadable", exitCodeUsage)
	default:
		return Exit("Cloudflare KV read failed; check trusted namespace, read-token authority and limits", exitCodeDatabase)
	}
}
func runCloudflareKVCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	if configPath == "" || configPath == "-" || requestPath == "" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return cloudflareKVCommandError(errDynamoInput, true)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	raw, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return cloudflareKVCommandError(err, true)
	}
	var config cloudflareKVConfig
	if err = decodeCloudflareKVConfig(raw, &config); err != nil {
		return cloudflareKVCommandError(err, true)
	}
	if requestPath == "-" {
		raw, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		raw, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return cloudflareKVCommandError(err, true)
	}
	var request cloudflareKVRequest
	if err = decodeCloudflareKVRequest(raw, &request); err != nil {
		return cloudflareKVCommandError(err, true)
	}
	if err = admitCloudflareKVRead(ctx, config, &request); err != nil {
		return cloudflareKVCommandError(err, true)
	}
	options, err := cloudflareKVOptions(ctx, config, timeout)
	if err != nil {
		return cloudflareKVCommandError(err, true)
	}
	transport, err := influxTransport(ctx, influxNativeConfig{Endpoint: config.origin(), CAFile: config.CAFile})
	if err != nil {
		return cloudflareKVCommandError(err, true)
	}
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.MaxResponseHeaderBytes = 64 << 10
	transport.TLSHandshakeTimeout = timeout
	transport.ResponseHeaderTimeout = timeout
	client, err := newCloudflareKVNativeClient(options, transport)
	if err != nil {
		transport.CloseIdleConnections()
		return cloudflareKVCommandError(err, true)
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), cloudflareKVPolicyNote)
	return deliverCloudflareKVRead(ctx, cmd.OutOrStdout(), client, transport.CloseIdleConnections, config, request)
}
func cloudflareKVOptions(ctx context.Context, c cloudflareKVConfig, timeout time.Duration) (kv.Options, error) {
	options := kv.Options{Endpoint: strings.TrimSuffix(c.origin(), "/") + "/client/v4", AccountID: c.AccountID, NamespaceID: c.NamespaceID, CollectionAlias: c.alias(), AllowedPrefixes: c.AllowedPrefixes, AllowNamespaceAccess: c.AllowNamespaceAccess, Timeout: timeout, MaxConcurrent: 1, MaxKeys: 100, MaxRows: 1000, MaxPages: 100, MaxRequests: 100, MaxValueBytes: 1 << 20, MaxResponseBytes: 4 << 20, MaxRequestBytes: 64 << 10, MaxWork: 1000, MaxCursors: 1, MaxCursorBytes: 1 << 20, MaxCursorNodes: 4096}
	if !dbcopy.ValidEnvName(c.TokenEnv) {
		return options, errDynamoInput
	}
	options.Token = influxTokenProvider(c.TokenEnv)
	if err := dynamoFresh(ctx); err != nil {
		return options, err
	}
	return options, nil
}
func newCloudflareKVNativeClient(options kv.Options, transport *http.Transport) (*kv.Client, error) {
	options.RoundTripper = transport
	return kv.New(options)
}
func cloudflareKVKey(key string) bool {
	return key != "" && key != "." && key != ".." && len(key) <= 512 && utf8.ValidString(key)
}
func admitCloudflareKVRead(ctx context.Context, c cloudflareKVConfig, r *cloudflareKVRequest) error {
	if r.Collection != c.alias() {
		return errDynamoInput
	}
	switch r.Operation {
	case "get", "exists", "testConnection":
		if !cloudflareKVKey(r.Key) || !c.scoped(r.Key) {
			return errDynamoInput
		}
	case "getMany":
		if r.Keys == nil || len(r.Keys) > 100 {
			return errDynamoInput
		}
		r.Keys = append([]string{}, r.Keys...)
		for _, key := range r.Keys {
			if err := dynamoFresh(ctx); err != nil {
				return err
			}
			if !cloudflareKVKey(key) || !c.scoped(key) {
				return errDynamoInput
			}
		}
	case "list":
		if r.Prefix == nil || len(*r.Prefix) > 512 || !utf8.ValidString(*r.Prefix) || !c.scoped(*r.Prefix) || r.PageSize < 10 || r.PageSize > 1000 || r.Pages < 1 || r.Pages > 100 || r.Pages > 1000/r.PageSize {
			return errDynamoInput
		}
		prefix := *r.Prefix
		r.Prefix = &prefix
	default:
		return errDynamoInput
	}
	return dynamoFresh(ctx)
}
func deliverCloudflareKVRead(ctx context.Context, output io.Writer, client cloudflareKVReadClient, closeTransport func(), config cloudflareKVConfig, request cloudflareKVRequest) error {
	value, err := executeCloudflareKVRead(ctx, client, config, request)
	closeErr := client.Close()
	closeTransport()
	if final := dynamoFresh(ctx); final != nil {
		err = final
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return cloudflareKVCommandError(err, false)
	}
	err = writeDynamoResult(ctx, output, value)
	if errors.Is(err, errDynamoResponse) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return cloudflareKVCommandError(err, false)
	}
	return err
}
func executeCloudflareKVRead(ctx context.Context, client cloudflareKVReadClient, config cloudflareKVConfig, r cloudflareKVRequest) (map[string]any, error) {
	result := map[string]any{"operation": r.Operation, "format": "cloudflare_kv_raw_v1", "collection": r.Collection}
	budget := cloudflareKVWireBudget{}
	switch r.Operation {
	case "get":
		entry, err := client.Get(ctx, r.Key)
		if err != nil {
			return nil, err
		}
		if entry.Key != r.Key {
			return nil, errDynamoResponse
		}
		wire, err := budget.entry(ctx, config, entry)
		if err != nil {
			return nil, err
		}
		result["entry"] = wire
	case "exists", "testConnection":
		exists, err := client.Exists(ctx, r.Key)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errDynamoResponse
		}
		result["exists"] = true
		if r.Operation == "testConnection" {
			result["requestCompleted"] = true
		}
	case "getMany":
		entries := []kv.Entry{}
		if len(r.Keys) > 0 {
			var err error
			entries, err = client.GetMany(ctx, r.Keys)
			if err != nil {
				return nil, err
			}
			if len(entries) != len(r.Keys) {
				return nil, errDynamoResponse
			}
		}
		wire := make([]cloudflareKVWireEntry, len(entries))
		for i, entry := range entries {
			if entry.Key != r.Keys[i] {
				return nil, errDynamoResponse
			}
			value, err := budget.entry(ctx, config, entry)
			if err != nil {
				return nil, err
			}
			wire[i] = value
		}
		result["entries"] = wire
	case "list":
		keys := []cloudflareKVWireKey{}
		cursor, last := "", ""
		seen := map[string]bool{}
		for i := 0; i < r.Pages; i++ {
			if err := dynamoFresh(ctx); err != nil {
				return nil, err
			}
			page, err := client.ListKeys(ctx, kv.ListOptions{Prefix: *r.Prefix, Limit: r.PageSize, Cursor: cursor})
			if err != nil {
				return nil, err
			}
			if len(page.Keys) > r.PageSize || len(keys) > 1000-len(page.Keys) || page.Complete && page.NextCursor != "" || !page.Complete && (page.NextCursor == "" || seen[page.NextCursor]) {
				return nil, errDynamoResponse
			}
			for _, key := range page.Keys {
				if !strings.HasPrefix(key.Name, *r.Prefix) || last != "" && key.Name <= last {
					return nil, errDynamoResponse
				}
				wire, err := budget.key(ctx, config, key)
				if err != nil {
					return nil, err
				}
				keys = append(keys, wire)
				last = key.Name
			}
			result["pages"] = i + 1
			result["hasMore"] = !page.Complete
			if page.Complete {
				break
			}
			cursor = page.NextCursor
			seen[cursor] = true
		}
		result["keys"] = keys
	}
	return result, nil
}
