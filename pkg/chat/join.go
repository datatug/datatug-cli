package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
)

// RelationInstanceID identifies a node in the DTQL source tree. It is stable
// for a given document: root is "root" and child joins append their index.
type RelationInstanceID string

// JoinCandidateID is opaque. Callers must pass the value selected from the
// catalog back to ApplyJoinCandidate instead of reconstructing an edge.
type JoinCandidateID string

type RelationInstance struct {
	ID       RelationInstanceID
	Schema   string
	Relation string
	Alias    string
}

// ForeignKey is source-scoped schema evidence. Fields are paired by index and
// are intentionally not flattened: a composite FK remains one relationship.
type ForeignKey struct {
	ConstraintID string
	Schema       string
	FromRelation string
	FromFields   []string
	ToSchema     string
	ToRelation   string
	ToFields     []string
}

type ForeignKeySnapshot struct {
	Source string
	Keys   []ForeignKey
	// Columns contains the source's ordered physical columns, keyed by
	// lowercase schema.relation. It is used to expand wildcards before a JOIN.
	Columns map[string][]string
}

type JoinFieldPair struct{ SourceField, TargetField string }

type JoinCandidate struct {
	ID           JoinCandidateID
	Source       RelationInstance
	Target       RelationInstance
	ConstraintID string
	Direction    string // outgoing or incoming, relative to Source
	Cardinality  string // many-to-one or one-to-many, relative to Source
	Fields       []JoinFieldPair
	Evidence     string // foreign-key
}

// JoinApplication is the single domain seam used by terminal and agent code.
// It keeps candidate discovery and execution out of UI/model code.
type JoinApplication interface {
	Candidates(context.Context, RecordSet) ([]JoinCandidate, error)
	Apply(context.Context, RecordSet, JoinCandidateID) (QueryResult, error)
}

// ForeignKeyJoinApplication is the production-neutral implementation. Its
// snapshot is source-bound and it delegates every read to the existing secure
// DTQL executor; it never constructs SQL or joins displayed rows locally.
// When Secure is true, every existing source is preflighted for unrestricted
// readability before a JOIN is exposed or executed.
type ForeignKeyJoinApplication struct {
	Source   string
	Snapshot ForeignKeySnapshot
	// Refresh is called for candidate exposure and again immediately before
	// apply. It is normally LoadSQLiteForeignKeySnapshot bound to the selected
	// source; a stale edge never falls back to the startup snapshot.
	Refresh       func(context.Context) (ForeignKeySnapshot, error)
	CanReadTarget func(context.Context, RelationInstance) error
	Executor      DTQLExecutor
	Secure        bool
}

func (a ForeignKeyJoinApplication) Candidates(ctx context.Context, record RecordSet) ([]JoinCandidate, error) {
	if a.Source == "" || record.Source != a.Source {
		return []JoinCandidate{}, nil
	}
	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := dtql.Deserialize([]byte(record.DTQL))
	if err != nil {
		return nil, err
	}
	if a.Secure && !a.canExpandQuery(ctx, parent.From()) {
		return []JoinCandidate{}, nil
	}
	candidates, err := DiscoverJoinCandidates([]byte(record.DTQL), snapshot, appliedEdges(record)...)
	if err != nil {
		return nil, err
	}
	if a.Secure && a.CanReadTarget == nil {
		return []JoinCandidate{}, nil
	}
	if a.CanReadTarget == nil {
		return candidates, nil
	}
	allowed := make([]JoinCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if a.CanReadTarget(ctx, candidate.Target) == nil {
			allowed = append(allowed, candidate)
		}
	}
	return allowed, nil
}

func (a ForeignKeyJoinApplication) Apply(ctx context.Context, record RecordSet, id JoinCandidateID) (QueryResult, error) {
	return a.apply(ctx, record, id, dal.JoinInner)
}

