package secureread

import (
	"context"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// RunFederatedDTQL runs a named-database query through secured, independent
// leaf reads. The caller supplies source URLs from its project environment.
// No joined query is delegated to a database or to an OVDB server.
func (e *Executor) RunFederatedDTQL(ctx context.Context, document []byte, sourceURLs map[string]string, variables map[string]any) (Result, error) {
	query, err := dtql.Deserialize(document)
	if err != nil {
		return Result{}, fmt.Errorf("parse federated DTQL: %w", err)
	}
	opened := map[string]dal.DB{}
	var limitations []Limitation
	var closes []func()
	defer func() {
		for i := len(closes) - 1; i >= 0; i-- {
			closes[i]()
		}
	}()
	observer, _ := ctx.Value(federatedProgressKey{}).(func(dal.FederatedProgress))
	reader, err := dal.ExecuteFederatedQueryWithOptions(ctx, query, func(ctx context.Context, database string) (dal.QueryExecutor, error) {
		if db, ok := opened[database]; ok {
			return securedLeaf{db: db, session: e.session, variables: variables, limitations: &limitations}, nil
		}
		url := sourceURLs[database]
		if url == "" {
			return nil, fmt.Errorf("database %q is not configured", database)
		}
		db, closeSource, err := openSource(ctx, url, false)
		if err != nil {
			return nil, fmt.Errorf("open database %q: %w", database, err)
		}
		opened[database] = db
		closes = append(closes, closeSource)
		return securedLeaf{db: db, session: e.session, variables: variables, limitations: &limitations}, nil
	}, dal.FederatedQueryOptions{OnProgress: observer})
	if err != nil {
		return Result{}, err
	}
	rows, statistics, err := collectRows(reader)
	if err != nil {
		return Result{}, err
	}
	columns := columnsFor(query, rows)
	// Map iteration never determines column or row order; expose the same
	// deterministic fallback as other saved query results.
	if len(columns) == 0 && len(rows) > 0 {
		for name := range rows[0].Data {
			columns = append(columns, name)
		}
		sort.Strings(columns)
	}
	return Result{Columns: columns, Rows: rows, Statistics: statistics.finalize(columns), Limitations: limitations}, nil
}

type securedLeaf struct {
	db          dal.DB
	session     Session
	variables   map[string]any
	limitations *[]Limitation
}

type federatedProgressKey struct{}

// WithFederatedProgress installs a request-scoped observer without changing
// the saved-query API or routing progress through stdout result data.
func WithFederatedProgress(ctx context.Context, observer func(dal.FederatedProgress)) context.Context {
	return context.WithValue(ctx, federatedProgressKey{}, observer)
}

func (s securedLeaf) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	result, err := accesspolicies.Run(ctx, s.db, query, accesspolicies.Options{
		Principal: s.session.Principal, Variables: s.variables,
		Policies: s.session.Policies, Unrestricted: s.session.Unrestricted,
	})
	if err != nil {
		return nil, err
	}
	*s.limitations = append(*s.limitations, limitationsFromLines(result.Lines)...)
	return result.Reader, nil
}

func (s securedLeaf) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, fmt.Errorf("federated recordset readers are not supported")
}
