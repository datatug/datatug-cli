package commands

import (
	"context"
	"encoding/json"
	"sort"

	athenasdk "github.com/dal-go/dalgo2athena"
)

const (
	athenaPointFormat        = "athena_string_null_v1"
	athenaMetadataProvenance = "trusted configuration, not live catalog/IAM or spend proof"
)

// Closed primitive DTOs: no SDK Document/Metadata embed, no raw SourceOptions,
// no custom marshaler seam.
type athenaWireDocument struct {
	ID      string             `json:"id"`
	Data    map[string]*string `json:"data"`
	Columns []athenaWireColumn `json:"columns"`
}

type athenaWireGetEnvelope struct {
	Operation  string              `json:"operation"`
	Format     string              `json:"format"`
	Collection string              `json:"collection"`
	Document   *athenaWireDocument `json:"document"`
}

type athenaWireExistsEnvelope struct {
	Operation        string `json:"operation"`
	Format           string `json:"format"`
	Collection       string `json:"collection"`
	ID               string `json:"id"`
	Exists           bool   `json:"exists"`
	RequestCompleted *bool  `json:"requestCompleted,omitempty"`
}

type athenaWireGetManyEnvelope struct {
	Operation  string                `json:"operation"`
	Format     string                `json:"format"`
	Collection string                `json:"collection"`
	Documents  []*athenaWireDocument `json:"documents"`
}

type athenaWireMetadataColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type athenaWireMetadataTable struct {
	Alias    string                     `json:"alias"`
	Name     string                     `json:"name"`
	IDColumn string                     `json:"idColumn"`
	Columns  []athenaWireMetadataColumn `json:"columns"`
}

type athenaWireMetadataEnvelope struct {
	Operation        string                    `json:"operation"`
	Format           string                    `json:"format"`
	Tables           []athenaWireMetadataTable `json:"tables"`
	Provenance       string                    `json:"provenance"`
	DynamicDiscovery bool                      `json:"dynamicDiscovery"`
}

func athenaWireBuild(ctx context.Context, used athenaWireBudget, build func(athenaWireBudget) ([]byte, athenaWireBudget, error)) (frame []byte, next athenaWireBudget, err error) {
	if ctx == nil {
		return nil, used, errAthenaOutput
	}
	next = used
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			frame, next, err = nil, used, ctxErr
			return
		}
		if err != nil {
			frame, next = nil, used
		}
	}()
	return build(used)
}

func athenaWireAdmitRecords(ctx context.Context, b athenaWireBudget, records int) (athenaWireBudget, error) {
	if err := athenaFresh(ctx); err != nil {
		return b, err
	}
	if !b.valid() || records < 0 || records > athenaMaxWireRows-b.rows {
		return b, errAthenaOutput
	}
	b.rows += records
	return b, nil
}

func athenaWireHeader(ctx context.Context, b athenaWireBudget, names []string, operation, collection string, extra ...string) (athenaWireBudget, error) {
	var err error
	if b, err = b.object(ctx, names...); err != nil {
		return b, err
	}
	for _, value := range []string{operation, athenaPointFormat, collection} {
		if b, err = b.text(ctx, value); err != nil {
			return b, err
		}
	}
	for _, value := range extra {
		if b, err = b.text(ctx, value); err != nil {
			return b, err
		}
	}
	return b, nil
}

func athenaWireFinish(ctx context.Context, b athenaWireBudget, value interface{}) (frame []byte, next athenaWireBudget, err error) {
	if b, err = b.charge(ctx, 0, 1); err != nil {
		return nil, b, err
	}
	frame, _ = json.Marshal(value)
	return append(frame, '\n'), b, nil
}

// athenaWireDocumentOf validates a returned document against the admitted
// request, charges the full document and only then copies schema and values.
// nil data means SQL NULL; a missing key or an extra key is invalid.
func athenaWireDocumentOf(ctx context.Context, b athenaWireBudget, document *athenasdk.Document, requestedID, idColumn string) (athenaWireBudget, *athenaWireDocument, error) {
	if err := athenaFresh(ctx); err != nil {
		return b, nil, err
	}
	if document == nil || document.ID != requestedID || len(document.Columns) == 0 ||
		len(document.Columns) > athenaMaxColumns || !athenaInputID(requestedID) ||
		!athenaWireRequiredText(idColumn) {
		return b, nil, errAthenaOutput
	}
	names := make([]string, len(document.Columns))
	seen := make(map[string]bool, len(document.Columns))
	for i, column := range document.Columns {
		if !athenaWireRequiredText(column.Name) || seen[column.Name] {
			return b, nil, errAthenaOutput
		}
		seen[column.Name] = true
		names[i] = column.Name
	}
	if !seen[idColumn] || document.Data == nil || len(document.Data) != len(names) {
		return b, nil, errAthenaOutput
	}
	for _, name := range names {
		if _, ok := document.Data[name]; !ok {
			return b, nil, errAthenaOutput
		}
	}
	if value, ok := document.Data[idColumn]; !ok || value == nil || *value != requestedID {
		return b, nil, errAthenaOutput
	}
	candidate := b
	var err error
	if candidate, err = candidate.object(ctx, "id", "data", "columns"); err != nil {
		return b, nil, err
	}
	if candidate, err = candidate.text(ctx, document.ID); err != nil {
		return b, nil, err
	}
	if candidate, err = candidate.object(ctx, names...); err != nil {
		return b, nil, err
	}
	for _, name := range names {
		value := document.Data[name]
		if value == nil {
			if candidate, err = candidate.charge(ctx, 1, 4); err != nil {
				return b, nil, err
			}
		} else if candidate, err = candidate.text(ctx, *value); err != nil {
			return b, nil, err
		}
	}
	var columns []athenaWireColumn
	if candidate, columns, err = athenaWireColumns(ctx, candidate, document.Columns); err != nil {
		return b, nil, err
	}
	data := make(map[string]*string, len(names))
	for _, name := range names {
		if value := document.Data[name]; value != nil {
			copied := *value
			data[name] = &copied
		} else {
			data[name] = nil
		}
	}
	return candidate, &athenaWireDocument{ID: document.ID, Data: data, Columns: columns}, nil
}

