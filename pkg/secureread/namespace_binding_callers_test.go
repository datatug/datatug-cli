package secureread

import (
	"context"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// A trusted context can already carry a different host binding. Both callers
// must preserve the setter's refusal, without changing identity or policy gates.
func TestBoundNativeNamespaceConflictRefusesBeforeData(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	executor := NewExecutor(f.load(t))
	ctx, err := access.WithNativeTableNamespace(context.Background(), "temp")
	if err != nil {
		t.Fatal(err)
	}
	original := openBoundSourceFn
	opens, dataCalls := 0, 0
	t.Cleanup(func() { openBoundSourceFn = original })
	openBoundSourceFn = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, func(), error) {
		opens++
		db, closeDB, err := original(ctx, ref)
		if err != nil {
			return nil, closeDB, err
		}
		return phase1DispatchDB{DB: db, calls: &dataCalls}, closeDB, nil
	}
	if err := executor.Authorize(ctx, identityQuery("A"), nil); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatal("Authorize dropped namespace conflict", err)
	}
	if opens != 0 || dataCalls != 0 {
		t.Fatal("Authorize conflict opened/dispatched", opens, dataCalls)
	}
	result, err := executor.RunStructured(ctx, executor.session.binding.url, identityQuery("A"), nil)
	if err == nil || !strings.Contains(err.Error(), "already bound") || len(result.Rows) != 0 {
		t.Fatal("data path dropped namespace conflict", err, result.Rows)
	}
	if opens != 1 || dataCalls != 0 {
		t.Fatal("data conflict dispatched or skipped expected opener", opens, dataCalls)
	}
}
