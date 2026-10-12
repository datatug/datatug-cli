// Package dbcompare compares the schemas and complete rows of two DALgo
// databases. It keeps provider values lossless and delegates keyed row
// matching and field-delta classification to DALgo recordops.
package dbcompare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/recordops"
	"github.com/dal-go/record"
	_ "modernc.org/sqlite"
)

type Options struct {
	Details     bool
	DetailLimit int
	LeftSchema  string
	RightSchema string
	TableMaps   map[string]string
	KeyMaps     map[string][]string
}

type Report struct {
	LeftName       string           `json:"leftName"`
	RightName      string           `json:"rightName"`
	SchemaChanges  []string         `json:"schemaChanges"`
	Warnings       []string         `json:"warnings,omitempty"`
	Relations      []RelationReport `json:"relations"`
	Summary        DifferenceCounts `json:"summary"`
	DetailsLimited bool             `json:"detailsLimited"`
}

type DifferenceCounts struct {
	Added     int64 `json:"added"`
	Removed   int64 `json:"removed"`
	Changed   int64 `json:"changed"`
	Unchanged int64 `json:"unchanged"`
}

type RelationReport struct {
	Name           string           `json:"name"`
	RightName      string           `json:"rightName,omitempty"`
	Kind           string           `json:"kind"`
	Identity       string           `json:"identity"`
	LeftRows       int64            `json:"leftRows"`
	RightRows      int64            `json:"rightRows"`
	Counts         DifferenceCounts `json:"counts"`
	Details        []RecordDiff     `json:"details,omitempty"`
	DetailsLimited bool             `json:"detailsLimited,omitempty"`
}

type RecordDiff struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Fields []FieldDiff `json:"fields,omitempty"`
}

type FieldDiff struct {
	Name         string `json:"name"`
	Before       any    `json:"before"`
	After        any    `json:"after"`
	BeforeAbsent bool   `json:"beforeAbsent,omitempty"`
	AfterAbsent  bool   `json:"afterAbsent,omitempty"`
}

type relation struct {
	ref          dal.CollectionRef
	def          dbschema.CollectionDef
	indexesKnown bool
}

type relationInventory struct {
	collections map[string]relation
	views       map[string]dbschema.SourceViewDef
	adapterName string
	warnings    []string
}

type relationPair struct {
	name      string
	rightName string
	left      relation
	right     relation
	hasLeft   bool
	hasRight  bool
}

type stagedRow struct {
	Values []normalizedValue `json:"values"`
}

type rowStore struct {
	db      *sql.DB
	tempDir string
	count   int64
	keyed   bool
}

func (s *rowStore) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	if s.db != nil {
		closeErr = s.db.Close()
	}
	removeErr := os.RemoveAll(s.tempDir)
	if removeErr != nil {
		removeErr = errors.New("comparison temporary row files could not be removed")
	}
	return errors.Join(closeErr, removeErr)
}

func newRowStore(keyed bool) (*rowStore, error) {
	dir, err := os.MkdirTemp("", "datatug-compare-")
	if err != nil {
		return nil, errors.New("could not create comparison row staging")
	}
	path := filepath.Join(dir, "rows.sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.New("could not create comparison row spool")
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.New("could not initialize comparison row spool")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.New("could not open comparison row spool")
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA cache_size = -8192`,
		`PRAGMA temp_store = FILE`,
		`PRAGMA journal_mode = OFF`,
		`PRAGMA synchronous = OFF`,
		`CREATE TABLE staged_rows(sort_key BLOB NOT NULL, payload BLOB NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			_ = os.RemoveAll(dir)
			return nil, errors.New("could not initialize comparison row spool")
		}
	}
	return &rowStore{db: db, tempDir: dir, keyed: keyed}, nil
}

func rowCount(store *rowStore) int64 {
	if store == nil {
		return 0
	}
	return store.count
}

type normalizedValue struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Null   bool   `json:"null,omitempty"`
	Absent bool   `json:"absent,omitempty"`
}

var decimalLexeme = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var integerLexeme = regexp.MustCompile(`^[+-]?[0-9]+$`)

