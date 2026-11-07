package endpoints

import (
	"context"
	"errors"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/tugqlbind"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// Cross-database saved TugQL in exec/run_query (spec/features/cli/serve,
// REQ:serve-cross-database-queries). A saved query that reads named databases
// — a text body's `databases (` block with `alias::path` sources, or a
// structured body whose sources carry a `database` — binds each one to a
// source of the served project (pkg/tugqlbind), the way `query run` does, and
// reads every bound source as its own secured leaf through
// Executor.RunFederatedTugQL.

// federatedLabelGuardFor builds the sensitivity-label guard of one project's
// sources. `datatug serve` installs it (SetFederatedLabelGuard). Without one a
// federated read has no guard: a policy-free session reads on, and a
// policy-governed one is refused (secureread.Executor.RunFederatedTugQL).
var federatedLabelGuardFor func(projectDir string) secureread.FederatedColumnGuard

// SetFederatedLabelGuard installs the label guard every federated saved query
// checks each leaf with, against the labels of the source its alias is bound to.
func SetFederatedLabelGuard(guardFor func(projectDir string) secureread.FederatedColumnGuard) {
	federatedLabelGuardFor = guardFor
}

var runFederatedTugQLHook = (*secureread.Executor).RunFederatedTugQL
var agentRawRefusalHook = agentRawRefusal

// bindingSourceError is a bound name whose source the served project cannot
// resolve.
type bindingSourceError struct{ err error }

func (e bindingSourceError) Error() string { return e.err.Error() }

// bindSavedTugQL binds the databases a saved TugQL query reads to sources of
// the served project, in the request's environment. A query that reads no
// named database has no bindings, and neither has one whose document does not
// parse. serve binds only the query's own project: a
// target naming another project is refused, as is a connection string.
func bindSavedTugQL(ctx context.Context, projStore datatug.ProjectStore, projDir string, req apicontract.ExecutionRequest, queryDef *datatug.QueryDef) (dal.StructuredQuery, map[string]tugqlbind.Binding, error) {
	parsed, bindings, err := tugqlbind.Saved(ctx, queryDef, tugqlbind.Resolver{
		ProjectID:   req.Project,
		ProjectDir:  projDir,
		Environment: func(context.Context) (string, error) { return req.Environment, nil },
		Local: func(ctx context.Context, env, catalog string) (string, error) {
			source, err := resolveSourceHook(ctx, projStore, projDir, env, catalog)
			if err != nil {
				return "", bindingSourceError{err}
			}
			return source.URL, nil
		},
	})
	var unavailable bindingSourceError
	switch {
	case err == nil:
		return parsed, bindings, nil
	case errors.As(err, &unavailable):
		return nil, nil, newSourceUnavailable(err.Error())
	case tugqlbind.IsError(err):
		return nil, nil, newInvalidRequest("queryId", err.Error())
	default:
		// A document that does not parse binds nothing: the read reports it,
		// as it does for any saved query.
		return nil, nil, nil
	}
}

// federatedExecutor is executor checking each leaf against the labels of the
// project source its alias is bound to.
func federatedExecutor(executor *secureread.Executor, bindings map[string]tugqlbind.Binding) *secureread.Executor {
	if federatedLabelGuardFor == nil {
		return executor
	}
	return executor.WithFederatedColumns(tugqlbind.LabelGuard(bindings, federatedLabelGuardFor))
}

// agentFederatedGuard judges an agent's federated read like raw SQL on every
// bound source: a source with protected columns, or one that cannot be
// described, refuses it (agents read such sources with records/read).
func agentFederatedGuard(ctx context.Context, project string, bindings map[string]tugqlbind.Binding) error {
	aliases := make([]string, 0, len(bindings))
	for alias := range bindings {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		binding := bindings[alias]
		if err := agentRawRefusalHook(ctx, project, binding.Env, binding.Catalog); err != nil {
			return err
		}
	}
	return nil
}
