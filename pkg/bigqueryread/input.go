// Package bigqueryread composes the released BigQuery driver with DataTug policies.
package bigqueryread

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	bigquery "github.com/dal-go/dalgo2bigquery"
)

const MaxInputBytes = 256 << 10

var ErrInput = errors.New("invalid BigQuery input: expected bounded typed JSON")

// Input is an explicit scalar DTQL query and reviewed native source profile.
// QueryShape intentionally excludes SQL, joins, subqueries, aliases and offsets.
type Input struct {
	Profile bigquery.SourceProfile `json:"profile"`
	Query   QueryShape             `json:"query"`
}
type QueryShape struct {
	From    From       `json:"from"`
	Columns []Column   `json:"columns"`
	Where   *Condition `json:"where,omitempty"`
	OrderBy []Order    `json:"orderBy,omitempty"`
	Limit   int        `json:"limit"`
}
type From struct {
	Name string `json:"name"`
}
type Column struct {
	Field string `json:"field"`
}
type Order struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc,omitempty"`
}
type Expression struct {
	Field string          `json:"field,omitempty"`
	Param string          `json:"param,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}
type Condition struct {
	Op        string      `json:"op,omitempty"`
	Left      *Expression `json:"left,omitempty"`
	Right     *Expression `json:"right,omitempty"`
	And       []Condition `json:"and,omitempty"`
	Or        []Condition `json:"or,omitempty"`
	IsNull    *Column     `json:"isNull,omitempty"`
	IsNotNull *Column     `json:"isNotNull,omitempty"`
}

func Decode(r io.Reader, target any) error {
	return DecodeLimit(r, target, MaxInputBytes)
}

// DecodeLimit is used only for bounded preview/result recovery envelopes.
func DecodeLimit(r io.Reader, target any, limit int) error {
	if limit < 1 || limit > bigquery.MaxResponseBytes {
		return ErrInput
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return ErrInput
	}
	if _, err = bigquery.ParseJSON(raw, limit); err != nil {
		return ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err = d.Decode(target); err != nil {
		return ErrInput
	}
	return nil
}
func (in Input) DALQuery() (dal.Query, error) {
	nodes := 0
	var visit func(*Condition, int) error
	visit = func(c *Condition, depth int) error {
		if c == nil {
			return nil
		}
		nodes++
		if depth >= 8 || nodes > 128 {
			return ErrInput
		}
		for _, x := range []*Expression{c.Left, c.Right} {
			if x != nil && len(x.Value) > 0 {
				v, e := bigquery.ParseJSON(x.Value, MaxInputBytes)
				if e != nil {
					return ErrInput
				}
				switch a := v.(type) {
				case map[string]any:
					return ErrInput
				case []any:
					if len(a) > 1000 {
						return ErrInput
					}
					for _, n := range a {
						switch n.(type) {
						case map[string]any, []any:
							return ErrInput
						}
					}
				}
			}
		}
		for i := range c.And {
			if e := visit(&c.And[i], depth+1); e != nil {
				return e
			}
		}
		for i := range c.Or {
			if e := visit(&c.Or[i], depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if len(in.Query.Columns) > 128 || len(in.Query.OrderBy) > 16 {
		return nil, ErrInput
	}
	if err := visit(in.Query.Where, 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in.Query)
	if err != nil {
		return nil, ErrInput
	}
	q, err := dtql.Deserialize(raw)
	if err != nil {
		return nil, ErrInput
	}
	if err = Guard(q); err != nil {
		return nil, err
	}
	return q, nil
}

// Guard permits bounded RHS parameters before shared substitution. All other
// shape checks use the released driver's whole-AST guard, without a paid leaf.
func Guard(query dal.Query) error {
	q, err := driverQuery(query, true)
	if err != nil {
		return err
	}
	return bigquery.ValidateQuery(q)
}

// DALgo policy substitution represents IN values as dal.Array. The released
// driver represents the same supported RHS as a Constant containing a slice.
// Copy only that representation boundary; never broaden the supported shape.
func driverQuery(query dal.Query, parameters bool) (dal.Query, error) {
	q, ok := query.(dal.StructuredQuery)
	if !ok {
		return query, nil
	}
	count := 0
	var replace func(dal.Condition, int) (dal.Condition, error)
	replace = func(c dal.Condition, depth int) (dal.Condition, error) {
		if c == nil {
			return nil, nil
		}
		count++
		if depth >= 8 || count > 128 {
			return nil, ErrInput
		}
		switch v := c.(type) {
		case *dal.Comparison:
			if v == nil {
				return nil, ErrInput
			}
			return replace(*v, depth)
		case dal.Comparison:
			var placeholder any = "guard"
			if v.Operator == dal.In {
				placeholder = []string{"guard"}
			}
			switch p := v.Right.(type) {
			case dal.Param:
				if parameters {
					v.Right = dal.Constant{Value: placeholder}
				}
			case *dal.Param:
				if p == nil {
					return nil, ErrInput
				}
				if parameters {
					v.Right = dal.Constant{Value: placeholder}
				}
			case dal.Array:
				if v.Operator == dal.In {
					v.Right = dal.Constant(p)
				}
			case *dal.Array:
				if p == nil {
					return nil, ErrInput
				}
				if v.Operator == dal.In {
					v.Right = dal.Constant{Value: p.Value}
				}
			}
			if v.Operator == dal.In {
				if constant, ok := v.Right.(dal.Constant); ok && !validINArray(constant.Value) {
					return nil, ErrInput
				}
			}
			return v, nil
		case *dal.GroupCondition:
			if v == nil {
				return nil, ErrInput
			}
			return replace(*v, depth)
		case dal.GroupCondition:
			children := v.Conditions()
			out := make([]dal.Condition, len(children))
			for i, x := range children {
				var e error
				out[i], e = replace(x, depth+1)
				if e != nil {
					return nil, e
				}
			}
			return dal.NewGroupCondition(v.Operator(), out...), nil
		default:
			return c, nil
		}
	}
	where, err := replace(q.Where(), 0)
	if err != nil {
		return nil, err
	}
	return dal.WithWhere(q, where), nil
}

// IN materializes only homogeneous, non-null constant scalars. Source-specific
// normalization/type checking still belongs to the released compiler.
func validINArray(value any) bool {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Slice || rv.Len() > 1000 {
		return false
	}
	kind := ""
	for i := 0; i < rv.Len(); i++ {
		current := ""
		switch rv.Index(i).Interface().(type) {
		case string:
			current = "string"
		case bool:
			current = "bool"
		default:
			return false
		}
		if kind != "" && kind != current {
			return false
		}
		kind = current
	}
	return true
}