// Compare compares the union of both relation inventories and reads every row
// from every common collection. DetailLimit affects only retained examples; it
// never stops reading either source.
func Compare(ctx context.Context, leftName string, left dal.DB, rightName string, right dal.DB, options Options) (Report, error) {
	report := Report{
		LeftName: leftName, RightName: rightName,
		SchemaChanges: []string{}, Warnings: []string{}, Relations: []RelationReport{},
	}
	if left == nil || right == nil {
		return report, errors.New("compare requires two open DALgo databases")
	}
	if options.DetailLimit < 0 {
		return report, errors.New("detail limit cannot be negative")
	}
	if adapter := left.Adapter(); adapter != nil && adapter.Name() == "dalgo2postgres" && strings.TrimSpace(options.LeftSchema) == "" {
		return report, errors.New("left PostgreSQL schema must be explicit to avoid search_path ambiguity")
	}
	if adapter := right.Adapter(); adapter != nil && adapter.Name() == "dalgo2postgres" && strings.TrimSpace(options.RightSchema) == "" {
		return report, errors.New("right PostgreSQL schema must be explicit to avoid search_path ambiguity")
	}
	leftInventory, err := inspect(ctx, left, options.LeftSchema)
	if err != nil {
		return report, fmt.Errorf("inspect left database %q: %w", leftName, err)
	}
	rightInventory, err := inspect(ctx, right, options.RightSchema)
	if err != nil {
		return report, fmt.Errorf("inspect right database %q: %w", rightName, err)
	}
	pairs, err := matchRelations(leftInventory.collections, rightInventory.collections, options.TableMaps)
	if err != nil {
		return report, err
	}
	appendInventoryChanges(&report, leftInventory, rightInventory, pairs)
	report.Warnings = append(report.Warnings, leftInventory.warnings...)
	report.Warnings = append(report.Warnings, rightInventory.warnings...)

	knownNames := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		knownNames[pair.name] = true
	}
	for name := range options.KeyMaps {
		if !knownNames[name] {
			return report, fmt.Errorf("comparison key mapping names unknown relation %q", name)
		}
	}

	remainingDetails := options.DetailLimit
	for _, pair := range pairs {
		name := pair.name
		leftRelation, hasLeft := pair.left, pair.hasLeft
		rightRelation, hasRight := pair.right, pair.hasRight
		relationResult := RelationReport{Name: name, Kind: "relation"}
		if hasRight && pair.rightName != pair.name {
			relationResult.RightName = pair.rightName
		}
		var leftRows, rightRows *rowStore
		var key []string
		var err error
		switch {
		case hasLeft && hasRight:
			changes, compatible := compareRelationSchema(leftRelation.def, rightRelation.def, name, leftRelation.indexesKnown && rightRelation.indexesKnown)
			report.SchemaChanges = append(report.SchemaChanges, changes...)
			if !compatible {
				return report, fmt.Errorf("compare collection %q: shared columns have incompatible DALgo types", name)
			}
			key, err = comparisonKey(name, leftRelation.def, rightRelation.def, options.KeyMaps)
			if err != nil {
				return report, err
			}
			leftRows, err = readRows(ctx, left, leftRelation.ref, leftRelation.def, rightRelation.def, key)
			if err == nil {
				rightRows, err = readRows(ctx, right, rightRelation.ref, rightRelation.def, leftRelation.def, key)
			}
		case hasLeft:
			key, err = comparisonKey(name, leftRelation.def, dbschema.CollectionDef{}, options.KeyMaps)
			if err != nil {
				return report, err
			}
			leftRows, err = readRows(ctx, left, leftRelation.ref, leftRelation.def, dbschema.CollectionDef{}, key)
		case hasRight:
			key, err = comparisonKey(name, dbschema.CollectionDef{}, rightRelation.def, options.KeyMaps)
			if err != nil {
				return report, err
			}
			rightRows, err = readRows(ctx, right, rightRelation.ref, dbschema.CollectionDef{}, rightRelation.def, key)
		}
		if err != nil {
			_ = leftRows.Close()
			_ = rightRows.Close()
			return report, fmt.Errorf("read collection %q: %w", name, err)
		}
		switch {
		case len(key) == 0:
			relationResult.Identity = "multiset"
		case options.KeyMaps[name] != nil:
			relationResult.Identity = "explicit-key"
		default:
			relationResult.Identity = "primary-key"
		}
		relationResult.LeftRows = rowCount(leftRows)
		relationResult.RightRows = rowCount(rightRows)
		perRelationLimit := -1
		if options.Details && remainingDetails > 0 {
			perRelationLimit = remainingDetails
		} else if options.Details {
			perRelationLimit = 0
		}
		columns := relationColumns(leftRelation.def, rightRelation.def)
		counts, details, limited, err := diffRows(ctx, leftRows, rightRows, columns, key, perRelationLimit)
		closeErr := errors.Join(leftRows.Close(), rightRows.Close())
		if err != nil {
			return report, fmt.Errorf("compare rows in collection %q: %w", name, err)
		}
		if closeErr != nil {
			return report, fmt.Errorf("close comparison spool for collection %q: %w", name, closeErr)
		}
		relationResult.Counts = counts
		relationResult.Details = details
		relationResult.DetailsLimited = limited
		if options.Details {
			remainingDetails -= len(details)
			if remainingDetails < 0 {
				remainingDetails = 0
			}
		}
		report.DetailsLimited = report.DetailsLimited || limited
		report.Summary.Added += counts.Added
		report.Summary.Removed += counts.Removed
		report.Summary.Changed += counts.Changed
		report.Summary.Unchanged += counts.Unchanged
		report.Relations = append(report.Relations, relationResult)
	}
	sort.Strings(report.SchemaChanges)
	return report, nil
}

func inspect(ctx context.Context, db dal.DB, querySchema string) (relationInventory, error) {
	refs, err := dbschema.ListCollections(ctx, db, nil)
	if err != nil {
		return relationInventory{}, fmt.Errorf("list collections: %w", err)
	}
	inventory := relationInventory{collections: make(map[string]relation, len(refs)), views: map[string]dbschema.SourceViewDef{}}
	if adapter := db.Adapter(); adapter != nil {
		inventory.adapterName = adapter.Name()
	}
	viewNames := map[string]bool{}
	if views, ok := dal.As[dbschema.SourceViewReader](db); ok {
		definitions, viewErr := views.ListSourceViews(ctx)
		if viewErr != nil {
			return relationInventory{}, fmt.Errorf("list views: %w", viewErr)
		}
		for _, view := range definitions {
			if view.Name == "" || viewNames[view.Name] {
				return relationInventory{}, fmt.Errorf("source returned an empty or duplicate view name %q", view.Name)
			}
			viewNames[view.Name] = true
			inventory.views[view.Name] = view
		}
	} else {
		adapterName := inventory.adapterName
		if adapterName == "" {
			adapterName = "database"
		}
		inventory.warnings = append(inventory.warnings, fmt.Sprintf("view inventory is unsupported by the %s adapter; views were not compared", adapterName))
	}
	for _, ref := range refs {
		if ref.Name() == "" {
			return relationInventory{}, errors.New("source returned an empty collection name")
		}
		if viewNames[ref.Name()] {
			continue
		}
		name := relationName(ref)
		if _, duplicate := inventory.collections[name]; duplicate {
			return relationInventory{}, fmt.Errorf("source returned duplicate collection %q", name)
		}
		definition, describeErr := dbschema.DescribeCollection(ctx, db, &ref)
		if describeErr != nil {
			return relationInventory{}, fmt.Errorf("describe %q: %w", name, describeErr)
		}
		if definition == nil || definition.Name != ref.Name() {
			return relationInventory{}, fmt.Errorf("collection %q returned inconsistent schema metadata", name)
		}
		queryRef := ref
		if querySchema != "" && inventory.adapterName == "dalgo2postgres" {
			// WithSchema scopes PostgreSQL catalog discovery but deliberately does
			// not change search_path. Qualify the row query with the selected schema
			// so it cannot read a same-named table from another schema.
			queryRef = dal.NewQualifiedRootCollectionRef(querySchema, ref.Name(), "")
		}
		fieldNames := make(map[string]bool, len(definition.Fields))
		for _, field := range definition.Fields {
			fieldName := string(field.Name)
			if fieldName == "" || fieldNames[fieldName] {
				return relationInventory{}, fmt.Errorf("collection %q returned an empty or duplicate field name %q", name, fieldName)
			}
			fieldNames[fieldName] = true
		}
		indexes, indexErr := dbschema.ListIndexes(ctx, db, &ref)
		if indexErr != nil {
			if errors.Is(indexErr, dal.ErrNotSupported) {
				inventory.warnings = append(inventory.warnings, fmt.Sprintf("index metadata for %s is unsupported by the source adapter; index differences were not compared", name))
				inventory.collections[name] = relation{ref: queryRef, def: *definition}
				continue
			}
			return relationInventory{}, fmt.Errorf("list indexes for %q: %w", name, indexErr)
		}
		definition.Indexes = mergeIndexes(definition.Indexes, indexes)
		inventory.collections[name] = relation{ref: queryRef, def: *definition, indexesKnown: true}
	}
	return inventory, nil
}

