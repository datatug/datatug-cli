package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// ResultColumnRow is admitted metadata, never a retained row value.
type ResultColumnRow struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
	Type     string `json:"type,omitempty"`
	Returned bool   `json:"returned"`
	Addable  bool   `json:"addable"`
	Reason   string `json:"reason,omitempty"`
	field    string
	edge     *ForeignKey
}

type ResultColumnsPreview struct {
	DTQL        string   `json:"-"`
	Fingerprint string   `json:"fingerprint"`
	Fields      []string `json:"fields"`
	Proof       string   `json:"proof"`
}

type resultColumnsQuery struct {
	dal.StructuredQuery
	columns       []dal.Column
	groups        []dal.Expression
	where, having dal.Condition
	order         []dal.OrderExpression
}

func (q resultColumnsQuery) Columns() []dal.Column          { return q.columns }
func (q resultColumnsQuery) GroupBy() []dal.Expression      { return q.groups }
func (q resultColumnsQuery) Where() dal.Condition           { return q.where }
func (q resultColumnsQuery) Having() dal.Condition          { return q.having }
func (q resultColumnsQuery) OrderBy() []dal.OrderExpression { return q.order }

func resultColumnRows(record *RecordSet) []ResultColumnRow {
	if record == nil {
		return nil
	}
	rows := make([]ResultColumnRow, len(record.Result.Columns))
	for i, name := range record.Result.Columns {
		rows[i] = ResultColumnRow{Name: name, Type: record.Result.ColumnTypes[name], Returned: true}
	}
	return rows
}

func columnCandidateID(record RecordSet, relation, field, edge string) string {
	h := sha256.Sum256([]byte(record.ID + "\x00" + record.DTQL + "\x00" + relation + "\x00" + field + "\x00" + edge))
	return hex.EncodeToString(h[:12])
}

func resultColumnQuery(record RecordSet) (dal.StructuredQuery, RelationInstance, error) {
	q, err := dtql.Deserialize([]byte(record.DTQL))
	if err != nil {
		return nil, RelationInstance{}, fmt.Errorf("the executed query cannot be parsed")
	}
	if _, ok := q.From().Base().(dal.CollectionRef); !ok || len(q.From().Joins()) != 0 {
		return nil, RelationInstance{}, fmt.Errorf("column enrichment needs one direct physical source relation")
	}
	if err = secureread.ValidateRecursiveQuery(q); err != nil {
		return nil, RelationInstance{}, err
	}
	instances := relationInstances(q.From())
	root := instances[0]
	if root.Schema != "" && !strings.EqualFold(root.Schema, "main") {
		return nil, root, fmt.Errorf("column metadata is unavailable for this schema")
	}
	for _, col := range q.Columns() {
		if col.Wildcard != nil {
			continue
		}
		switch value := col.Expression.(type) {
		case dal.FieldRef:
			if !columnRootField(value, root) {
				return nil, root, fmt.Errorf("projection provenance is ambiguous")
			}
		case dal.AggregateFunc:
			if !strings.EqualFold(value.FuncName(), dal.COUNT) {
				return nil, root, fmt.Errorf("column enrichment preserves COUNT only; this aggregate needs an exact numeric planner")
			}
			if len(value.FuncArgs()) != 1 {
				return nil, root, fmt.Errorf("unsupported COUNT expression")
			}
			if d, ok := value.(dal.DistinctAggregateFunc); ok && d.IsDistinct() {
				return nil, root, fmt.Errorf("COUNT DISTINCT enrichment is not supported")
			}
			if _, ok := value.FuncArgs()[0].(dal.FieldRef); !ok && value.FuncArgs()[0].String() != "*" {
				return nil, root, fmt.Errorf("unsupported COUNT operand")
			}
			if value.String() != dal.NewAggregate(dal.COUNT, false, value.FuncArgs()...).String() {
				return nil, root, fmt.Errorf("ordered COUNT enrichment is not supported")
			}
		default:
			return nil, root, fmt.Errorf("computed or nondeterministic projections cannot be enriched")
		}
	}
	for _, group := range q.GroupBy() {
		field, ok := group.(dal.FieldRef)
		if !ok || !columnRootField(field, root) {
			return nil, root, fmt.Errorf("computed or ambiguous GROUP BY equality cannot be proven")
		}
	}
	if dal.HasAggregation(q) && len(q.GroupBy()) == 0 {
		return nil, root, fmt.Errorf("a global aggregate has no entity group key to enrich")
	}
	return q, root, nil
}

