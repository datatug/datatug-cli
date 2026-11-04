package commands

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
)

type admittedAthenaRead struct {
	operation, collection, id string
	ids                       []string
	table                     athenaTableConfig
	query                     dal.Query
	limit                     int
	pageSize                  int
}

type athenaRequestJSON struct {
	Operation  string            `json:"operation"`
	Collection string            `json:"collection,omitempty"`
	ID         string            `json:"id,omitempty"`
	IDs        []string          `json:"ids,omitempty"`
	Projection []string          `json:"projection,omitempty"`
	Where      json.RawMessage   `json:"where,omitempty"`
	Order      []json.RawMessage `json:"order,omitempty"`
	Limit      *int              `json:"limit,omitempty"`
	Offset     *int              `json:"offset,omitempty"`
	PageSize   *int              `json:"pageSize,omitempty"`
}

func athenaInputID(id string) bool {
	if id == "" || len(id) > 1024 || !utf8.ValidString(id) {
		return false
	}
	return 2+utf8.RuneCountInString(id)+strings.Count(id, "'") <= 1024
}

func admitAthenaRequest(ctx context.Context, cfg athenaNativeConfig, raw []byte) (out admittedAthenaRead, err error) {
	if ctx == nil {
		return out, errAthenaInput
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			out = admittedAthenaRead{}
			err = ctxErr
			return
		}
		if err != nil {
			out = admittedAthenaRead{}
			err = errAthenaInput
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return out, err
	}
	var request athenaRequestJSON
	if err = decodeDynamoDocument(raw, &request); err != nil {
		return out, err
	}
	var names []string
	switch request.Operation {
	case "metadata":
		names = []string{"operation"}
	case "get", "exists", "testConnection":
		names = []string{"operation", "collection", "id"}
	case "getMany":
		names = []string{"operation", "collection", "ids"}
	case "query":
		names = []string{"operation", "collection", "projection", "where", "order", "limit", "offset", "pageSize"}
	default:
		return out, errAthenaInput
	}
	fields, err := athenaFields(raw, names...)
	if err != nil {
		return out, err
	}
	out.operation = request.Operation
	if request.Operation == "metadata" {
		return out, athenaFresh(ctx)
	}
	if _, ok := fields["collection"]; !ok {
		return out, errAthenaInput
	}
	table, ok := cfg.Tables[request.Collection]
	if !ok {
		return out, errAthenaInput
	}
	out.collection = request.Collection
	out.table = athenaTableConfig{
		Name:     table.Name,
		IDColumn: table.IDColumn,
		Columns:  make([]athenaColumnConfig, len(table.Columns)),
	}
	copy(out.table.Columns, table.Columns)
	configured := make(map[string]bool, len(out.table.Columns))
	for _, column := range out.table.Columns {
		configured[column.Name] = true
	}
	switch request.Operation {
	case "get", "exists", "testConnection":
		_, hasID := fields["id"]
		if !hasID || table.IDColumn == "" || !athenaInputID(request.ID) {
			return out, errAthenaInput
		}
		out.id = request.ID
	case "getMany":
		_, hasIDs := fields["ids"]
		if !hasIDs || table.IDColumn == "" || len(request.IDs) > 100 {
			return out, errAthenaInput
		}
		out.ids = append([]string{}, request.IDs...)
		for _, id := range out.ids {
			if !athenaInputID(id) {
				return out, errAthenaInput
			}
		}
	case "query":
		if _, hasLimit := fields["limit"]; !hasLimit || *request.Limit < 1 || *request.Limit > 1000 {
			return out, errAthenaInput
		}
		pageSize := 100
		if _, ok := fields["pageSize"]; ok {
			if *request.PageSize < 1 || *request.PageSize > 1000 {
				return out, errAthenaInput
			}
			pageSize = *request.PageSize
		}
		offset := 0
		if _, ok := fields["offset"]; ok {
			if *request.Offset < 0 || *request.Offset > 100000 {
				return out, errAthenaInput
			}
			offset = *request.Offset
		}
		if _, ok := fields["order"]; ok && (len(request.Order) == 0 || len(request.Order) > 16) {
			return out, errAthenaInput
		}
		builder := dal.From(dal.NewRootCollectionRef(out.collection, "")).NewQuery()
		if whereRaw, ok := fields["where"]; ok {
			condition, err := compileAthenaCondition(ctx, out.table, whereRaw)
			if err != nil {
				return out, err
			}
			builder.Where(condition)
		}
		seen := make(map[string]bool, len(request.Order))
		for _, rawOrder := range request.Order {
			orderFields, err := athenaFields(rawOrder, "field", "direction")
			if err != nil || len(orderFields) != 2 {
				return out, errAthenaInput
			}
			field, err := athenaString(orderFields["field"])
			if err != nil {
				return out, err
			}
			direction, err := athenaString(orderFields["direction"])
			if err != nil {
				return out, err
			}
			if !configured[field] || seen[field] || (direction != "asc" && direction != "desc") {
				return out, errAthenaInput
			}
			seen[field] = true
			if direction == "desc" {
				builder.OrderBy(dal.Descending(dal.Field(field)))
			} else {
				builder.OrderBy(dal.Ascending(dal.Field(field)))
			}
		}
		var columns []dal.Column
		if _, ok := fields["projection"]; ok {
			if len(request.Projection) == 0 || len(request.Projection) > 128 {
				return out, errAthenaInput
			}
			columns = make([]dal.Column, 0, len(request.Projection))
			clear(seen)
			for _, name := range request.Projection {
				if !configured[name] || seen[name] {
					return out, errAthenaInput
				}
				seen[name] = true
				columns = append(columns, dal.Column{Expression: dal.Field(name)})
			}
		}
		builder.Limit(*request.Limit)
		if _, ok := fields["offset"]; ok {
			builder.Offset(offset)
		}
		out.limit = *request.Limit
		out.pageSize = pageSize
		out.query = builder.SelectColumns(columns...)
	}
	return out, athenaFresh(ctx)
}