func athenaGetFrame(ctx context.Context, request admittedAthenaRead, document *athenasdk.Document, used athenaWireBudget) ([]byte, athenaWireBudget, error) {
	return athenaWireBuild(ctx, used, func(candidate athenaWireBudget) ([]byte, athenaWireBudget, error) {
		if request.operation != "get" || !athenaWireRequiredText(request.collection) ||
			!athenaInputID(request.id) || !athenaWireRequiredText(request.table.IDColumn) {
			return nil, used, errAthenaOutput
		}
		var err error
		if candidate, err = athenaWireAdmitRecords(ctx, candidate, 1); err != nil {
			return nil, used, err
		}
		if candidate, err = athenaWireHeader(ctx, candidate,
			[]string{"operation", "format", "collection", "document"}, "get", request.collection); err != nil {
			return nil, used, err
		}
		var wireDocument *athenaWireDocument
		if document == nil {
			if candidate, err = candidate.charge(ctx, 1, 4); err != nil {
				return nil, used, err
			}
		} else if candidate, wireDocument, err = athenaWireDocumentOf(ctx, candidate, document, request.id, request.table.IDColumn); err != nil {
			return nil, used, err
		}
		envelope := athenaWireGetEnvelope{Operation: "get", Format: athenaPointFormat, Collection: request.collection, Document: wireDocument}
		return athenaWireFinish(ctx, candidate, envelope)
	})
}

// athenaExistsFrame encodes exists and testConnection. testConnection admits
// requestCompleted only because the executor calls it after a successful Exists.
func athenaExistsFrame(ctx context.Context, request admittedAthenaRead, exists bool, used athenaWireBudget) ([]byte, athenaWireBudget, error) {
	return athenaWireBuild(ctx, used, func(candidate athenaWireBudget) ([]byte, athenaWireBudget, error) {
		completed := request.operation == "testConnection"
		if (!completed && request.operation != "exists") || !athenaWireRequiredText(request.collection) ||
			!athenaInputID(request.id) || !athenaWireRequiredText(request.table.IDColumn) {
			return nil, used, errAthenaOutput
		}
		var err error
		if candidate, err = athenaWireAdmitRecords(ctx, candidate, 1); err != nil {
			return nil, used, err
		}
		names := []string{"operation", "format", "collection", "id", "exists"}
		if completed {
			names = append(names, "requestCompleted")
		}
		if candidate, err = athenaWireHeader(ctx, candidate, names, request.operation, request.collection, request.id); err != nil {
			return nil, used, err
		}
		if candidate, err = candidate.boolean(ctx, exists); err != nil {
			return nil, used, err
		}
		var completedField *bool
		if completed {
			value := true
			completedField = &value
			if candidate, err = candidate.boolean(ctx, true); err != nil {
				return nil, used, err
			}
		}
		envelope := athenaWireExistsEnvelope{
			Operation: request.operation, Format: athenaPointFormat, Collection: request.collection,
			ID: request.id, Exists: exists, RequestCompleted: completedField,
		}
		return athenaWireFinish(ctx, candidate, envelope)
	})
}

func athenaTestConnectionFrame(ctx context.Context, request admittedAthenaRead, exists bool, used athenaWireBudget) ([]byte, athenaWireBudget, error) {
	return athenaWireBuild(ctx, used, func(athenaWireBudget) ([]byte, athenaWireBudget, error) {
		if request.operation != "testConnection" {
			return nil, used, errAthenaOutput
		}
		return athenaExistsFrame(ctx, request, exists, used)
	})
}

