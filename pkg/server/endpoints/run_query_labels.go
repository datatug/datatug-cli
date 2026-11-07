package endpoints

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/callercontext"
	"github.com/datatug/datatug-cli/pkg/labelguard"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// serveCallerIsAgent reports whether this serve process runs under an AI
// agent harness (callercontext). Like the agent header, it is self-reported,
// so it only ever makes a read stricter.
var serveCallerIsAgent = detectServeCallerIsAgent

func detectServeCallerIsAgent() bool { return callercontext.Detect(os.LookupEnv).Harness != "" }

// policyFreeReads reports whether the served session is the policy-free
// profile of a local project owner.
var policyFreeReads = api.SecurePolicyFreeReads

// labelWarnings is where a policy-free owner's read of columns that only
// read-time suggestions protect is reported: the serve process's stderr, once
// per column for the process's life.
var labelWarnings = labelguard.NewWarner(os.Stderr)

// runQueryStrict reports whether read-time suggestions alone refuse an
// exec/run_query read: for an agent (the request's agent header, which every
// MCP request carries, or a serve process under an agent harness) and for a
// policy-governed session. Without either signal the caller is the policy-free
// owner, who reads on with a warning.
func runQueryStrict(ctx context.Context) bool {
	return agentFromContext(ctx) || serveCallerIsAgent() || !policyFreeReads()
}

// labelledRunQueryExecutor is executor with the project's sensitivity labels
// checked before every exec/run_query read, whoever the caller is: TugQL
// (ad hoc or saved), saved native SQL and saved HTTP queries alike
// (ai-onboarding: labelled columns are protected for every principal).
//
// A stored protected label (owner or proposed, on a column or its table)
// refuses for every principal. A column only a read-time suggestion protects
// refuses a strict principal (runQueryStrict); anyone else reads it and the
// serve log warns once. The agent guards (agentTugQLGuard,
// agentSavedSQLRefusal) still run first for an agent: they refuse what the
// masking routes would mask.
func labelledRunQueryExecutor(ctx context.Context, executor *secureread.Executor, projStore datatug.ProjectStore, projectDir, env string, source api.ResolvedSource) *secureread.Executor {
	guard := &runQueryLabels{projStore: projStore, projectDir: projectDir, env: env, source: source, strict: runQueryStrict(ctx)}
	return executor.WithLabels(guard.columns, guard.nativeSQL, map[string][]string{source.URL: {source.ID}})
}

type runQueryLabels struct {
	projStore  datatug.ProjectStore
	projectDir string
	env        string
	source     api.ResolvedSource
	strict     bool
}

// errRunQueryLabelsUnreadable refuses a read whose project labels cannot be read.
var errRunQueryLabelsUnreadable = &secureread.FieldProtectedError{Message: "the project's sensitivity labels (datatug-labels.json) cannot be read, so the read is refused"}

func (g *runQueryLabels) load(ctx context.Context) (meaninglabels.Document, error) {
	doc, err := labelsStoreFor(g.projectDir).Load(ctx)
	if err != nil {
		return meaninglabels.Document{}, errRunQueryLabelsUnreadable
	}
	return doc, nil
}

// tableColumns lists a table's columns from the project's description of the
// source; ok is false when it has none.
func (g *runQueryLabels) tableColumns(ctx context.Context, table string) ([]string, bool) {
	resolved, err := resolveSourceTableHook(ctx, g.projStore, g.projectDir, g.env, g.source.ID, table)
	if err != nil || len(resolved.Columns) == 0 {
		return nil, false
	}
	names := make([]string, len(resolved.Columns))
	for i, c := range resolved.Columns {
		names[i] = c.Name
	}
	return names, true
}

// isView reports whether table is a view of a SQL source; a source whose
// objects cannot be listed treats it as one (it masks whole, fail closed).
func (g *runQueryLabels) isView(ctx context.Context, table string) bool {
	if g.source.Kind != api.SourceKindSQL {
		return false
	}
	objects, err := listSourceObjectsHook(ctx, g.source.URL)
	if err != nil {
		return true
	}
	for _, o := range objects {
		if strings.EqualFold(o.Name, table) {
			return o.View
		}
	}
	return false
}

