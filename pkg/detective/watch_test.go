package detective

import (
	"context"
	"errors"
	"testing"

	core "github.com/datatug/datatug-core/pkg/detective"
)

type watchCatalogReader struct {
	check     core.Check
	validator core.PublishedValidator
}

func (r watchCatalogReader) GetCheck(context.Context, string) (core.Check, error) {
	return r.check, nil
}
func (r watchCatalogReader) GetPublishedValidator(context.Context, string) (core.PublishedValidator, error) {
	return r.validator, nil
}
func (r watchCatalogReader) GetQueryValidators(context.Context, string) (core.QueryValidators, bool, error) {
	return core.QueryValidators{}, false, nil
}
func (r watchCatalogReader) QueryExists(context.Context, string) (bool, error) { return true, nil }

func TestWatchCatalogPinsRootAndTransitiveQueryRevisionBeforeDispatch(t *testing.T) {
	reader := watchCatalogReader{check: core.Check{ID: "check", Title: "Check", Version: 1, Steps: []core.Step{{ID: "query", Kind: "query", QueryID: "saved-query"}}}}
	catalog, err := core.LoadCatalog(context.Background(), "check", "", reader)
	if err != nil {
		t.Fatal(err)
	}
	pin := &watchSourcePin{version: "1"}
	ctx := context.WithValue(context.Background(), watchVersionKey{}, pin)
	if err = checkWatchCatalog(ctx, catalog, "check", map[string]string{"saved-query": "revision-1"}); err != nil || pin.actual == "" {
		t.Fatal(err)
	}
	pin.expected = pin.actual
	if err = checkWatchCatalog(ctx, catalog, "check", map[string]string{"saved-query": "revision-2"}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed saved query silently redefined baseline")
	}
	pin.version = "2"
	if err = checkWatchCatalog(ctx, catalog, "check", map[string]string{"saved-query": "revision-1"}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed Check version accepted")
	}
}
