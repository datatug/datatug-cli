package detective

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/datatug/datatug-core/pkg/detective"
)

func TestCaptureQueryRevisionsCoversTransitiveAndRootQueries(t *testing.T) {
	ctx := context.Background()
	if _, ok := AdmittedQueryRevision(ctx, "root"); ok {
		t.Fatal("unadmitted context exposed a revision")
	}
	catalog := core.Catalog{
		Checks:          map[string]core.Check{"parent": {Steps: []core.Step{{Kind: "query", QueryID: "child"}, {Kind: "check", CheckID: "nested"}}}},
		QueryValidators: map[string]core.QueryValidators{"local": {QueryID: "local"}},
	}
	var seen []string
	lookup := func(_ context.Context, id string) (string, error) {
		seen = append(seen, id)
		return "revision-" + id, nil
	}
	revisions, err := captureQueryRevisions(ctx, catalog, "root", lookup)
	if err != nil || !reflect.DeepEqual(seen, []string{"child", "local", "root"}) {
		t.Fatalf("catalog admission = %+v, %v, lookups=%v", revisions, err, seen)
	}
	admitted := withAdmittedQueryRevisions(ctx, revisions)
	if revision, ok := AdmittedQueryRevision(admitted, "root"); !ok || revision != "revision-root" {
		t.Fatalf("root admission = %q, %v", revision, ok)
	}
	if _, ok := AdmittedQueryRevision(admitted, "not-in-catalog"); ok {
		t.Fatal("unrelated query inherited catalog admission")
	}
	if revisions, err := captureQueryRevisions(ctx, catalog, "root", nil); err != nil || revisions != nil {
		t.Fatalf("local fake runner unexpectedly required a revision: %+v, %v", revisions, err)
	}
	lookupFailure := errors.New("query revision unavailable")
	if _, err := captureQueryRevisions(ctx, catalog, "root", func(context.Context, string) (string, error) { return "", lookupFailure }); !errors.Is(err, lookupFailure) {
		t.Fatalf("revision lookup error = %v", err)
	}
	if _, err := captureQueryRevisions(ctx, catalog, "root", func(context.Context, string) (string, error) { return "", nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty revision accepted: %v", err)
	}
}