// columns is the TugQL check of one read: the columns it names on its table,
// or all of them for a wildcard (or no column list). The source is opened
// through the session's own opener first, so a source the session refuses is
// never described. A source with no stored protected label is judged by
// read-time suggestions only, and a wildcard whose columns cannot be listed
// passes there; a source with one refuses what it cannot open or describe.
func (g *runQueryLabels) columns(ctx context.Context, leaf secureread.FederatedLeaf) error {
	doc, err := g.load(ctx)
	if err != nil {
		return err
	}
	if leaf.AllColumns {
		if l, ok := labelguard.StoredTableLabel(doc, leaf.Database, leaf.Collection); ok {
			return runQueryProtected(leaf.Database, leaf.Collection, l)
		}
	}
	labelled := labelguard.SourceHasProtectedLabel(doc, leaf.Database)
	opened := false
	if labelled || leaf.AllColumns {
		if _, err := leaf.Open(ctx); err == nil {
			opened = true
		} else if labelled {
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s cannot be opened to check its sensitivity labels, so the read is refused", leaf.Database)}
		}
	}
	names := leaf.Columns
	if leaf.AllColumns {
		var columns []string
		ok := false
		if opened {
			columns, ok = g.tableColumns(ctx, leaf.Collection)
		}
		if !ok && labelled {
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("the columns of %s.%s cannot be listed to check their sensitivity labels; name the columns the query reads", leaf.Database, leaf.Collection)}
		}
		names = append(names, columns...)
	}
	view := labelled && g.isView(ctx, leaf.Collection)
	refused, suggested := labelguard.CheckColumns(doc, leaf.Database, leaf.Collection, view, names, g.strict)
	if refused != nil {
		return runQueryProtected(leaf.Database, leaf.Collection, *refused)
	}
	labelWarnings.Warn("this exec/run_query read", leaf.Database, suggested)
	return nil
}

// nativeSQL is the check of a saved native SQL read: its text cannot be
// inspected, so the whole source is judged. A stored protected label refuses
// it for every principal; read-time suggestions refuse a strict principal,
// as does a source whose tables cannot be listed to check them.
func (g *runQueryLabels) nativeSQL(ctx context.Context, catalog, sourceURL string) error {
	doc, err := g.load(ctx)
	if err != nil {
		return err
	}
	if labelguard.SourceHasProtectedLabel(doc, catalog) {
		return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s has columns protected by sensitivity labels, and native SQL cannot be masked, so the read is refused; use a TugQL query, which is checked column by column", catalog)}
	}
	objects, err := listSourceObjectsHook(ctx, sourceURL)
	if err != nil {
		if g.strict {
			return &secureread.FieldProtectedError{Message: fmt.Sprintf("the tables of source %s cannot be listed to check their sensitivity labels, so the read is refused", catalog)}
		}
		return nil
	}
	tables := map[string][]meaninglabels.Column{}
	for _, o := range objects {
		columns, ok := g.tableColumns(ctx, o.Name)
		if !ok {
			columns = o.Columns
		}
		tables[o.Name] = labelguard.Columns(columns)
	}
	suggested := labelguard.SuggestedColumns(doc, catalog, tables)
	switch {
	case len(suggested) == 0:
		return nil
	case g.strict:
		return &secureread.FieldProtectedError{Message: fmt.Sprintf("source %s has columns that read-time suggestions mark sensitive (%s), and native SQL cannot be masked, so the read is refused; use a TugQL query, which is checked column by column", catalog, strings.Join(suggested, ", "))}
	default:
		labelWarnings.Warn("native SQL", catalog, suggested)
		return nil
	}
}

// runQueryProtected is the FIELD_PROTECTED refusal of a TugQL read.
func runQueryProtected(source, table string, l meaninglabels.ColumnLabel) error {
	target := source + "." + table
	if l.Column != "" {
		target += "." + l.Column
	}
	return &secureread.FieldProtectedError{Message: fmt.Sprintf("%s is protected (%s), and exec/run_query cannot mask labelled columns, so the read is refused; name only unlabelled columns, or read the table with records/read to get it masked", target, l.Class)}
}
