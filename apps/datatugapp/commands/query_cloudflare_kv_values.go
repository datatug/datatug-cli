package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"

	kv "github.com/dal-go/dalgo2cloudflarekv"
)

func decodeCloudflareKVConfig(raw []byte, c *cloudflareKVConfig) error {
	var fields map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &fields); err != nil {
		return err
	}
	if err := convexControlFields(fields, "endpoint", "accountId", "namespaceId", "collectionAlias", "allowedPrefixes", "allowNamespaceAccess", "tokenEnv", "caFile"); err != nil {
		return err
	}
	return decodeDynamoDocument(raw, c)
}
func decodeCloudflareKVRequest(raw []byte, r *cloudflareKVRequest) error {
	var fields map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &fields); err != nil {
		return err
	}
	if err := decodeDynamoDocument(raw, r); err != nil {
		return err
	}
	var names []string
	switch r.Operation {
	case "get", "exists", "testConnection":
		names = []string{"operation", "collection", "key"}
	case "getMany":
		names = []string{"operation", "collection", "keys"}
	case "list":
		names = []string{"operation", "collection", "prefix", "pageSize", "pages"}
	default:
		return errDynamoInput
	}
	return convexControlFields(fields, names...)
}

type cloudflareKVWireBytes struct {
	Kind   string `json:"kind"`
	Base64 string `json:"base64"`
}
type cloudflareKVWireUint struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type cloudflareKVWireJSON struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type cloudflareKVWireEntry struct {
	Key        string                `json:"key"`
	Value      cloudflareKVWireBytes `json:"value"`
	Expiration *cloudflareKVWireUint `json:"expiration,omitempty"`
}
type cloudflareKVWireKey struct {
	Name       string                `json:"name"`
	Metadata   *cloudflareKVWireJSON `json:"metadata,omitempty"`
	Expiration *cloudflareKVWireUint `json:"expiration,omitempty"`
}
type cloudflareKVWireBudget struct{ bytes, raw, rows int }

func (b *cloudflareKVWireBudget) charge(encoded, raw int) error {
	if b.rows >= 1000 || encoded > (8<<20)-b.bytes || raw > (4<<20)-b.raw {
		return errDynamoResponse
	}
	b.rows++
	b.bytes += encoded
	b.raw += raw
	return nil
}
func cloudflareKVExpiry(expiration *uint64) *cloudflareKVWireUint {
	if expiration == nil {
		return nil
	}
	return &cloudflareKVWireUint{Kind: "uint64", Value: strconv.FormatUint(*expiration, 10)}
}
func (b *cloudflareKVWireBudget) entry(ctx context.Context, c cloudflareKVConfig, entry kv.Entry) (cloudflareKVWireEntry, error) {
	var zero cloudflareKVWireEntry
	if err := dynamoFresh(ctx); err != nil {
		return zero, err
	}
	if !cloudflareKVKey(entry.Key) || !c.scoped(entry.Key) || entry.Value == nil || len(entry.Value) > 1<<20 {
		return zero, errDynamoResponse
	}
	encoded := base64.StdEncoding.EncodedLen(len(entry.Value)) + 6*len(entry.Key) + 256
	if err := b.charge(encoded, len(entry.Value)); err != nil {
		return zero, err
	}
	wire := cloudflareKVWireEntry{Key: entry.Key, Value: cloudflareKVWireBytes{Kind: "bytes", Base64: base64.StdEncoding.EncodeToString(entry.Value)}, Expiration: cloudflareKVExpiry(entry.Expiration)}
	if err := dynamoFresh(ctx); err != nil {
		return zero, err
	}
	return wire, nil
}
func (b *cloudflareKVWireBudget) key(ctx context.Context, c cloudflareKVConfig, key kv.KeyInfo) (cloudflareKVWireKey, error) {
	var zero cloudflareKVWireKey
	if err := dynamoFresh(ctx); err != nil {
		return zero, err
	}
	if !cloudflareKVKey(key.Name) || !c.scoped(key.Name) || len(key.Metadata) > 1024 {
		return zero, errDynamoResponse
	}
	encoded := 6*len(key.Name) + 6*len(key.Metadata) + 256
	if err := b.charge(encoded, len(key.Name)+len(key.Metadata)); err != nil {
		return zero, err
	}
	wire := cloudflareKVWireKey{Name: key.Name, Expiration: cloudflareKVExpiry(key.Expiration)}
	if key.Metadata != nil {
		wrapped := make([]byte, 0, len(key.Metadata)+16)
		wrapped = append(wrapped, `{"metadata":`...)
		wrapped = append(wrapped, key.Metadata...)
		wrapped = append(wrapped, '}')
		var object map[string]json.RawMessage
		if err := decodeDynamoDocument(wrapped, &object); err != nil {
			return zero, errDynamoResponse
		}
		wire.Metadata = &cloudflareKVWireJSON{Kind: "json", Value: string(key.Metadata)}
	}
	if err := dynamoFresh(ctx); err != nil {
		return zero, err
	}
	return wire, nil
}
