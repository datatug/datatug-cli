package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/querybody"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

// Cross-database TugQL (spec/features/cli/query, REQ:cross-database-sources).
// A text document's root `databases (` block declares aliases, and a source
// written `alias::path` reads the database bound to its alias. DALgo leaves
// binding to the caller; this file is that caller for `query run`:
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

// databaseBinding is the source one database alias reads.
type databaseBinding struct {
	// url is the source opened; it never leaves the process.
	url string
	// ref is the datatug:// reference of a project source, written in full,
	// or nil for a connection-string target. It is the only form of the
	// source a result's meta.json records.
	ref *dbcopy.DatatugRef
	// projectDir, env and catalog name the project source whose labels apply;
	// projectDir is "" for a connection-string target, which no label names.
	projectDir string
	env        string
	catalog    string
}

// databaseBindingError is a database alias that cannot be bound: a usage error.
type databaseBindingError string

func (e databaseBindingError) Error() string { return string(e) }

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
	return databaseBinding{url: target}, nil
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
	binding := databaseBinding{url: url, projectDir: dir, env: reference.Env, catalog: reference.DB}
	if full, ok := fullDatatugRef(ctx, target); ok {
		binding.ref, binding.env = &full, full.Env
	}
	return binding, nil
}

// savedDatabaseBindings binds every database a saved TugQL query reads: the
// aliases its text declares, and the database names a structured document's
// sources carry (referenced). A name without a target is the project source of
// that name in the query's environment.
func savedDatabaseBindings(ctx context.Context, store datatug.ProjectStore, projectDir, envFlag string, queryDef *datatug.QueryDef, referenced map[string]bool) (map[string]databaseBinding, error) {
	declared, err := querybody.Databases(queryDef)
	if err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, database := range declared {
		targets[database.Name] = database.Target
		referenced[database.Name] = true
	}
	if len(referenced) == 0 {
		return nil, nil
	}
	envID, err := resolveQueryEnvironment(ctx, store, envFlag)
	if err != nil {
		return nil, err
	}
	projectID := registeredProjectID(projectDir)
	names := make([]string, 0, len(referenced))
	for name := range referenced {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make(map[string]databaseBinding, len(names))
	for _, name := range names {
		binding, err := savedDatabaseBinding(ctx, store, projectDir, projectID, envID, queryDef.ID, name, targets[name])
		if err != nil {
			return nil, err
		}
		bindings[name] = binding
	}
	return bindings, nil
}

func savedDatabaseBinding(ctx context.Context, store datatug.ProjectStore, projectDir, projectID, envID, queryID, name, target string) (databaseBinding, error) {
	catalog := name
	if target != "" {
		// The target itself is not repeated: it may name a host.
		if !dbcopy.IsDatatugRef(target) {
			return databaseBinding{}, databaseBindingError(fmt.Sprintf("saved query %q: database %s is bound to a connection string; a saved query binds a database to a project source, datatug://<project>/<db>[?env=<ENV>], or leaves the target empty for the project source of the same name", queryID, name))
		}
		reference, err := dbcopy.ParseDatatugRef(target)
		if err != nil {
			return databaseBinding{}, databaseBindingError(fmt.Sprintf("saved query %q: database %s: %v", queryID, name, err))
		}
		if reference.Project != "" && reference.Project != projectID {
			return projectDatabaseBinding(ctx, target)
		}
		catalog = reference.DB
		if reference.Env != "" {
			envID = reference.Env
		}
	}
	url, err := resolveQuerySourceURL(ctx, store, projectDir, envID, catalog)
	if err != nil {
		return databaseBinding{}, err
	}
	binding := databaseBinding{url: url, projectDir: projectDir, env: envID, catalog: catalog}
	if projectID != "" {
		binding.ref = &dbcopy.DatatugRef{Project: projectID, Env: envID, DB: catalog}
	}
	return binding, nil
}

// unqualifiedSource is the first source of from that names no database, or "".
// DALgo's federated execution needs every source qualified: a query with a
// databases block cannot also read the default database.
func unqualifiedSource(from dal.FromSource) string {
	switch ref := from.Base().(type) {
	case dal.CollectionRef:
		if ref.Database() == "" {
			return ref.Name()
		}
	case *dal.CollectionRef:
		if ref.Database() == "" {
			return ref.Name()
		}
	}
	for _, joined := range from.Joins() {
		child := joined.From()
		if child == nil {
			child = dal.From(joined.RecordsetSource)
		}
		if name := unqualifiedSource(child); name != "" {
			return name
		}
	}
	return ""
}

// unqualifiedSourceMessage refuses a source with no alias next to a databases block.
func unqualifiedSourceMessage(name string) string {
	return fmt.Sprintf("source %s names no database: a query with a databases block reads every source as <alias>::%s", name, name)
}

// bindingURLs is the source URL of each alias, nil when there is none.
func bindingURLs(bindings map[string]databaseBinding) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	urls := make(map[string]string, len(bindings))
	for alias, binding := range bindings {
		urls[alias] = binding.url
	}
	return urls
}

// installBindingLabels checks every federated leaf against the labels of the
// project source its alias is bound to, named by that source's catalog id.
// A connection-string source belongs to no project, so no label names it, as
// for a --db read of one.
func installBindingLabels(session *secureread.Session, bindings map[string]databaseBinding, warn io.Writer) {
	strict := labelStrictness(*session)
	guards := map[string]secureread.FederatedColumnGuard{}
	session.FederatedColumns = func(ctx context.Context, leaf secureread.FederatedLeaf) error {
		binding, ok := bindings[leaf.Database]
		if !ok {
			return fmt.Errorf("database %q is not configured", leaf.Database)
		}
		if binding.projectDir == "" {
			return nil
		}
		guard := guards[binding.projectDir]
		if guard == nil {
			guard = federatedLabelGuard(binding.projectDir, strict, warn)
			guards[binding.projectDir] = guard
		}
		leaf.Database = binding.catalog
		return guard(ctx, leaf)
	}
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
			backend, err := chatParseSource(binding.url)
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
			if binding.ref == nil {
				return nil
			}
			text := binding.ref.String()
			return &text
		},
		scanned: func(database, schema, relation string) ([]string, bool) {
			binding := bindings[database]
			return scannedColumns(binding.projectDir, binding.env, binding.catalog)(database, schema, relation)
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
	backend, _ := dbcopy.Parse(binding.url) // a bound URL was parsed when it was bound
	if binding.ref == nil {
		return queryresults.DB{Driver: backend.Scheme}
	}
	return projectDB(*binding.ref, backend.Scheme)
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
	if name := unqualifiedSource(document.query.(dal.StructuredQuery).From()); name != "" {
		return Exit(unqualifiedSourceMessage(name), exitCodeUsage)
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
	installBindingLabels(&session, bindings, stderr)
	if len(session.Policies) == 0 {
		_, _ = fmt.Fprintln(stderr, "access: running without access policies")
	}
	if !o.quiet {
		ctx = secureread.WithFederatedProgress(ctx, func(item dal.FederatedProgress) {
			_, _ = fmt.Fprintf(stderr, "query %s: %d rows %s\n", item.Phase, item.Rows, item.Database)
		})
	}
	result, err := secureread.NewExecutor(session).RunFederatedTugQL(ctx, []byte(document.text), bindingURLs(bindings), variables)
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

// isDatabaseBindingError reports a saved query's unbindable database alias.
func isDatabaseBindingError(err error) bool {
	var binding databaseBindingError
	return errors.As(err, &binding)
}
