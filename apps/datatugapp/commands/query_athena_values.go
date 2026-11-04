package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	athenasdk "github.com/dal-go/dalgo2athena"
)

var errAthenaOutput = errors.New("athena response could not be encoded safely")

const (
	athenaMaxWireBytes   = 8 << 20
	athenaMaxWireNodes   = 65536
	athenaMaxWireRows    = 1000
	athenaMaxWireText    = 4 << 20
	athenaMaxWireCounter = int64(1<<53 - 1)
)

type athenaWireInt64 struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type athenaWireColumn struct {
	Name          string           `json:"name"`
	Type          string           `json:"type"`
	Label         string           `json:"label"`
	Nullable      string           `json:"nullable"`
	CatalogName   string           `json:"catalogName"`
	SchemaName    string           `json:"schemaName"`
	TableName     string           `json:"tableName"`
	Precision     *athenaWireInt64 `json:"precision,omitempty"`
	Scale         *athenaWireInt64 `json:"scale,omitempty"`
	CaseSensitive *bool            `json:"caseSensitive,omitempty"`
}
type athenaWireReceipt struct {
	QueryID            string `json:"queryId"`
	SourceDigest       string `json:"sourceDigest"`
	PlanDigest         string `json:"planDigest"`
	PrincipalDigest    string `json:"principalDigest"`
	ScopeDigest        string `json:"scopeDigest"`
	State              string `json:"state"`
	CancelOutcome      string `json:"cancelOutcome"`
	ExpiresAt          string `json:"expiresAt"`
	Requests           int    `json:"requests"`
	PagesFetched       int    `json:"pagesFetched"`
	RowsFetched        int    `json:"rowsFetched"`
	RowsAcknowledged   int    `json:"rowsAcknowledged"`
	ResponseBytes      int    `json:"responseBytes"`
	Nodes              int    `json:"nodes"`
	Deliveries         int    `json:"deliveries"`
	DataScannedInBytes string `json:"dataScannedInBytes"`
	Complete           bool   `json:"complete"`
}
type athenaWirePage struct {
	Operation    string             `json:"operation"`
	Format       string             `json:"format"`
	Columns      []athenaWireColumn `json:"columns"`
	Cells        [][]*string        `json:"cells"`
	OrdinalStart int                `json:"ordinalStart"`
	HasMore      bool               `json:"hasMore"`
	Receipt      athenaWireReceipt  `json:"receipt"`
}

// A budget is passed by value. Helpers return the advanced candidate only after
// a successful charge, so callers keep the original on any error.
type athenaWireBudget struct {
	bytes, nodes, rows int
}

func (b athenaWireBudget) valid() bool {
	return b.bytes >= 0 && b.bytes <= athenaMaxWireBytes &&
		b.nodes >= 0 && b.nodes <= athenaMaxWireNodes &&
		b.rows >= 0 && b.rows <= athenaMaxWireRows
}
func (b athenaWireBudget) add(nodes, bytes int) (athenaWireBudget, error) {
	if !b.valid() || nodes < 0 || bytes < 0 ||
		nodes > athenaMaxWireNodes-b.nodes || bytes > athenaMaxWireBytes-b.bytes {
		return b, errAthenaOutput
	}
	b.nodes += nodes
	b.bytes += bytes
	return b, nil
}
func (b athenaWireBudget) charge(ctx context.Context, nodes, bytes int) (athenaWireBudget, error) {
	if err := athenaFresh(ctx); err != nil {
		return b, err
	}
	return b.add(nodes, bytes)
}
func (b athenaWireBudget) text(ctx context.Context, value string) (athenaWireBudget, error) {
	if err := athenaFresh(ctx); err != nil {
		return b, err
	}
	if len(value) > athenaMaxWireText || !utf8.ValidString(value) {
		return b, errAthenaOutput
	}
	return b.add(1, athenaWireTextBytes(value))
}
func (b athenaWireBudget) object(ctx context.Context, names ...string) (athenaWireBudget, error) {
	size := 2
	if len(names) > 0 {
		size += len(names) - 1
	}
	for _, name := range names {
		size += athenaWireTextBytes(name) + 1
	}
	return b.charge(ctx, 1+len(names), size)
}
func (b athenaWireBudget) array(ctx context.Context, length int) (athenaWireBudget, error) {
	size := 2
	if length > 0 {
		size += length - 1
	}
	return b.charge(ctx, 1, size)
}
func (b athenaWireBudget) boolean(ctx context.Context, value bool) (athenaWireBudget, error) {
	if value {
		return b.charge(ctx, 1, 4)
	}
	return b.charge(ctx, 1, 5)
}
func (b athenaWireBudget) integer(ctx context.Context, value int) (athenaWireBudget, error) {
	return b.charge(ctx, 1, len(strconv.Itoa(value)))
}
func (b athenaWireBudget) descriptor(ctx context.Context, value *int64) (athenaWireBudget, *athenaWireInt64, error) {
	if value == nil {
		return b, nil, nil
	}
	text := strconv.FormatInt(*value, 10)
	b, err := b.object(ctx, "kind", "value")
	if err != nil {
		return b, nil, err
	}
	if b, err = b.text(ctx, "int64"); err != nil {
		return b, nil, err
	}
	if b, err = b.text(ctx, text); err != nil {
		return b, nil, err
	}
	return b, &athenaWireInt64{Kind: "int64", Value: text}, nil
}

