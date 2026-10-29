package secureread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
)

const exactAggregateMaxRows = 10000

var exactAggregateMaxBytes = 16 << 20

// ExactAggregateResult carries authoritative types for derived aliases. The
// caller must persist these with Result; catalog field-name inference is not
// sufficient for aggregate outputs.
type ExactAggregateResult struct {
	Result
	ColumnTypes map[string]string
}

type exactAggregatePlan struct {
	original, query dal.StructuredQuery
	scope           queryops.Scope
	keys            []string
	count           bool
	terms           []resultops.AggregateTerm
	inputs          []resultops.Column
	fields          map[string]dal.FieldRef
	outputs         []resultops.Column
	order           []resultops.SortKey
}

type aggregateUnordered struct{ dal.StructuredQuery }

func (q aggregateUnordered) OrderBy() []dal.OrderExpression { return nil }

// RunExactAggregateTugQL executes a narrow canonical Group preview over a single
// source. OriginalDocument supplies projection/type provenance; document alone
// determines the input scope. Every source row is acquired through the current
// policy path. No native opaque SQL or generic float aggregation is used.
// Input budgets are hard errors, never partial aggregates. Integer/decimal
// results use exact JSON numeric lexemes; FLOAT follows resultops IEEE addition.
func (e *Executor) RunExactAggregateTugQL(ctx context.Context, sourceURL string, originalDocument, document []byte, variables map[string]any, columns []resultops.Column) (ExactAggregateResult, error) {
	if err := ctx.Err(); err != nil {
		return ExactAggregateResult{}, err
	}
	if err := refuseContextVariables(document); err != nil {
		return ExactAggregateResult{}, err
	}
	plan, err := prepareExactAggregate(originalDocument, document, columns)
	if err != nil {
		return ExactAggregateResult{}, err
	}
	variables, err = accesspolicies.ExactVariables(variables)
	if err != nil {
		return ExactAggregateResult{}, err
	}
	db, closeSource, err := openSourceFn(ctx, sourceURL, false, len(e.session.Policies))
	if err != nil {
		return ExactAggregateResult{}, err
	}
	defer closeSource()
	recorder := dalgo2http.NewRecorder()
	result, err := e.executeExactAggregate(recorder.WithContext(ctx), db, plan, variables)
	if provenance, ok := recorder.Last(); ok {
		result.Provenance = &provenance
	}
	return result, err
}