func columnRootField(field dal.FieldRef, root RelationInstance) bool {
	return field.Source() == "" || strings.EqualFold(field.Source(), root.Alias) || strings.EqualFold(field.Source(), root.Relation)
}

func qualifyColumnHaving(condition dal.Condition, source string, columns []dal.Column) (dal.Condition, error) {
	resolve := func(e dal.Expression) (dal.Expression, error) {
		if f, ok := e.(dal.FieldRef); ok && f.Source() == "" {
			for _, c := range columns {
				if c.Alias == f.Name() {
					return qualifyJoinExpression(c.Expression, source, true)
				}
			}
		}
		return qualifyJoinExpression(e, source, true)
	}
	switch v := condition.(type) {
	case nil:
		return nil, nil
	case dal.Comparison:
		l, e := resolve(v.Left)
		if e != nil {
			return nil, e
		}
		r, e := resolve(v.Right)
		if e != nil {
			return nil, e
		}
		return dal.NewComparison(l, v.Operator, r), nil
	case dal.GroupCondition:
		items := make([]dal.Condition, len(v.Conditions()))
		for i, child := range v.Conditions() {
			var e error
			items[i], e = qualifyColumnHaving(child, source, columns)
			if e != nil {
				return nil, e
			}
		}
		return dal.NewGroupCondition(v.Operator(), items...), nil
	default:
		return nil, fmt.Errorf("unsupported HAVING shape")
	}
}
func columnGroupHas(q dal.StructuredQuery, name string) bool {
	for _, expression := range q.GroupBy() {
		if f, ok := expression.(dal.FieldRef); ok && strings.EqualFold(f.Name(), name) {
			return true
		}
	}
	return false
}
func columnProjected(q dal.StructuredQuery, root RelationInstance, name string) bool {
	if len(q.Columns()) == 0 {
		return true
	}
	for _, col := range q.Columns() {
		if col.Wildcard != nil && (col.Wildcard.Source == "" || strings.EqualFold(col.Wildcard.Source, root.Alias) || strings.EqualFold(col.Wildcard.Source, root.Relation)) && !slices.Contains(col.Wildcard.Exclude, name) {
			return true
		}
		if field, ok := col.Expression.(dal.FieldRef); ok && columnRootField(field, root) && strings.EqualFold(field.Name(), name) {
			return true
		}
	}
	return false
}

