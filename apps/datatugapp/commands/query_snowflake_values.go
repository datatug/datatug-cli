package commands

import (
	"context"
	"strconv"
	"unicode/utf8"

	snowsdk "github.com/dal-go/dalgo2snowflake"
)

type snowflakeWireInt64 struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type snowflakeWireColumn struct {
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Precision  *snowflakeWireInt64 `json:"precision,omitempty"`
	Scale      *snowflakeWireInt64 `json:"scale,omitempty"`
	Length     *snowflakeWireInt64 `json:"length,omitempty"`
	ByteLength *snowflakeWireInt64 `json:"byteLength,omitempty"`
	Nullable   *bool               `json:"nullable,omitempty"`
}
type snowflakeWireDocument struct {
	ID      string                `json:"id"`
	Data    map[string]any        `json:"data"`
	Columns []snowflakeWireColumn `json:"columns"`
}

// The first accounting failure is sticky. Callers stop variable loops before
// allocation and report that failure once; cancellation is freshly checked at
// provider admission and final delivery as well as every successful charge.
type snowflakeWireBudget struct {
	nodes, bytes, rows int
	err                error
}

func (b *snowflakeWireBudget) charge(ctx context.Context, nodes, bytes int) {
	if b.err != nil {
		return
	}
	if err := dynamoFresh(ctx); err != nil {
		b.err = err
		return
	}
	if nodes > 65536-b.nodes || bytes > (8<<20)-b.bytes {
		b.err = errDynamoResponse
		return
	}
	b.nodes += nodes
	b.bytes += bytes
}
func (b *snowflakeWireBudget) text(ctx context.Context, value string) {
	if b.err != nil {
		return
	}
	if len(value) > 4<<20 || !utf8.ValidString(value) {
		b.err = errDynamoResponse
		return
	}
	b.charge(ctx, 1, 2+6*len(value))
}
func (b *snowflakeWireBudget) key(ctx context.Context, name string) {
	if b.err != nil {
		return
	}
	// Quoted/HTML-escaped key, colon and conservative comma.
	if len(name) > 4096 || !utf8.ValidString(name) {
		b.err = errDynamoResponse
		return
	}
	b.charge(ctx, 1, 4+6*len(name))
}
func (b *snowflakeWireBudget) object(ctx context.Context, names ...string) {
	b.charge(ctx, 1, 2)
	for _, name := range names {
		b.key(ctx, name)
		if b.err != nil {
			return
		}
	}
}
func (b *snowflakeWireBudget) array(ctx context.Context, length int) {
	if b.err != nil {
		return
	}
	if length > 65536-b.nodes {
		b.err = errDynamoResponse
		return
	}
	b.charge(ctx, 1, 2+length)
}
func (b *snowflakeWireBudget) literal(ctx context.Context, bytes int) { b.charge(ctx, 1, bytes) }
func (b *snowflakeWireBudget) descriptor(ctx context.Context, value *int64) *snowflakeWireInt64 {
	if value == nil || b.err != nil {
		return nil
	}
	text := strconv.FormatInt(*value, 10)
	b.object(ctx, "kind", "value")
	b.text(ctx, "int64")
	b.text(ctx, text)
	if b.err != nil {
		return nil
	}
	return &snowflakeWireInt64{Kind: "int64", Value: text}
}
func (b *snowflakeWireBudget) columns(ctx context.Context, columns []snowsdk.Column) ([]snowflakeWireColumn, error) {
	if len(columns) == 0 || len(columns) > 128 {
		return nil, errDynamoResponse
	}
	b.array(ctx, len(columns))
	if b.err != nil {
		return nil, b.err
	}
	out := make([]snowflakeWireColumn, len(columns))
	seen := make(map[string]bool, len(columns))
	for i, column := range columns {
		if !snowflakeName(column.Name) || !snowflakeName(column.Type) || seen[column.Name] {
			return nil, errDynamoResponse
		}
		seen[column.Name] = true
		b.object(ctx, "name", "type")
		b.text(ctx, column.Name)
		b.text(ctx, column.Type)
		wire := snowflakeWireColumn{Name: column.Name, Type: column.Type}
		for _, item := range []struct {
			name   string
			source *int64
			target **snowflakeWireInt64
		}{{"precision", column.Precision, &wire.Precision}, {"scale", column.Scale, &wire.Scale}, {"length", column.Length, &wire.Length}, {"byteLength", column.ByteLength, &wire.ByteLength}} {
			if b.err != nil {
				break
			}
			if item.source != nil {
				b.key(ctx, item.name)
				*item.target = b.descriptor(ctx, item.source)
			}
		}
		if column.Nullable != nil && b.err == nil {
			b.key(ctx, "nullable")
			b.literal(ctx, 5)
			value := *column.Nullable
			wire.Nullable = &value
		}
		if b.err != nil {
			return nil, b.err
		}
		out[i] = wire
	}
	return out, nil
}
func (b *snowflakeWireBudget) admitRow(ctx context.Context, row snowsdk.Row, columns []snowsdk.Column) error {
	if b.rows >= 1000 || len(row) != len(columns) {
		return errDynamoResponse
	}
	b.rows++
	b.object(ctx)
	for _, column := range columns {
		if b.err != nil {
			break
		}
		value, present := row[column.Name]
		if !present {
			return errDynamoResponse
		}
		b.key(ctx, column.Name)
		if value == nil {
			b.literal(ctx, 4)
		} else {
			b.text(ctx, *value)
		}
	}
	return b.err
}
func (b *snowflakeWireBudget) document(ctx context.Context, document *snowsdk.Document, idColumn, expectedID string) (any, error) {
	if document == nil {
		if b.rows >= 1000 {
			return nil, errDynamoResponse
		}
		b.rows++
		b.literal(ctx, 4)
		return nil, b.err
	}
	if document.ID != expectedID || !snowflakeID(document.ID) {
		return nil, errDynamoResponse
	}
	id, present := document.Row[idColumn]
	if !present || id == nil || *id != document.ID {
		return nil, errDynamoResponse
	}
	b.object(ctx, "id", "data", "columns")
	b.text(ctx, document.ID)
	columns, err := b.columns(ctx, document.Columns)
	if err != nil {
		return nil, err
	}
	if err = b.admitRow(ctx, document.Row, document.Columns); err != nil {
		return nil, err
	}
	data := make(map[string]any, len(document.Row))
	for name, value := range document.Row {
		if value == nil {
			data[name] = nil
		} else {
			data[name] = *value
		}
	}
	return snowflakeWireDocument{ID: document.ID, Data: data, Columns: columns}, nil
}