func prepareExactAggregate(originalDocument, document []byte, columns []resultops.Column) (exactAggregatePlan, error) {
	var p exactAggregatePlan
	if len(originalDocument) > 1<<20 || len(document) > 1<<20 || len(columns) > 1024 {
		return p, fmt.Errorf("aggregate plan exceeds metadata budget")
	}
	var err error
	p.original, err = tugqlDeserialize(originalDocument)
	if err != nil {
		return p, fmt.Errorf("original aggregate input: %w", err)
	}
	p.query, err = tugqlDeserialize(document)
	if err != nil {
		return p, fmt.Errorf("aggregate preview: %w", err)
	}
	original := p.original
	q := p.query
	if original.From() == nil || q.From() == nil || len(original.From().Joins()) != 0 || len(q.From().Joins()) != 0 {
		return p, fmt.Errorf("exact aggregation requires one source without joins")
	}
	if _, ok := original.From().Base().(dal.CollectionRef); !ok {
		return p, fmt.Errorf("exact aggregation requires a direct original source")
	}
	ref := original.From().Base().(dal.CollectionRef)
	if ref.Database() != "" || ref.ScanLimit() != 0 || len(ref.ScanOrders()) != 0 {
		return p, fmt.Errorf("exact aggregation does not route databases or source scan limits")
	}
	if original.StartFrom() != "" || original.StartAfter() != "" || original.Offset() < 0 || original.Limit() < 0 {
		return p, fmt.Errorf("unsupported original cursor or bound")
	}
	if len(original.GroupBy()) > 0 || original.Having() != nil || dal.HasAggregation(original) {
		return p, fmt.Errorf("exact aggregation over an existing aggregate is unsupported")
	}
	if err = ValidateRecursiveQuery(original); err != nil {
		return p, err
	}
	if q.Having() != nil || len(q.GroupBy()) == 0 || q.StartFrom() != "" || q.StartAfter() != "" || q.Limit() < 1 || q.Limit() > exactAggregateMaxRows || q.Offset() < 0 {
		return p, fmt.Errorf("unsupported or unbounded aggregate preview")
	}
	p.scope = queryops.MatchingSource
	if _, ok := q.From().Base().(dal.QuerySource); ok {
		p.scope = queryops.OriginalOutput
	}
	metadata := make(map[string]string, len(columns))
	for _, column := range columns {
		if column.Name == "" || column.Type == "" {
			return p, fmt.Errorf("aggregate input needs authoritative name/type metadata")
		}
		if _, exists := metadata[column.Name]; exists {
			return p, fmt.Errorf("duplicate input metadata %q", column.Name)
		}
		metadata[column.Name] = column.Type
	}
	p.fields = make(map[string]dal.FieldRef)
	exprNames := make(map[string]string)
	for _, column := range original.Columns() {
		field, ok := column.Expression.(dal.FieldRef)
		if !ok || column.Wildcard != nil {
			return p, fmt.Errorf("exact aggregation requires direct field projections")
		}
		name := column.Alias
		if name == "" {
			name = field.Name()
		}
		if _, exists := p.fields[name]; exists {
			return p, fmt.Errorf("ambiguous original projection %q", name)
		}
		p.fields[name] = field
		expression := field.String()
		if p.scope == queryops.OriginalOutput {
			expression = dal.NewFieldRef("original", name).String()
		}
		if _, exists := exprNames[expression]; exists {
			return p, fmt.Errorf("ambiguous original field projection %q", expression)
		}
		exprNames[expression] = name
	}
	resolve := func(expression dal.Expression) (string, error) {
		field, ok := expression.(dal.FieldRef)
		if !ok {
			return "", fmt.Errorf("aggregate input must be a projected field")
		}
		name, ok := exprNames[field.String()]
		if !ok {
			return "", fmt.Errorf("aggregate field %q is outside original projection", field.String())
		}
		if metadata[name] == "" {
			return "", fmt.Errorf("missing authoritative type for %q", name)
		}
		return name, nil
	}
	used := map[string]bool{}
	addInput := func(name string) {
		if !used[name] {
			used[name] = true
			p.inputs = append(p.inputs, resultops.Column{Name: name, Type: metadata[name]})
		}
	}
	for i, key := range q.GroupBy() {
		name, err := resolve(key)
		if err != nil {
			return p, err
		}
		if i >= len(q.Columns()) || q.Columns()[i].Alias != name || q.Columns()[i].Expression.String() != key.String() {
			return p, fmt.Errorf("group projection does not match preview keys")
		}
		p.keys = append(p.keys, name)
		addInput(name)
		p.outputs = append(p.outputs, resultops.Column{Name: name, Type: metadata[name]})
	}
	var deriveTerms []queryops.AggregateTerm
	for i, column := range q.Columns()[len(p.keys):] {
		aggregate, ok := column.Expression.(dal.AggregateFunc)
		if !ok || len(aggregate.FuncArgs()) != 1 {
			return p, fmt.Errorf("unsupported aggregate expression")
		}
		fn := strings.ToUpper(aggregate.FuncName())
		if distinct, ok := aggregate.(dal.DistinctAggregateFunc); ok && distinct.IsDistinct() {
			return p, fmt.Errorf("DISTINCT aggregates are unsupported")
		}
		if _, star := aggregate.FuncArgs()[0].(dal.StarExpression); star {
			if fn != "COUNT" || i != 0 || column.Alias != "count" {
				return p, fmt.Errorf("COUNT(*) must be the separate count output")
			}
			p.count = true
			p.outputs = append(p.outputs, resultops.Column{Name: "count", Type: "INTEGER"})
			continue
		}
		name, err := resolve(aggregate.FuncArgs()[0])
		if err != nil {
			return p, err
		}
		switch fn {
		case "SUM", "COUNT", "MIN", "MAX":
		default:
			return p, fmt.Errorf("unsupported exact aggregate %q", fn)
		}
		addInput(name)
		p.terms = append(p.terms, resultops.AggregateTerm{Column: name, Function: fn, Alias: column.Alias})
		deriveTerms = append(deriveTerms, queryops.AggregateTerm{Column: name, Function: fn, Alias: column.Alias})
		outputType := metadata[name]
		if fn == "SUM" {
			base := strings.ToUpper(strings.TrimSpace(strings.SplitN(outputType, "(", 2)[0]))
			switch base {
			case "INT", "INTEGER", "INT2", "INT4", "INT8", "SMALLINT", "BIGINT", "TINYINT", "MEDIUMINT", "SERIAL", "BIGSERIAL", "UNSIGNED":
				outputType = "INTEGER"
			case "DECIMAL", "NUMERIC", "NUMBER", "MONEY":
				outputType = "DECIMAL"
			}
		}
		if fn == "COUNT" {
			outputType = "INTEGER"
		}
		p.outputs = append(p.outputs, resultops.Column{Name: column.Alias, Type: outputType})
	}
	expected, err := queryops.DeriveGroupAggregates(string(originalDocument), p.keys, p.count, deriveTerms, p.scope)
	if err != nil {
		return p, err
	}
	actual, err := tugqlSerialize(aggregateUnordered{q})
	if err != nil {
		return p, err
	}
	if !bytes.Equal(actual, []byte(expected.TugQL)) {
		return p, fmt.Errorf("aggregate preview does not preserve original scope/projection")
	}
	if len(p.outputs) > 1024 {
		return p, fmt.Errorf("aggregate plan exceeds output column budget")
	}
	outputs := map[string]bool{}
	for _, column := range p.outputs {
		outputs[column.Name] = true
	}
	for _, order := range q.OrderBy() {
		field, ok := order.Expression().(dal.FieldRef)
		if !ok || field.Source() != "" || !outputs[field.Name()] {
			return p, fmt.Errorf("aggregate ordering must name a projected output")
		}
		p.order = append(p.order, resultops.SortKey{Column: field.Name(), Descending: order.Descending()})
	}
	return p, nil
}