func discoverResultColumns(record RecordSet, metadata resultColumnMetadata, admitted map[string]bool) ([]ResultColumnRow, error) {
	q, root, err := resultColumnQuery(record)
	if err != nil {
		return nil, err
	}
	key := relationKey(root.Schema, root.Relation)
	table, ok := metadata.tables[key]
	if !ok || !admitted[key] {
		return nil, fmt.Errorf("complete admitted metadata is unavailable for the source relation")
	}
	rows := resultColumnRows(&record)
	grouped := len(q.GroupBy()) > 0
	wildcard := false
	for _, column := range q.Columns() {
		wildcard = wildcard || column.Wildcard != nil
	}
	generatedProjection := false
	for _, name := range table.columns {
		generatedProjection = generatedProjection || (table.generated[name] && columnProjected(q, root, name))
	}
	targetEdges := map[string]int{}
	for _, fk := range metadata.fk.Keys {
		if relationKey(fk.Schema, fk.FromRelation) == key && (!grouped || (len(fk.FromFields) == 1 && columnGroupHas(q, fk.FromFields[0]) && columnProjected(q, root, fk.FromFields[0]))) {
			targetEdges[relationKey(fk.ToSchema, fk.ToRelation)]++
		}
	}
	for _, name := range table.columns {
		if columnProjected(q, root, name) {
			continue
		}
		row := ResultColumnRow{ID: columnCandidateID(record, key, name, ""), Name: name, Source: root.Relation, Type: table.types[name], field: name, Addable: true}
		if grouped {
			row.Addable = false
			// A physical unique key does not prove the evaluator's grouping
			// equality. In particular, adjacent int64 keys beyond 2^53 can
			// normalize to one group; adding a label would split that group.
			row.Reason = "Exact evaluator group equality is unproven; grouped direct fields cannot be added"
		}
		if wildcard {
			row.Addable = false
			row.Reason = "Explicit wildcards must be expanded before column enrichment"
		}
		rows = append(rows, row)
	}
	for _, fk := range metadata.fk.Keys {
		if !strings.EqualFold(fk.FromRelation, root.Relation) || relationKey(fk.Schema, fk.FromRelation) != key {
			continue
		}
		targetKey := relationKey(fk.ToSchema, fk.ToRelation)
		target, exists := metadata.tables[targetKey]
		if !exists || !admitted[targetKey] {
			continue
		}
		reason := ""
		if targetEdges[targetKey] > 1 {
			reason = "Multiple relationships to this target are ambiguous; use an explicit relationship choice"
		}
		if len(fk.FromFields) != 1 || len(fk.ToFields) != 1 || target.rowidKey == "" || !strings.EqualFold(target.rowidKey, fk.ToFields[0]) {
			reason = "Unique key and compatible equality are unproven; composite/text targets are unavailable"
		}
		if reason == "" && !table.binaryGroup[fk.FromFields[0]] {
			reason = "Original GROUP BY equality is not proven BINARY"
		}
		if reason == "" && !strings.Contains(strings.ToUpper(table.types[fk.FromFields[0]]), "INT") {
			reason = "Source key affinity is not proven INTEGER"
		}
		if reason == "" && grouped && (!columnGroupHas(q, fk.FromFields[0]) || !columnProjected(q, root, fk.FromFields[0])) {
			reason = "The complete foreign key must be projected and grouped"
		}
		if reason == "" && returnedColumnKeyOutOfRange(record, q, fk.FromFields[0]) {
			reason = "Returned foreign key exceeds the JOIN evaluator's exact integer range"
		}
		// These projection shapes are unsupported regardless of relationship
		// evidence. Keep discovery consistent with what Apply can execute.
		if generatedProjection {
			reason = "Original projection contains generated fields unsupported by the JOIN executor; it cannot be reconstructed exactly"
		}
		if wildcard {
			reason = "Explicit wildcards must be expanded before column enrichment"
		}
		for _, name := range target.columns {
			if slices.Contains(fk.ToFields, name) {
				continue
			}
			edge := fk
			fieldReason := reason
			if target.generated[name] && !wildcard {
				fieldReason = "Generated target fields are unsupported by the JOIN executor"
			}
			rows = append(rows, ResultColumnRow{ID: columnCandidateID(record, targetKey, name, fk.ConstraintID), Name: fk.ToRelation + "." + name, Source: fk.ToRelation, Type: target.types[name], Addable: fieldReason == "", Reason: fieldReason, field: name, edge: &edge})
		}
	}
	return rows, nil
}