// athenaGetManyFrame requires documents to match the admitted ids exactly in
// length and order. A nil slot is a missing document; any invalid slot rejects
// the whole frame so the executor writes no prefix.
func athenaGetManyFrame(ctx context.Context, request admittedAthenaRead, documents []*athenasdk.Document, used athenaWireBudget) ([]byte, athenaWireBudget, error) {
	return athenaWireBuild(ctx, used, func(candidate athenaWireBudget) ([]byte, athenaWireBudget, error) {
		if request.operation != "getMany" || !athenaWireRequiredText(request.collection) ||
			!athenaWireRequiredText(request.table.IDColumn) || len(documents) != len(request.ids) {
			return nil, used, errAthenaOutput
		}
		var err error
		if candidate, err = athenaWireAdmitRecords(ctx, candidate, len(documents)); err != nil {
			return nil, used, err
		}
		if candidate, err = athenaWireHeader(ctx, candidate,
			[]string{"operation", "format", "collection", "documents"}, "getMany", request.collection); err != nil {
			return nil, used, err
		}
		if candidate, err = candidate.array(ctx, len(documents)); err != nil {
			return nil, used, err
		}
		wireDocuments := make([]*athenaWireDocument, len(documents))
		for i, document := range documents {
			if document == nil {
				if candidate, err = candidate.charge(ctx, 1, 4); err != nil {
					return nil, used, err
				}
				continue
			}
			if candidate, wireDocuments[i], err = athenaWireDocumentOf(ctx, candidate, document, request.ids[i], request.table.IDColumn); err != nil {
				return nil, used, err
			}
		}
		envelope := athenaWireGetManyEnvelope{Operation: "getMany", Format: athenaPointFormat, Collection: request.collection, Documents: wireDocuments}
		return athenaWireFinish(ctx, candidate, envelope)
	})
}

// athenaConfiguredMetadataFrame exposes only sorted configured table metadata.
// It never opens the SDK, reads CA/ENV/auth material, or claims observed state.
func athenaConfiguredMetadataFrame(ctx context.Context, cfg athenaNativeConfig, used athenaWireBudget) ([]byte, athenaWireBudget, error) {
	return athenaWireBuild(ctx, used, func(candidate athenaWireBudget) ([]byte, athenaWireBudget, error) {
		if len(cfg.Tables) == 0 || len(cfg.Tables) > athenaMaxTables {
			return nil, used, errAthenaOutput
		}
		aliases := make([]string, 0, len(cfg.Tables))
		for alias := range cfg.Tables {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		var err error
		if candidate, err = candidate.object(ctx, "operation", "format", "tables", "provenance", "dynamicDiscovery"); err != nil {
			return nil, used, err
		}
		for _, value := range []string{"metadata", athenaPointFormat} {
			if candidate, err = candidate.text(ctx, value); err != nil {
				return nil, used, err
			}
		}
		if candidate, err = candidate.array(ctx, len(aliases)); err != nil {
			return nil, used, err
		}
		wireTables := make([]athenaWireMetadataTable, 0, len(aliases))
		for _, alias := range aliases {
			table := cfg.Tables[alias]
			if !athenaWireRequiredText(alias) || !athenaWireRequiredText(table.Name) ||
				!athenaWireMetadataText(table.IDColumn) || len(table.Columns) == 0 || len(table.Columns) > athenaMaxColumns {
				return nil, used, errAthenaOutput
			}
			if candidate, err = candidate.object(ctx, "alias", "name", "idColumn", "columns"); err != nil {
				return nil, used, err
			}
			for _, value := range []string{alias, table.Name, table.IDColumn} {
				if candidate, err = candidate.text(ctx, value); err != nil {
					return nil, used, err
				}
			}
			if candidate, err = candidate.array(ctx, len(table.Columns)); err != nil {
				return nil, used, err
			}
			wireColumns := make([]athenaWireMetadataColumn, 0, len(table.Columns))
			for _, column := range table.Columns {
				if !athenaWireRequiredText(column.Name) || !athenaWireRequiredText(column.Type) {
					return nil, used, errAthenaOutput
				}
				if candidate, err = candidate.object(ctx, "name", "type"); err != nil {
					return nil, used, err
				}
				for _, value := range []string{column.Name, column.Type} {
					if candidate, err = candidate.text(ctx, value); err != nil {
						return nil, used, err
					}
				}
				wireColumns = append(wireColumns, athenaWireMetadataColumn(column))
			}
			wireTables = append(wireTables, athenaWireMetadataTable{Alias: alias, Name: table.Name, IDColumn: table.IDColumn, Columns: wireColumns})
		}
		if candidate, err = candidate.text(ctx, athenaMetadataProvenance); err != nil {
			return nil, used, err
		}
		if candidate, err = candidate.boolean(ctx, false); err != nil {
			return nil, used, err
		}
		envelope := athenaWireMetadataEnvelope{
			Operation: "metadata", Format: athenaPointFormat, Tables: wireTables,
			Provenance: athenaMetadataProvenance, DynamicDiscovery: false,
		}
		return athenaWireFinish(ctx, candidate, envelope)
	})
}
