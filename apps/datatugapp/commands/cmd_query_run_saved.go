package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
)

// runSavedQueryCommand is `datatug query run --project <dir-or-registered-id>
// --query <id> [--env <envID>] [--var k=v]...` (Phase 1 plan, "the library is
// runnable knowledge"): loads a saved query by ID and runs it through the
// same policy-enforced secureread.Executor path datatug serve's endpoints
// use, respecting --as/--role/--group and the project's own policies/
// directory exactly like serve (resolveServeSession, cmd_serve.go, reused
// unchanged - not edited by this file).
func runSavedQueryCommand(cmd *cobra.Command, o queryOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	stderr := cmd.ErrOrStderr()

	projectDir, projStore, err := resolveQueryProject(o.project)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}

	// Queries are loaded through the store's own query loader
	// (datatug-core v0.23.0+: fsQueriesStore.LoadQuery hydrates QueryDef.Text
	// from its "<id>.query.<type>" sidecar directly, the same load path
	// filestore.LoadProject's Queries tree now uses) - not by hand-reading
	// the sidecar file ourselves, and not through LoadProject's whole-project
	// walk, which would load every query in the project just to run one.
	queryDef, err := projStore.LoadQuery(ctx, o.query)
	if err != nil {
		return Exit(fmt.Sprintf("load query %q: %v", o.query, err), exitCodeDatabase)
	}

	variables, err := accesspolicies.ParseVariables(o.vars)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}

	session, err := resolveServeSession(projectDir, serveFlags{projectDir: projectDir, as: o.as, roles: o.roles, groups: o.groups})
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if session.Unrestricted {
		_, _ = fmt.Fprintln(stderr, "access: running without access policies")
	}
	executor := secureread.NewExecutor(session)

	var result secureread.Result
	switch queryDef.Type {
	case datatug.QueryTypeHTTP:
		result, err = runHTTPSavedQuery(ctx, executor, projectDir, queryDef, variables)
	case datatug.QueryTypeSQL:
		result, err = runSQLSavedQuery(ctx, executor, projStore, o.env, queryDef, variables)
	case datatug.QueryTypeDTQL:
		result, err = runDTQLSavedQuery(ctx, executor, projStore, o.env, queryDef, variables)
	default:
		err = fmt.Errorf("query %q has type %q, which is not yet runnable through the policy-enforced path", o.query, queryDef.Type)
	}
	if err != nil {
		return savedQueryFailure(err)
	}

	if !o.quiet {
		writeSavedQueryLimitations(stderr, result.Limitations)
	}
	// provenance (live vs. fixtures/http/ snapshot, PR #204) is nil here:
	// secureread.Result carries no Provenance field - accesspolicies.Run
	// executes through the generic dal.DB/dal.Query interface, which does
	// not surface pkg/httpsource's dalgo2http.Provenance the way calling
	// pkg/httpsource.ExecuteQuery directly would. Every non-HTTP backend
	// already renders with provenance nil (byte-for-byte unchanged per
	// PR #204); a policy-enforced saved HTTP query renders the same way -
	// a real, documented gap versus the ad-hoc --db http:// path, not a bug,
	// flagged in this PR's body as a follow-up.
	if err := writeQueryRows(cmd.OutOrStdout(), o.format, result.Columns, secureRowsToQueryRows(result.Rows), nil); err != nil {
		return Exit(err.Error(), 1)
	}
	return nil
}

// resolveQueryProject resolves --project to a project directory and store:
// an existing directory is used as-is (filestore.NewSingleProjectStore,
// mirroring projectBaseCommand.initProjectCommand's own ProjectDir branch);
// anything else is looked up as a registered project ID in ~/.datatug.yaml
// (the same projectBaseCommand.initProjectCommand path `datatug scan`/
// `render`/etc. already use for --project/-p, reused directly rather than
// re-implemented here).
func resolveQueryProject(project string) (projectDir string, projStore datatug.ProjectStore, err error) {
	var v projectBaseCommand
	if info, statErr := os.Stat(project); statErr == nil && info.IsDir() {
		v.ProjectDir = project
	} else {
		v.ProjectName = project
	}
	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		if errors.Is(err, ErrUnknownProjectName) {
			return "", nil, fmt.Errorf("--project %q is neither an existing directory nor a registered project ID", project)
		}
		return "", nil, err
	}
	if v.ProjectDir == "" {
		return "", nil, fmt.Errorf("--project %q resolved to no directory", project)
	}
	return v.ProjectDir, v.store.GetProjectStore(v.projectID), nil
}