// This is an early refusal using already-retained data, never a new scan or a
// whole-source proof. The secure JOIN executor independently checks every live
// join key before producing rows, so unsafe values outside a limited result
// still fail execution without persisting a child.
func returnedColumnKeyOutOfRange(record RecordSet, q dal.StructuredQuery, name string) bool {
	output := name
	for _, column := range q.Columns() {
		if field, ok := column.Expression.(dal.FieldRef); ok && strings.EqualFold(field.Name(), name) && column.Alias != "" {
			output = column.Alias
		}
	}
	const maxExactInteger = 9007199254740991
	for _, row := range record.Result.Rows {
		switch value := row.Data[output].(type) {
		case int64:
			if value < -maxExactInteger || value > maxExactInteger {
				return true
			}
		case int:
			if int64(value) < -maxExactInteger || int64(value) > maxExactInteger {
				return true
			}
		case float64:
			if math.Abs(value) > maxExactInteger {
				return true
			}
		case json.Number:
			integer, err := value.Int64()
			if err != nil || integer < -maxExactInteger || integer > maxExactInteger {
				return true
			}
		}
	}
	return false
}

func planResultColumns(record RecordSet, metadata resultColumnMetadata, admitted map[string]bool, ids []string) (ResultColumnsPreview, error) {
	if len(ids) == 0 || len(ids) > 12 {
		return ResultColumnsPreview{}, fmt.Errorf("select between 1 and 12 omitted columns")
	}
	rows, err := discoverResultColumns(record, metadata, admitted)
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if selected[id] {
			return ResultColumnsPreview{}, fmt.Errorf("duplicate column candidate")
		}
		selected[id] = true
	}
	q, root, err := resultColumnQuery(record)
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	source := root.Alias
	if source == "" {
		source = root.Relation
	}
	out := resultColumnsQuery{StructuredQuery: q, groups: append([]dal.Expression(nil), q.GroupBy()...), where: q.Where(), having: q.Having(), order: append([]dal.OrderExpression(nil), q.OrderBy()...)}
	// Rebuild only expressions already admitted by the strict shape validator.
	for _, col := range q.Columns() {
		if col.Wildcard != nil {
			return ResultColumnsPreview{}, fmt.Errorf("explicit wildcards must be expanded before enrichment")
		}
		col.Expression, err = qualifyJoinExpression(col.Expression, source, true)
		if err != nil {
			return ResultColumnsPreview{}, err
		}
		out.columns = append(out.columns, col)
	}
	if len(q.Columns()) == 0 {
		for _, name := range metadata.tables[relationKey(root.Schema, root.Relation)].columns {
			out.columns = append(out.columns, dal.Column{Expression: dal.NewFieldRef(source, name)})
		}
	}
	for i, e := range out.groups {
		out.groups[i], err = qualifyJoinExpression(e, source, true)
		if err != nil {
			return ResultColumnsPreview{}, err
		}
	}
	out.where, err = qualifyJoinCondition(out.where, source, true)
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	out.having, err = qualifyColumnHaving(out.having, source, q.Columns())
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	// ORDER BY aliases are output names and must remain aliases.
	for i, o := range out.order {
		e := o.Expression()
		if f, ok := e.(dal.FieldRef); ok && f.Source() == "" {
			alias := false
			for _, c := range q.Columns() {
				alias = alias || c.Alias == f.Name()
			}
			if !alias {
				e = dal.NewFieldRef(source, f.Name())
			}
		}
		out.order[i] = dal.Ascending(e)
		if o.Descending() {
			out.order[i] = dal.Descending(e)
		}
	}
	aliases := map[string]bool{strings.ToLower(source): true}
	outputs := map[string]bool{}
	for _, c := range out.columns {
		name := c.Alias
		if name == "" {
			if f, ok := c.Expression.(dal.FieldRef); ok {
				name = f.Name()
			} else {
				name = c.Expression.String()
			}
		}
		outputs[strings.ToLower(name)] = true
	}
	joined := map[string]string{}
	fields := []string{}
	for _, row := range rows {
		if !selected[row.ID] {
			continue
		}
		delete(selected, row.ID)
		if !row.Addable {
			return ResultColumnsPreview{}, fmt.Errorf("column is unavailable: %s", row.Reason)
		}
		fieldSource := source
		alias := row.field
		if row.edge != nil {
			fk := row.edge
			fieldSource = joined[fk.ConstraintID]
			if fieldSource == "" {
				fieldSource = fk.ToRelation
				for n := 2; aliases[strings.ToLower(fieldSource)]; n++ {
					fieldSource = fmt.Sprintf("%s%d", fk.ToRelation, n)
				}
				aliases[strings.ToLower(fieldSource)] = true
				joined[fk.ConstraintID] = fieldSource
				target := dal.NewRootCollectionRef(fk.ToRelation, fieldSource)
				// Target rowid is left: known INTEGER affinity and BINARY equality.
				on := dal.NewComparison(dal.NewFieldRef(fieldSource, fk.ToFields[0]), dal.Equal, dal.NewFieldRef(source, fk.FromFields[0]))
				q.From().Join(dal.NewJoinedSource(target, dal.JoinLeft, on))
			}
			alias = fk.ToRelation + "_" + row.field
		}
		baseAlias := alias
		for n := 2; outputs[strings.ToLower(alias)]; n++ {
			alias = fmt.Sprintf("%s%d", baseAlias, n)
		}
		outputs[strings.ToLower(alias)] = true
		expression := dal.NewFieldRef(fieldSource, row.field)
		out.columns = append(out.columns, dal.Column{Expression: expression, Alias: alias})
		if len(q.GroupBy()) > 0 {
			out.groups = append(out.groups, expression)
		}
		fields = append(fields, row.Name)
	}
	if len(selected) > 0 {
		return ResultColumnsPreview{}, fmt.Errorf("column metadata changed; reopen the choices")
	}
	doc, err := dtql.Serialize(out)
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	if _, err = dtql.Deserialize(doc); err != nil {
		return ResultColumnsPreview{}, err
	}
	proof := "Direct physical projection; existing clauses and parameters preserved."
	if len(joined) > 0 {
		if len(q.GroupBy()) == 0 {
			proof = "LEFT JOIN to enforced INTEGER rowid primary key, target-key-left numeric equality; source INTEGER affinity and engine-proven BINARY key semantics. The secure JOIN evaluator refuses live keys beyond its exact integer range. At most one target row per original source row. Existing projection, filters, order, offset, limit and parameters retained."
		} else {
			proof = "LEFT JOIN to enforced INTEGER rowid primary key, target-key-left numeric equality; source INTEGER affinity and engine-proven BINARY grouping. The secure JOIN evaluator refuses live keys beyond its exact integer range. At most one target and one label tuple per admitted original group. COUNT, null/missing groups, filters, HAVING, order, offset and limit retained. Grouped output remains aggregate output."
		}
	}
	// Metadata and bound values participate locally in the fingerprint. Only the
	// digest crosses the model boundary; bound parameter values never do.
	bind, _ := json.Marshal(record.Parameters)
	meta, _ := json.Marshal(struct {
		FK      ForeignKeySnapshot
		Columns []ResultColumnRow
	}{metadata.fk, rows})
	h := sha256.Sum256(append(append(append([]byte(record.ID+"\x00"+record.Source+"\x00"+record.DTQL+"\x00"+string(doc)+"\x00"+proof), bind...), meta...), []byte(strings.Join(fields, "\x00"))...))
	return ResultColumnsPreview{DTQL: string(doc), Fingerprint: hex.EncodeToString(h[:]), Fields: fields, Proof: proof}, nil
}

