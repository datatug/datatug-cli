package detective

import (
	"context"
	"testing"

	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/stretchr/testify/require"
)

func TestContextWithReconcilerCarriesTheOption(t *testing.T) {
	require.Empty(t, evaluationOptions(context.Background()))
	called := false
	ctx := ContextWithReconciler(context.Background(), func(context.Context, string) (reconcile.Result, error) {
		called = true
		return reconcile.Result{Status: reconcile.StatusConsistent}, nil
	})
	require.Len(t, evaluationOptions(ctx), 1)
	_, err := evaluateCheckWithContextOptions(ctx, core.RunRequest{}, core.Catalog{}, nil, nil)
	require.ErrorIs(t, err, core.ErrInvalid)
	require.False(t, called, "an invalid request evaluates nothing")
}
