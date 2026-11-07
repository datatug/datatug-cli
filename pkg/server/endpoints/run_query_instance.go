package endpoints

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/policyscope"
)

// instanceExecutor returns executor with the session's policies as they stand
// for one catalog of one environment (backstage spec/features/perimeter,
// REQ:local-rule-selectors): rules that select another environment kind or
// another instance are left out. It is used only for a read of that one
// source, so compare and reconcile read each instance under its own rules.
// An environment whose kind cannot be read has none: a rule that selects an
// environment then denies the read.
func instanceExecutor(ctx context.Context, executor *secureread.Executor, store datatug.ProjectStore, environment, catalog string) *secureread.Executor {
	target := policyscope.Target{Environment: environment, Catalog: catalog}
	if env, err := store.LoadEnvironment(ctx, environment); err == nil && env != nil {
		target.Kind = env.Kind
	}
	return executor.ForTarget(target)
}