// ApplyAttached preserves rows from a fresh root query when optional attached
// metadata is joined. Interactive JOIN actions keep their existing semantics.
func (a ForeignKeyJoinApplication) ApplyAttached(ctx context.Context, record RecordSet, id JoinCandidateID) (QueryResult, error) {
	return a.apply(ctx, record, id, dal.JoinLeft)
}

func (a ForeignKeyJoinApplication) apply(ctx context.Context, record RecordSet, id JoinCandidateID, joinType dal.JoinType) (QueryResult, error) {
	if a.Executor == nil {
		return QueryResult{}, fmt.Errorf("JOIN exploration has no secure query executor")
	}
	if a.Source == "" || record.Source != a.Source {
		return QueryResult{}, fmt.Errorf("selected foreign-key edge is not available for this source")
	}
	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return QueryResult{}, err
	}
	q, err := dtql.Deserialize([]byte(record.DTQL))
	if err != nil {
		return QueryResult{}, fmt.Errorf("parse RecordSet DTQL: %w", err)
	}
	if a.Secure && !a.canExpandQuery(ctx, q.From()) {
		return QueryResult{}, fmt.Errorf("JOIN exploration cannot prove every existing source is fully readable under this policy")
	}
	doc, candidate, err := deriveJoinDTQL([]byte(record.DTQL), snapshot, id, joinType, appliedEdges(record)...)
	if err != nil {
		return QueryResult{}, err
	}
	if a.Secure && a.CanReadTarget == nil {
		return QueryResult{}, fmt.Errorf("JOIN exploration is unavailable until target policy access is proven")
	}
	if a.CanReadTarget != nil && a.CanReadTarget(ctx, candidate.Target) != nil {
		return QueryResult{}, fmt.Errorf("selected JOIN target is not readable by this policy")
	}
	result, err := a.Executor.RunDTQL(ctx, a.Source, doc, record.Parameters)
	if err != nil {
		return QueryResult{}, err
	}
	edges := append([]AppliedJoinEdge(nil), appliedEdges(record)...)
	edges = append(edges, AppliedJoinEdge{JoinPath: candidate.Target.ID, SourcePath: candidate.Source.ID, ConstraintID: candidate.ConstraintID, Direction: candidate.Direction, CandidateID: id, Fields: append([]JoinFieldPair(nil), candidate.Fields...)})
	return QueryResult{Title: record.Title + " + " + candidate.Target.Relation, DTQL: string(doc), Result: result, Parameters: record.Parameters, Source: a.Source, Lineage: &JoinLineage{ParentRecordSetID: record.ID, CandidateID: id, AppliedEdges: edges}}, nil
}

func appliedEdges(record RecordSet) []AppliedJoinEdge {
	if record.Lineage == nil {
		return nil
	}
	return record.Lineage.AppliedEdges
}

// DALgo's current policy authorization visits the root and flat JOINs. For
// a query that already has JOINs, DataTug separately proves unrestricted
// read access to every participating source before deriving a deeper JOIN.
// A denied, row-limited or field-limited source keeps this path fail-closed.
func (a ForeignKeyJoinApplication) canExpandQuery(ctx context.Context, from dal.FromSource) bool {
	if a.CanReadTarget == nil {
		return false
	}
	for _, instance := range relationInstances(from) {
		if err := a.CanReadTarget(ctx, instance); err != nil {
			return false
		}
	}
	return true
}

func (a ForeignKeyJoinApplication) snapshot(ctx context.Context) (ForeignKeySnapshot, error) {
	if a.Refresh != nil {
		return a.Refresh(ctx)
	}
	if a.Snapshot.Source != a.Source {
		return ForeignKeySnapshot{}, fmt.Errorf("foreign-key metadata is unavailable for this source")
	}
	return a.Snapshot, nil
}

