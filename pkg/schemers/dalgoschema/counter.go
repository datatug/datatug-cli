package dalgoschema

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/dal-go/dalgo/dal"
)

// countAlias names the one column of a COUNT(*) query.
const countAlias = "n"

// planAggregation is DALgo's aggregation planner, a seam: the COUNT(*) query
// this package builds is always valid, so only a test can make the planner
// refuse it. Always dal.PlanAggregation in production.
var planAggregation = dal.PlanAggregation

// NewNativeCounter counts a table's records with one COUNT(*) structured query,
// and only when db runs that aggregate natively.
//
// A database that does not advertise a native COUNT would have DALgo read every
// row of the table through this process and count them here. A schema scan must
// never do that to someone's database, so such a table has no count: the count
// is unknown, and it appears on its own once the database's adapter declares the
// aggregate.
func NewNativeCounter(db dal.DB) RecordsCounter {
	var capabilities dal.QueryCapabilities
	if provider, ok := dal.As[dal.QueryCapabilitiesProvider](db); ok {
		capabilities = provider.QueryCapabilities()
	}
	return newNativeCounter(db, capabilities)
}

func newNativeCounter(executor dal.QueryExecutor, capabilities dal.QueryCapabilities) RecordsCounter {
	return &nativeCounter{executor: executor, capabilities: capabilities}
}

type nativeCounter struct {
	executor     dal.QueryExecutor
	capabilities dal.QueryCapabilities
	explained    sync.Once
}

func (c *nativeCounter) CountRecords(ctx context.Context, schema, table string) (*int, error) {
	collection := dal.NewRootCollectionRef(table, "")
	if schema != "" {
		collection = dal.NewQualifiedRootCollectionRef(schema, table, "")
	}
	count := dal.Count()
	count.Alias = countAlias
	query := dal.From(collection).NewQuery().SelectColumns(count)
	plan, err := planAggregation(query, c.capabilities)
	if err != nil {
		return nil, fmt.Errorf("count records of %s: %w", table, err)
	}
	if plan.Strategy != dal.AggregationNative {
		c.explained.Do(func() {
			log.Printf("dalgoschema: record counts are skipped: the database does not run COUNT(*) natively, and counting here would read every row of every table")
		})
		return nil, nil
	}
	n, err := c.count(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("count records of %s: %w", table, err)
	}
	return &n, nil
}

func (c *nativeCounter) count(ctx context.Context, query dal.StructuredQuery) (n int, err error) {
	reader, err := c.executor.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
	}()
	row, err := reader.Next()
	if err != nil {
		return 0, err
	}
	data, ok := row.Data().(map[string]any)
	if !ok {
		return 0, fmt.Errorf("unexpected COUNT(*) row of type %T", row.Data())
	}
	switch value := data[countAlias].(type) {
	case int64:
		return int(value), nil
	case int:
		return value, nil
	case float64:
		return int(value), nil
	default:
		return 0, fmt.Errorf("unexpected COUNT(*) value of type %T", value)
	}
}
