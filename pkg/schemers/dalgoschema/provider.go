// Package dalgoschema is a schemer.SchemaProvider built on DALgo's
// dbschema.SchemaReader, so one scanner serves every database whose DALgo
// adapter can read its own schema. PostgreSQL is the first.
//
// A SchemaReader describes a table by name and keeps native names and types out
// of reach, so what a scan can report is what the reader reports: the portable
// column type (a type the reader cannot map is a string), the primary key,
// foreign keys, unique constraints and indexes, under the exact names the reader
// returns. It cannot tell a view from a table, so every collection is reported
// as a table; and it reads a single schema, so a foreign key into another schema
// is left out of the scan.
package dalgoschema

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
)

const (
	// dbTypeBaseTable is the DbType the scanner knows a table by.
	dbTypeBaseTable = "BASE TABLE"

	// indexType names every index: the reader does not report the index method.
	indexType = "INDEX"

	// constraintTypeUnique is the type ListConstraints gives a UNIQUE constraint.
	constraintTypeUnique = "unique"
)

// RecordsCounter reads the number of records of one table. A nil count with a
// nil error means the count is not available, which is not the same as zero.
type RecordsCounter interface {
	CountRecords(ctx context.Context, schema, table string) (*int, error)
}

// NewSchemaProvider returns a schemer.SchemaProvider that reads catalog through
// reader. schema is the schema the reader inspects; every table is reported in
// it. counter may be nil, in which case no table has a record count.
//
// The provider is safe for the concurrent use the scanner makes of it: it reads
// each table's description, indexes and constraints once and shares them.
func NewSchemaProvider(reader dbschema.SchemaReader, counter RecordsCounter, catalog, schema string) schemer.SchemaProvider {
	if reader == nil {
		panic("reader cannot be nil")
	}
	return &provider{reader: reader, counter: counter, catalog: catalog, schema: schema}
}

type provider struct {
	reader  dbschema.SchemaReader
	counter RecordsCounter
	catalog string
	schema  string

	defs    memo[*dbschema.CollectionDef]
	indexes memo[[]dbschema.IndexDef]
}

var _ schemer.SchemaProvider = (*provider)(nil)

// memo runs each load once per key and shares its outcome, an error included.
type memo[V any] struct {
	mu    sync.Mutex
	items map[string]*memoEntry[V]
}

type memoEntry[V any] struct {
	once  sync.Once
	value V
	err   error
}

func (m *memo[V]) get(key string, load func() (V, error)) (V, error) {
	m.mu.Lock()
	if m.items == nil {
		m.items = map[string]*memoEntry[V]{}
	}
	entry, found := m.items[key]
	if !found {
		entry = &memoEntry[V]{}
		m.items[key] = entry
	}
	m.mu.Unlock()
	entry.once.Do(func() { entry.value, entry.err = load() })
	return entry.value, entry.err
}

func (*provider) IsBulkProvider() bool {
	// The reader describes one table at a time: use the scanner's per-table path.
	return false
}

// ref names a table the way the scanner does: in the given schema, or in the
// reader's own when none is given.
func (*provider) ref(schema, table string) *dal.CollectionRef {
	ref := dal.NewRootCollectionRef(table, "")
	if schema != "" {
		ref = dal.NewQualifiedRootCollectionRef(schema, table, "")
	}
	return &ref
}

// schemaOf is the schema a reference is read from.
func (p *provider) schemaOf(ref *dal.CollectionRef) string {
	if schema := ref.Schema(); schema != "" {
		return schema
	}
	return p.schema
}

func (p *provider) key(ref *dal.CollectionRef) string {
	return p.schemaOf(ref) + "\x00" + ref.Name()
}

func (p *provider) describe(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return p.defs.get(p.key(ref), func() (*dbschema.CollectionDef, error) {
		def, err := p.reader.DescribeCollection(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("describe %s: %w", ref.Name(), err)
		}
		return def, nil
	})
}

func (p *provider) listIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return p.indexes.get(p.key(ref), func() ([]dbschema.IndexDef, error) {
		indexes, err := p.reader.ListIndexes(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("list indexes of %s: %w", ref.Name(), err)
		}
		return indexes, nil
	})
}

