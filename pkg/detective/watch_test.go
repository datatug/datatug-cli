package detective

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
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
func TestWatchPinAdmissionRejectsInvalidCallsAndUnserializableCatalog(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	revisions := func(context.Context, string) (string, error) { return "revision", nil }
	for _, call := range []struct {
		req     ActionRequest
		version string
		lookup  QueryRevisionLookup
	}{{ActionRequest{Type: "other"}, "1", revisions}, {ActionRequest{Type: "run-check"}, "", revisions}, {ActionRequest{Type: "run-check"}, "1", nil}} {
		if _, _, e := s.ExecuteWatchAction(ctx, "op", "alice", call.req, call.version, "", nil, nil, call.lookup); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if e := checkWatchCatalog(ctx, core.Catalog{}, "missing", nil); e != nil {
		t.Fatal(e)
	}
	pin := &watchSourcePin{version: "1"}
	ctx = context.WithValue(ctx, watchVersionKey{}, pin)
	c := core.Catalog{Checks: map[string]core.Check{"check": {ID: "check", Version: 1, UpdatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}}
	if e := checkWatchCatalog(ctx, c, "check", nil); !errors.Is(e, ErrInvalid) {
		t.Fatal("unserializable catalog accepted", e)
	}
}
func TestWatchExecuteActionUsesPinnedExistingEngine(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, e := s.SaveCheck(ctx, Check{ID: "watch-check", Title: "Watch Check", Steps: []Step{{ID: "query", Kind: "query", QueryID: "q"}}}, "alice"); e != nil {
		t.Fatal(e)
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "watch-check", Environment: "qa"}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	out, fingerprint, e := s.ExecuteWatchAction(ctx, "watch-action", "alice", req, "1", "", runner, nil, func(context.Context, string) (string, error) { return "r1", nil })
	if e != nil || out.Run == nil || fingerprint == "" {
		t.Fatal(out, fingerprint, e)
	}
	if _, _, e = s.ExecuteWatchAction(ctx, "watch-changed-root", "alice", req, "999", fingerprint, runner, nil, func(context.Context, string) (string, error) { return "r1", nil }); !errors.Is(e, ErrConflict) {
		t.Fatal("changed root admitted by execution engine", e)
	}
}
