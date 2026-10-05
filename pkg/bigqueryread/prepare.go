package bigqueryread

import (
	"context"

	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// Preparer reloads the exact policy documents on every operation. User-supplied
// policy principal labels never attest the independent Google execution subject.
type Preparer struct {
	Input   Input
	Options func() (accesspolicies.Options, error)
}

func (p Preparer) Prepare(ctx context.Context) (bigquery.ReadPlan, string, error) {
	plan, fingerprint, _, err := p.PrepareWithReport(ctx)
	return plan, fingerprint, err
}

func (p Preparer) PrepareWithReport(ctx context.Context) (bigquery.ReadPlan, string, []accesspolicies.Line, error) {
	q, err := p.Input.DALQuery()
	if err != nil {
		return bigquery.ReadPlan{}, "", nil, err
	}
	o, err := p.Options()
	if err != nil {
		return bigquery.ReadPlan{}, "", nil, err
	}
	result, err := accesspolicies.PrepareRecordset(ctx, q, o, Guard)
	if err != nil {
		return bigquery.ReadPlan{}, "", nil, err
	}
	// A policy residual must also satisfy the released compiler, including types.
	compiledQuery, err := driverQuery(result.Query, false)
	if err != nil {
		return bigquery.ReadPlan{}, "", nil, err
	}
	plan, err := bigquery.Compile(p.Input.Profile, compiledQuery)
	return plan, accesspolicies.Fingerprint(o.Policies, o.Unrestricted, o.Principal), result.Lines, err
}
