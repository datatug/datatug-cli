package commands

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/tugqlbind"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

// Cross-database TugQL (spec/features/cli/query, REQ:cross-database-sources).
// A text document's root `databases (` block declares aliases, and a source
// written `alias::path` reads the database bound to its alias. DALgo leaves
// binding to the caller. A saved query is bound by pkg/tugqlbind, for `query
// run`, `chat` and `serve` alike; this file binds an ad-hoc run and supplies
// the CLI's resolver of a saved one (savedBindingResolver):
//
//   - a `datatug://<project>/<db>[?env=]` target is that project source;
//   - an alias without a target is bound by --db <alias>=<url|datatug://...>
//     in an ad-hoc run, and by the project source of the same name in a saved
//     query;
//   - a connection-string target is allowed in an ad-hoc run only: a saved
//     query is project knowledge, and names its sources by project reference.
//
// Every bound source is read as its own secured leaf (RunFederatedTugQL), so
// access policies and sensitivity labels apply per source.

// databaseBinding is the source one database alias reads (pkg/tugqlbind).
type databaseBinding = tugqlbind.Binding

// dbAliasFlag matches a --db value that binds an alias: a TugQL word, then `=`.
// A source URL never matches: its scheme is followed by `:`.
var dbAliasFlag = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// parseDBFlags splits the --db values into the default database (a value with
// no alias) and the alias bindings.
func parseDBFlags(values []string) (string, map[string]string, error) {
	var bare string
	aliases := map[string]string{}
	for _, value := range values {
		if !dbAliasFlag.MatchString(value) {
			if bare != "" {
				return "", nil, Exit("--db names the default database once; bind each other database as --db <alias>=<url|datatug://...>", exitCodeUsage)
			}
			bare = value
			continue
		}
		alias, target, _ := strings.Cut(value, "=")
		if strings.TrimSpace(target) == "" {
			return "", nil, Exit(fmt.Sprintf("--db %s= names no source", alias), exitCodeUsage)
		}
		if _, bound := lookupFold(aliases, alias); bound {
			return "", nil, Exit(fmt.Sprintf("--db binds database %s more than once", alias), exitCodeUsage)
		}
		aliases[alias] = target
	}
	return bare, aliases, nil
}

