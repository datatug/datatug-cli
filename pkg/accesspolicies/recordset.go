package accesspolicies

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
)

var wrapRecordsetSession = func(s dal.ReadSession) dal.ReadSession {
	return s
}

// PrepareRecordset captures the policy-effective query without executing a leaf.
// guard must visit the entire original AST before parameter substitution and the
// SecureReadSession recursive route, then the effective AST before compilation.
func PrepareRecordset(ctx context.Context, query dal.Query, o Options, guard func(dal.Query) error) (Result, error) {
	return prepareRecordset(ctx, query, o, guard, ExactVariables)
}

// PrepareRecordsetWithVariables is for native recordset executors whose scalar
// domain includes exact integers/decimals unavailable to keyed local comparison.
// normalize must be pure, bounded and detach supported scalar values. It does
// not authorize a query. The mandatory original/effective whole-AST guard and
// exactly-one capture still run; no RecordsReader/local evaluation is exposed.
// Run and PrepareRecordset retain ExactVariables and their existing refusals.
func PrepareRecordsetWithVariables(ctx context.Context, query dal.Query, o Options, guard func(dal.Query) error, normalize func(map[string]any) (map[string]any, error)) (Result, error) {
	if normalize == nil {
		return Result{}, fmt.Errorf("%w: missing native scalar normalizer", ErrInvalidQuery)
	}
	return prepareRecordset(ctx, query, o, guard, normalize)
}

func prepareRecordset(ctx context.Context, query dal.Query, o Options, guard func(dal.Query) error, normalize func(map[string]any) (map[string]any, error)) (Result, error) {
	if guard == nil {
		return Result{}, fmt.Errorf("%w: missing whole-query guard", ErrInvalidQuery)
	}
	if err := guard(query); err != nil {
		return Result{}, err
	}
	ctx, result, err := prepareReadWithVariables(ctx, query, o, normalize)
	if err != nil {
		return result, err
	}
	capture := &recordsetCapture{guard: guard}
	var session dal.ReadSession = capture
	if len(o.Policies) > 0 {
		session = access.SecureReadSession(capture, Policies(o.Policies)...)
	}
	session = wrapRecordsetSession(session)
	_, err = session.ExecuteQueryToRecordsetReader(ctx, result.Query)
	if err != nil {
		return result, err
	}
	if capture.calls != 1 {
		return result, fmt.Errorf("%w: policy capture did not yield exactly one query", ErrInvalidQuery)
	}
	result.Query = capture.query
	return result, nil
}

type recordsetCapture struct {
	query dal.Query
	calls int
	guard func(dal.Query) error
}

func (*recordsetCapture) Get(context.Context, record.Record) error        { return dal.ErrNotSupported }
func (*recordsetCapture) GetMulti(context.Context, []record.Record) error { return dal.ErrNotSupported }
func (*recordsetCapture) Exists(context.Context, *record.Key) (bool, error) {
	return false, dal.ErrNotSupported
}
func (*recordsetCapture) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return nil, dal.ErrNotSupported
}
func (c *recordsetCapture) ExecuteQueryToRecordsetReader(_ context.Context, q dal.Query, _ ...recordset.Option) (dal.RecordsetReader, error) {
	if err := c.guard(q); err != nil {
		return nil, err
	}
	c.calls++
	c.query = q
	return nil, nil
}
