package watch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

// This test-only DALGO contract fixture exercises ordinary CI without Java,
// networking or credentials. It is not evidence of Firestore concurrency. The
// required emulator job runs exactly the same domain cases against real clients.
// It rejects physical reads after the first physical write and commits only a
// successful complete callback, making accidental early flushes observable.
type contractFirestoreDB struct {
	dal.DB
	mu   sync.Mutex
	rows map[string]contractFirestoreRow
}
type contractFirestoreRow struct {
	key  *record.Key
	data map[string]any
}
type contractFirestoreTX struct {
	dal.ReadwriteTransaction
	rows  map[string]contractFirestoreRow
	wrote bool
}

func firestoreContractFixture(t *testing.T) ([]*Store, *clock, FirestoreOptions) {
	t.Helper()
	c, options := firestoreFixtureOptions()
	db := &contractFirestoreDB{rows: map[string]contractFirestoreRow{}}
	db.rows[options.ProjectKey.String()] = contractFirestoreRow{options.ProjectKey, map[string]any{"title": "trusted project"}}
	stores := []*Store{}
	for i := 0; i < 2; i++ {
		s, e := NewFirestoreStore(db, options)
		if e != nil {
			t.Fatal(e)
		}
		stores = append(stores, s)
	}
	return stores, c, options
}
func (*contractFirestoreDB) Adapter() dal.Adapter {
	return dal.NewAdapter("firestore", "contract-fixture")
}
func (db *contractFirestoreDB) Get(ctx context.Context, r record.Record) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return (&contractFirestoreTX{rows: db.rows}).Get(ctx, r)
}
func (db *contractFirestoreDB) RunReadonlyTransaction(ctx context.Context, fn dal.ROTxWorker, _ ...dal.TransactionOption) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	return fn(ctx, &contractFirestoreTX{rows: db.rows})
}
func (db *contractFirestoreDB) RunReadwriteTransaction(ctx context.Context, fn dal.RWTxWorker, _ ...dal.TransactionOption) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	rows := make(map[string]contractFirestoreRow, len(db.rows))
	for id, row := range db.rows {
		data := map[string]any{}
		for k, v := range row.data {
			data[k] = v
		}
		rows[id] = contractFirestoreRow{row.key, data}
	}
	if e := fn(ctx, &contractFirestoreTX{rows: rows}); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	db.rows = rows
	return nil
}
func (tx *contractFirestoreTX) Get(ctx context.Context, r record.Record) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if tx.wrote {
		return errors.New("contract: read after physical write")
	}
	row, exists := tx.rows[r.Key().String()]
	if !exists {
		r.SetError(record.ErrRecordNotFound)
		return record.ErrRecordNotFound
	}
	r.SetError(nil)
	data, ok := r.Data().(map[string]any)
	if !ok {
		return errors.New("contract: map record required")
	}
	for k := range data {
		delete(data, k)
	}
	for k, v := range row.data {
		data[k] = v
	}
	return nil
}
func (tx *contractFirestoreTX) Insert(ctx context.Context, r record.Record, _ ...dal.InsertOption) error {
	if _, exists := tx.rows[r.Key().String()]; exists {
		return record.ErrRecordExists
	}
	return tx.Set(ctx, r)
}
func (tx *contractFirestoreTX) Set(ctx context.Context, r record.Record) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	tx.wrote = true
	r.SetError(nil)
	data := map[string]any{}
	for k, v := range r.Data().(map[string]any) {
		data[k] = v
	}
	tx.rows[r.Key().String()] = contractFirestoreRow{r.Key(), data}
	return nil
}
func (tx *contractFirestoreTX) Update(ctx context.Context, key *record.Key, updates []update.Update, _ ...dal.Precondition) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	row, exists := tx.rows[key.String()]
	if !exists {
		return record.ErrRecordNotFound
	}
	tx.wrote = true
	for _, change := range updates {
		row.data[change.FieldName()] = change.Value()
	}
	return nil
}
func (tx *contractFirestoreTX) Delete(ctx context.Context, key *record.Key) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	tx.wrote = true
	delete(tx.rows, key.String())
	return nil
}
func (tx *contractFirestoreTX) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if tx.wrote {
		return nil, errors.New("contract: query after physical write")
	}
	q := query.(dal.StructuredQuery)
	from := q.From().Base().(dal.CollectionRef)
	parent := from.Parent()
	path := record.NewKeyWithParentAndID(parent, from.Name(), "prototype").CollectionPath()
	rows := []contractFirestoreRow{}
	for _, row := range tx.rows {
		if row.key.CollectionPath() != path || string(q.StartAfter()) != "" && fmt.Sprint(row.key.ID) <= string(q.StartAfter()) || !contractCondition(row.data, q.Where()) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		for _, order := range q.OrderBy() {
			field := order.Expression().(dal.FieldRef)
			left, right := fmt.Sprint(rows[i].key.ID), fmt.Sprint(rows[j].key.ID)
			if !field.IsID() {
				l, r := rows[i].data[field.Name()], rows[j].data[field.Name()]
				if l == r {
					continue
				}
				if lv, ok := l.(int64); ok {
					less := lv < r.(int64)
					if order.Descending() {
						return !less
					}
					return less
				}
				left, right = fmt.Sprint(l), fmt.Sprint(r)
			}
			if left != right {
				if order.Descending() {
					return left > right
				}
				return left < right
			}
		}
		return fmt.Sprint(rows[i].key.ID) < fmt.Sprint(rows[j].key.ID)
	})
	if q.Limit() > 0 && len(rows) > q.Limit() {
		rows = rows[:q.Limit()]
	}
	records := make([]record.Record, 0, len(rows))
	for _, row := range rows {
		data := map[string]any{}
		for k, v := range row.data {
			data[k] = v
		}
		records = append(records, record.NewRecordWithData(row.key, data))
	}
	return dal.NewRecordsReader(records), nil
}
func contractCondition(data map[string]any, c dal.Condition) bool {
	if c == nil {
		return true
	}
	switch v := c.(type) {
	case dal.GroupCondition:
		for _, child := range v.Conditions() {
			if !contractCondition(data, child) {
				return false
			}
		}
		return true
	case dal.Comparison:
		field := v.Left.(dal.FieldRef).Name()
		expected := v.Right.(dal.Constant).Value
		switch v.Operator {
		case dal.Equal:
			return data[field] == expected
		case dal.LessOrEqual:
			value, ok := data[field].(int64)
			return ok && value <= expected.(int64)
		case dal.GreaterOrEqual:
			value, ok := data[field].(int64)
			return ok && value >= expected.(int64)
		}
	}
	return false
}
