package accesspolicies

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
)

// PrepareRecordset captures the policy-effective query without executing a leaf.
// guard must visit the entire original AST before parameter substitution and the
// SecureReadSession recursive route, then the effective AST before compilation.
func PrepareRecordset(ctx context.Context, query dal.Query, o Options, guard func(dal.Query) error) (Result, error) {
	if guard == nil {
		return Result{}, fmt.Errorf("%w: missing whole-query guard", ErrInvalidQuery)
	}
	if err := guard(query); err != nil {
		return Result{}, err
	}
	ctx, result, err := prepareRead(ctx, query, o)
	if err != nil {
		return result, err
	}
	capture := &recordsetCapture{guard: guard}
	var session dal.ReadSession = capture
	if len(o.Policies) > 0 {
		session = access.SecureReadSession(capture, Policies(o.Policies)...)
	}
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
