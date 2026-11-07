package endpoints

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/tugqlbind"
	"github.com/datatug/datatug-cli/pkg/watch"
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
		Local: func(ctx context.Context, env, name string) (string, string, error) {
			return servedCatalog(ctx, projStore, projDir, req.Environment, env, name)
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

// servedCatalog resolves a bound name to a database catalog of the served
// project and names it by the id its labels use: the same deduplicated id a
// single-source read reports, whichever of the catalog's names (its DbModel or
// its catalog id) the query wrote. A request reads only its own environment, so
// a binding of another one is refused, and so is a source that is no database
// catalog (a recordset definition, an HTTP query).
func servedCatalog(ctx context.Context, projStore datatug.ProjectStore, projDir, requested, env, name string) (string, string, error) {
	if env != requested {
		return "", "", tugqlbind.Error(fmt.Sprintf("database %s names environment %s; a served query reads the request's environment, %s", dbcopy.SourceIDDisplay(name), dbcopy.SourceIDDisplay(env), dbcopy.SourceIDDisplay(requested)))
	}
	source, err := resolveSourceHook(ctx, projStore, projDir, env, name)
	if err != nil {
		return "", "", bindingSourceError{err}
	}
	if source.Kind != api.SourceKindSQL && source.Kind != api.SourceKindInGitDB {
		return "", "", tugqlbind.Error(fmt.Sprintf("database %s is not a database catalog of the project", dbcopy.SourceIDDisplay(name)))
	}
	eligible, err := eligibleTargetsHook(ctx, projStore, projDir, env, &datatug.QueryDef{Type: datatug.QueryTypeTugQL})
	if err != nil {
		return "", "", bindingSourceError{err}
	}
	for _, candidate := range eligible {
		if candidate.URL == source.URL && candidate.Kind == source.Kind {
			return source.URL, candidate.ID, nil
		}
	}
	return source.URL, source.ID, nil
}

// federatedRunRefusal refuses what a federated saved query does not take: a
// request's source (its sources are its bindings, as `query run` refuses
// --source with a databases block) and a cloud-watch artifact override, which
// binds SQL queries only.
func federatedRunRefusal(ctx context.Context, req apicontract.ExecutionRequest) error {
	if req.Source != "" {
		return newInvalidRequest("source", "a saved query that reads named databases reads the sources it binds: source does not go with it")
	}
	paths, _ := ctx.Value(watchCloudSourcesKey{}).(map[string]watchCloudSource)
	if _, override := paths[req.QueryID]; override {
		return watch.ErrForbidden
	}
	return nil
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
// described, refuses it (agents read such sources with records/read). As for a
// single-source read, a query the policy denies gets the policy's own answer
// first, never a label one that would confirm what the denied tables hold.
func agentFederatedGuard(ctx context.Context, project string, query dal.StructuredQuery, bindings map[string]tugqlbind.Binding) error {
	if executor, configured := secureExecutorHook(); configured {
		if err := executor.Authorize(ctx, query, nil); errors.Is(err, secureread.ErrAccessDenied) {
			return contractErrAccessDenied("access denied by policy")
		}
	}
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