func relationName(ref dal.CollectionRef) string {
	if ref.Schema() != "" {
		return ref.Schema() + "." + ref.Name()
	}
	return ref.Name()
}

func mergeIndexes(primary, extra []dbschema.IndexDef) []dbschema.IndexDef {
	seen := make(map[string]bool, len(primary)+len(extra))
	result := append([]dbschema.IndexDef(nil), primary...)
	for _, index := range primary {
		seen[indexSignature(index)] = true
	}
	for _, index := range extra {
		signature := indexSignature(index)
		if !seen[signature] {
			result = append(result, index)
			seen[signature] = true
		}
	}
	return result
}

func indexSignature(index dbschema.IndexDef) string {
	fields := make([]string, len(index.Fields))
	for i, field := range index.Fields {
		fields[i] = string(field)
	}
	return fmt.Sprintf("%t:%s", index.Unique, strings.Join(fields, "\x00"))
}

func matchRelations(left, right map[string]relation, mappings map[string]string) ([]relationPair, error) {
	leftNames := make([]string, 0, len(left))
	for name := range left {
		leftNames = append(leftNames, name)
	}
	sort.Strings(leftNames)
	usedRight := make(map[string]string, len(mappings))
	for leftName, rightName := range mappings {
		if _, ok := left[leftName]; !ok {
			return nil, fmt.Errorf("table mapping names unknown left relation %q", leftName)
		}
		if _, ok := right[rightName]; !ok {
			return nil, fmt.Errorf("table mapping %q=%q names unknown right relation %q", leftName, rightName, rightName)
		}
		if previous, exists := usedRight[rightName]; exists {
			return nil, fmt.Errorf("table mappings for %q and %q both target right relation %q", previous, leftName, rightName)
		}
		usedRight[rightName] = leftName
	}
	pairs := make([]relationPair, 0, len(left)+len(right))
	matchedRight := make(map[string]bool, len(right))
	for _, leftName := range leftNames {
		rightName, explicitlyMapped := mappings[leftName]
		if !explicitlyMapped {
			if _, claimed := usedRight[leftName]; claimed {
				// Do not silently pair this same-name left relation to a right relation
				// explicitly assigned to another left relation.
				pairs = append(pairs, relationPair{name: leftName, left: left[leftName], hasLeft: true})
				continue
			}
			if _, sameName := right[leftName]; sameName {
				rightName = leftName
			}
		}
		if rightName == "" {
			pairs = append(pairs, relationPair{name: leftName, left: left[leftName], hasLeft: true})
			continue
		}
		pairs = append(pairs, relationPair{name: leftName, rightName: rightName, left: left[leftName], right: right[rightName], hasLeft: true, hasRight: true})
		matchedRight[rightName] = true
	}
	rightNames := make([]string, 0, len(right))
	for name := range right {
		rightNames = append(rightNames, name)
	}
	sort.Strings(rightNames)
	for _, rightName := range rightNames {
		if !matchedRight[rightName] {
			pairs = append(pairs, relationPair{name: rightName, rightName: rightName, right: right[rightName], hasRight: true})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })
	return pairs, nil
}

func appendInventoryChanges(report *Report, left, right relationInventory, pairs []relationPair) {
	for _, pair := range pairs {
		if pair.hasLeft && !pair.hasRight {
			report.SchemaChanges = append(report.SchemaChanges, fmt.Sprintf("relation only in %s: %s", report.LeftName, pair.name))
		} else if pair.hasRight && !pair.hasLeft {
			report.SchemaChanges = append(report.SchemaChanges, fmt.Sprintf("relation only in %s: %s", report.RightName, pair.name))
		}
	}
	for name, leftView := range left.views {
		rightView, ok := right.views[name]
		if !ok {
			report.SchemaChanges = append(report.SchemaChanges, fmt.Sprintf("view only in %s: %s", report.LeftName, name))
			continue
		}
		if !reflect.DeepEqual(leftView.Columns, rightView.Columns) {
			report.SchemaChanges = append(report.SchemaChanges, fmt.Sprintf("view %s has different ordered columns", name))
		}
		if left.adapterName != "" && left.adapterName == right.adapterName && leftView.CreateSQL != "" && rightView.CreateSQL != "" && strings.TrimSpace(leftView.CreateSQL) != strings.TrimSpace(rightView.CreateSQL) {
			report.SchemaChanges = append(report.SchemaChanges, fmt.Sprintf("view %s definition differs", name))
		}
	}
	for name := range right.views {
		if _, ok := left.views[name]; !ok {
			report.SchemaChanges = append(report.SchemaChanges, fmt.Sprintf("view only in %s: %s", report.RightName, name))
		}
	}
}