func (e *Executor) executeExactAggregate(ctx context.Context, db dal.DB, p exactAggregatePlan, variables map[string]any) (ExactAggregateResult, error) {
	sourceQuery := p.original
	if p.scope == queryops.MatchingSource {
		builder := p.original.From().NewQuery()
		if p.original.Where() != nil {
			builder.Where(p.original.Where())
		}
		scanColumns := make([]dal.Column, 0, len(p.inputs))
		seen := map[string]bool{}
		for _, input := range p.inputs {
			field := p.fields[input.Name]
			if !seen[field.String()] {
				scanColumns = append(scanColumns, dal.Column{Expression: field})
				seen[field.String()] = true
			}
		}
		sourceQuery = builder.SelectColumns(scanColumns...)
	}
	secured, err := accesspoliciesRun(ctx, db, sourceQuery, accesspolicies.Options{Principal: e.session.Principal, Variables: variables, Policies: e.session.Policies, Unrestricted: e.session.Unrestricted})
	if err != nil {
		return ExactAggregateResult{}, err
	}
	if secured.Reader == nil {
		return ExactAggregateResult{}, fmt.Errorf("aggregate source has no reader")
	}
	defer func() { _ = secured.Reader.Close() }()
	rows, err := captureExactAggregateRows(ctx, secured.Reader, p)
	if err != nil {
		return ExactAggregateResult{}, err
	}
	snapshot, err := resultopsCapture(p.inputs, rows)
	if err != nil {
		return ExactAggregateResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return ExactAggregateResult{}, err
	}
	if err = preflightAggregateOutput(snapshot, rows, p); err != nil {
		return ExactAggregateResult{}, err
	}
	groups, err := snapshot.GroupByAggregates(p.keys, p.count, p.terms)
	if err != nil {
		return ExactAggregateResult{}, err
	}
	outputRows := make([]resultops.Row, len(groups))
	for i, group := range groups {
		cells := append([]resultops.Cell(nil), group.Keys...)
		if p.count {
			cells = append(cells, resultops.Cell{Present: true, Value: group.Count})
		}
		cells = append(cells, group.Aggregates...)
		outputRows[i] = resultops.Row{Cells: cells}
	}
	output, err := resultopsCapture(p.outputs, outputRows)
	if err != nil {
		return ExactAggregateResult{}, err
	}
	order := output.OriginalOrder()
	if len(p.order) > 0 {
		order, err = outputSort(output, p.order)
		if err != nil {
			return ExactAggregateResult{}, err
		}
	}
	start := p.query.Offset()
	if start > len(order) {
		start = len(order)
	}
	end := len(order)
	if p.query.Limit() < end-start {
		end = start + p.query.Limit()
	}
	result := ExactAggregateResult{ColumnTypes: map[string]string{}}
	for _, column := range p.outputs {
		result.Columns = append(result.Columns, column.Name)
		result.ColumnTypes[column.Name] = column.Type
	}
	statistics := newStatisticsAccumulator()
	outputBytes := 0
	for _, index := range order[start:end] {
		if err = ctx.Err(); err != nil {
			return ExactAggregateResult{}, err
		}
		data := make(map[string]any, len(p.outputs))
		for i, column := range p.outputs {
			cell := outputRows[index].Cells[i]
			if !cell.Present {
				return ExactAggregateResult{}, fmt.Errorf("aggregate output %q is missing", column.Name)
			}
			value, err := exactAggregateJSONValue(cell.Value)
			if err != nil {
				return ExactAggregateResult{}, err
			}
			data[column.Name] = value
		}
		encoded, err := exactAggregateJSONMarshal(data)
		if err != nil {
			return ExactAggregateResult{}, err
		}
		outputBytes += len(encoded) + len(p.outputs)*64 + 64
		if outputBytes > exactAggregateMaxBytes {
			return ExactAggregateResult{}, fmt.Errorf("exact aggregation exceeds 16 MiB output budget")
		}
		row := Row{Key: strconv.Itoa(index), Data: data}
		result.Rows = append(result.Rows, row)
		statistics.addRow(data)
	}
	result.Statistics = statistics.finalize(result.Columns)
	result.Limitations = limitationsFromLines(secured.Lines)
	result.Collection = p.original.From().Base().Name()
	return result, nil
}

