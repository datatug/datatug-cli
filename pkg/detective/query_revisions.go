package detective

import (
	"context"
	"sort"

	core "github.com/datatug/datatug-core/pkg/detective"
)

// QueryRevisionLookup resolves the saved-query revision at run admission.
// The endpoint checks the same revision when it loads the query for execution.
type QueryRevisionLookup func(context.Context, string) (string, error)

type admittedQueryRevisionsKey struct{}

func withAdmittedQueryRevisions(ctx context.Context, revisions map[string]string) context.Context {
	return context.WithValue(ctx, admittedQueryRevisionsKey{}, revisions)
}

// AdmittedQueryRevision returns the revision captured with the sealed catalog.
func AdmittedQueryRevision(ctx context.Context, queryID string) (string, bool) {
	revisions, ok := ctx.Value(admittedQueryRevisionsKey{}).(map[string]string)
	if !ok {
		return "", false
	}
	revision := revisions[queryID]
	return revision, revision != ""
}

func captureQueryRevisions(ctx context.Context, catalog core.Catalog, rootQueryID string, lookup QueryRevisionLookup) (map[string]string, error) {
	if lookup == nil {
		return nil, nil
	}
	ids := map[string]bool{}
	if rootQueryID != "" {
		ids[rootQueryID] = true
	}
	for _, check := range catalog.Checks {
		for _, step := range check.Steps {
			if step.Kind == "query" {
				ids[step.QueryID] = true
			}
		}
	}
	for queryID := range catalog.QueryValidators {
		ids[queryID] = true
	}
	ordered := make([]string, 0, len(ids))
	for queryID := range ids {
		ordered = append(ordered, queryID)
	}
	sort.Strings(ordered)
	revisions := make(map[string]string, len(ordered))
	for _, queryID := range ordered {
		revision, err := lookup(ctx, queryID)
		if err != nil {
			return nil, err
		}
		if revision == "" {
			return nil, ErrInvalid
		}
		revisions[queryID] = revision
	}
	return revisions, nil
}
