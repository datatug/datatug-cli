// Package tugqlbind binds the databases a saved TugQL query reads to project
// sources (spec/features/cli/query, REQ:cross-database-sources). A text body's
// root `databases (` block declares aliases and an `alias::path` source reads
// the database bound to its alias; a structured body names a source's database
// with its `database` key. DALgo leaves binding to the caller: this package is
// that caller for every surface that runs a saved query — `query run`, `chat`
// and `datatug serve` — so each binds the same way.
//
// A name without a target is the project source of that name in the run's
// environment. A `datatug://<project>/<db>[?env=]` target is that project
// source. A connection-string target is refused: a saved query is project
// knowledge, and names its sources by project reference.
package tugqlbind

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/querybody"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// Binding is the source one database alias reads.
type Binding struct {
	// URL is the source opened; it never leaves the process.
	URL string
	// Ref is the datatug:// reference of a project source, written in full, or
	// nil for a source with no registered project. It is the only form of the
	// source a result records.
	Ref *dbcopy.DatatugRef
	// ProjectDir, Env and Catalog name the project source whose labels apply;
	// ProjectDir is "" for a source no label names.
	ProjectDir string
	Env        string
	Catalog    string
}

// Error is a database of a saved query that cannot be bound: a usage error.
type Error string

func (e Error) Error() string { return string(e) }

// IsError reports a saved query's unbindable database.
func IsError(err error) bool {
	var binding Error
	return errors.As(err, &binding)
}

// Resolver resolves the project sources of one saved-query run.
type Resolver struct {
	// ProjectID is the registered id of the query's project, "" when it has none.
	ProjectID string
	// ProjectDir is the query's project directory.
	ProjectDir string
	// Environment is the run's environment. It is called only when the query
	// binds a database.
	Environment func(ctx context.Context) (string, error)
	// Local resolves a name of a source of the query's own project in env to
	// the source's URL and its catalog id: the one name its sensitivity labels
	// use, whichever of the source's names the query wrote.
	Local func(ctx context.Context, env, name string) (url, catalog string, err error)
	// Foreign binds a datatug:// target that names another project. Nil
	// refuses one.
	Foreign func(ctx context.Context, target string) (Binding, error)
}

// Saved parses a saved TugQL query once and binds every database it reads:
// the aliases its text declares and the database names its sources carry,
// subqueries included. A query that reads no named database has no bindings.
func Saved(ctx context.Context, queryDef *datatug.QueryDef, r Resolver) (dal.StructuredQuery, map[string]Binding, error) {
	document, declared := queryDef.Text, []tugql.TugQLDatabase(nil)
	if queryDef.Type == datatug.QueryTypeTugQL && !queryDef.StructuredBody {
		var err error
		if document, declared, err = querybody.ParseText(queryDef.Text); err != nil {
			return nil, nil, err
		}
	}
	parsed, err := tugql.Deserialize([]byte(document))
	if err != nil {
		return nil, nil, err
	}
	referenced := Databases(parsed)
	targets := map[string]string{}
	for _, database := range declared {
		targets[database.Name] = database.Target
		referenced[database.Name] = true
	}
	if len(referenced) == 0 {
		return parsed, nil, nil
	}
	if name := Unqualified(parsed); name != "" {
		return nil, nil, Error(fmt.Sprintf("saved query %q: %s", queryDef.ID, SavedUnqualifiedMessage(name)))
	}
	env, err := r.Environment(ctx)
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(referenced))
	for name := range referenced {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make(map[string]Binding, len(names))
	for _, name := range names {
		binding, err := r.bind(ctx, env, queryDef.ID, name, targets[name])
		if err != nil {
			return nil, nil, err
		}
		bindings[name] = binding
	}
	return parsed, bindings, nil
}

func (r Resolver) bind(ctx context.Context, env, queryID, name, target string) (Binding, error) {
	catalog := name
	if target != "" {
		// The target itself is not repeated: it may name a host.
		if !dbcopy.IsDatatugRef(target) {
			return Binding{}, Error(fmt.Sprintf("saved query %q: database %s is bound to a connection string; a saved query binds a database to a project source, datatug://<project>/<db>[?env=<ENV>], or leaves the target empty for the project source of the same name", queryID, name))
		}
		reference, err := dbcopy.ParseDatatugRef(target)
		if err != nil {
			return Binding{}, Error(fmt.Sprintf("saved query %q: database %s: %v", queryID, name, err))
		}
		if reference.Project != "" && reference.Project != r.ProjectID {
			if r.Foreign == nil {
				return Binding{}, Error(fmt.Sprintf("saved query %q: database %s names another project; here a saved query binds only sources of its own project", queryID, name))
			}
			return r.Foreign(ctx, target)
		}
		catalog = reference.DB
		if reference.Env != "" {
			env = reference.Env
		}
	}
	url, catalog, err := r.Local(ctx, env, catalog)
	if err != nil {
		return Binding{}, err
	}
	binding := Binding{URL: url, ProjectDir: r.ProjectDir, Env: env, Catalog: catalog}
	if r.ProjectID != "" {
		binding.Ref = &dbcopy.DatatugRef{Project: r.ProjectID, Env: env, DB: catalog}
	}
	return binding, nil
}

// URLs is the source URL of each alias, nil when there is none.
func URLs(bindings map[string]Binding) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	urls := make(map[string]string, len(bindings))
	for alias, binding := range bindings {
		urls[alias] = binding.URL
	}
	return urls
}

// UnqualifiedMessage refuses a source with no alias next to a databases block.
func UnqualifiedMessage(name string) string {
	return fmt.Sprintf("source %s names no database: a query with a databases block reads every source as <alias>::%s", name, name)
}

// SavedUnqualifiedMessage refuses a saved query's source that names no
// database while another does. It is worded for both forms of a saved query.
func SavedUnqualifiedMessage(name string) string {
	return fmt.Sprintf("source %s names no database: when any source of a query names its database (an <alias>::path source of a databases block, or a structured source's database key), every source, subqueries included, names one", name)
}

// Root is the binding of the first database query reads, in walk order: the
// root source's, or a FROM subquery's first. It names the source a result
// reports as the one it ran on.
func Root(query dal.StructuredQuery, bindings map[string]Binding) (Binding, bool) {
	var root Binding
	found := false
	Sources(query, func(ref dal.CollectionRef) {
		if !found {
			root, found = bindings[ref.Database()]
		}
	})
	return root, found
}

// LabelGuard checks every federated leaf against the labels of the project
// source its alias is bound to, named by that source's catalog id. guardFor
// builds the label guard of one project directory; it is called once per
// directory. A source of no project (ProjectDir "") is named by no label.
func LabelGuard(bindings map[string]Binding, guardFor func(projectDir string) secureread.FederatedColumnGuard) secureread.FederatedColumnGuard {
	guards := map[string]secureread.FederatedColumnGuard{}
	return func(ctx context.Context, leaf secureread.FederatedLeaf) error {
		binding, ok := bindings[leaf.Database]
		if !ok {
			return fmt.Errorf("database %q is not configured", leaf.Database)
		}
		if binding.ProjectDir == "" {
			return nil
		}
		guard, built := guards[binding.ProjectDir]
		if !built {
			guard = guardFor(binding.ProjectDir)
			guards[binding.ProjectDir] = guard
		}
		leaf.Database = binding.Catalog
		return guard(ctx, leaf)
	}
}
