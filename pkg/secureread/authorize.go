package secureread

import (
	"context"
	"errors"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// errAuthorized is what authorizeSession answers when the policies let the query through to the
// source: the decision is made, and nothing was read.
var errAuthorized = errors.New("secureread: authorized")

// Authorize applies the same shared policies as RunStructured without
// dispatching a data query or reading data rows. Legacy profiles use a data-less
// session and perform no source I/O. The configured SQLite profile opens its
// fixed read-only source, then verifies native identity metadata after policy
// admission. Supported bound tables must exist with exact ordinary main names.
// A policy denial itself never depends on whether the collection exists.
func (e *Executor) Authorize(ctx context.Context, query dal.Query, variables map[string]any) error {
	if err := e.session.CheckSourceBinding(ctx); err != nil {
		return err
	}
	var session dal.ReadSession = authorizeSession{}
	if e.session.SourceBound() {
		var err error
		ctx, err = access.WithNativeTableNamespace(ctx, "main")
		if err != nil {
			return err
		}
		if structured, ok := query.(dal.StructuredQuery); !ok || dal.HasSubquery(structured) {
			return unsupportedBoundIdentity()
		}
		db, closeDB, err := e.openSource(ctx, e.session.binding.url, false)
		if err != nil {
			return err
		}
		defer closeDB()
		session = db
		ctx = context.WithValue(ctx, boundIdentityNoDataKey{}, true)
	}
	_, err := accesspolicies.Run(ctx, session, query, accesspolicies.Options{
		Principal:    e.session.Principal,
		Variables:    variables,
		Policies:     e.session.Policies,
		Unrestricted: e.session.DatabaseProfile.PolicyFreeReads(),
	})
	if errors.Is(err, errAuthorized) {
		return e.session.CheckSourceBinding(ctx)
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