func captureExactAggregateRows(ctx context.Context, reader dal.RecordsReader, p exactAggregatePlan) ([]resultops.Row, error) {
	var rows []resultops.Row
	retained := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) || (err == nil && item == nil) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !item.Exists() {
			continue
		}
		if len(rows) >= exactAggregateMaxRows {
			return nil, fmt.Errorf("exact aggregation exceeds %d input rows", exactAggregateMaxRows)
		}
		data, err := resultDataMap(item.Data())
		if err != nil {
			return nil, err
		}
		encoded, err := exactAggregateJSONMarshal(data)
		if err != nil {
			return nil, err
		}
		retained += len(encoded) + len(p.inputs)*64 + 64
		if retained > exactAggregateMaxBytes {
			return nil, fmt.Errorf("exact aggregation exceeds 16 MiB input budget")
		}
		row := resultops.Row{Cells: make([]resultops.Cell, len(p.inputs))}
		for i, column := range p.inputs {
			name := column.Name
			if p.scope == queryops.MatchingSource {
				name = p.fields[name].Name()
			}
			value, present := data[name]
			if err := boundedAggregateNumber(value); err != nil {
				return nil, fmt.Errorf("column %q: %w", column.Name, err)
			}
			row.Cells[i] = resultops.Cell{Present: present, Value: value}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func exactAggregateJSONValue(value any) (any, error) {
	switch value := value.(type) {
	case int64:
		return json.Number(strconv.FormatInt(value, 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(value, 10)), nil
	case *big.Int:
		return json.Number(value.String()), nil
	case *big.Rat:
		denominator := new(big.Int).Set(value.Denom())
		two, five := big.NewInt(2), big.NewInt(5)
		a, b := 0, 0
		remainder := new(big.Int)
		for denominator.Sign() > 0 {
			remainder.Mod(denominator, two)
			if remainder.Sign() != 0 {
				break
			}
			denominator.Div(denominator, two)
			a++
			if a > 10000 {
				return nil, fmt.Errorf("decimal scale exceeds exact output budget")
			}
		}
		for denominator.Sign() > 0 {
			remainder.Mod(denominator, five)
			if remainder.Sign() != 0 {
				break
			}
			denominator.Div(denominator, five)
			b++
			if b > 10000 {
				return nil, fmt.Errorf("decimal scale exceeds exact output budget")
			}
		}
		if denominator.Cmp(big.NewInt(1)) != 0 {
			return nil, fmt.Errorf("decimal result is not a terminating decimal")
		}
		scale := a
		if b > scale {
			scale = b
		}
		text := exactAggregateRatFloatString(value, scale)
		if strings.Contains(text, ".") {
			text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
		}
		if text == "-0" {
			text = "0"
		}
		return canonicalAggregateDecimal(text)
	default:
		return value, nil
	}
}

// Check short exponent lexemes before the numeric kernel can expand a Rat.
// This also bounds native arbitrary-precision values before snapshot cloning.
func boundedAggregateNumber(value any) error {
	switch value := value.(type) {
	case json.Number:
		text := string(value)
		if len(text) > 2048 {
			return fmt.Errorf("numeric input exceeds precision budget")
		}
		coefficient := text
		if i := strings.IndexAny(text, "eE"); i >= 0 {
			coefficient = text[:i]
			exponent, err := strconv.ParseInt(text[i+1:], 10, 32)
			if err != nil || exponent < -1000 || exponent > 1000 {
				return fmt.Errorf("numeric exponent exceeds precision budget")
			}
		}
		digits := 0
		for _, c := range coefficient {
			if c >= '0' && c <= '9' {
				digits++
			}
		}
		if digits > 1000 {
			return fmt.Errorf("numeric input exceeds precision budget")
		}
	case *big.Int:
		if value != nil && value.BitLen() > 3322 {
			return fmt.Errorf("integer input exceeds precision budget")
		}
	case *big.Rat:
		if value != nil && (value.Num().BitLen() > 6644 || value.Denom().BitLen() > 6644) {
			return fmt.Errorf("decimal input exceeds precision budget")
		}
	}
	return nil
}

// Bound measure duplication before the kernel clones MIN/MAX values. Using an
// upper bound deliberately refuses uncertain large plans instead of allocating
// a partial result. Final JSON output is independently checked as well.
func preflightAggregateOutput(snapshot resultops.Snapshot, rows []resultops.Row, p exactAggregatePlan) error {
	groups, err := snapshot.GroupBy(p.keys)
	if err != nil {
		return err
	}
	widths := map[string]int{}
	for _, row := range rows {
		for i, column := range p.inputs {
			encoded, err := json.Marshal(row.Cells[i].Value)
			if err != nil {
				return err
			}
			width := len(encoded)
			switch value := row.Cells[i].Value.(type) {
			case json.Number:
				if at := strings.IndexAny(string(value), "eE"); at >= 0 {
					exponent, _ := strconv.Atoi(string(value)[at+1:])
					if exponent < 0 {
						exponent = -exponent
					}
					width += exponent
				}
			case *big.Rat:
				if value != nil {
					width += value.Denom().BitLen()
				}
			}
			if width > widths[column.Name] {
				widths[column.Name] = width
			}
		}
	}
	perGroup := len(p.outputs)*64 + 256
	for _, key := range p.keys {
		perGroup += widths[key]
	}
	for _, term := range p.terms {
		if term.Function == "COUNT" {
			perGroup += 32
		} else {
			perGroup += widths[term.Column] + 32
		}
	}
	if len(groups) > 0 && perGroup > exactAggregateMaxBytes/len(groups) {
		return fmt.Errorf("exact aggregation exceeds 16 MiB output allocation budget")
	}
	return nil
}

func canonicalAggregateDecimal(text string) (any, error) {
	if err := boundedAggregateNumber(json.Number(text)); err == nil {
		return json.Number(text), nil
	}
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign = "-"
		text = text[1:]
	}
	exponent := 0
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		exponent = -(len(text) - dot - 1)
		text = text[:dot] + text[dot+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return json.Number("0"), nil
	}
	for strings.HasSuffix(text, "0") {
		text = text[:len(text)-1]
		exponent++
	}
	exponent += len(text) - 1
	mantissa := text[:1]
	if len(text) > 1 {
		mantissa += "." + text[1:]
	}
	lexeme := json.Number(sign + mantissa + "e" + strconv.Itoa(exponent))
	if err := boundedAggregateNumber(lexeme); err != nil {
		return nil, fmt.Errorf("decimal result exceeds reusable numeric precision budget: %w", err)
	}
	return lexeme, nil
}
