package api

import (
	"context"
	"os"
	"reflect"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

func guardedBoardCreator(store datatug.ProjectStore) (datatug.GuardedBoardCreator, bool) {
	creator, ok := store.(datatug.GuardedBoardCreator)
	if !ok {
		return nil, false
	}
	value := reflect.ValueOf(creator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, false
		}
	}
	return creator, true
}

// LocalGuardedBoardCreateSupported is only an affordance. The actual create
// reacquires the project lock and current authority and requires this exact
// provider capability again; neither a cached flag nor a version is a grant.
func LocalGuardedBoardCreateSupported(ctx context.Context, ref dto.ProjectRef, branch string) bool {
	generation, actor, allowed := localBoardWriter()
	if !allowed || ctx.Err() != nil {
		return false
	}
	current := func() bool {
		nextGeneration, nextActor, allowed := localBoardWriter()
		return allowed && generation == nextGeneration && actor == nextActor && ctx.Err() == nil
	}
	supported := false
	err := withLocalProjectFiles(ctx, ref, branch, false, func(_ *os.Root, _ gitState) error {
		store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
		if err != nil {
			return err
		}
		_, supported = guardedBoardCreator(store)
		supported = supported && current()
		return nil
	})
	// withLocalProjectFiles performs final Git I/O too. Recheck authority after
	// that returns, so a replacement session cannot inherit the observed flag.
	return err == nil && supported && current()
}
