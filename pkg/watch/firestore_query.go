package watch

import (
	"context"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"reflect"
	"sort"
)

func cloudConditions(condition dal.Condition) (map[string]dal.Comparison, error) {
	result := map[string]dal.Comparison{}
	var visit func(dal.Condition) error
	visit = func(c dal.Condition) error {
		switch value := c.(type) {
		case dal.GroupCondition:
			if value.Operator() != dal.And {
				return dal.ErrNotSupported
			}
			for _, child := range value.Conditions() {
				if err := visit(child); err != nil {
					return err
				}
			}
		case dal.Comparison:
			field, ok := value.Left.(dal.FieldRef)
			if !ok {
				return dal.ErrNotSupported
			}
			constant, ok := value.Right.(dal.Constant)
			if !ok {
				return dal.ErrNotSupported
			}
			switch field.Name() {
			case "namespace", "project", "environment", "watch", "owner", "active", "resource", "lane", "live":
				if value.Operator != dal.Equal {
					return dal.ErrNotSupported
				}
				if _, ok := constant.Value.(string); !ok {
					return dal.ErrNotSupported
				}
			case "ready", "updated":
				if value.Operator != dal.Equal && value.Operator != dal.LessOrEqual && value.Operator != dal.GreaterOrEqual {
					return dal.ErrNotSupported
				}
				if _, ok := constant.Value.(int64); !ok {
					return dal.ErrNotSupported
				}
			default:
				return dal.ErrNotSupported
			}
			if _, exists := result[field.Name()]; exists {
				return ErrInvalid
			}
			result[field.Name()] = value
		default:
			return dal.ErrNotSupported
		}
		return nil
	}
	if err := visit(condition); err != nil {
		return nil, err
	}
	return result, nil
}
func cloudMatches(data map[string]any, fields map[string]dal.Comparison) bool {
	for name, c := range fields {
		constant := c.Right.(dal.Constant).Value
		switch c.Operator {
		case dal.Equal:
			if data[name] != constant {
				return false
			}
		case dal.LessOrEqual:
			n, ok := data[name].(int64)
			if !ok || n > constant.(int64) {
				return false
			}
		case dal.GreaterOrEqual:
			n, ok := data[name].(int64)
			if !ok || n < constant.(int64) {
				return false
			}
		}
	}
	return true
}
func cloudField(fields map[string]dal.Comparison, name string) any {
	if c, ok := fields[name]; ok {
		return c.Right.(dal.Constant).Value
	}
	return nil
}