func (c *SessionChat) resultColumnEvidence(ctx context.Context, record RecordSet) (resultColumnMetadata, map[string]bool, error) {
	live, ok := c.store.info.Sources[record.Database]
	if !ok || !storedSourceNames(record.Source, live) {
		return resultColumnMetadata{}, nil, fmt.Errorf("origin source is no longer available")
	}
	checker, ok := c.queryExecutor.(interface {
		CanReadWholeCollection(context.Context, string) error
	})
	if !ok {
		return resultColumnMetadata{}, nil, fmt.Errorf("column permissions cannot be proven by this executor")
	}
	q, root, err := resultColumnQuery(record)
	if err != nil {
		return resultColumnMetadata{}, nil, err
	}
	_ = q
	if err = checker.CanReadWholeCollection(ctx, root.Relation); err != nil {
		return resultColumnMetadata{}, nil, fmt.Errorf("complete source column access is unavailable under this policy")
	}
	m, err := loadResultColumnMetadata(ctx, live)
	if err != nil {
		return m, nil, err
	}
	admitted := map[string]bool{relationKey(root.Schema, root.Relation): true}
	for _, fk := range m.fk.Keys {
		if strings.EqualFold(fk.FromRelation, root.Relation) && checker.CanReadWholeCollection(ctx, fk.ToRelation) == nil {
			admitted[relationKey(fk.ToSchema, fk.ToRelation)] = true
		}
	}
	return m, admitted, nil
}

