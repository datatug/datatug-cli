package detective

import (
	"context"

	core "github.com/datatug/datatug-core/pkg/detective"
)

type reconcilerKey struct{}

// ContextWithReconciler lets every Check evaluated under ctx run its
// reconcile steps with reconciler. The server attaches the one that executes
// instances exactly as POST /datatug/reconcile does; without one a reconcile
// step reports RECONCILE_UNAVAILABLE, never a pass.
func ContextWithReconciler(ctx context.Context, reconciler core.Reconciler) context.Context {
	return context.WithValue(ctx, reconcilerKey{}, reconciler)
}

// evaluationOptions are the core evaluation options ctx carries.
func evaluationOptions(ctx context.Context) []core.Option {
	reconciler, _ := ctx.Value(reconcilerKey{}).(core.Reconciler)
	if reconciler == nil {
		return nil
	}
	return []core.Option{core.WithReconciler(reconciler)}
}

// evaluateCheckWithContextOptions is core.EvaluateCheck with the options of
// ctx applied; it keeps the five-argument seam the package's tests replace.
func evaluateCheckWithContextOptions(ctx context.Context, req core.RunRequest, catalog core.Catalog, runner core.QueryRunner, metadata core.ExecutionMetadata) (core.CheckRun, error) {
	return core.EvaluateCheck(ctx, req, catalog, runner, metadata, evaluationOptions(ctx)...)
}