func compareRelationSchema(left, right dbschema.CollectionDef, name string, indexesKnown bool) ([]string, bool) {
	changes := []string{}
	compatible := true
	leftFields, rightFields := fieldsByName(left.Fields), fieldsByName(right.Fields)
	fieldNames := make([]string, 0, len(leftFields)+len(rightFields))
	seen := map[string]bool{}
	for field := range leftFields {
		fieldNames = append(fieldNames, field)
		seen[field] = true
	}
	for field := range rightFields {
		if !seen[field] {
			fieldNames = append(fieldNames, field)
		}
	}
	sort.Strings(fieldNames)
	for _, field := range fieldNames {
		lf, hasLeft := leftFields[field]
		rf, hasRight := rightFields[field]
		switch {
		case !hasLeft:
			changes = append(changes, fmt.Sprintf("%s.%s exists only in %s", name, field, "right"))
		case !hasRight:
			changes = append(changes, fmt.Sprintf("%s.%s exists only in %s", name, field, "left"))
		default:
			if lf.Type != rf.Type {
				changes = append(changes, fmt.Sprintf("%s.%s type differs: %s vs %s", name, field, lf.Type, rf.Type))
				compatible = false
			}
			if lf.Nullable != rf.Nullable {
				changes = append(changes, fmt.Sprintf("%s.%s nullability differs", name, field))
			}
			if !equalOptionalInt(lf.Length, rf.Length) {
				changes = append(changes, fmt.Sprintf("%s.%s length differs", name, field))
			}
			if !equalPrecision(lf.Precision, rf.Precision) {
				changes = append(changes, fmt.Sprintf("%s.%s decimal precision differs", name, field))
			}
			if !reflect.DeepEqual(lf.Default, rf.Default) {
				changes = append(changes, fmt.Sprintf("%s.%s default differs", name, field))
			}
			if lf.AutoIncrement != rf.AutoIncrement {
				changes = append(changes, fmt.Sprintf("%s.%s auto-increment differs", name, field))
			}
		}
	}
	leftOrder, rightOrder := fieldOrder(left.Fields), fieldOrder(right.Fields)
	if len(leftOrder) == len(rightOrder) && !reflect.DeepEqual(leftOrder, rightOrder) {
		changes = append(changes, fmt.Sprintf("%s column order differs", name))
	}
	if !equalStrings(leftPK(left), leftPK(right)) {
		changes = append(changes, fmt.Sprintf("%s primary key differs", name))
	}
	if !equalStringLists(foreignKeySignatures(left.ForeignKeys), foreignKeySignatures(right.ForeignKeys)) {
		changes = append(changes, fmt.Sprintf("%s foreign keys differ", name))
	}
	if indexesKnown && !equalStringLists(indexSignatures(left.Indexes), indexSignatures(right.Indexes)) {
		changes = append(changes, fmt.Sprintf("%s indexes differ", name))
	}
	changes = append(changes, declaredTypeChanges(left.SourceDefinition, right.SourceDefinition, name)...)
	return changes, compatible
}

func relationColumns(left, right dbschema.CollectionDef) []string {
	columns := make([]string, 0, len(left.Fields)+len(right.Fields))
	seen := make(map[string]bool, cap(columns))
	for _, field := range left.Fields {
		name := string(field.Name)
		if !seen[name] {
			columns = append(columns, name)
			seen[name] = true
		}
	}
	for _, field := range right.Fields {
		name := string(field.Name)
		if !seen[name] {
			columns = append(columns, name)
			seen[name] = true
		}
	}
	sort.Strings(columns)
	return columns
}

func declaredTypeChanges(left, right *dbschema.SourceDefinition, collection string) []string {
	if left == nil || right == nil {
		return nil
	}
	leftColumns := make(map[string]string, len(left.Columns))
	rightColumns := make(map[string]string, len(right.Columns))
	for _, column := range left.Columns {
		leftColumns[column.Name] = strings.ToLower(strings.TrimSpace(column.DeclaredType))
	}
	for _, column := range right.Columns {
		rightColumns[column.Name] = strings.ToLower(strings.TrimSpace(column.DeclaredType))
	}
	columnNames := make([]string, 0, len(leftColumns))
	for name := range leftColumns {
		columnNames = append(columnNames, name)
	}
	sort.Strings(columnNames)
	changes := make([]string, 0)
	for _, name := range columnNames {
		leftType := leftColumns[name]
		if rightType, ok := rightColumns[name]; ok && leftType != "" && rightType != "" && leftType != rightType {
			changes = append(changes, fmt.Sprintf("%s.%s declared source type differs: %s vs %s", collection, name, leftType, rightType))
		}
	}
	return changes
}

func fieldsByName(fields []dbschema.FieldDef) map[string]dbschema.FieldDef {
	result := make(map[string]dbschema.FieldDef, len(fields))
	for _, field := range fields {
		result[string(field.Name)] = field
	}
	return result
}

func fieldOrder(fields []dbschema.FieldDef) []string {
	result := make([]string, len(fields))
	for i, field := range fields {
		result[i] = string(field.Name)
	}
	return result
}

func leftPK(def dbschema.CollectionDef) []string {
	result := make([]string, len(def.PrimaryKey))
	for i, field := range def.PrimaryKey {
		result[i] = string(field)
	}
	return result
}

func foreignKeySignatures(keys []dbschema.ForeignKeyDef) []string {
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		fields, referenced := make([]string, len(key.Fields)), make([]string, len(key.ReferencedFields))
		for i, field := range key.Fields {
			fields[i] = string(field)
		}
		for i, field := range key.ReferencedFields {
			referenced[i] = string(field)
		}
		result = append(result, strings.Join([]string{key.ReferencedNamespace, key.ReferencedCollection, strings.Join(fields, "\x00"), strings.Join(referenced, "\x00"), string(key.Enforcement), key.OnUpdate, key.OnDelete}, "\x01"))
	}
	sort.Strings(result)
	return result
}

func indexSignatures(indexes []dbschema.IndexDef) []string {
	result := make([]string, 0, len(indexes))
	for _, index := range indexes {
		result = append(result, indexSignature(index))
	}
	sort.Strings(result)
	return result
}

func equalOptionalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalPrecision(a, b *dbschema.Precision) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalStrings(a, b []string) bool     { return reflect.DeepEqual(a, b) }
func equalStringLists(a, b []string) bool { return reflect.DeepEqual(a, b) }

func sameUsablePrimaryKey(left, right dbschema.CollectionDef) []string {
	leftKey, rightKey := leftPK(left), leftPK(right)
	if len(leftKey) == 0 || !equalStrings(leftKey, rightKey) {
		return nil
	}
	leftFields, rightFields := fieldsByName(left.Fields), fieldsByName(right.Fields)
	for _, name := range leftKey {
		lf, lok := leftFields[name]
		rf, rok := rightFields[name]
		if !lok || !rok || lf.Type != rf.Type || lf.Nullable || rf.Nullable {
			return nil
		}
	}
	return leftKey
}

func comparisonKey(name string, left, right dbschema.CollectionDef, mappings map[string][]string) ([]string, error) {
	key, explicit := mappings[name]
	if !explicit {
		return sameUsablePrimaryKey(left, right), nil
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("comparison key mapping for %q is empty", name)
	}
	leftFields, rightFields := fieldsByName(left.Fields), fieldsByName(right.Fields)
	seen := make(map[string]bool, len(key))
	for _, fieldName := range key {
		if fieldName == "" || seen[fieldName] {
			return nil, fmt.Errorf("comparison key mapping for %q has an empty or duplicate column", name)
		}
		seen[fieldName] = true
		lf, hasLeft := leftFields[fieldName]
		rf, hasRight := rightFields[fieldName]
		if (len(left.Fields) > 0 && !hasLeft) || (len(right.Fields) > 0 && !hasRight) {
			return nil, fmt.Errorf("comparison key mapping for %q names missing column %q", name, fieldName)
		}
		if hasLeft && hasRight && lf.Type != rf.Type {
			return nil, fmt.Errorf("comparison key column %q in %q has incompatible types", fieldName, name)
		}
	}
	return append([]string(nil), key...), nil
}

func readRows(ctx context.Context, db dal.DB, ref dal.CollectionRef, own dbschema.CollectionDef, other dbschema.CollectionDef, key []string) (*rowStore, error) {
	ownFields, otherFields := fieldsByName(own.Fields), fieldsByName(other.Fields)
	union := make([]string, 0, len(own.Fields)+len(other.Fields))
	seen := map[string]bool{}
	for _, field := range own.Fields {
		union = append(union, string(field.Name))
		seen[string(field.Name)] = true
	}
	for _, field := range other.Fields {
		if !seen[string(field.Name)] {
			union = append(union, string(field.Name))
		}
	}
	sort.Strings(union)
	for fieldName := range ownFields {
		if otherField, ok := otherFields[fieldName]; ok && ownFields[fieldName].Type != otherField.Type {
			return nil, fmt.Errorf("column %q has incompatible source types %s and %s", fieldName, ownFields[fieldName].Type, otherField.Type)
		}
	}
	store, err := newRowStore(len(key) > 0)
	if err != nil {
		return nil, err
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = store.Close()
		}
	}()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("could not begin comparison row staging")
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO staged_rows(sort_key, payload) VALUES (?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return nil, errors.New("could not prepare comparison row staging")
	}
	appendRow := func(values map[string]any) error {
		cells := make([]normalizedValue, len(union))
		for i, fieldName := range union {
			definition, exists := ownFields[fieldName]
			if !exists {
				cells[i] = normalizedValue{Kind: otherFields[fieldName].Type.String(), Absent: true}
				continue
			}
			raw, present := values[fieldName]
			if !present {
				return fmt.Errorf("row is missing declared column %q", fieldName)
			}
			cell, err := normalizeValueForSource(definition, sourceDialect(own.SourceDefinition), declaredType(own.SourceDefinition, fieldName), raw)
			if err != nil {
				return fmt.Errorf("column %q: %w", fieldName, err)
			}
			cells[i] = cell
		}
		keyCells := make([]normalizedValue, 0, len(key))
		if len(key) > 0 {
			for _, fieldName := range key {
				index := sort.SearchStrings(union, fieldName)
				if index == len(union) || union[index] != fieldName {
					return fmt.Errorf("comparison key column %q is absent", fieldName)
				}
				if cells[index].Null || cells[index].Absent {
					return fmt.Errorf("comparison key column %q contains NULL or is absent", fieldName)
				}
				keyCells = append(keyCells, cells[index])
			}
		}
		sortValues := cells
		if len(keyCells) > 0 {
			sortValues = keyCells
		}
		sortKey, err := json.Marshal(sortValues)
		if err != nil {
			return errors.New("could not encode normalized comparison key")
		}
		payload, err := json.Marshal(stagedRow{Values: cells})
		if err != nil {
			return errors.New("could not encode normalized comparison row")
		}
		if _, err := statement.ExecContext(ctx, sortKey, payload); err != nil {
			return errors.New("could not stage normalized comparison row")
		}
		store.count++
		return nil
	}
	var sourceErr error
	if sourceRows, ok := dal.As[dbschema.SourceRowsReader](db); ok {
		cursor, err := sourceRows.OpenSourceRows(ctx, &ref)
		if err != nil {
			sourceErr = fmt.Errorf("open physical rows: %w", err)
		} else {
			for {
				row, nextErr := cursor.Next()
				if errors.Is(nextErr, io.EOF) {
					break
				}
				if nextErr != nil {
					sourceErr = fmt.Errorf("read physical row: %w", nextErr)
					break
				}
				if appendErr := appendRow(row.Values); appendErr != nil {
					sourceErr = appendErr
					break
				}
			}
			if closeErr := cursor.Close(); sourceErr == nil && closeErr != nil {
				sourceErr = fmt.Errorf("close physical rows: %w", closeErr)
			}
		}
	} else {
		reader, err := db.ExecuteQueryToRecordsReader(ctx, dal.NewQueryBuilder(dal.From(ref)).SelectIntoRecordset())
		if err != nil {
			sourceErr = fmt.Errorf("open row reader: %w", err)
		} else {
			for {
				rec, nextErr := reader.Next()
				if errors.Is(nextErr, io.EOF) || errors.Is(nextErr, dal.ErrNoMoreRecords) {
					break
				}
				if nextErr != nil {
					sourceErr = fmt.Errorf("read row: %w", nextErr)
					break
				}
				values, ok := rec.Data().(map[string]any)
				if !ok {
					sourceErr = fmt.Errorf("row data has type %T; want map[string]any", rec.Data())
					break
				}
				if appendErr := appendRow(values); appendErr != nil {
					sourceErr = appendErr
					break
				}
			}
			if closeErr := reader.Close(); sourceErr == nil && closeErr != nil {
				sourceErr = fmt.Errorf("close row reader: %w", closeErr)
			}
		}
	}
	if sourceErr != nil {
		_ = statement.Close()
		_ = tx.Rollback()
		return nil, sourceErr
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		return nil, errors.New("could not finish comparison row staging")
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("could not commit comparison row staging")
	}
	if _, err := store.db.ExecContext(ctx, `CREATE INDEX staged_rows_sort ON staged_rows(sort_key)`); err != nil {
		return nil, errors.New("could not index comparison row staging")
	}
	cleanupOnError = false
	return store, nil
}