func (t *firestoreSession) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	q, ok := query.(dal.StructuredQuery)
	if !ok || q.Limit() < 0 || q.Limit() > cloudQueryRows || q.Offset() != 0 || len(q.GroupBy()) != 0 || q.Having() != nil || q.StartFrom() != "" || len(q.Columns()) != 0 {
		return nil, dal.ErrNotSupported
	}
	from, ok := q.From().Base().(dal.CollectionRef)
	if !ok || from.Parent() != nil {
		return nil, dal.ErrNotSupported
	}
	fields, err := cloudConditions(q.Where())
	if err != nil {
		return nil, err
	}
	expectedNamespace := t.database.options.Config.Namespace
	if from.Name() == quotas {
		expectedNamespace = t.database.options.AccountNamespace
	}
	if cloudField(fields, "namespace") != expectedNamespace {
		return nil, ErrForbidden
	}
	if project := cloudField(fields, "project"); project != nil && project != t.database.options.ProjectID && (from.Name() != quotas || project != "_accounts") {
		return nil, ErrForbidden
	}
	prototype := key(from.Name(), Scope{}, "prototype")
	physical, err := t.physical(prototype)
	if err != nil {
		return nil, err
	}
	for _, order := range q.OrderBy() {
		field, ok := order.Expression().(dal.FieldRef)
		if !ok || (!field.IsID() && field.Name() != "ready" && field.Name() != "updated") {
			return nil, dal.ErrNotSupported
		}
	}
	cacheID := fmt.Sprint(physical.CollectionPath(), ":", q.Where().String(), ":", q.OrderBy(), ":", q.StartAfter(), ":", q.Limit())
	base, exists := t.queryCache[cacheID]
	if !exists {
		bound := cloudQueryRows + 1
		if q.Limit() > 0 {
			bound = q.Limit() + 1
		}
		// Scope predicates are checked above and enforced by the private parent
		// path. Excluding them from the physical index avoids redundant Space/
		// project permutations; fetched rows still pass validateRow below.
		physicalConditions := []dal.Condition{}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if name != "namespace" && name != "project" {
				physicalConditions = append(physicalConditions, fields[name])
			}
		}
		builder := dal.From(dal.NewCollectionRef(from.Name(), "", physical.Parent())).NewQuery().Limit(bound)
		if len(physicalConditions) > 0 {
			builder = builder.Where(dal.NewGroupCondition(dal.And, physicalConditions...))
		}
		if len(q.OrderBy()) > 0 {
			builder = builder.OrderBy(q.OrderBy()...)
		}
		if q.StartAfter() != "" {
			builder = builder.StartAfter(q.StartAfter())
		}
		actual := builder.SelectIntoRecord(func() record.Record {
			return record.NewRecordWithData(record.NewIncompleteKey(from.Name(), reflect.String, physical.Parent()), map[string]any{})
		})
		reader, err := t.reader.ExecuteQueryToRecordsReader(ctx, actual)
		if err != nil {
			return nil, err
		}
		base = []record.Record{}
		for {
			r, err := reader.Next()
			if errors.Is(err, dal.ErrNoMoreRecords) {
				break
			}
			if err != nil {
				_ = reader.Close()
				return nil, err
			}
			t.readCount++
			if len(base) >= cloudQueryRows || t.readCount > cloudReadRows {
				_ = reader.Close()
				return nil, ErrCapacity
			}
			data, ok := r.Data().(map[string]any)
			if !ok {
				_ = reader.Close()
				return nil, ErrInvalid
			}
			if err := t.validateRow(from.Name(), data); err != nil {
				_ = reader.Close()
				return nil, err
			}
			row := cloudRow{key: r.Key(), data: copyCloudMap(data), exists: true}
			t.original[r.Key().String()] = row
			base = append(base, record.NewRecordWithData(r.Key(), row.data))
		}
		if err := reader.Close(); err != nil {
			return nil, err
		}
		t.queryCache[cacheID] = base
	}
	rows := map[string]cloudRow{}
	for _, r := range base {
		rows[r.Key().String()] = t.original[r.Key().String()]
	}
	for id, row := range t.pending {
		if row == nil {
			delete(rows, id)
			continue
		}
		if row.key.CollectionPath() == physical.CollectionPath() {
			if cloudMatches(row.data, fields) {
				rows[id] = *row
			} else {
				delete(rows, id)
			}
		}
	}
	if len(rows) > cloudQueryRows {
		return nil, ErrCapacity
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		for _, order := range q.OrderBy() {
			field := order.Expression().(dal.FieldRef)
			left, right := any(rows[ids[i]].key.ID), any(rows[ids[j]].key.ID)
			if !field.IsID() {
				left, right = rows[ids[i]].data[field.Name()], rows[ids[j]].data[field.Name()]
			}
			if left == right {
				continue
			}
			less := false
			switch a := left.(type) {
			case int64:
				b, ok := right.(int64)
				if !ok {
					return ids[i] < ids[j]
				}
				less = a < b
			case string:
				b, ok := right.(string)
				if !ok {
					return ids[i] < ids[j]
				}
				less = a < b
			default:
				return ids[i] < ids[j]
			}
			if order.Descending() {
				return !less
			}
			return less
		}
		return ids[i] < ids[j]
	})
	if q.Limit() > 0 && len(ids) > q.Limit() {
		ids = ids[:q.Limit()]
	}
	result := make([]record.Record, 0, len(ids))
	for _, id := range ids {
		row := rows[id]
		result = append(result, record.NewRecordWithData(record.NewKeyWithID(from.Name(), row.key.ID), copyCloudMap(row.data)))
	}
	return dal.NewRecordsReader(result), nil
}