// LoadSQLiteForeignKeySnapshot reads SQLite's authoritative PRAGMA metadata.
// It retains PRAGMA id/seq rather than fabricating names, and groups every
// composite constraint atomically in deterministic table/id/sequence order.
func LoadSQLiteForeignKeySnapshot(ctx context.Context, source string, db *sql.DB) (ForeignKeySnapshot, error) {
	if db == nil || source == "" {
		return ForeignKeySnapshot{}, fmt.Errorf("SQLite FK metadata requires a source and database")
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return ForeignKeySnapshot{}, err
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return ForeignKeySnapshot{}, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return ForeignKeySnapshot{}, err
	}
	snapshot := ForeignKeySnapshot{Source: source, Keys: []ForeignKey{}, Columns: map[string][]string{}}
	primaryKeys := map[string][]string{}
	for _, table := range tables {
		pragma := "PRAGMA table_info('" + strings.ReplaceAll(table, "'", "''") + "')"
		columnRows, queryErr := db.QueryContext(ctx, pragma)
		if queryErr != nil {
			return ForeignKeySnapshot{}, queryErr
		}
		type primaryColumn struct {
			position int
			name     string
		}
		var primary []primaryColumn
		for columnRows.Next() {
			var cid, notNull, pk int
			var name, dbType string
			var defaultValue any
			if scanErr := columnRows.Scan(&cid, &name, &dbType, &notNull, &defaultValue, &pk); scanErr != nil {
				_ = columnRows.Close()
				return ForeignKeySnapshot{}, scanErr
			}
			snapshot.Columns[relationKey("main", table)] = append(snapshot.Columns[relationKey("main", table)], name)
			if pk > 0 {
				primary = append(primary, primaryColumn{pk, name})
			}
		}
		if rowsErr := columnRows.Err(); rowsErr != nil {
			_ = columnRows.Close()
			return ForeignKeySnapshot{}, rowsErr
		}
		_ = columnRows.Close()
		sort.Slice(primary, func(i, j int) bool { return primary[i].position < primary[j].position })
		for _, column := range primary {
			primaryKeys[relationKey("main", table)] = append(primaryKeys[relationKey("main", table)], column.name)
		}
	}
	for _, table := range tables {
		pragma := "PRAGMA foreign_key_list('" + strings.ReplaceAll(table, "'", "''") + "')"
		fkRows, err := db.QueryContext(ctx, pragma)
		if err != nil {
			return ForeignKeySnapshot{}, err
		}
		byID := map[int]*ForeignKey{}
		byIDSequence := map[int]map[int]JoinFieldPair{}
		for fkRows.Next() {
			var id, seq int
			var target, from, to, onUpdate, onDelete, match string
			if err := fkRows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				_ = fkRows.Close()
				return ForeignKeySnapshot{}, err
			}
			fk := byID[id]
			if fk == nil {
				fk = &ForeignKey{ConstraintID: fmt.Sprintf("sqlite:%s:%d", table, id), Schema: "main", FromRelation: table, ToSchema: "main", ToRelation: target}
				byID[id] = fk
			}
			if byIDSequence[id] == nil {
				byIDSequence[id] = map[int]JoinFieldPair{}
			}
			byIDSequence[id][seq] = JoinFieldPair{SourceField: from, TargetField: to}
		}
		if err := fkRows.Err(); err != nil {
			_ = fkRows.Close()
			return ForeignKeySnapshot{}, err
		}
		_ = fkRows.Close()
		var ids []int
		for id := range byID {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			fk := byID[id]
			sequences := make([]int, 0, len(byIDSequence[id]))
			for sequence := range byIDSequence[id] {
				sequences = append(sequences, sequence)
			}
			sort.Ints(sequences)
			for i, sequence := range sequences {
				pair := byIDSequence[id][sequence]
				fk.FromFields = append(fk.FromFields, pair.SourceField)
				targetField := pair.TargetField
				if targetField == "" {
					pk := primaryKeys[relationKey("main", fk.ToRelation)]
					if i >= len(pk) {
						return ForeignKeySnapshot{}, fmt.Errorf("foreign key %s has no resolvable target primary key", fk.ConstraintID)
					}
					targetField = pk[i]
				}
				fk.ToFields = append(fk.ToFields, targetField)
			}
			snapshot.Keys = append(snapshot.Keys, *fk)
		}
	}
	return snapshot, nil
}