func normalizeValue(field dbschema.FieldDef, raw any) (normalizedValue, error) {
	return normalizeValueForSource(field, "", "", raw)
}

func declaredType(source *dbschema.SourceDefinition, field string) string {
	if source == nil {
		return ""
	}
	for _, column := range source.Columns {
		if column.Name == field {
			return column.DeclaredType
		}
	}
	return ""
}

func sourceDialect(source *dbschema.SourceDefinition) string {
	if source == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(source.Dialect))
}

func normalizeValueForSource(field dbschema.FieldDef, dialect, declared string, raw any) (normalizedValue, error) {
	if raw == nil {
		return normalizedValue{Kind: field.Type.String(), Null: true}, nil
	}
	switch field.Type {
	case dbschema.Bool:
		if value, ok := raw.(bool); ok {
			if value {
				return normalizedValue{Kind: "bool", Text: "1"}, nil
			}
			return normalizedValue{Kind: "bool", Text: "0"}, nil
		}
		if integer, ok := integerString(raw); ok && (integer == "0" || integer == "1") {
			return normalizedValue{Kind: "bool", Text: integer}, nil
		}
		return normalizedValue{}, fmt.Errorf("unsupported boolean value %T", raw)
	case dbschema.Int:
		value, ok := integerString(raw)
		if !ok {
			return normalizedValue{}, fmt.Errorf("unsupported integer value %T", raw)
		}
		return normalizedValue{Kind: "int", Text: value}, nil
	case dbschema.Decimal:
		value, ok := decimalString(raw)
		if !ok || !decimalLexeme.MatchString(value) {
			return normalizedValue{}, fmt.Errorf("unsupported decimal value %T", raw)
		}
		rat := new(big.Rat)
		if _, ok := rat.SetString(value); !ok {
			return normalizedValue{}, fmt.Errorf("invalid decimal value")
		}
		canonical, ok := canonicalDecimal(rat)
		if !ok {
			return normalizedValue{}, fmt.Errorf("decimal value has no finite decimal representation")
		}
		return normalizedValue{Kind: "decimal", Text: canonical}, nil
	case dbschema.Float:
		value, ok := floatValue(raw)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return normalizedValue{}, fmt.Errorf("unsupported floating-point value %T", raw)
		}
		if value == 0 {
			value = 0
		}
		return normalizedValue{Kind: "float", Text: strconv.FormatUint(math.Float64bits(value), 16)}, nil
	case dbschema.String:
		value, ok := raw.(string)
		if !ok {
			return normalizedValue{}, fmt.Errorf("unsupported text value %T", raw)
		}
		return normalizedValue{Kind: "string", Text: value}, nil
	case dbschema.Bytes:
		value, ok := raw.([]byte)
		if !ok {
			return normalizedValue{}, fmt.Errorf("unsupported binary value %T", raw)
		}
		return normalizedValue{Kind: "bytes", Text: hex.EncodeToString(value)}, nil
	case dbschema.Time:
		value, ok := timeValueForDeclaredType(raw, dialect, declared)
		if !ok {
			return normalizedValue{}, fmt.Errorf("unsupported temporal value %T", raw)
		}
		return normalizedValue{Kind: "time", Text: value}, nil
	default:
		return normalizedValue{}, fmt.Errorf("unsupported schema type %s", field.Type)
	}
}

func canonicalDecimal(value *big.Rat) (string, bool) {
	if value == nil {
		return "", false
	}
	denominator := new(big.Int).Set(value.Denom())
	two, five, one := big.NewInt(2), big.NewInt(5), big.NewInt(1)
	powers := []int{0, 0}
	for index, prime := range []*big.Int{two, five} {
		quotient, remainder := new(big.Int), new(big.Int)
		for {
			quotient.QuoRem(denominator, prime, remainder)
			if remainder.Sign() != 0 {
				break
			}
			denominator.Set(quotient)
			powers[index]++
		}
	}
	if denominator.Cmp(one) != 0 {
		return "", false
	}
	scale := powers[0]
	if powers[1] > scale {
		scale = powers[1]
	}
	text := value.FloatString(scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "-0" || text == "" {
		text = "0"
	}
	return text, true
}

func integerString(value any) (string, bool) {
	switch v := value.(type) {
	case int:
		return strconv.FormatInt(int64(v), 10), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case big.Int:
		return v.String(), true
	case *big.Int:
		if v != nil {
			return v.String(), true
		}
	case string:
		if integerLexeme.MatchString(v) {
			i, ok := new(big.Int).SetString(v, 10)
			if ok {
				return i.String(), true
			}
		}
	case []byte:
		text := string(v)
		if integerLexeme.MatchString(text) {
			i, ok := new(big.Int).SetString(text, 10)
			if ok {
				return i.String(), true
			}
		}
	case json.Number:
		i, ok := new(big.Int).SetString(string(v), 10)
		if ok {
			return i.String(), true
		}
	case float64:
		// Even an integral float outside this interval may already have lost
		// source integer precision before it reached the DALgo reader. Refuse it
		// instead of treating the rounded value as an exact integer.
		const maxExactFloatInteger = float64(1<<53 - 1)
		if math.IsInf(v, 0) || math.IsNaN(v) || v > maxExactFloatInteger || v < -maxExactFloatInteger {
			return "", false
		}
		rat := new(big.Rat).SetFloat64(v)
		if rat == nil || !rat.IsInt() {
			return "", false
		}
		return rat.Num().String(), true
	}
	return "", false
}

func decimalString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, v != ""
	case []byte:
		return string(v), len(v) > 0
	case json.Number:
		return string(v), v != ""
	case big.Int:
		return v.String(), true
	case *big.Int:
		if v != nil {
			return v.String(), true
		}
	default:
		return integerString(v)
	}
	return "", false
}

func floatValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		text, _ := integerString(v)
		i, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return 0, false
		}
		r := new(big.Rat).SetInt(i)
		f, _ := r.Float64()
		if new(big.Rat).SetFloat64(f).Cmp(r) != 0 {
			return 0, false
		}
		return f, true
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return 0, false
		}
		canonical := strconv.FormatFloat(f, 'g', -1, 64)
		parsed, parsedOK := new(big.Rat).SetString(string(v))
		canonicalValue, canonicalOK := new(big.Rat).SetString(canonical)
		return f, parsedOK && canonicalOK && parsed.Cmp(canonicalValue) == 0
	}
	return 0, false
}

func timeValueForDeclaredType(value any, dialect, declared string) (string, bool) {
	declared = strings.ToLower(strings.TrimSpace(declared))
	dialect = strings.ToLower(strings.TrimSpace(dialect))
	if strings.HasPrefix(declared, "date") {
		return dateValue(value)
	}
	if declared == "time" || strings.HasPrefix(declared, "time ") {
		return timeOfDayValue(value, strings.Contains(declared, "with time zone"))
	}
	if declared == "timestamp with time zone" || declared == "timestamptz" || declared == "timestamp_tz" || (declared == "timestamp" && dialect == "bigquery") {
		// PostgreSQL timestamp without time zone is a wall-clock value; BigQuery
		// TIMESTAMP and explicit zone-bearing timestamp types are instants.
		return instantValue(value)
	}
	if strings.HasPrefix(declared, "datetime") || declared == "timestamp" || declared == "timestamp without time zone" || declared == "timestamp_ntz" {
		return wallTimestampValue(value)
	}
	if declared == "" {
		return inferredTimeValue(value)
	}
	return "", false
}

func temporalText(value any) (string, time.Time, bool, bool) {
	if parsed, ok := value.(time.Time); ok {
		return "", parsed, true, true
	}
	text, ok := value.(string)
	if !ok {
		if raw, bytesOK := value.([]byte); bytesOK {
			text, ok = string(raw), true
		}
	}
	return text, time.Time{}, ok, false
}

func dateValue(value any) (string, bool) {
	text, parsed, ok, asTime := temporalText(value)
	if !ok {
		return "", false
	}
	if asTime {
		return parsed.Format("2006-01-02"), true
	}
	day, err := time.Parse("2006-01-02", text)
	if err != nil || day.Format("2006-01-02") != text {
		return "", false
	}
	return text, true
}

func timeOfDayValue(value any, withZone bool) (string, bool) {
	text, parsed, ok, asTime := temporalText(value)
	if !ok {
		return "", false
	}
	if asTime {
		layout := "15:04:05.999999999"
		if withZone {
			layout += "Z07:00"
		}
		return parsed.Format(layout), true
	}
	layouts := []string{"15:04:05.999999999", "15:04:05"}
	if withZone {
		layouts = []string{"15:04:05.999999999Z07:00", "15:04:05Z07:00"}
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			return parsed.Format(layout), true
		}
	}
	return "", false
}

func wallTimestampValue(value any) (string, bool) {
	text, parsed, ok, asTime := temporalText(value)
	if !ok {
		return "", false
	}
	const layout = "2006-01-02T15:04:05.999999999"
	if asTime {
		return parsed.Format(layout), true
	}
	for _, inputLayout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", layout} {
		parsed, err := time.Parse(inputLayout, text)
		if err == nil {
			return parsed.Format(layout), true
		}
	}
	return "", false
}

func instantValue(value any) (string, bool) {
	if parsed, ok := value.(time.Time); ok {
		return parsed.UTC().Format(time.RFC3339Nano), true
	}
	text, _, ok, asTime := temporalText(value)
	if !ok || asTime {
		return "", false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05Z07:00"} {
		parsed, err := time.Parse(layout, text)
		if err == nil && strings.ContainsAny(text, "Zz+-") {
			return parsed.UTC().Format(time.RFC3339Nano), true
		}
	}
	return "", false
}

func inferredTimeValue(value any) (string, bool) {
	if parsed, ok := value.(time.Time); ok {
		return parsed.UTC().Format(time.RFC3339Nano), true
	}
	text, _, ok, asTime := temporalText(value)
	if !ok || asTime {
		return "", false
	}
	if strings.ContainsAny(text, "Zz+-") && (strings.Contains(text, "T") || strings.Contains(text, " ")) {
		return instantValue(text)
	}
	if len(text) == len("2006-01-02") {
		return dateValue(text)
	}
	if strings.Contains(text, ":") && !strings.Contains(text, "-") {
		return timeOfDayValue(text, false)
	}
	return wallTimestampValue(text)
}

