package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

// firestoreSession is private to the Watch state machine. Its supported surface
// is deliberately the CRUD and scoped equality queries that Watch uses. It is
// not a general DALGO adapter. No SDK write is queued before flush.
type firestoreSession struct {
	dal.ReadwriteTransaction
	database      *firestoreDatabase
	reader        dal.ReadTransaction
	original      map[string]cloudRow
	pending       map[string]*cloudRow
	queryCache    map[string][]record.Record
	readCount     int
	pendingOutbox int64
	sweepCursor   string
	ready         map[string]WorkEvent
	changed       map[string]Definition
	// Set only by the ordinary audit writer in this SDK attempt. Preserve the
	// writer's original time/ID; a JSON time cannot reconstruct time.String().
	registrationAudit *AuditEvent
}

type cloudRow struct {
	key    *record.Key
	data   map[string]any
	exists bool
}

func newFirestoreSession(db *firestoreDatabase, reader dal.ReadTransaction) *firestoreSession {
	return &firestoreSession{database: db, reader: reader, original: map[string]cloudRow{}, pending: map[string]*cloudRow{}, queryCache: map[string][]record.Record{}, changed: map[string]Definition{}, ready: map[string]WorkEvent{}}
}

func (t *firestoreSession) physical(k *record.Key) (*record.Key, error) {
	if k == nil || k.Parent() != nil {
		return nil, ErrInvalid
	}
	id, ok := k.ID.(string)
	if !ok || len(id) != 64 {
		return nil, ErrInvalid
	}
	allowed := false
	for _, table := range tables {
		allowed = allowed || table == k.Collection()
	}
	if !allowed {
		return nil, ErrInvalid
	}
	root := t.database.root
	if k.Collection() == quotas {
		root = t.database.quotaRoot
	} else if k.Collection() == telegramBots || k.Collection() == telegramPolls {
		root = t.database.coordinationRoot
	}
	return record.NewKeyWithParentAndID(root, k.Collection(), id), nil
}

func copyCloudMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for k, v := range source {
		result[k] = v
	}
	return result
}

func (t *firestoreSession) load(ctx context.Context, k *record.Key) (cloudRow, error) {
	physical, err := t.physical(k)
	if err != nil {
		return cloudRow{}, err
	}
	id := physical.String()
	if row, exists := t.pending[id]; exists {
		if row == nil {
			return cloudRow{key: physical}, nil
		}
		return *row, nil
	}
	if row, ok := t.original[id]; ok {
		return row, nil
	}
	t.readCount++
	if t.readCount > cloudReadRows {
		return cloudRow{}, ErrCapacity
	}
	data := map[string]any{}
	err = t.reader.Get(ctx, record.NewRecordWithData(physical, data))
	if err != nil && !record.IsNotFound(err) {
		return cloudRow{}, err
	}
	row := cloudRow{key: physical, data: data, exists: err == nil}
	t.original[id] = row
	return row, nil
}

func (t *firestoreSession) Get(ctx context.Context, r record.Record) error {
	row, err := t.load(ctx, r.Key())
	if err == nil && !row.exists {
		err = record.ErrRecordNotFound
	}
	if err != nil {
		r.SetError(err)
		return err
	}
	r.SetError(nil)
	data, ok := r.Data().(map[string]any)
	if !ok {
		return fmt.Errorf("%w: Watch requires map rows", ErrInvalid)
	}
	for k := range data {
		delete(data, k)
	}
	for k, v := range row.data {
		data[k] = v
	}
	return nil
}

func (t *firestoreSession) Exists(ctx context.Context, k *record.Key) (bool, error) {
	row, err := t.load(ctx, k)
	return row.exists, err
}

