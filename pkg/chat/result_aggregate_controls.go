package chat

import (
	"math/big"
	"strings"

	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
)

func numericAggregateKind(raw string) string {
	kind := strings.ToUpper(strings.TrimSpace(raw))
	if at := strings.IndexByte(kind, '('); at >= 0 {
		kind = strings.TrimSpace(kind[:at])
	}
	switch kind {
	case "INT", "INTEGER", "INT2", "INT4", "INT8", "SMALLINT", "BIGINT", "TINYINT", "MEDIUMINT", "SERIAL", "BIGSERIAL", "UNSIGNED":
		return "integer"
	case "DECIMAL", "NUMERIC", "NUMBER", "MONEY":
		return "decimal"
	case "REAL", "FLOAT", "FLOAT4", "FLOAT8", "DOUBLE", "DOUBLE PRECISION":
		return "float"
	}
	return ""
}

// Automatic integer sums require positive provenance and complete FK metadata.
// Key exclusion never depends on spelling such as Id or CustomerId.
func (o *resultOperationOverlay) defaultSumEligible(column int) bool {
	meta := o.ui.columnMeta(&o.record, o.capture.columnNames[column])
	kind := numericAggregateKind(meta.dbType)
	if kind == "float" || kind == "decimal" {
		return true
	}
	if kind != "integer" || meta.qualified == "" || meta.qualified == "ambiguous source" || o.ui.sessions == nil {
		return false
	}
	var object *ProjectObject
	for i := range o.ui.catalog.Objects {
		candidate := &o.ui.catalog.Objects[i]
		if candidate.Reference.SourceID == o.record.Database && strings.HasPrefix(strings.ToLower(meta.qualified), strings.ToLower(candidate.Reference.ObjectID)+".") {
			object = candidate
			break
		}
	}
	if object == nil {
		return false
	}
	field := meta.qualified[len(object.Reference.ObjectID)+1:]
	for _, key := range object.PrimaryKey {
		if strings.EqualFold(key, field) {
			return false
		}
	}
	app, ok := o.ui.sessions.joinApplication.(ForeignKeyJoinApplication)
	if !ok || !storedSourceNames(o.record.Source, app.Source) || app.Snapshot.Source != app.Source {
		return false
	}
	if _, known := app.Snapshot.Columns[strings.ToLower(object.Reference.ObjectID)]; !known {
		return false
	}
	for _, key := range app.Snapshot.Keys {
		if strings.EqualFold(relationKey(key.Schema, key.FromRelation), object.Reference.ObjectID) {
			for _, fk := range key.FromFields {
				if strings.EqualFold(fk, field) {
					return false
				}
			}
		}
	}
	return true
}
func (o *resultOperationOverlay) aggregateFunction(column int) string {
	if column < 0 || column >= len(o.capture.columnNames) {
		return ""
	}
	if o.selectedAt(column) >= 0 {
		return ""
	}
	if function, manual := o.aggregates[column]; manual {
		return function
	}
	if o.defaultSumEligible(column) {
		return "SUM"
	}
	return ""
}
func (o *resultOperationOverlay) aggregateTerms() []resultops.AggregateTerm {
	var terms []resultops.AggregateTerm
	reserved := append([]string(nil), o.names()...)
	if o.count {
		reserved = append(reserved, "count")
	}
	for column, name := range o.capture.columnNames {
		if function := o.aggregateFunction(column); function != "" {
			alias := queryops.AggregateAlias(function, name, reserved)
			reserved = append(reserved, alias)
			terms = append(terms, resultops.AggregateTerm{Column: name, Function: function, Alias: alias})
		}
	}
	return terms
}
func (o *resultOperationOverlay) setAggregate(column int, function string) {
	if column < 0 {
		return
	}
	if o.selectedAt(column) >= 0 {
		o.err = "This is a grouping key. Remove its key selection before aggregating it."
		return
	}
	if function == "SUM" && numericAggregateKind(o.ui.columnMeta(&o.record, o.capture.columnNames[column]).dbType) == "" {
		o.err = "SUM needs an authoritative numeric type. COUNT, MIN or MAX may be available."
		return
	}
	if function == "AVG" {
		o.err = "AVG is unavailable because exact result semantics are not supported. Use SUM and COUNT explicitly."
		return
	}
	if o.aggregates == nil {
		o.aggregates = map[int]string{}
	}
	o.aggregates[column] = function
	o.err = ""
}

// Exact rational sums of finite decimal inputs have only 2 and 5 factors in
// their denominator. Render that decimal without a float conversion.
func formatAggregateValue(value any) string {
	switch value := value.(type) {
	case *big.Int:
		return value.String()
	case *big.Rat:
		denominator := new(big.Int).Set(value.Denom())
		two, five := big.NewInt(2), big.NewInt(5)
		counts := [2]int{}
		for i, factor := range []*big.Int{two, five} {
			for new(big.Int).Mod(denominator, factor).Sign() == 0 {
				denominator.Div(denominator, factor)
				counts[i]++
			}
		}
		if denominator.Cmp(big.NewInt(1)) == 0 {
			return value.FloatString(max(counts[0], counts[1]))
		}
		return value.RatString()
	default:
		return sanitizeTerminalText(FormatValue(value))
	}
}
