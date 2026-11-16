package endpoints

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// instanceExecutor returns executor with the session's policies as they stand
// for one catalog of one environment (backstage spec/features/perimeter,
// REQ:local-rule-selectors): rules that select another environment kind or
// another instance are left out. It is used only for a read of that one
// source, so compare and reconcile read each instance under its own rules.
// The instance is named as the project stores it (api.InstanceTarget): a
// name the store does not hold exactly, or an environment with no valid
// kind under an environment rule, is denied.
func instanceExecutor(ctx context.Context, executor *secureread.Executor, store datatug.ProjectStore, environment, catalog string) *secureread.Executor {
	return executor.ForTarget(api.InstanceTarget(ctx, store, environment, catalog))
}