// resolveQueryEnvironment picks the environment a saved SQL/DTQL query runs
// against: envFlag (--env) if given, else the project's only environment -
// this is REQ-shaped as "the query targets or the project single
// environment" (datatug.QueryDefTarget carries no Environment field to read
// one from), erroring with the full candidate list when neither resolves
// unambiguously.
func resolveQueryEnvironment(ctx context.Context, projStore datatug.ProjectStore, envFlag string) (string, error) {
	if envFlag != "" {
		return envFlag, nil
	}
	envs, err := projStore.LoadEnvironments(ctx)
	if err != nil {
		return "", fmt.Errorf("load environments: %w", err)
	}
	switch len(envs) {
	case 0:
		return "", errors.New("project has no environments configured; pass --env")
	case 1:
		return envs[0].GetID(), nil
	default:
		return "", fmt.Errorf("project has %d environments (%s); pass --env to pick one", len(envs), strings.Join(envs.IDs(), ", "))
	}
}

// resolveQueryDatabase picks the database catalog a saved SQL/DTQL query
// runs against: the query's first declared target naming a Catalog, else
// the resolved environment's only database catalog, erroring with the full
// candidate list otherwise.
func resolveQueryDatabase(ctx context.Context, projStore datatug.ProjectStore, envID string, queryDef *datatug.QueryDef) (string, error) {
	for _, target := range queryDef.Targets {
		if target.Catalog != "" {
			return target.Catalog, nil
		}
	}
	catalogs, err := projStore.LoadEnvDbCatalogs(ctx, envID)
	if err != nil {
		return "", fmt.Errorf("load database catalogs for environment %q: %w", envID, err)
	}
	switch len(catalogs) {
	case 0:
		return "", fmt.Errorf("environment %q has no database catalogs configured", envID)
	case 1:
		return catalogs[0].GetID(), nil
	default:
		return "", fmt.Errorf("environment %q has %d database catalogs (%s); query %q does not declare which one to use (targets[].catalog)",
			envID, len(catalogs), strings.Join(catalogs.IDs(), ", "), queryDef.ID)
	}
}

// resolveQuerySourceURL turns environment+database into the pkg/dbcopy
// source URL secureread.Executor opens - the same shape
// pkg/api/source_resolver.go's resolveSourceURL builds for the server (not
// reused directly: it is unexported in pkg/api, which this stream must not
// edit), with one deliberate difference: catalog.Path is expanded through
// go-homedir before being embedded in the URL. pkg/api's version does not
// expand it, which would build an unopenable "sqlite://~/datatug/dbs/..."
// URL for demo-project-1's real catalog data (environments/*/catalogs/*/*.db.json,
// fixed to carry "~/datatug/..." paths by the S45 dead-layout cleanup) -
// flagged in this stream's PR body as a gap in pkg/api, not fixed there.
func resolveQuerySourceURL(ctx context.Context, projStore datatug.ProjectStore, envID, database string) (string, error) {
	env, err := projStore.LoadEnvironment(ctx, envID)
	if err != nil {
		return "", fmt.Errorf("load environment %q: %w", envID, err)
	}
	var lastErr error
	for _, server := range env.DbServers {
		if server == nil {
			continue
		}
		catalog, catErr := projStore.LoadEnvDbCatalog(ctx, envID, server.GetID(), database)
		if catErr != nil {
			lastErr = catErr
			continue
		}
		return querySourceURLFromCatalog(catalog)
	}
	if lastErr != nil {
		return "", fmt.Errorf("database %q not found in environment %q: %w", database, envID, lastErr)
	}
	return "", fmt.Errorf("environment %q has no DB servers configured; cannot resolve database %q", envID, database)
}

