// Package dalgoschema is a schemer.SchemaProvider built on DALgo's
// dbschema.SchemaReader, so one scanner serves every database whose DALgo
// adapter can read its own schema. PostgreSQL is the first.
//
// A SchemaReader describes a table by name and keeps native names and types out
// of reach, so what a scan can report is what the reader reports: the portable
// column type (a type the reader cannot map is a string), the column's default as
// the text of its expression, the primary key, foreign keys, unique constraints and
// indexes, under the exact names the reader returns.
//
// A SchemaReader cannot tell a view from a table, so unless the reader also is a
// ViewLister or a SchemaLister every collection is reported as a table. A reader
// that is a SchemaLister (the PostgreSQL reader of dalgo2postgres) is read in every
// schema it lists, not in one: a collection is in the schema its reference names, or
// else in the schema the provider was given, which is the only one a reader that
// lists no schema in its references reads, so for that reader a foreign key into
// another schema is left out of the scan.
//
// A scan opens connections to somebody's server, so the provider bounds how many
// reads and counts it has in flight at once (maxConcurrentReads): the core
// scanner starts a goroutine per table for each of them, and without a bound a
// schema of a few dozen tables would ask for more connections than a default
// PostgreSQL server grants.
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

	// dbTypeView is the DbType the scanner knows a view by.
	dbTypeView = "VIEW"

	// indexType names every index: the reader does not report the index method.
	indexType = "INDEX"

	// constraintTypeUnique is the type ListConstraints gives a UNIQUE constraint.
	constraintTypeUnique = "unique"

	// maxConcurrentReads is how many calls to the reader and the counter the
	// provider has in flight at once. Each call holds a connection of the scanned
	// server's pool, and the core scanner starts them without a limit.
	maxConcurrentReads = 4
)

// RecordsCounter reads the number of records of one table. A nil count with a
// nil error means the count is not available, which is not the same as zero.
type RecordsCounter interface {
	CountRecords(ctx context.Context, schema, table string) (*int, error)
}

// ViewLister is the optional capability of a reader that can tell a view from a
// table: it lists the collections that are views, each by the reference that
// ListCollections gave it (a view is among the collections ListCollections lists).
// A scan reports a collection it lists as a view, and does not count its records.
type ViewLister interface {
	ListViews(ctx context.Context) ([]dal.CollectionRef, error)
}

// SchemaLister is the optional capability of a reader that holds more than one schema:
// it lists the schemas, and the collections and the views of each, every reference
// naming the schema it is in. dbschema has no interface for it, so a reader declares
// these methods as its own: dalgo2postgres does. A scan reads every schema such a
// reader lists, in the order it lists them, and a collection it lists as a view is
// reported as a view.
type SchemaLister interface {
	ListSchemas(ctx context.Context) ([]string, error)
	ListSchemaCollections(ctx context.Context, schema string) ([]dal.CollectionRef, error)
	ListSchemaViews(ctx context.Context, schema string) ([]dal.CollectionRef, error)
}

// NewSchemaProvider returns a schemer.SchemaProvider that reads catalog through
// reader. schema is the schema the reader inspects: every collection whose
// reference does not name another is reported in it. counter may be nil, in which
// case no table has a record count.
//
// The provider is safe for the concurrent use the scanner makes of it: it reads
// each table's description, indexes and constraints once and shares them, and it
// never has more than maxConcurrentReads calls to reader and counter in flight.
func NewSchemaProvider(reader dbschema.SchemaReader, counter RecordsCounter, catalog, schema string) schemer.SchemaProvider {
	if reader == nil {
		panic("reader cannot be nil")
	}
	return &provider{reader: reader, counter: counter, catalog: catalog, schema: schema, slots: make(chan struct{}, maxConcurrentReads)}
}

type provider struct {
	reader  dbschema.SchemaReader
	counter RecordsCounter
	catalog string
	schema  string

	// slots holds one entry for every call to the reader or the counter in flight.
	slots chan struct{}

	defs    memo[*dbschema.CollectionDef]
	indexes memo[[]dbschema.IndexDef]
	schemas memo[[]string]

	listedMu sync.Mutex
	// listed is the collections the reader last listed, by schema and name (see key),
	// nil until it has.
	listed map[string]bool
}

