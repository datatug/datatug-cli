package detective

import (
	"context"
	"encoding/json"

	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// WithWatchAcceptedQueries is a trusted daemon adapter for an already verified
// immutable cloud bundle. The ordinary query runner still checks local source
// admission; this context never grants access to a source or local Check asset.
func WithWatchAcceptedQueries(ctx context.Context, queries map[string]sessioncontext.DetectiveAcceptedQueryPin) context.Context {
	owned := make(map[string]sessioncontext.DetectiveAcceptedQueryPin, len(queries))
	revisions := make(map[string]string, len(queries))
	for id, q := range queries {
		q.DefinitionJSON = append(json.RawMessage(nil), q.DefinitionJSON...)
		owned[id] = q
		revisions[id] = q.Revision
	}
	return context.WithValue(withAdmittedQueryRevisions(ctx, revisions), nativeQueriesKey{}, owned)
}