func querySourceURLFromCatalog(catalog datatug.DbCatalog) (string, error) {
	switch catalog.Driver {
	case "sqlite3", "sqlite":
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no path configured for its sqlite driver", catalog.ID)
		}
		expanded, err := homedir.Expand(catalog.Path)
		if err != nil {
			return "", fmt.Errorf("expand catalog %q path %q: %w", catalog.ID, catalog.Path, err)
		}
		return "sqlite://" + expanded, nil
	case "ingitdb":
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no path configured for its ingitdb driver", catalog.ID)
		}
		expanded, err := homedir.Expand(catalog.Path)
		if err != nil {
			return "", fmt.Errorf("expand catalog %q path %q: %w", catalog.ID, catalog.Path, err)
		}
		return "ingitdb://" + expanded, nil
	default:
		return "", fmt.Errorf("database driver %q is not supported for policy-enforced reads (want sqlite3 or ingitdb)", catalog.Driver)
	}
}

// runSQLSavedQuery resolves the SQL query's environment+database, binds its
// named "@param" placeholders (see bindSQLNamedParams) and runs it through
// Executor.RunNativeSQL.
func runSQLSavedQuery(ctx context.Context, executor *secureread.Executor, projStore datatug.ProjectStore, envFlag string, queryDef *datatug.QueryDef, variables map[string]any) (secureread.Result, error) {
	sourceURL, err := resolveSQLOrDTQLSourceURL(ctx, projStore, envFlag, queryDef)
	if err != nil {
		return secureread.Result{}, err
	}
	text, err := bindSQLNamedParams(queryDef.Text, variables)
	if err != nil {
		return secureread.Result{}, fmt.Errorf("query %q: %w", queryDef.ID, err)
	}
	return executor.RunNativeSQL(ctx, sourceURL, text)
}

// runDTQLSavedQuery resolves the DTQL query's environment+database and runs
// it through Executor.RunDTQL, which resolves its "param:" nodes (and
// $currentUser) from variables the same way accesspolicies.Run always has.
func runDTQLSavedQuery(ctx context.Context, executor *secureread.Executor, projStore datatug.ProjectStore, envFlag string, queryDef *datatug.QueryDef, variables map[string]any) (secureread.Result, error) {
	sourceURL, err := resolveSQLOrDTQLSourceURL(ctx, projStore, envFlag, queryDef)
	if err != nil {
		return secureread.Result{}, err
	}
	return executor.RunDTQL(ctx, sourceURL, []byte(queryDef.Text), variables)
}

func resolveSQLOrDTQLSourceURL(ctx context.Context, projStore datatug.ProjectStore, envFlag string, queryDef *datatug.QueryDef) (string, error) {
	envID, err := resolveQueryEnvironment(ctx, projStore, envFlag)
	if err != nil {
		return "", err
	}
	database, err := resolveQueryDatabase(ctx, projStore, envID, queryDef)
	if err != nil {
		return "", err
	}
	return resolveQuerySourceURL(ctx, projStore, envID, database)
}

// runHTTPSavedQuery runs an HTTP-type query through pkg/httpsource, which
// pkg/dbcopy.Parse's http(s):// scheme already opens as a dal.DB serving
// every HTTP QueryDef under projectDir/queries/** as one dalgo2http
// collection per query (Collection.Name = QueryDef.ID) -
// Executor.RunStructured then runs a structured query against it through
// the exact same access-policy path SQL/DTQL use; an HTTP query's
// parameters bind as literal WHERE-equality values (dal.WhereField),
// matching pkg/httpsource's own tests, not through the "variables" resolution
// DTQL's "param:" nodes use.
func runHTTPSavedQuery(ctx context.Context, executor *secureread.Executor, projectDir string, queryDef *datatug.QueryDef, variables map[string]any) (secureread.Result, error) {
	sourceURL := "http://" + projectDir
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(queryDef.ID, "")))
	var missing []string
	for _, p := range queryDef.Parameters {
		value, ok := variables[p.ID]
		if !ok {
			if p.IsRequired {
				missing = append(missing, p.ID)
			}
			continue
		}
		builder = builder.WhereField(p.ID, dal.Equal, value)
	}
	if len(missing) > 0 {
		return secureread.Result{}, fmt.Errorf("query %q: missing --var for required parameter(s): %s", queryDef.ID, strings.Join(missing, ", "))
	}
	return executor.RunStructured(ctx, sourceURL, builder.SelectColumns(), nil)
}