// limited runs call once one of the slots is free, and releases the slot when it
// returns. It stops waiting when ctx ends. A call holds a slot only while it
// talks to the reader or the counter, never while it waits for another call, so
// no two calls can wait on each other.
func limited[V any](ctx context.Context, slots chan struct{}, call func() (V, error)) (V, error) {
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
		return call()
	case <-ctx.Done():
		var none V
		return none, ctx.Err()
	}
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
	return relationKey(p.schemaOf(ref), ref.Name())
}

// relationKey identifies a table or view of one schema.
func relationKey(schema, name string) string { return schema + "\x00" + name }

func (p *provider) describe(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return p.defs.get(p.key(ref), func() (*dbschema.CollectionDef, error) {
		def, err := limited(ctx, p.slots, func() (*dbschema.CollectionDef, error) { return p.reader.DescribeCollection(ctx, ref) })
		if err != nil {
			return nil, fmt.Errorf("describe %s: %w", ref.Name(), err)
		}
		return def, nil
	})
}

func (p *provider) listIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return p.indexes.get(p.key(ref), func() ([]dbschema.IndexDef, error) {
		indexes, err := limited(ctx, p.slots, func() ([]dbschema.IndexDef, error) { return p.reader.ListIndexes(ctx, ref) })
		if err != nil {
			return nil, fmt.Errorf("list indexes of %s: %w", ref.Name(), err)
		}
		return indexes, nil
	})
}

// listSchemas reads the schemas of a reader that lists them, once: the collections and the
// views of a scan are each listed schema by schema.
func (p *provider) listSchemas(ctx context.Context, lister SchemaLister) ([]string, error) {
	return p.schemas.get("schemas", func() ([]string, error) {
		schemas, err := limited(ctx, p.slots, func() ([]string, error) { return lister.ListSchemas(ctx) })
		if err != nil {
			return nil, fmt.Errorf("list schemas: %w", err)
		}
		return schemas, nil
	})
}

// listBySchema reads what list answers for each schema of the reader, in the order it lists the
// schemas. what names it in an error ("collections", "views").
func (p *provider) listBySchema(ctx context.Context, lister SchemaLister, what string, list func(schema string) ([]dal.CollectionRef, error)) ([]dal.CollectionRef, error) {
	schemas, err := p.listSchemas(ctx, lister)
	if err != nil {
		return nil, err
	}
	var refs []dal.CollectionRef
	for _, schema := range schemas {
		inSchema, err := limited(ctx, p.slots, func() ([]dal.CollectionRef, error) { return list(schema) })
		if err != nil {
			return nil, fmt.Errorf("list %s of schema %q: %w", what, schema, err)
		}
		refs = append(refs, inSchema...)
	}
	return refs, nil
}