func (c *SessionChat) ResultColumns(ctx context.Context, recordSetID string) ([]ResultColumnRow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return nil, err
	}
	record, ok := snapshot.RecordSets[recordSetID]
	if !ok {
		return nil, fmt.Errorf("origin result is unavailable")
	}
	m, a, err := c.resultColumnEvidence(ctx, record)
	if err != nil {
		return nil, err
	}
	return discoverResultColumns(record, m, a)
}

func (c *SessionChat) resultColumnsPreview(ctx context.Context, sessionID, parentID string, ids []string) (ResultColumnsPreview, error) {
	if c.activeID != sessionID {
		return ResultColumnsPreview{}, ErrActiveSessionChanged
	}
	s, err := c.store.Load(ctx, sessionID)
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	r, ok := s.RecordSets[parentID]
	if !ok {
		return ResultColumnsPreview{}, fmt.Errorf("origin result is unavailable")
	}
	m, a, err := c.resultColumnEvidence(ctx, r)
	if err != nil {
		return ResultColumnsPreview{}, err
	}
	return planResultColumns(r, m, a, ids)
}

func (c *SessionChat) executeResultColumns(ctx context.Context, sessionID, parentID string, ids []string, fingerprint, originID string) (ChatSession, QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	preview, err := c.resultColumnsPreview(ctx, sessionID, parentID, ids)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	if preview.Fingerprint != fingerprint {
		return ChatSession{}, QueryResult{}, fmt.Errorf("query or metadata changed; review the columns again")
	}
	s, err := c.store.Load(ctx, sessionID)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	parent := s.RecordSets[parentID]
	if err = ctx.Err(); err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	result, err := c.queryExecutor.RunDTQL(ctx, parent.Source, []byte(preview.DTQL), parent.Parameters)
	if err != nil {
		return ChatSession{}, QueryResult{}, fmt.Errorf("column query failed: %s", publicQueryError(err, parent.Parameters))
	}
	if err = ctx.Err(); err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	if c.activeID != sessionID {
		return ChatSession{}, QueryResult{}, ErrActiveSessionChanged
	}
	action := "Automatic columns: "
	if originID == "" {
		action = "Add columns: "
		origin, appendErr := c.store.AppendUser(ctx, sessionID, "Add columns: "+strings.Join(preview.Fields, ", "))
		if appendErr != nil {
			return ChatSession{}, QueryResult{}, appendErr
		}
		originID = origin.ID
	}
	query, err := c.store.AppendQuery(ctx, sessionID, originID, parent.Source, QueryResult{Title: action + parent.Title, DTQL: preview.DTQL, Source: parent.Source, SourceID: parent.Database, Parameters: parent.Parameters, Result: result, Lineage: &JoinLineage{ParentRecordSetID: parent.ID, AppliedEdges: append([]AppliedJoinEdge(nil), appliedEdges(parent)...)}})
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	c.notifyChanged()
	snapshot, err := c.store.Load(ctx, sessionID)
	return snapshot, query, err
}