// One shared fresh final check covers successful conversion and real caller
// cancellation at callback boundaries without duplicating constant-size guards.
func snowflakeWireResult(ctx context.Context, result map[string]any) (map[string]any, error) {
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
func snowflakeMetadataResult(ctx context.Context, c snowflakeNativeConfig) (map[string]any, error) {
	b := snowflakeWireBudget{}
	b.object(ctx, "operation", "format", "metadata")
	b.text(ctx, "metadata")
	b.text(ctx, snowflakeWireFormat)
	b.object(ctx, "database", "schema", "tables", "provenance", "dynamicDiscovery")
	for _, value := range []string{c.Database, c.Schema, "trusted configuration"} {
		b.text(ctx, value)
	}
	b.literal(ctx, 5)
	if len(c.Tables) > 128 {
		return nil, errDynamoResponse
	}
	b.object(ctx)
	for alias, table := range c.Tables {
		if b.err != nil {
			break
		}
		b.key(ctx, alias)
		b.object(ctx, "name", "idColumn")
		b.text(ctx, table.Name)
		b.text(ctx, table.IDColumn)
	}
	for key, value := range map[string]string{"warehouse": c.Warehouse, "role": c.Role} {
		if b.err != nil {
			break
		}
		if value != "" {
			b.key(ctx, key)
			b.text(ctx, value)
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	tables := make(map[string]any, len(c.Tables))
	for alias, table := range c.Tables {
		tables[alias] = map[string]any{"name": table.Name, "idColumn": table.IDColumn}
	}
	metadata := map[string]any{"database": c.Database, "schema": c.Schema, "tables": tables, "provenance": "trusted configuration", "dynamicDiscovery": false}
	if c.Warehouse != "" {
		metadata["warehouse"] = c.Warehouse
	}
	if c.Role != "" {
		metadata["role"] = c.Role
	}
	return snowflakeWireResult(ctx, map[string]any{"operation": "metadata", "format": snowflakeWireFormat, "metadata": metadata})
}
