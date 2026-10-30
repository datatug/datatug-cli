package secureread

import (
	"context"
	"errors"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// errAuthorized is what authorizeSession answers when the policies let the query through to the
// source: the decision is made, and nothing was read.
var errAuthorized = errors.New("secureread: authorized")

// Authorize makes the access decision of the session for a read of query, the same decision
// RunStructured makes (accesspolicies.Run with the same principal and policies), without opening a
// source or reading a row. It returns nil when the policies let the query reach the source and an
// error wrapping ErrAccessDenied (or the policy's own refusal) when they do not. It is made on the
// collection the query names, so it does not depend on the collection existing: a denied
// principal gets the same answer for a collection that is there and one that is not.
func (e *Executor) Authorize(ctx context.Context, query dal.Query, variables map[string]any) error {
	if err := e.session.configurationErr; err != nil {
		return err
	}
	_, err := accesspolicies.Run(ctx, authorizeSession{}, query, accesspolicies.Options{
		Principal:    e.session.Principal,
		Variables:    variables,
		Policies:     e.session.Policies,
		Unrestricted: e.session.DatabaseProfile.PolicyFreeReads(),
	})
	if errors.Is(err, errAuthorized) {
		return nil
	}
	return err
}

// authorizeSession is a read session with no data: it reports that the query was let through.
type authorizeSession struct{}

func (authorizeSession) Get(context.Context, record.Record) error { return dal.ErrNotSupported }
func (authorizeSession) GetMulti(context.Context, []record.Record) error {
	return dal.ErrNotSupported
}
func (authorizeSession) Exists(context.Context, *record.Key) (bool, error) {
	return false, dal.ErrNotSupported
}
func (authorizeSession) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return nil, errAuthorized
}
func (authorizeSession) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, errAuthorized
}