func diffRows(ctx context.Context, left, right *rowStore, columns, key []string, detailLimit int) (DifferenceCounts, []RecordDiff, bool, error) {
	leftSeq := rowSequence(ctx, left, columns)
	rightSeq := rowSequence(ctx, right, columns)
	counts := DifferenceCounts{}
	details := make([]RecordDiff, 0)
	limited := false
	for diff, err := range recordops.Diff[string](leftSeq, []recordops.RecordSeq[string]{rightSeq}, recordops.WithIncludeMatched()) {
		if err != nil {
			return counts, details, limited, err
		}
		candidate := diff.Candidates[0]
		status := ""
		switch candidate.Status {
		case recordops.Missing:
			counts.Removed++
			status = "removed"
		case recordops.Extra:
			counts.Added++
			status = "added"
		case recordops.Changed:
			counts.Changed++
			status = "changed"
		case recordops.Matched:
			counts.Unchanged++
			continue
		default:
			return counts, details, limited, fmt.Errorf("unknown recordops status %d", candidate.Status)
		}
		if detailLimit < 0 {
			continue
		}
		if detailLimit == 0 {
			limited = true
			continue
		}
		if len(details) >= detailLimit {
			limited = true
			continue
		}
		id := displayID(diff.ID, key)
		details = append(details, RecordDiff{ID: id, Status: status, Fields: fieldDiffs(diff.Baseline, candidate)})
	}
	return counts, details, limited, nil
}

func fieldDiffs(baseline *recordops.RecordSnapshot, candidate recordops.CandidateState) []FieldDiff {
	if candidate.Status == recordops.Missing {
		if baseline == nil {
			return nil
		}
		result := make([]FieldDiff, 0, len(baseline.Fields))
		for _, field := range baseline.Fields {
			result = append(result, FieldDiff{Name: field.Name, Before: publicValue(field.Value), AfterAbsent: true})
		}
		return result
	}
	if candidate.Status == recordops.Extra {
		result := make([]FieldDiff, 0, len(candidate.Fields))
		for _, field := range candidate.Fields {
			result = append(result, FieldDiff{Name: field.Name, After: publicValue(field.Value), BeforeAbsent: true})
		}
		return result
	}
	if candidate.Status != recordops.Changed || baseline == nil {
		return nil
	}
	before := make(map[string]any, len(baseline.Fields))
	for _, field := range baseline.Fields {
		before[field.Name] = field.Value
	}
	result := make([]FieldDiff, 0, len(candidate.Fields))
	for _, field := range candidate.Fields {
		previous, present := before[field.Name]
		result = append(result, FieldDiff{Name: field.Name, Before: publicValue(previous), After: publicValue(field.Value), BeforeAbsent: !present, AfterAbsent: field.Absent})
	}
	return result
}

func publicValue(value any) any {
	normalized, ok := value.(normalizedValue)
	if !ok {
		return value
	}
	if normalized.Null || normalized.Absent {
		return nil
	}
	switch normalized.Kind {
	case "bool":
		return normalized.Text == "1"
	case "bytes":
		return "0x" + normalized.Text
	case "int", "decimal", "string", "time":
		return normalized.Text
	case "float":
		bits, err := strconv.ParseUint(normalized.Text, 16, 64)
		if err == nil {
			return math.Float64frombits(bits)
		}
	}
	return normalized.Text
}

func displayValue(value normalizedValue) string {
	if value.Absent {
		return "<absent>"
	}
	if value.Null {
		return "null"
	}
	switch value.Kind {
	case "string":
		return strconv.Quote(value.Text)
	case "bytes":
		return "0x" + value.Text
	case "bool":
		return value.Text
	case "int", "decimal":
		return value.Text
	case "float":
		bits, err := strconv.ParseUint(value.Text, 16, 64)
		if err != nil {
			return "<invalid-float>"
		}
		return strconv.FormatFloat(math.Float64frombits(bits), 'g', -1, 64)
	default:
		return value.Text
	}
}

func rowSequence(ctx context.Context, store *rowStore, columns []string) recordops.RecordSeq[string] {
	return func(yield func(record.WithID[string], error) bool) {
		if store == nil {
			return
		}
		rows, err := store.db.QueryContext(ctx, `SELECT sort_key, payload FROM staged_rows ORDER BY sort_key, rowid`)
		if err != nil {
			var zero record.WithID[string]
			yield(zero, errors.New("could not read comparison row spool"))
			return
		}
		defer func() { _ = rows.Close() }()
		var previous []byte
		occurrence := int64(0)
		for rows.Next() {
			var sortKey, payload []byte
			if err := rows.Scan(&sortKey, &payload); err != nil {
				var zero record.WithID[string]
				yield(zero, errors.New("could not scan comparison row spool"))
				return
			}
			var row stagedRow
			if err := json.Unmarshal(payload, &row); err != nil {
				var zero record.WithID[string]
				yield(zero, errors.New("could not decode comparison row spool"))
				return
			}
			id := hex.EncodeToString(sortKey)
			if !store.keyed {
				if bytes.Equal(previous, sortKey) {
					occurrence++
				} else {
					previous = append(previous[:0], sortKey...)
					occurrence = 1
				}
				id += "!" + fmt.Sprintf("%020d", occurrence)
			}
			data := make(map[string]any, len(columns))
			for i, column := range columns {
				if i < len(row.Values) {
					data[column] = row.Values[i]
				}
			}
			rec := record.NewRecordWithoutKey(data)
			if !yield(record.WithID[string]{ID: id, Record: rec}, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			var zero record.WithID[string]
			yield(zero, errors.New("comparison row spool read failed"))
		}
	}
}

func displayID(id string, key []string) string {
	sortKey := id
	occurrence := ""
	if len(key) == 0 {
		if prefix, suffix, ok := strings.Cut(id, "!"); ok {
			sortKey, occurrence = prefix, suffix
		}
	}
	encoded, err := hex.DecodeString(sortKey)
	if err != nil {
		return id
	}
	if len(key) == 0 {
		digest := sha256.Sum256(encoded)
		return fmt.Sprintf("keyless row sha256:%x occurrence:%s", digest, strings.TrimLeft(occurrence, "0"))
	}
	var values []normalizedValue
	if err := json.Unmarshal(encoded, &values); err != nil || len(values) != len(key) {
		return id
	}
	parts := make([]string, len(key))
	for i, name := range key {
		parts[i] = name + "=" + displayValue(values[i])
	}
	return strings.Join(parts, ", ")
}
