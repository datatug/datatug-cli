package chat

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// SavedQuery is the small, presentation-only shape used by the command picker.
// The project store remains authoritative for query definitions and execution.
type SavedQuery struct {
	ID         string
	Title      string
	Type       string
	Tags       []string
	Parameters []SavedQueryParameter
}

type SavedQueryParameter struct {
	ID           string
	Title        string
	Type         string
	Required     bool
	Multi        bool
	DefaultValue string
	Entity       string
	Field        string
}

// SavedQueryLookupService resolves a parameter against the saved query's own
// source and returns policy-filtered rows. A nil result means no scalar FK.
type SavedQueryLookupService interface {
	LookupParameter(context.Context, string, string) (*SavedQueryLookup, error)
}

type SavedQueryLookup struct {
	Key    string
	Multi  bool
	Result secureread.Result
}

type SavedQueryService interface {
	List(context.Context) ([]SavedQuery, error)
	Run(context.Context, string) (QueryResult, error)
}

type SavedQueryParameterizedRunner interface {
	RunWithVariables(context.Context, string, map[string]string) (QueryResult, error)
}

type SavedQuerySaveRequest struct {
	Title    string
	Tags     []string
	Type     string
	Text     string
	Database string
}

type SavedQueryWriter interface {
	Save(context.Context, SavedQuerySaveRequest) (SavedQuery, error)
}