func (t *firestoreSession) Insert(ctx context.Context, r record.Record, options ...dal.InsertOption) error {
	if len(options) != 0 {
		return dal.ErrNotSupported
	}
	row, err := t.load(ctx, r.Key())
	if err != nil {
		return err
	}
	if row.exists {
		return record.ErrRecordExists
	}
	data, ok := r.Data().(map[string]any)
	if !ok {
		return ErrInvalid
	}
	if err := t.validateRow(r.Key().Collection(), data); err != nil {
		return err
	}
	row.data, row.exists = copyCloudMap(data), true
	t.pending[row.key.String()] = &row
	return t.budget()
}

func (t *firestoreSession) Update(ctx context.Context, k *record.Key, updates []update.Update, conditions ...dal.Precondition) error {
	if len(conditions) != 0 {
		return dal.ErrNotSupported
	}
	row, err := t.load(ctx, k)
	if err != nil {
		return err
	}
	if !row.exists {
		return record.ErrRecordNotFound
	}
	row.data = copyCloudMap(row.data)
	for _, field := range updates {
		if field.FieldName() == "" || len(field.FieldPath()) != 0 {
			return dal.ErrNotSupported
		}
		row.data[field.FieldName()] = field.Value()
	}
	if err := t.validateRow(k.Collection(), row.data); err != nil {
		return err
	}
	t.pending[row.key.String()] = &row
	return t.budget()
}

func (t *firestoreSession) Delete(ctx context.Context, k *record.Key) error {
	row, err := t.load(ctx, k)
	if err != nil {
		return err
	}
	t.pending[row.key.String()] = nil
	return t.budget()
}

func (t *firestoreSession) validateRow(table string, data map[string]any) error {
	expectedNamespace := t.database.options.Config.Namespace
	if table == quotas {
		expectedNamespace = t.database.options.AccountNamespace
	}
	if data["namespace"] != expectedNamespace {
		return ErrForbidden
	}
	if table == telegramBots || table == telegramPolls {
		if data["project"] != "" || data["environment"] != "" {
			return ErrForbidden
		}
	} else if table == quotas {
		if data["project"] != "_accounts" || data["environment"] != "_quota" {
			return ErrForbidden
		}
	} else if data["project"] != t.database.options.ProjectID {
		return ErrForbidden
	}
	if _, ok := data["payload"].(string); !ok {
		return ErrInvalid
	}
	return nil
}

func (t *firestoreSession) budget() error {
	if len(t.pending)+len(t.changed)+len(t.ready)+1 > cloudWriteRows {
		return ErrCapacity
	}
	size := (len(t.changed)+len(t.ready))*1024 + 1024
	for _, row := range t.pending {
		if row == nil {
			continue
		}
		body, err := json.Marshal(row.data)
		if err != nil {
			return err
		}
		size += len(body) + len(row.key.String())
	}
	if size > cloudWriteBytes {
		return ErrCapacity
	}
	return nil
}

func (t *firestoreSession) flush(ctx context.Context) error {
	if err := t.budget(); err != nil {
		return err
	}
	ids := make([]string, 0, len(t.pending))
	for id := range t.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		row := t.pending[id]
		original := t.original[id]
		if row == nil {
			if original.exists {
				if err := t.ReadwriteTransaction.Delete(ctx, original.key); err != nil {
					return err
				}
			}
			continue
		}
		if !original.exists {
			if err := t.ReadwriteTransaction.Insert(ctx, record.NewRecordWithData(row.key, row.data)); err != nil {
				return err
			}
		} else {
			fields := make([]update.Update, 0, len(row.data))
			for name, value := range row.data {
				fields = append(fields, update.ByFieldName(name, value))
			}
			if err := t.ReadwriteTransaction.Update(ctx, row.key, fields); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *firestoreSession) backlog() int64 {
	total := t.pendingOutbox
	for id, row := range t.pending {
		original := t.original[id]
		if original.key == nil || original.key.Collection() != outbox {
			continue
		}
		if original.exists && original.data["active"] == "yes" {
			total--
		}
		if row != nil && row.data["active"] == "yes" {
			total++
		}
	}
	return total
}
