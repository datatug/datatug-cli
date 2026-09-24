package chat

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

type savedQueryStub struct {
	queries []SavedQuery
	ranID   string
	saved   SavedQuerySaveRequest
	vars    map[string]string
	runErr  error
	saveErr error
}

func (s *savedQueryStub) List(context.Context) ([]SavedQuery, error) { return s.queries, nil }
func (s *savedQueryStub) Run(_ context.Context, id string) (QueryResult, error) {
	s.ranID = id
	if s.runErr != nil {
		return QueryResult{}, s.runErr
	}
	return QueryResult{Title: "Prague customers", Source: "sqlite:///chinook.db", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}, nil
}

func (s *savedQueryStub) Save(_ context.Context, request SavedQuerySaveRequest) (SavedQuery, error) {
	s.saved = request
	if s.saveErr != nil {
		return SavedQuery{}, s.saveErr
	}
	query := SavedQuery{ID: "saved-1", Title: request.Title, Type: request.Type, Tags: request.Tags}
	s.queries = append(s.queries, query)
	return query, nil
}

func (s *savedQueryStub) RunWithVariables(ctx context.Context, id string, variables map[string]string) (QueryResult, error) {
	s.vars = variables
	return s.Run(ctx, id)
}