// sqlNamedParamPattern matches a "@ParamName" placeholder in saved SQL query
// text, e.g. demo-project-1's queries/invoices/invoice-lines.query.sql:
// "WHERE il.InvoiceId = @InvoiceId".
var sqlNamedParamPattern = regexp.MustCompile(`@([A-Za-z_][A-Za-z0-9_]*)`)

// bindSQLNamedParams substitutes every "@ParamName" placeholder in sqlText
// with variables[ParamName], SQL-quoted as a literal.
//
// secureread.Executor.RunNativeSQL takes no variables/args parameter -
// dal.NewTextQuery does accept them (dal.QueryArg{Name, Value}), but
// dal-go/dalgo2sql's reader_base.go passes each raw QueryArg struct straight
// through to database/sql as a driver arg (`a[i] = arg`, not `arg.Value` or
// sql.Named(arg.Name, arg.Value)) - database/sql's default parameter
// converter rejects any type it doesn't recognize (struct included), so
// that path errors before a query ever runs. That is a dal-go/dalgo2sql bug,
// several repos upstream of this one; flagged in the PR body, not fixed
// here. Substituting into the literal SQL text ourselves, entirely within
// this file, sidesteps it without depending on unverified third-party
// binding behavior. sqlText only ever comes from a saved query file this
// CLI's own user already controls (git-tracked project content), and each
// substituted value is quoted as a SQL literal (strings single-quoted with
// ” escaping; everything else via fmt.Sprint) rather than concatenated
// unescaped.
func bindSQLNamedParams(sqlText string, variables map[string]any) (string, error) {
	var missing []string
	bound := sqlNamedParamPattern.ReplaceAllStringFunc(sqlText, func(match string) string {
		name := match[1:]
		value, ok := variables[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return sqlLiteral(value)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing --var for SQL parameter(s): %s", strings.Join(missing, ", "))
	}
	return bound, nil
}

func sqlLiteral(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	default:
		return fmt.Sprint(v)
	}
}

// secureRowsToQueryRows adapts secureread.Result's rows to query_output.go's
// queryRow shape (structurally identical: Key/key, Data/data) so
// writeQueryRows renders a saved-query result exactly like an ad-hoc one,
// with zero changes to query_output.go.
func secureRowsToQueryRows(rows []secureread.Row) []queryRow {
	out := make([]queryRow, len(rows))
	for i, r := range rows {
		out[i] = queryRow{key: r.Key, data: r.Data}
	}
	return out
}

// writeSavedQueryLimitations reports secureread.Result.Limitations on
// stderr, mirroring queryRunCommandAction's own accesspolicies.Line
// reporting for the ad-hoc path (REQ:limitation-visible: every applied
// limitation must be caller-visible, never silent).
func writeSavedQueryLimitations(w interface{ Write([]byte) (int, error) }, limitations []secureread.Limitation) {
	for _, lim := range limitations {
		switch lim.Kind {
		case secureread.LimitationPolicy, secureread.LimitationNativeSQL:
			_, _ = fmt.Fprintln(w, lim.Note)
		case secureread.LimitationRowsFiltered:
			_, _ = fmt.Fprintln(w, "access: rows filtered by policy")
		case secureread.LimitationHiddenColumns:
			_, _ = fmt.Fprintln(w, "access: hidden columns: "+strings.Join(lim.Columns, ", "))
		}
	}
}

// savedQueryFailure maps a saved-query run error to the CLI's standard exit
// codes (same convention queryFailure uses for the ad-hoc path).
func savedQueryFailure(err error) error {
	switch {
	case errors.Is(err, secureread.ErrAccessDenied):
		return Exit(err.Error(), exitCodeAccessDenied)
	case errors.Is(err, secureread.ErrNoPrincipal), errors.Is(err, accesspolicies.ErrNoPolicies), errors.Is(err, accesspolicies.ErrInvalidQuery), errors.Is(err, secureread.ErrNativeSQLUnsupported):
		return Exit(err.Error(), exitCodeUsage)
	default:
		return Exit(err.Error(), exitCodeDatabase)
	}
}
