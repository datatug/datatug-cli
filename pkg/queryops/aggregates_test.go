package queryops

import (
	"github.com/dal-go/dalgo/dal"
	"reflect"
	"strings"
	"testing"
)

func TestGroupAggregatesScopesAliasesAndImmutability(t *testing.T) {
	t.Parallel()
	terms := []AggregateTerm{{Column: "total", Function: "sum"}, {Column: "total", Function: "COUNT", Alias: "non_null"}, {Column: "total", Function: "MIN", Alias: "minimum"}, {Column: "total", Function: "MAX", Alias: "maximum"}}
	before := append([]AggregateTerm(nil), terms...)
	for _, scope := range []Scope{MatchingSource, OriginalOutput} {
		p, err := DeriveGroupAggregates(simpleQuery, []string{"category"}, true, terms, scope)
		if err != nil {
			t.Fatal(err)
		}
		q := parsePreview(t, p)
		if !reflect.DeepEqual(p.OutputColumns, []string{"category", "count", "sum_total", "non_null", "minimum", "maximum"}) {
			t.Fatal(p.OutputColumns)
		}
		if len(q.Columns()) != 6 || q.Limit() != 2 {
			t.Fatal(p)
		}
		sum := q.Columns()[2].Expression.(dal.AggregateFunc)
		want := "s.total"
		if scope == OriginalOutput {
			want = "original.total"
			inner := q.From().Base().(dal.QuerySource).Query()
			if inner.Limit() != 2 || inner.Offset() != 1 || len(inner.OrderBy()) != 1 {
				t.Fatal("inner scope changed")
			}
		}
		if sum.FuncArgs()[0].String() != want {
			t.Fatal(sum)
		}
		if !strings.Contains(p.DTQL, "param: Region") {
			t.Fatal("lost bound param")
		}
	}
	if !reflect.DeepEqual(terms, before) {
		t.Fatal("mutated terms")
	}
	old, err := DeriveGroup(simpleQuery, []string{"category"}, true, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := DeriveGroupAggregates(simpleQuery, []string{"category"}, true, nil, MatchingSource)
	if old.DTQL != wrapped.DTQL {
		t.Fatal("wrapper changed")
	}
}
func TestGroupAggregatesRefusesAmbiguousOutputs(t *testing.T) {
	t.Parallel()
	if _, err := DeriveGroupAggregates(simpleQuery, nil, true, nil, MatchingSource); err == nil {
		t.Fatal("expected error for empty keys")
	}
	if _, err := DeriveGroupAggregates("invalid: [", []string{"category"}, true, nil, MatchingSource); err == nil {
		t.Fatal("expected error for invalid DTQL")
	}
	caseCollideDTQL := "from: {name: T}\ncolumns: [{field: a, as: col}, {field: b, as: COL}]\n"
	if _, err := DeriveGroupAggregates(caseCollideDTQL, []string{"col", "COL"}, false, nil, MatchingSource); err == nil {
		t.Fatal("expected error for case-colliding group keys")
	}
	for _, terms := range [][]AggregateTerm{
		{{Column: "total", Function: "AVG"}},
		{{Column: "total", Function: "STDDEV"}},
		{{Column: "missing", Function: "SUM"}},
		{{Column: "total", Function: "SUM", Alias: "COUNT"}},
		{{Column: "total", Function: "MIN", Alias: "category"}},
		{{Column: "total", Function: "SUM", Alias: "same"}, {Column: "total", Function: "COUNT", Alias: "Same"}},
		{{Column: "total", Function: "SUM", Alias: "bad alias"}},
	} {
		if _, err := DeriveGroupAggregates(simpleQuery, []string{"category"}, true, terms, MatchingSource); err == nil {
			t.Fatal(terms)
		}
	}
}

func TestAggregateAliasReadableAndCollisionSafe(t *testing.T) {
	t.Parallel()
	if got := AggregateAlias("SUM", "Invoice Total", []string{"sum_Invoice_Total", "SUM_INVOICE_TOTAL_2"}); got != "sum_Invoice_Total_3" {
		t.Fatal(got)
	}
	if got := AggregateAlias("COUNT", "☀", nil); got != "count_column" {
		t.Fatal(got)
	}
	if got := AggregateAlias("", "col", nil); got != "aggregate_col" {
		t.Fatal(got)
	}
	if got := AggregateAlias("123", "col", nil); got != "aggregate_123_col" {
		t.Fatal(got)
	}
	p, err := DeriveGroupAggregates(simpleQuery, []string{"category"}, true, []AggregateTerm{{Column: "total", Function: "SUM"}, {Column: "total", Function: "SUM"}}, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.OutputColumns, []string{"category", "count", "sum_total", "sum_total_2"}) {
		t.Fatal(p.OutputColumns)
	}
}