// listCollections reads the collections the reader reports, of every schema when it lists
// them (see SchemaLister), and remembers their names, so a key to a collection that is not
// among them can be told apart.
func (p *provider) listCollections(ctx context.Context) ([]dal.CollectionRef, map[string]bool, error) {
	var refs []dal.CollectionRef
	var err error
	if lister, ok := p.reader.(SchemaLister); ok {
		refs, err = p.listBySchema(ctx, lister, "collections", func(schema string) ([]dal.CollectionRef, error) {
			return lister.ListSchemaCollections(ctx, schema)
		})
	} else {
		refs, err = limited(ctx, p.slots, func() ([]dal.CollectionRef, error) { return p.reader.ListCollections(ctx, nil) })
		if err != nil {
			err = fmt.Errorf("list collections: %w", err)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	names := make(map[string]bool, len(refs))
	for i := range refs {
		names[p.key(&refs[i])] = true
	}
	p.listedMu.Lock()
	p.listed = names
	p.listedMu.Unlock()
	return refs, names, nil
}

// isListed reports whether the reader lists a collection of that name in schema,
// reading the listing first if nobody has.
func (p *provider) isListed(ctx context.Context, schema, name string) (bool, error) {
	p.listedMu.Lock()
	names := p.listed
	p.listedMu.Unlock()
	if names == nil {
		var err error
		if _, names, err = p.listCollections(ctx); err != nil {
			return false, err
		}
	}
	return names[relationKey(schema, name)], nil
}

// listViews reads which of the collections the reader lists are views, by schema and
// name (see relationKey): of every schema when the reader lists them (see SchemaLister),
// of its own when it is a ViewLister, and none when it cannot tell.
func (p *provider) listViews(ctx context.Context) (map[string]bool, error) {
	var refs []dal.CollectionRef
	var err error
	switch reader := p.reader.(type) {
	case SchemaLister:
		refs, err = p.listBySchema(ctx, reader, "views", func(schema string) ([]dal.CollectionRef, error) {
			return reader.ListSchemaViews(ctx, schema)
		})
	case ViewLister:
		refs, err = limited(ctx, p.slots, func() ([]dal.CollectionRef, error) { return reader.ListViews(ctx) })
		if err != nil {
			err = fmt.Errorf("list views: %w", err)
		}
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	views := make(map[string]bool, len(refs))
	for i := range refs {
		views[p.key(&refs[i])] = true
	}
	return views, nil
}

// GetCollections lists every collection the reader reports: a table, or a view when
// the reader says it is one (see ViewLister), in the schema its reference names or
// else in the provider's own. A reference that names its schema stays the reference
// the columns of the collection are read through, so that two collections of one name
// in two schemas are two.
func (p *provider) GetCollections(ctx context.Context, _ *record.Key) (schemer.CollectionsReader, error) {
	refs, _, err := p.listCollections(ctx)
	if err != nil {
		return nil, err
	}
	views, err := p.listViews(ctx)
	if err != nil {
		return nil, err
	}
	collections := make([]*datatug.CollectionInfo, len(refs))
	for i := range refs {
		kind, dbType := datatug.CollectionTypeTable, dbTypeBaseTable
		if views[p.key(&refs[i])] {
			kind, dbType = datatug.CollectionTypeView, dbTypeView
		}
		key := datatug.NewCollectionKey(kind, refs[i].Name(), p.schemaOf(&refs[i]), p.catalog, nil)
		if refs[i].Schema() != "" {
			key.Ref = refs[i]
		}
		collections[i] = &datatug.CollectionInfo{DBCollectionKey: key, TableProps: datatug.TableProps{DbType: dbType}}
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
		column.Default = defaultText(field.Default)
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
// every key column twice. A foreign key into another schema is left out too, and
// so is one into a table of this schema the reader did not list (it lists the
// tables the role may use but reads every key from the catalogs): the scan holds
// the tables it was told about, and the scanner fails on a key whose table it
// does not have.
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
		listed, err := p.isListed(ctx, scanned, key.ReferencedCollection)
		if err != nil {
			return nil, err
		}
		if !listed {
			log.Printf("dalgoschema: foreign key %s of %s.%s is left out: it references %s.%s, which the reader did not list (the role may not see it)",
				key.Name, scanned, table, scanned, key.ReferencedCollection)
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
	listed, err := limited(ctx, p.slots, func() ([]dbschema.ConstraintDef, error) { return p.reader.ListConstraints(ctx, ref) })
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
	referrers, err := limited(ctx, p.slots, func() ([]dbschema.Referrer, error) { return p.reader.ListReferrers(ctx, p.ref(schema, table)) })
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
	return limited(ctx, p.slots, func() (*int, error) { return p.counter.CountRecords(ctx, schema, table) })
}

// defaultText is the text a column's default is saved as: the expression the reader
// reports. The PostgreSQL reader reports it as a DefaultLiteral whose value is the text of
// the SQL expression, which is kept as it is; the other values a DefaultLiteral holds are
// written as SQL writes them. nil when the column has no default, or has one of a kind
// the reader has no way to say (a pointer to a default, which no reader answers).
func defaultText(expr dbschema.DefaultExpr) *string {
	var text string
	switch typed := expr.(type) {
	case nil:
		return nil
	case dbschema.DefaultLiteral:
		switch value := typed.Value.(type) {
		case nil:
			text = "NULL"
		case string:
			text = value
		default:
			text = fmt.Sprint(value)
		}
	case dbschema.DefaultCurrentTimestamp:
		text = "CURRENT_TIMESTAMP"
	default:
		return nil // a kind of default this scan does not know is not recorded as an empty one
	}
	return &text
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