// GetCollections lists every collection the reader reports, as a table.
func (p *provider) GetCollections(ctx context.Context, _ *record.Key) (schemer.CollectionsReader, error) {
	refs, err := p.reader.ListCollections(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	collections := make([]*datatug.CollectionInfo, len(refs))
	for i := range refs {
		collections[i] = &datatug.CollectionInfo{
			DBCollectionKey: datatug.NewCollectionKey(datatug.CollectionTypeTable, refs[i].Name(), p.schema, p.catalog, nil),
			TableProps:      datatug.TableProps{DbType: dbTypeBaseTable},
		}
	}
	return &collectionsReader{items: collections}, nil
}

type collectionsReader struct {
	items []*datatug.CollectionInfo
}

func (r *collectionsReader) NextCollection() (*datatug.CollectionInfo, error) {
	if len(r.items) == 0 {
		return nil, io.EOF
	}
	next := r.items[0]
	r.items = r.items[1:]
	return next, nil
}

// GetColumns returns the columns of the table filter names, in table order. A
// column keeps its position in the table when a name filter drops others.
func (p *provider) GetColumns(ctx context.Context, _ string, filter schemer.ColumnsFilter) ([]schemer.Column, error) {
	if filter.CollectionRef == nil {
		return nil, errors.New("collection reference is required to read columns")
	}
	def, err := p.describe(ctx, filter.CollectionRef)
	if err != nil {
		return nil, err
	}
	tableRef := schemer.TableRef{SchemaName: p.schemaOf(filter.CollectionRef), TableName: filter.CollectionRef.Name()}
	primaryKey := map[dal.FieldName]int{}
	for i, name := range def.PrimaryKey {
		primaryKey[name] = i + 1
	}
	var columns []schemer.Column
	for i, field := range def.Fields {
		if filter.ColNameRegex != nil && !filter.ColNameRegex.MatchString(string(field.Name)) {
			continue
		}
		column := schemer.Column{TableRef: tableRef}
		column.Name = string(field.Name)
		column.OrdinalPosition = i + 1
		column.PrimaryKeyPosition = primaryKey[field.Name]
		column.IsNullable = field.Nullable
		column.DbType = field.Type.String()
		if field.Length != nil && (field.Type == dbschema.String || field.Type == dbschema.Bytes) {
			length := *field.Length
			column.CharMaxLength = &length
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func (p *provider) GetColumnsReader(ctx context.Context, catalog string, filter schemer.ColumnsFilter) (schemer.ColumnsReader, error) {
	columns, err := p.GetColumns(ctx, catalog, filter)
	if err != nil {
		return nil, err
	}
	return &columnsReader{items: columns}, nil
}

type columnsReader struct {
	items []schemer.Column
}

func (r *columnsReader) NextColumn() (schemer.Column, error) {
	if len(r.items) == 0 {
		return schemer.Column{}, io.EOF
	}
	next := r.items[0]
	r.items = r.items[1:]
	return next, nil
}

// GetIndexes lists the table's indexes; GetIndexColumns lists their columns.
func (p *provider) GetIndexes(ctx context.Context, _, schema, table string) (schemer.IndexesReader, error) {
	ref := p.ref(schema, table)
	indexes, err := p.listIndexes(ctx, ref)
	if err != nil {
		return nil, err
	}
	tableRef := schemer.TableRef{SchemaName: p.schemaOf(ref), TableName: table}
	items := make([]*schemer.Index, len(indexes))
	for i, index := range indexes {
		items[i] = &schemer.Index{
			TableRef: tableRef,
			Index:    &datatug.Index{Name: index.Name, Type: indexType, IsUnique: index.Unique},
		}
	}
	return &indexesReader{items: items}, nil
}

type indexesReader struct {
	items []*schemer.Index
}

func (r *indexesReader) NextIndex() (*schemer.Index, error) {
	if len(r.items) == 0 {
		return nil, io.EOF
	}
	next := r.items[0]
	r.items = r.items[1:]
	return next, nil
}

func (p *provider) GetIndexColumns(ctx context.Context, _, schema, table, index string) (schemer.IndexColumnsReader, error) {
	ref := p.ref(schema, table)
	indexes, err := p.listIndexes(ctx, ref)
	if err != nil {
		return nil, err
	}
	tableRef := schemer.TableRef{SchemaName: p.schemaOf(ref), TableName: table}
	for _, candidate := range indexes {
		if candidate.Name != index {
			continue
		}
		items := make([]*schemer.IndexColumn, len(candidate.Fields))
		for i, field := range candidate.Fields {
			items[i] = &schemer.IndexColumn{TableRef: tableRef, IndexName: index, IndexColumn: &datatug.IndexColumn{Name: string(field)}}
		}
		return &indexColumnsReader{items: items}, nil
	}
	return nil, fmt.Errorf("index %q not found on %s", index, table)
}

type indexColumnsReader struct {
	items []*schemer.IndexColumn
}

func (r *indexColumnsReader) NextIndexColumn() (*schemer.IndexColumn, error) {
	if len(r.items) == 0 {
		return nil, io.EOF
	}
	next := r.items[0]
	r.items = r.items[1:]
	return next, nil
}

// GetConstraints returns the table's foreign keys and unique constraints, one
// row per constraint column, the way the scanner groups them. It leaves out the
// primary key, which the scanner takes from the columns: it would otherwise add
// every key column twice. A foreign key into another schema is left out too:
// the scan holds one schema, and the scanner fails on a key whose table it does
// not have.
func (p *provider) GetConstraints(ctx context.Context, _, schema, table string) (schemer.ConstraintsReader, error) {
	ref := p.ref(schema, table)
	def, err := p.describe(ctx, ref)
	if err != nil {
		return nil, err
	}
	scanned := p.schemaOf(ref)
	tableRef := schemer.TableRef{SchemaName: scanned, TableName: table}
	var constraints []*schemer.Constraint
	for _, key := range def.ForeignKeys {
		if key.ReferencedNamespace != "" {
			log.Printf("dalgoschema: foreign key %s of %s.%s is left out: it references %s.%s, outside the scanned schema",
				key.Name, scanned, table, key.ReferencedNamespace, key.ReferencedCollection)
			continue
		}
		for i, field := range key.Fields {
			constraint := &schemer.Constraint{
				TableRef:        tableRef,
				ColumnName:      string(field),
				RefTableCatalog: p.catalog,
				RefTableSchema:  scanned,
				RefTableName:    key.ReferencedCollection,
				UpdateRule:      key.OnUpdate,
				DeleteRule:      key.OnDelete,
				Constraint:      &datatug.Constraint{Name: key.Name, Type: "FOREIGN KEY"},
			}
			if i < len(key.ReferencedFields) {
				constraint.RefColName = string(key.ReferencedFields[i])
			}
			constraints = append(constraints, constraint)
		}
	}
	unique, err := p.uniqueConstraints(ctx, ref, tableRef)
	if err != nil {
		return nil, err
	}
	return &constraintsReader{items: append(constraints, unique...)}, nil
}

// uniqueConstraints returns a row per column of every UNIQUE constraint of the
// table. The reader lists a constraint's name and type; the columns are those
// of the unique index of the same name.
func (p *provider) uniqueConstraints(ctx context.Context, ref *dal.CollectionRef, tableRef schemer.TableRef) ([]*schemer.Constraint, error) {
	listed, err := p.reader.ListConstraints(ctx, ref)
	if err != nil {
		if errors.Is(err, dal.ErrNotSupported) {
			return nil, nil
		}
		return nil, fmt.Errorf("list constraints of %s: %w", ref.Name(), err)
	}
	indexes, err := p.listIndexes(ctx, ref)
	if err != nil {
		return nil, err
	}
	var unique []*schemer.Constraint
	for _, constraint := range listed {
		if constraint.Type != constraintTypeUnique {
			continue
		}
		for _, index := range indexes {
			if index.Name != constraint.Name {
				continue
			}
			for _, field := range index.Fields {
				unique = append(unique, &schemer.Constraint{
					TableRef:   tableRef,
					ColumnName: string(field),
					Constraint: &datatug.Constraint{Name: constraint.Name, Type: "UNIQUE"},
				})
			}
		}
	}
	return unique, nil
}

type constraintsReader struct {
	items []*schemer.Constraint
}

func (r *constraintsReader) NextConstraint() (*schemer.Constraint, error) {
	if len(r.items) == 0 {
		return nil, io.EOF
	}
	next := r.items[0]
	r.items = r.items[1:]
	return next, nil
}

// GetForeignKeys returns the foreign keys of the table. A key into another
// schema names that schema in its target.
func (p *provider) GetForeignKeys(ctx context.Context, schema, table string) ([]schemer.ForeignKey, error) {
	def, err := p.describe(ctx, p.ref(schema, table))
	if err != nil {
		return nil, err
	}
	keys := make([]schemer.ForeignKey, len(def.ForeignKeys))
	for i, key := range def.ForeignKeys {
		target := key.ReferencedCollection
		if key.ReferencedNamespace != "" {
			target = key.ReferencedNamespace + "." + target
		}
		keys[i] = schemer.ForeignKey{
			Name: key.Name,
			From: schemer.FKAnchor{Name: table, Columns: fieldNames(key.Fields)},
			To:   schemer.FKAnchor{Name: target, Columns: fieldNames(key.ReferencedFields)},
		}
	}
	return keys, nil
}

func (p *provider) GetForeignKeysReader(ctx context.Context, schema, table string) (schemer.ForeignKeysReader, error) {
	keys, err := p.GetForeignKeys(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	return &foreignKeysReader{items: keys}, nil
}

type foreignKeysReader struct {
	items []schemer.ForeignKey
}

func (r *foreignKeysReader) NextForeignKey() (schemer.ForeignKey, error) {
	if len(r.items) == 0 {
		return schemer.ForeignKey{}, io.EOF
	}
	next := r.items[0]
	r.items = r.items[1:]
	return next, nil
}

// GetReferrers returns one entry per foreign key that references the table. The
// reader reports a referrer per foreign key, never one per table, so two keys
// from one table are two entries. The reader names only the referencing table
// and columns; the key's name and the referenced columns come from describing
// the referencing table, and are left empty if no key of it matches.
func (p *provider) GetReferrers(ctx context.Context, schema, table string) ([]schemer.ForeignKey, error) {
	referrers, err := p.reader.ListReferrers(ctx, p.ref(schema, table))
	if err != nil {
		if errors.Is(err, dal.ErrNotSupported) {
			return nil, nil
		}
		return nil, fmt.Errorf("list referrers of %s: %w", table, err)
	}
	used := map[string]bool{}
	keys := make([]schemer.ForeignKey, len(referrers))
	for i, referrer := range referrers {
		from := referrer.Collection.Name()
		keys[i] = schemer.ForeignKey{
			From: schemer.FKAnchor{Name: from, Columns: fieldNames(referrer.Fields)},
			To:   schemer.FKAnchor{Name: table},
		}
		def, err := p.describe(ctx, p.ref(schema, from))
		if err != nil {
			return nil, err
		}
		for j, key := range def.ForeignKeys {
			slot := from + "\x00" + fmt.Sprint(j)
			if used[slot] || key.ReferencedNamespace != "" || key.ReferencedCollection != table || !sameFields(key.Fields, referrer.Fields) {
				continue
			}
			used[slot] = true
			keys[i].Name = key.Name
			keys[i].To.Columns = fieldNames(key.ReferencedFields)
			break
		}
	}
	return keys, nil
}

// RecordsCount returns the table's record count, or nil when there is no
// counter or the counter has none.
func (p *provider) RecordsCount(ctx context.Context, _, schema, table string) (*int, error) {
	if p.counter == nil {
		return nil, nil
	}
	return p.counter.CountRecords(ctx, schema, table)
}

func fieldNames(fields []dal.FieldName) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = string(field)
	}
	return names
}

func sameFields(a, b []dal.FieldName) bool {
	return strings.Join(fieldNames(a), "\x00") == strings.Join(fieldNames(b), "\x00")
}