// lookupFold finds name in m case-insensitively, as TugQL compares aliases.
func lookupFold(m map[string]string, name string) (string, bool) {
	for key, value := range m {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

// adHocDatabaseBindings binds every declared alias of an ad-hoc query: a --db
// binding first, else the declared target. An alias left unbound, and a --db
// alias the query does not declare, are usage errors naming the alias.
func adHocDatabaseBindings(ctx context.Context, declared []tugql.TugQLDatabase, flags map[string]string) (map[string]databaseBinding, error) {
	bindings := make(map[string]databaseBinding, len(declared))
	for _, database := range declared {
		target := database.Target
		if flag, ok := lookupFold(flags, database.Name); ok {
			target = flag
		}
		if target == "" {
			return nil, Exit(fmt.Sprintf("database %s is declared but not bound: pass --db %s=<url|datatug://<project>/<db>>", database.Name, database.Name), exitCodeUsage)
		}
		binding, err := adHocDatabaseBinding(ctx, database.Name, target)
		if err != nil {
			return nil, err
		}
		bindings[database.Name] = binding
	}
	aliases := make([]string, 0, len(flags))
	for alias := range flags {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		found := false
		for _, database := range declared {
			found = found || strings.EqualFold(database.Name, alias)
		}
		if !found {
			return nil, Exit(fmt.Sprintf("--db %s=...: the query declares no database %s", alias, alias), exitCodeUsage)
		}
	}
	return bindings, nil
}

// adHocDatabaseBinding resolves one ad-hoc target: a project source, or a
// connection string checked the way --db checks one.
func adHocDatabaseBinding(ctx context.Context, alias, target string) (databaseBinding, error) {
	if dbcopy.IsDatatugRef(target) {
		return projectDatabaseBinding(ctx, target)
	}
	if _, err := dbcopy.Parse(target); err != nil {
		return databaseBinding{}, Exit(fmt.Sprintf("database %s: %v", alias, err), exitCodeUsage)
	}
	return databaseBinding{URL: target}, nil
}

// projectDatabaseBinding resolves a datatug:// target through the project
// registry, exactly as --db datatug://... does.
func projectDatabaseBinding(ctx context.Context, target string) (databaseBinding, error) {
	reference, err := dbcopy.ParseDatatugRef(target)
	if err != nil {
		return databaseBinding{}, Exit(err.Error(), exitCodeUsage)
	}
	url, dir, err := datatugRefSource(ctx, reference)
	if err != nil {
		return databaseBinding{}, err
	}
	binding := databaseBinding{URL: url, ProjectDir: dir, Env: reference.Env, Catalog: reference.DB}
	if full, ok := fullDatatugRef(ctx, target); ok {
		binding.Ref, binding.Env = &full, full.Env
	}
	return binding, nil
}

// savedBindingResolver resolves a saved query's databases as `query run` and
// `chat` do: a name is the project source of that name, in the query's
// environment, and a datatug:// target in another project resolves through
// the project registry, exactly as --db datatug://... does.
func savedBindingResolver(store datatug.ProjectStore, projectDir, envFlag string) tugqlbind.Resolver {
	return tugqlbind.Resolver{
		ProjectID:  registeredProjectID(projectDir),
		ProjectDir: projectDir,
		Environment: func(ctx context.Context) (string, error) {
			return resolveQueryEnvironment(ctx, store, envFlag)
		},
		// The CLI names a source by its catalog id only, so the name is the catalog.
		Local: func(ctx context.Context, env, catalog string) (string, string, error) {
			url, err := resolveQuerySourceURL(ctx, store, projectDir, env, catalog)
			return url, catalog, err
		},
		Foreign: projectDatabaseBinding,
	}
}

// installBindingLabels checks every federated leaf against the labels of the
// project source its alias is bound to, named by that source's catalog id.
// A connection-string source belongs to no project, so no label names it, as
// for a --db read of one.
//
// alwaysStrict makes every read-time suggestion refuse, as for chat.
func installBindingLabels(session *secureread.Session, bindings map[string]databaseBinding, warn io.Writer, alwaysStrict bool) {
	strict := alwaysStrict || labelStrictness(*session)
	session.FederatedColumns = tugqlbind.LabelGuard(bindings, func(projectDir string) secureread.FederatedColumnGuard {
		return federatedLabelGuard(projectDir, strict, warn)
	})
}

// bindingSources describes a federated result's participants by their aliases'
// sources: each one's datatug:// reference, never its URL.
func bindingSources(bindings map[string]databaseBinding) tugqlSources {
	return tugqlSources{
		open: func(ctx context.Context, database string) (dal.DB, string, func(), error) {
			binding, ok := bindings[database]
			if !ok {
				return nil, "", nil, fmt.Errorf("database %q is not configured", database)
			}
			backend, err := chatParseSource(binding.URL)
			if err != nil {
				return nil, "", nil, err
			}
			db, err := openBackend(ctx, backend)
			if err != nil {
				return nil, backend.Scheme, nil, err
			}
			return db, backend.Scheme, dbcopy.SourceCloser(db), nil
		},
		ref: func(database string) *string {
			binding := bindings[database]
			if binding.Ref == nil {
				return nil
			}
			text := binding.Ref.String()
			return &text
		},
		scanned: func(database, schema, relation string) ([]string, bool) {
			binding := bindings[database]
			return scannedColumns(binding.ProjectDir, binding.Env, binding.Catalog)(database, schema, relation)
		},
	}
}

// rootBindingDB is meta.json's db of a federated query: the source of its root.
func rootBindingDB(text string, bindings map[string]databaseBinding) queryresults.DB {
	query, err := tugql.Deserialize([]byte(text))
	if err != nil {
		return queryresults.DB{}
	}
	root, _ := query.From().Base().(dal.CollectionRef)
	binding, ok := bindings[root.Database()]
	if !ok {
		return queryresults.DB{}
	}
	backend, _ := dbcopy.Parse(binding.URL) // a bound URL was parsed when it was bound
	if binding.Ref == nil {
		return queryresults.DB{Driver: backend.Scheme}
	}
	return projectDB(*binding.Ref, backend.Scheme)
}

// describeFederatedAdHoc records an ad-hoc cross-database run.
func (s *resultSink) describeFederatedAdHoc(ctx context.Context, o queryOptions, text string, variables map[string]any, bindings map[string]databaseBinding) {
	sources := bindingSources(bindings)
	s.describe = func(columns []string) resultDescription {
		return describeTugQL(ctx, text, columns, sources)
	}
	s.meta.DB = rootBindingDB(text, bindings)
	s.meta.Query = queryresults.Query{Kind: queryresults.QueryKindTugQL, TugQL: text, Parameters: typedParameters(variables), Policy: adHocPolicy(o)}
	s.meta.SetEditor(text, queryresults.LanguageTugQL)
}

// runFederatedAdHocQuery runs an ad-hoc document that declares databases.
func runFederatedAdHocQuery(cmd *cobra.Command, o queryOptions, document adHocQuery) error {
	names := make([]string, len(document.databases))
	for i, database := range document.databases {
		names[i] = database.Name
	}
	switch {
	case o.source != "":
		return Exit("a query with a databases block reads the sources it binds: --project/--source does not go with it", exitCodeUsage)
	case strings.TrimSpace(o.db) != "":
		return Exit(fmt.Sprintf("the query declares databases (%s) and reads every source through one of them, so a --db without an alias has no source to bind; bind each as --db <alias>=<url|datatug://...>", strings.Join(names, ", ")), exitCodeUsage)
	}
	if name := tugqlbind.Unqualified(document.query.(dal.StructuredQuery)); name != "" {
		return Exit(tugqlbind.UnqualifiedMessage(name), exitCodeUsage)
	}
	// queryRunCommandAction always installs the read's cancellable context.
	ctx := cmd.Context()
	bindings, err := adHocDatabaseBindings(ctx, document.databases, o.dbAliases)
	if err != nil {
		return err
	}
	variables, err := accesspolicies.ParseVariables(o.vars)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: o.as, Roles: o.roles, Groups: o.groups, PoliciesDir: o.policiesDir, PolicyFiles: o.policies, NoPolicies: o.noPolicies})
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	stderr := cmd.ErrOrStderr()
	installBindingLabels(&session, bindings, stderr, false)
	if len(session.Policies) == 0 {
		_, _ = fmt.Fprintln(stderr, "access: running without access policies")
	}
	if !o.quiet {
		ctx = secureread.WithFederatedProgress(ctx, func(item dal.FederatedProgress) {
			_, _ = fmt.Fprintf(stderr, "query %s: %d rows %s\n", item.Phase, item.Rows, item.Database)
		})
	}
	result, err := secureread.NewExecutor(session).RunFederatedTugQL(ctx, []byte(document.text), tugqlbind.URLs(bindings), variables)
	if err != nil {
		return savedReadFailure(stderr, o, err)
	}
	if !o.quiet {
		writeSavedQueryLimitations(stderr, result.Limitations)
	}
	if o.sink != nil {
		o.sink.describeFederatedAdHoc(ctx, o, document.text, variables, bindings)
		return o.sink.writeSecureRows(cmd.OutOrStdout(), result.Columns, result.Rows)
	}
	if err := writeQueryRows(cmd.OutOrStdout(), o.format, result.Columns, secureRowsToQueryRows(result.Rows), nil); err != nil {
		return Exit(err.Error(), 1)
	}
	return nil
}