// athenaWireTextBytes is the exact default encoding/json string size,
// including HTML escaping of <, >, & and U+2028/U+2029. It does not allocate.
func athenaWireTextBytes(value string) int {
	size := 2 + len(value)
	for i := 0; i < len(value); {
		c := value[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				size++
			case '<', '>', '&':
				size += 5
			default:
				if c < 0x20 {
					size += 5
				}
			}
			i++
			continue
		}
		r, width := utf8.DecodeRuneInString(value[i:])
		if r == '\u2028' || r == '\u2029' {
			size += 3
		}
		i += width
	}
	return size
}

func athenaWireRequiredText(value string) bool {
	return value != "" && len(value) <= athenaMaxNameBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
func athenaWireMetadataText(value string) bool {
	return len(value) <= athenaMaxNameBytes && utf8.ValidString(value)
}

func athenaWireColumns(ctx context.Context, b athenaWireBudget, columns []athenasdk.Column) (athenaWireBudget, []athenaWireColumn, error) {
	b, err := b.array(ctx, len(columns))
	if err != nil {
		return b, nil, err
	}
	out := make([]athenaWireColumn, len(columns))
	for i, column := range columns {
		name, typ := column.Name, column.Type
		label, nullable := column.Label, column.Nullable
		catalog, schema, table := column.CatalogName, column.SchemaName, column.TableName
		if !athenaWireRequiredText(name) || !athenaWireRequiredText(typ) ||
			!athenaWireMetadataText(label) || !athenaWireMetadataText(nullable) ||
			!athenaWireMetadataText(catalog) || !athenaWireMetadataText(schema) ||
			!athenaWireMetadataText(table) {
			return b, nil, errAthenaOutput
		}
		var precision, scale *int64
		if column.Precision != nil {
			value := *column.Precision
			precision = &value
		}
		if column.Scale != nil {
			value := *column.Scale
			scale = &value
		}
		var caseSensitive *bool
		if column.CaseSensitive != nil {
			value := *column.CaseSensitive
			caseSensitive = &value
		}
		names := []string{"name", "type", "label", "nullable", "catalogName", "schemaName", "tableName"}
		if precision != nil {
			names = append(names, "precision")
		}
		if scale != nil {
			names = append(names, "scale")
		}
		if caseSensitive != nil {
			names = append(names, "caseSensitive")
		}
		if b, err = b.object(ctx, names...); err != nil {
			return b, nil, err
		}
		for _, value := range []string{name, typ, label, nullable, catalog, schema, table} {
			if b, err = b.text(ctx, value); err != nil {
				return b, nil, err
			}
		}
		var wirePrecision, wireScale *athenaWireInt64
		if precision != nil {
			if b, wirePrecision, err = b.descriptor(ctx, precision); err != nil {
				return b, nil, err
			}
		}
		if scale != nil {
			if b, wireScale, err = b.descriptor(ctx, scale); err != nil {
				return b, nil, err
			}
		}
		if caseSensitive != nil {
			if b, err = b.boolean(ctx, *caseSensitive); err != nil {
				return b, nil, err
			}
		}
		out[i] = athenaWireColumn{
			Name: name, Type: typ, Label: label, Nullable: nullable,
			CatalogName: catalog, SchemaName: schema, TableName: table,
			Precision: wirePrecision, Scale: wireScale, CaseSensitive: caseSensitive,
		}
	}
	return b, out, nil
}

func athenaWireCells(ctx context.Context, b athenaWireBudget, cells [][]*string, width int) (athenaWireBudget, [][]*string, error) {
	if err := athenaFresh(ctx); err != nil {
		return b, nil, err
	}
	if !b.valid() || len(cells) > athenaMaxWireRows-b.rows {
		return b, nil, errAthenaOutput
	}
	for _, row := range cells {
		if len(row) != width {
			return b, nil, errAthenaOutput
		}
	}
	b, err := b.array(ctx, len(cells))
	if err != nil {
		return b, nil, err
	}
	out := make([][]*string, len(cells))
	for i, row := range cells {
		b.rows++
		if b, err = b.array(ctx, len(row)); err != nil {
			return b, nil, err
		}
		copied := make([]*string, len(row))
		for j, cell := range row {
			if cell == nil {
				if b, err = b.charge(ctx, 1, 4); err != nil {
					return b, nil, err
				}
				continue
			}
			value := *cell
			if b, err = b.text(ctx, value); err != nil {
				return b, nil, err
			}
			copied[j] = &value
		}
		out[i] = copied
	}
	return b, out, nil
}

func athenaWireReceiptOf(ctx context.Context, b athenaWireBudget, receipt athenasdk.Receipt) (athenaWireBudget, athenaWireReceipt, error) {
	queryID := receipt.QueryID
	sourceDigest, planDigest := receipt.SourceDigest, receipt.PlanDigest
	principalDigest, scopeDigest := receipt.PrincipalDigest, receipt.ScopeDigest
	state, cancelOutcome := receipt.State, receipt.CancelOutcome
	expiresAt := receipt.ExpiresAt.UTC().Format(time.RFC3339Nano)
	requests, pagesFetched := receipt.Requests, receipt.PagesFetched
	rowsFetched, rowsAcknowledged := receipt.RowsFetched, receipt.RowsAcknowledged
	responseBytes, nodes := receipt.ResponseBytes, receipt.Nodes
	deliveries, dataScanned, complete := receipt.Deliveries, receipt.DataScannedInBytes, receipt.Complete
	for _, value := range []string{queryID, sourceDigest, planDigest, principalDigest, scopeDigest, state, cancelOutcome, expiresAt, dataScanned} {
		if len(value) > athenaMaxWireText || !utf8.ValidString(value) {
			return b, athenaWireReceipt{}, errAthenaOutput
		}
	}
	for _, value := range []int{requests, pagesFetched, rowsFetched, rowsAcknowledged, responseBytes, nodes, deliveries} {
		if value < 0 || int64(value) > athenaMaxWireCounter {
			return b, athenaWireReceipt{}, errAthenaOutput
		}
	}
	names := []string{
		"queryId", "sourceDigest", "planDigest", "principalDigest", "scopeDigest", "state", "cancelOutcome", "expiresAt",
		"requests", "pagesFetched", "rowsFetched", "rowsAcknowledged", "responseBytes", "nodes", "deliveries",
		"dataScannedInBytes", "complete",
	}
	b, err := b.object(ctx, names...)
	if err != nil {
		return b, athenaWireReceipt{}, err
	}
	for _, value := range []string{queryID, sourceDigest, planDigest, principalDigest, scopeDigest, state, cancelOutcome, expiresAt} {
		if b, err = b.text(ctx, value); err != nil {
			return b, athenaWireReceipt{}, err
		}
	}
	for _, value := range []int{requests, pagesFetched, rowsFetched, rowsAcknowledged, responseBytes, nodes, deliveries} {
		if b, err = b.integer(ctx, value); err != nil {
			return b, athenaWireReceipt{}, err
		}
	}
	if b, err = b.text(ctx, dataScanned); err != nil {
		return b, athenaWireReceipt{}, err
	}
	if b, err = b.boolean(ctx, complete); err != nil {
		return b, athenaWireReceipt{}, err
	}
	return b, athenaWireReceipt{
		QueryID: queryID, SourceDigest: sourceDigest, PlanDigest: planDigest,
		PrincipalDigest: principalDigest, ScopeDigest: scopeDigest, State: state,
		CancelOutcome: cancelOutcome, ExpiresAt: expiresAt,
		Requests: requests, PagesFetched: pagesFetched, RowsFetched: rowsFetched,
		RowsAcknowledged: rowsAcknowledged, ResponseBytes: responseBytes, Nodes: nodes,
		Deliveries: deliveries, DataScannedInBytes: dataScanned, Complete: complete,
	}, nil
}

// athenaPageFrame prepares one complete JSONL frame before acknowledgement. It
// never reads or encodes NextCursor and never writes to a writer.
func athenaPageFrame(ctx context.Context, page athenasdk.NativePage, used athenaWireBudget) (frame []byte, next athenaWireBudget, err error) {
	if ctx == nil {
		return nil, used, errAthenaOutput
	}
	next = used
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			frame = nil
			next = used
			err = ctxErr
			return
		}
		if err != nil {
			frame = nil
			next = used
		}
	}()
	columns, cells := page.Columns, page.Cells
	ordinalStart, hasMore := page.OrdinalStart, page.HasMore
	receipt := page.Receipt
	if err = athenaFresh(ctx); err != nil {
		return nil, used, err
	}
	if !used.valid() || len(columns) > athenaMaxColumns || len(columns) == 0 || ordinalStart < 0 || ordinalStart > athenaMaxWireRows {
		return nil, used, errAthenaOutput
	}
	candidate := used
	if candidate, err = candidate.object(ctx,
		"operation", "format", "columns", "cells", "ordinalStart", "hasMore", "receipt"); err != nil {
		return nil, used, err
	}
	if candidate, err = candidate.text(ctx, "query"); err != nil {
		return nil, used, err
	}
	if candidate, err = candidate.text(ctx, "athena_string_null_v1"); err != nil {
		return nil, used, err
	}
	var wireColumns []athenaWireColumn
	if candidate, wireColumns, err = athenaWireColumns(ctx, candidate, columns); err != nil {
		return nil, used, err
	}
	var wireCells [][]*string
	if candidate, wireCells, err = athenaWireCells(ctx, candidate, cells, len(columns)); err != nil {
		return nil, used, err
	}
	if candidate, err = candidate.integer(ctx, ordinalStart); err != nil {
		return nil, used, err
	}
	if candidate, err = candidate.boolean(ctx, hasMore); err != nil {
		return nil, used, err
	}
	var wireReceipt athenaWireReceipt
	if candidate, wireReceipt, err = athenaWireReceiptOf(ctx, candidate, receipt); err != nil {
		return nil, used, err
	}
	if candidate, err = candidate.charge(ctx, 0, 1); err != nil {
		return nil, used, err
	}
	wire := athenaWirePage{
		Operation: "query", Format: "athena_string_null_v1",
		Columns: wireColumns, Cells: wireCells, OrdinalStart: ordinalStart,
		HasMore: hasMore, Receipt: wireReceipt,
	}
	// athenaWirePage is closed and holds only primitives, so default
	// encoding/json cannot fail. Every validated text string is valid UTF-8.
	frame, _ = json.Marshal(wire)
	frame = append(frame, '\n')
	return frame, candidate, nil
}