func relationKey(schema, relation string) string {
	if schema == "" {
		schema = "main"
	}
	return strings.ToLower(schema + "." + relation)
}

// DiscoverJoinCandidates walks only the query's actual relation tree. It does
// not recursively walk metadata targets, so cyclic schemas cannot recurse.
func DiscoverJoinCandidates(doc []byte, snapshot ForeignKeySnapshot, applied ...AppliedJoinEdge) ([]JoinCandidate, error) {
	q, err := dtql.Deserialize(doc)
	if err != nil {
		return nil, fmt.Errorf("parse RecordSet DTQL: %w", err)
	}
	instances := relationInstances(q.From())
	active := activeJoinPairs(q.From(), applied)
	var out []JoinCandidate
	for _, instance := range instances {
		for _, fk := range snapshot.Keys {
			if len(fk.FromFields) == 0 || len(fk.FromFields) != len(fk.ToFields) || fk.ConstraintID == "" {
				continue // incomplete metadata is never guessed
			}
			appendCandidate := func(target RelationInstance, direction, cardinality string, pairs []JoinFieldPair) {
				target.ID = RelationInstanceID(string(instance.ID) + "/candidate/" + direction + "/" + fk.ConstraintID)
				candidate := JoinCandidate{Source: instance, Target: target, ConstraintID: fk.ConstraintID, Direction: direction, Cardinality: cardinality, Fields: pairs, Evidence: "foreign-key"}
				candidate.ID = candidateID(candidate)
				if !active[candidatePairKey(instance.ID, target.Schema, target.Relation, pairs)] && !isAppliedCandidate(candidate, applied) {
					out = append(out, candidate)
				}
			}
			if sameRelation(instance.Schema, instance.Relation, fk.Schema, fk.FromRelation) {
				pairs := make([]JoinFieldPair, len(fk.FromFields))
				for i := range fk.FromFields {
					pairs[i] = JoinFieldPair{fk.FromFields[i], fk.ToFields[i]}
				}
				appendCandidate(RelationInstance{Schema: fk.ToSchema, Relation: fk.ToRelation}, "outgoing", "many-to-one", pairs)
			}
			if sameRelation(instance.Schema, instance.Relation, fk.ToSchema, fk.ToRelation) {
				pairs := make([]JoinFieldPair, len(fk.FromFields))
				for i := range fk.FromFields {
					pairs[i] = JoinFieldPair{fk.ToFields[i], fk.FromFields[i]}
				}
				appendCandidate(RelationInstance{Schema: fk.Schema, Relation: fk.FromRelation}, "incoming", "one-to-many", pairs)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeriveJoinDTQL derives a real AST join. It refuses a stale candidate rather
// than accepting caller-provided field names or a replacement relation.
func DeriveJoinDTQL(parent []byte, snapshot ForeignKeySnapshot, id JoinCandidateID, applied ...AppliedJoinEdge) ([]byte, JoinCandidate, error) {
	return deriveJoinDTQL(parent, snapshot, id, dal.JoinInner, applied...)
}

func deriveJoinDTQL(parent []byte, snapshot ForeignKeySnapshot, id JoinCandidateID, joinType dal.JoinType, applied ...AppliedJoinEdge) ([]byte, JoinCandidate, error) {
	candidates, err := DiscoverJoinCandidates(parent, snapshot, applied...)
	if err != nil {
		return nil, JoinCandidate{}, err
	}
	var candidate JoinCandidate
	for _, current := range candidates {
		if current.ID == id {
			candidate = current
			break
		}
	}
	if candidate.ID == "" {
		return nil, JoinCandidate{}, fmt.Errorf("selected foreign-key edge is stale or unavailable")
	}
	q, err := dtql.Deserialize(parent)
	if err != nil {
		return nil, JoinCandidate{}, fmt.Errorf("parse RecordSet DTQL: %w", err)
	}
	if len(q.GroupBy()) > 0 || q.Having() != nil || queryHasAggregate(q) {
		return nil, JoinCandidate{}, fmt.Errorf("cannot add a JOIN to an aggregate query without changing its grouping")
	}
	aliases := map[string]bool{}
	for _, instance := range relationInstances(q.From()) {
		name := instance.Alias
		if name == "" {
			name = instance.Relation
		}
		aliases[strings.ToLower(name)] = true
	}
	targetAlias := candidate.Target.Relation
	for n := 2; aliases[strings.ToLower(targetAlias)]; n++ {
		targetAlias = fmt.Sprintf("%s%d", candidate.Target.Relation, n)
	}
	q, err = prepareJoinProjection(q, snapshot, candidate, targetAlias)
	if err != nil {
		return nil, JoinCandidate{}, err
	}
	source := candidate.Source.Alias
	if source == "" {
		source = candidate.Source.Relation
	}
	target := dal.NewRootCollectionRef(candidate.Target.Relation, targetAlias)
	if candidate.Target.Schema != "" && !strings.EqualFold(candidate.Target.Schema, "main") {
		target = dal.NewQualifiedRootCollectionRef(candidate.Target.Schema, candidate.Target.Relation, targetAlias)
	}
	on := make([]dal.Condition, len(candidate.Fields))
	for i, pair := range candidate.Fields {
		on[i] = dal.NewComparison(dal.NewFieldRef(source, pair.SourceField), dal.Equal, dal.NewFieldRef(targetAlias, pair.TargetField))
	}
	node := fromAtPath(q.From(), candidate.Source.ID)
	if node == nil {
		return nil, JoinCandidate{}, fmt.Errorf("selected relation instance is stale")
	}
	candidate.Target.ID = RelationInstanceID(fmt.Sprintf("%s/%d", candidate.Source.ID, len(node.Joins())))
	node.Join(dal.NewJoinedSource(target, joinType, on...))
	derived, err := dtql.Serialize(q)
	if err != nil {
		return nil, JoinCandidate{}, fmt.Errorf("derive JOIN DTQL: %w", err)
	}
	if _, err := dtql.Deserialize(derived); err != nil {
		return nil, JoinCandidate{}, fmt.Errorf("derived JOIN is invalid: %w", err)
	}
	return derived, candidate, nil
}

// joinPreparedQuery overrides only the expressions that need qualification.
// The original DALgo query retains filters, grouping, ordering, limit and
// offset; Serialize validates the complete derived AST before execution.
type joinPreparedQuery struct {
	dal.StructuredQuery
	columns []dal.Column
	where   dal.Condition
	order   []dal.OrderExpression
}

func (q joinPreparedQuery) Columns() []dal.Column          { return q.columns }
func (q joinPreparedQuery) Where() dal.Condition           { return q.where }
func (q joinPreparedQuery) OrderBy() []dal.OrderExpression { return q.order }

func prepareJoinProjection(q dal.StructuredQuery, snapshot ForeignKeySnapshot, candidate JoinCandidate, targetAlias string) (dal.StructuredQuery, error) {
	instances := relationInstances(q.From())
	if len(instances) == 0 {
		return nil, fmt.Errorf("query has no relation instance")
	}
	root := instances[0]
	rootAlias := root.Alias
	if rootAlias == "" {
		rootAlias = root.Relation
	}
	single := len(instances) == 1
	columns := make([]dal.Column, 0, len(q.Columns())+16)
	if len(q.Columns()) == 0 {
		if !single {
			return nil, fmt.Errorf("cannot add a JOIN to an implicit wildcard over multiple relations")
		}
		baseColumns := snapshot.Columns[relationKey(root.Schema, root.Relation)]
		if len(baseColumns) == 0 {
			return nil, fmt.Errorf("cannot expand source columns: schema metadata is unavailable")
		}
		for _, name := range baseColumns {
			columns = append(columns, dal.Column{Expression: dal.NewFieldRef(rootAlias, name)})
		}
	} else {
		for _, column := range q.Columns() {
			if column.Wildcard != nil {
				wildcardSource := column.Wildcard.Source
				if wildcardSource == "" {
					if !single {
						return nil, fmt.Errorf("cannot add a JOIN to an unqualified multi-relation wildcard")
					}
					wildcardSource = rootAlias
				}
				var instance *RelationInstance
				for i := range instances {
					alias := instances[i].Alias
					if alias == "" {
						alias = instances[i].Relation
					}
					if strings.EqualFold(alias, wildcardSource) {
						instance = &instances[i]
						break
					}
				}
				if instance == nil {
					return nil, fmt.Errorf("wildcard source is not in the query")
				}
				names := snapshot.Columns[relationKey(instance.Schema, instance.Relation)]
				if len(names) == 0 {
					return nil, fmt.Errorf("cannot expand wildcard: schema metadata is unavailable")
				}
				for _, name := range names {
					if column.Wildcard.Excludes(name) {
						continue
					}
					alias := ""
					if !single {
						alias = wildcardSource + "_" + name
					}
					columns = append(columns, dal.Column{Expression: dal.NewFieldRef(wildcardSource, name), Alias: alias})
				}
				continue
			}
			expr, err := qualifyJoinExpression(column.Expression, rootAlias, single)
			if err != nil {
				return nil, fmt.Errorf("cannot add JOIN to selected column: %w", err)
			}
			column.Expression = expr
			columns = append(columns, column)
		}
	}
	targetColumns := snapshot.Columns[relationKey(candidate.Target.Schema, candidate.Target.Relation)]
	if len(targetColumns) == 0 {
		return nil, fmt.Errorf("cannot add JOIN: target column metadata is unavailable")
	}
	outputNames := map[string]bool{}
	for _, column := range columns {
		name := column.Alias
		if name == "" {
			if field, ok := column.Expression.(dal.FieldRef); ok {
				name = field.Name()
			}
		}
		if name != "" {
			outputNames[strings.ToLower(name)] = true
		}
	}
	for _, name := range targetColumns {
		alias := targetAlias + "_" + name
		for suffix := 2; outputNames[strings.ToLower(alias)]; suffix++ {
			alias = fmt.Sprintf("%s_%s_%d", targetAlias, name, suffix)
		}
		outputNames[strings.ToLower(alias)] = true
		columns = append(columns, dal.Column{Expression: dal.NewFieldRef(targetAlias, name), Alias: alias})
	}
	where, err := qualifyJoinCondition(q.Where(), rootAlias, single)
	if err != nil {
		return nil, fmt.Errorf("cannot add JOIN to filter: %w", err)
	}
	order := make([]dal.OrderExpression, len(q.OrderBy()))
	for i, item := range q.OrderBy() {
		expr, err := qualifyJoinExpression(item.Expression(), rootAlias, single)
		if err != nil {
			return nil, fmt.Errorf("cannot add JOIN to ordering: %w", err)
		}
		if item.Descending() {
			order[i] = dal.Descending(expr)
		} else {
			order[i] = dal.Ascending(expr)
		}
	}
	return joinPreparedQuery{StructuredQuery: q, columns: columns, where: where, order: order}, nil
}

func qualifyJoinCondition(condition dal.Condition, source string, single bool) (dal.Condition, error) {
	if condition == nil {
		return nil, nil
	}
	switch value := condition.(type) {
	case dal.Comparison:
		left, err := qualifyJoinExpression(value.Left, source, single)
		if err != nil {
			return nil, err
		}
		right, err := qualifyJoinExpression(value.Right, source, single)
		if err != nil {
			return nil, err
		}
		return dal.NewComparison(left, value.Operator, right), nil
	case dal.GroupCondition:
		items := make([]dal.Condition, len(value.Conditions()))
		for i, child := range value.Conditions() {
			var err error
			items[i], err = qualifyJoinCondition(child, source, single)
			if err != nil {
				return nil, err
			}
		}
		return dal.NewGroupCondition(value.Operator(), items...), nil
	default:
		return nil, fmt.Errorf("unsupported condition %T", condition)
	}
}

func qualifyJoinExpression(expression dal.Expression, source string, single bool) (dal.Expression, error) {
	if expression == nil {
		return nil, nil
	}
	switch value := expression.(type) {
	case dal.FieldRef:
		if value.Source() != "" {
			return value, nil
		}
		if !single {
			return nil, fmt.Errorf("unqualified field %q in a multi-relation query", value.Name())
		}
		return dal.NewFieldRef(source, value.Name()), nil
	case dal.BinaryExpression:
		left, err := qualifyJoinExpression(value.Left, source, single)
		if err != nil {
			return nil, err
		}
		right, err := qualifyJoinExpression(value.Right, source, single)
		if err != nil {
			return nil, err
		}
		return dal.Binary(left, value.Operator, right), nil
	case dal.AggregateFunc:
		args := make([]dal.Expression, len(value.FuncArgs()))
		for i, arg := range value.FuncArgs() {
			var err error
			args[i], err = qualifyJoinExpression(arg, source, single)
			if err != nil {
				return nil, err
			}
		}
		distinct := false
		if named, ok := value.(dal.DistinctAggregateFunc); ok {
			distinct = named.IsDistinct()
		}
		return dal.NewAggregate(value.FuncName(), distinct, args...), nil
	default:
		return expression, nil // DTQL already validated constants, arrays, params and stars.
	}
}

func queryHasAggregate(q dal.StructuredQuery) bool {
	var has func(dal.Expression) bool
	has = func(expression dal.Expression) bool {
		switch value := expression.(type) {
		case dal.AggregateFunc:
			return true
		case dal.BinaryExpression:
			return has(value.Left) || has(value.Right)
		default:
			return false
		}
	}
	for _, column := range q.Columns() {
		if has(column.Expression) {
			return true
		}
	}
	for _, order := range q.OrderBy() {
		if has(order.Expression()) {
			return true
		}
	}
	return false
}

func fromAtPath(from dal.FromSource, id RelationInstanceID) dal.FromSource {
	if id == "root" {
		return from
	}
	path := strings.TrimPrefix(string(id), "root/")
	node := from
	for _, part := range strings.Split(path, "/") {
		var index int
		if _, err := fmt.Sscan(part, &index); err != nil || index < 0 || index >= len(node.Joins()) {
			return nil
		}
		child := node.Joins()[index].From()
		if child == nil {
			return nil
		}
		node = child
	}
	return node
}

func relationInstances(from dal.FromSource) []RelationInstance {
	var out []RelationInstance
	var walk func(dal.FromSource, string)
	walk = func(node dal.FromSource, path string) {
		base := node.Base()
		instance := RelationInstance{ID: RelationInstanceID(path), Relation: base.Name(), Alias: base.Alias()}
		if qualified, ok := base.(dal.CollectionRef); ok {
			instance.Schema = qualified.Schema()
		}
		out = append(out, instance)
		for i, join := range node.Joins() {
			child := join.From()
			if child == nil {
				child = dal.From(join.RecordsetSource)
			}
			walk(child, fmt.Sprintf("%s/%d", path, i))
		}
	}
	walk(from, "root")
	return out
}

func sameRelation(schema, relation, otherSchema, otherRelation string) bool {
	if schema == "" {
		schema = "main"
	}
	if otherSchema == "" {
		otherSchema = "main"
	}
	return strings.EqualFold(schema, otherSchema) && strings.EqualFold(relation, otherRelation)
}

func candidateID(c JoinCandidate) JoinCandidateID {
	parts := []string{string(c.Source.ID), c.ConstraintID, c.Direction, c.Source.Schema, c.Source.Relation, c.Target.Schema, c.Target.Relation}
	for _, p := range c.Fields {
		parts = append(parts, p.SourceField, p.TargetField)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return JoinCandidateID(hex.EncodeToString(sum[:16]))
}

func candidatePairKey(sourceID RelationInstanceID, targetSchema, targetRelation string, pairs []JoinFieldPair) string {
	if targetSchema == "" {
		targetSchema = "main"
	}
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, strings.ToLower(pair.SourceField)+"="+strings.ToLower(pair.TargetField))
	}
	sort.Strings(parts) // ON equality order does not change the FK identity.
	return string(sourceID) + "\x00" + strings.ToLower(targetSchema) + "\x00" + strings.ToLower(targetRelation) + "\x00" + strings.Join(parts, "\x00")
}

func isAppliedCandidate(candidate JoinCandidate, applied []AppliedJoinEdge) bool {
	for _, edge := range applied {
		if candidate.ID == edge.CandidateID {
			return true
		}
		if candidate.Source.ID == edge.JoinPath && candidate.ConstraintID == edge.ConstraintID && candidate.Direction != edge.Direction {
			return true // reverse traversal of the same active relationship
		}
	}
	return false
}

// activeJoinPairs maps actual ON predicates back to exact source instances.
// It accepts either equality-operand order and never hides all FKs just
// because one target physical table appears somewhere in the query.
func activeJoinPairs(from dal.FromSource, applied []AppliedJoinEdge) map[string]bool {
	active := map[string]bool{}
	provenance := map[RelationInstanceID]bool{}
	for _, edge := range applied {
		provenance[edge.JoinPath] = true
	}
	instances := relationInstances(from)
	byAlias := map[string]RelationInstance{}
	for _, instance := range instances {
		alias := instance.Alias
		if alias == "" {
			alias = instance.Relation
		}
		byAlias[strings.ToLower(alias)] = instance
	}
	var walk func(dal.FromSource, RelationInstanceID)
	walk = func(node dal.FromSource, path RelationInstanceID) {
		for index, join := range node.Joins() {
			childPath := RelationInstanceID(fmt.Sprintf("%s/%d", path, index))
			child := join.From()
			if child == nil {
				child = dal.From(join.RecordsetSource)
			}
			base := child.Base()
			targetAlias := base.Alias()
			if targetAlias == "" {
				targetAlias = base.Name()
			}
			target, targetKnown := byAlias[strings.ToLower(targetAlias)]
			if !targetKnown {
				walk(child, childPath)
				continue
			}
			if provenance[childPath] {
				walk(child, childPath)
				continue
			}
			var source RelationInstance
			var pairs []JoinFieldPair
			valid := len(join.On()) > 0
			for _, condition := range join.On() {
				comparison, ok := condition.(dal.Comparison)
				if !ok || comparison.Operator != dal.Equal {
					valid = false
					break
				}
				left, lok := comparison.Left.(dal.FieldRef)
				right, rok := comparison.Right.(dal.FieldRef)
				if !lok || !rok {
					valid = false
					break
				}
				var sourceField, targetField dal.FieldRef
				switch {
				case strings.EqualFold(right.Source(), targetAlias):
					sourceField, targetField = left, right
				case strings.EqualFold(left.Source(), targetAlias):
					sourceField, targetField = right, left
				default:
					valid = false
				}
				if !valid {
					break
				}
				current, ok := byAlias[strings.ToLower(sourceField.Source())]
				if !ok || (len(pairs) > 0 && current.ID != source.ID) {
					valid = false
					break
				}
				source = current
				pairs = append(pairs, JoinFieldPair{SourceField: sourceField.Name(), TargetField: targetField.Name()})
			}
			if valid {
				active[candidatePairKey(source.ID, target.Schema, target.Relation, pairs)] = true
				reverse := make([]JoinFieldPair, len(pairs))
				for i, pair := range pairs {
					reverse[i] = JoinFieldPair{SourceField: pair.TargetField, TargetField: pair.SourceField}
				}
				active[candidatePairKey(target.ID, source.Schema, source.Relation, reverse)] = true
			}
			walk(child, childPath)
		}
	}
	walk(from, "root")
	return active
}
