package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/strongo/buildinfo"
)

// reasonQueryFailed is the failure-line reason of every failure that is not a
// result-store failure; its details stay on stderr.
const reasonQueryFailed = "query-failed"

// openResultsStore opens the result store; a seam so tests can point it at a
// store whose checks fail.
var openResultsStore = queryresults.OpenDefault

// cliVersion is the version meta.json records.
var cliVersion = func() string { return buildinfo.Get("datatug").Version }

// checkResultFlags checks --result-id and --result-rows.
func checkResultFlags(o queryOptions, formatChanged, rowsChanged bool) error {
	switch {
	case o.resultID == "" && rowsChanged:
		return Exit("--result-rows works only with --result-id", exitCodeUsage)
	case o.resultID == "":
		return nil
	case !queryresults.ValidID(o.resultID):
		return storeExit(&queryresults.Error{Reason: queryresults.ReasonBadID})
	case formatChanged && o.format != "json":
		return Exit("--result-id writes JSON files: leave out --format or use --format json", exitCodeUsage)
	case o.resultRows < queryresults.MinRows || o.resultRows > queryresults.MaxRows:
		return Exit(fmt.Sprintf("--result-rows must be from %d to %d", queryresults.MinRows, queryresults.MaxRows), exitCodeUsage)
	}
	return nil
}

// resultStoreError is a result-store failure as an exit-2 error; its message
// is the reason code alone, with no path and no id.
type resultStoreError struct{ reason string }

func (e *resultStoreError) Error() string { return (&queryresults.Error{Reason: e.reason}).Error() }
func (e *resultStoreError) ExitCode() int { return exitCodeUsage }
func (e *resultStoreError) Unwrap() error { return &queryresults.Error{Reason: e.reason} }

func storeExit(err error) error { return &resultStoreError{reason: queryresults.ReasonOf(err)} }

// resultFailureReason is the reason the failure line names for err.
func resultFailureReason(err error) string {
	if reason := queryresults.ReasonOf(err); reason != "" {
		return reason
	}
	return reasonQueryFailed
}

// resultSink is a --result-id result being written. It is opened before the
// database is, so an unusable store fails before any row is read.
type resultSink struct {
	id      string
	rows    int
	store   *queryresults.Store
	result  *queryresults.Result
	meta    queryresults.Meta
	written bool
	// describe reads the schema facts of the result once its columns are
	// known; nil describes nothing.
	describe func(columns []string) resultDescription
}

func openResultSink(o queryOptions) (*resultSink, error) {
	store, err := openResultsStore()
	if err != nil {
		return nil, storeExit(err)
	}
	result, err := store.Create(o.resultID)
	if err != nil {
		return nil, storeExit(err)
	}
	return &resultSink{id: o.resultID, rows: o.resultRows, store: store, result: result}, nil
}

// discardUnlessWritten removes the result directory of a run that failed.
func (s *resultSink) discardUnlessWritten() {
	if !s.written {
		s.result.Discard()
	}
}

// describeAdHoc records the database and the query of an ad-hoc run.
func (s *resultSink) describeAdHoc(ctx context.Context, o queryOptions, backend dbcopy.BackendRef, db dal.DB, queryText string, variables map[string]any) {
	s.describe = func(columns []string) resultDescription {
		return describeDTQL(ctx, queryText, columns, dtqlSources{
			open: func(context.Context, string) (dal.DB, string, func(), error) {
				return db, backend.Scheme, func() {}, nil
			},
		})
	}
	s.meta.DB = queryresults.DB{Driver: backend.Scheme}
	if ref, ok := fullDatatugRef(ctx, o.db); ok {
		s.meta.DB = projectDB(ref, backend.Scheme)
	}
	s.meta.Query = queryresults.Query{Kind: queryresults.QueryKindDTQL, DTQL: queryText, Parameters: typedParameters(variables), Policy: adHocPolicy(o)}
	if o.from != "" {
		s.meta.Query.Kind, s.meta.Query.From = queryresults.QueryKindFrom, o.from
	}
}

// describeSaved records the database and the query of a saved-query run.
func (s *resultSink) describeSaved(ctx context.Context, store datatug.ProjectStore, projectDir string, o queryOptions, queryID string, queryDef *datatug.QueryDef, variables map[string]any, result secureread.Result) {
	s.meta.Query = queryresults.Query{Kind: queryresults.QueryKindSaved, SavedQueryID: queryID, SavedQueryType: strings.ToLower(string(queryDef.Type)), Parameters: typedParameters(variables), Policy: queryresults.PolicyProject}
	s.meta.DB = savedQueryDB(ctx, store, projectDir, o.project, o.env, queryDef)
	switch queryDef.Type {
	case datatug.QueryTypeSQL:
		s.describe = func([]string) resultDescription { return describeNativeSQL(result.ColumnTypes) }
	case datatug.QueryTypeHTTP:
		s.describe = func([]string) resultDescription { return describeHTTP(result.Collection) }
	default:
		sources := savedDTQLSources(ctx, store, projectDir, o.env, queryDef)
		s.describe = func(columns []string) resultDescription {
			return describeDTQL(ctx, queryDef.Text, columns, sources)
		}
	}
}

// savedDTQLSources opens the databases of a saved DTQL query: each named
// database of a federated query, else the one it runs on.
func savedDTQLSources(ctx context.Context, store datatug.ProjectStore, projectDir, envFlag string, queryDef *datatug.QueryDef) dtqlSources {
	envID, _ := resolveQueryEnvironment(ctx, store, envFlag)
	projectID := registeredProjectID(projectDir)
	return dtqlSources{
		open: func(ctx context.Context, database string) (dal.DB, string, func(), error) {
			var source string
			var err error
			if database == "" {
				source, err = resolveSQLOrDTQLSourceURL(ctx, store, projectDir, envFlag, queryDef)
			} else {
				source, err = resolveQuerySourceURL(ctx, store, projectDir, envID, database)
			}
			if err != nil {
				return nil, "", nil, err
			}
			backend, err := chatParseSource(source)
			if err != nil {
				return nil, "", nil, err
			}
			db, err := openBackend(ctx, backend)
			if err != nil {
				return nil, backend.Scheme, nil, err
			}
			return db, backend.Scheme, func() {
				if closer, ok := db.(io.Closer); ok {
					_ = closer.Close()
				}
			}, nil
		},
		ref: func(database string) *string {
			if projectID == "" || envID == "" {
				return nil
			}
			text := dbcopy.DatatugRef{Project: projectID, DB: database, Env: envID}.String()
			return &text
		},
	}
}

// collectAndWrite reads at most --result-rows + 1 records and writes the files.
func (s *resultSink) collectAndWrite(stdout io.Writer, reader dal.RecordsReader, query dal.Query) error {
	collector := queryresults.NewCollector(s.rows)
	if err := collectResultRows(reader, collector); err != nil {
		return queryFailure(err)
	}
	columns, err := rowColumns(query, fieldRows(collector))
	if err != nil {
		return Exit(err.Error(), 1)
	}
	return s.write(stdout, collector, columns)
}

// writeSecureRows keeps the first --result-rows rows of a collected
// saved-query result and writes the files.
func (s *resultSink) writeSecureRows(stdout io.Writer, columns []string, rows []secureread.Row) error {
	collector := queryresults.NewCollector(s.rows)
	for _, row := range rows {
		if _, found := row.Data[keyColumn]; found {
			return Exit(errReservedKeyField.Error(), 1)
		}
		if !collector.Add(row.Key, row.Data) {
			break
		}
	}
	return s.write(stdout, collector, columns)
}

func (s *resultSink) write(stdout io.Writer, collector *queryresults.Collector, columns []string) error {
	description := resultDescription{}
	if s.describe != nil {
		description = s.describe(columns)
	}
	data := collector.Build(s.id, columns, description.declared)
	meta := queryresults.NewMeta(s.id, cliVersion(), data)
	meta.DB, meta.Query, meta.Provenance = s.meta.DB, s.meta.Query, s.meta.Provenance
	meta.Describe(description.facts)
	meta.Participants, meta.Related = description.participants, description.related
	meta.Unavailable.Participants, meta.Unavailable.Related, meta.Unavailable.DeclaredTypes = description.unavailable.Participants, description.unavailable.Related, description.unavailable.DeclaredTypes
	if err := s.result.Write(data.Bytes, meta.Encode()); err != nil {
		return storeExit(err)
	}
	s.written = true
	_, _ = s.store.Prune(false)
	if _, err := stdout.Write(queryresults.LocatorLine(data)); err != nil {
		return Exit(err.Error(), 1)
	}
	return nil
}

// collectResultRows feeds reader's records to collector until it wants no
// more; it never reads the whole source to count it.
func collectResultRows(reader dal.RecordsReader, collector *queryresults.Collector) error {
	if reader == nil {
		return nil
	}
	defer func() { _ = reader.Close() }()
	for {
		rec, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) || (err == nil && rec == nil) {
			return nil
		}
		if err != nil {
			return err
		}
		data := map[string]any{}
		if rec.Exists() {
			if data, err = queryresults.RecordData(rec.Data()); err != nil {
				return err
			}
		}
		if !collector.Add(fmt.Sprint(rec.Key().ID), data) {
			return nil
		}
	}
}

// fieldRows is the collected rows' field names in the shape rowColumns takes.
func fieldRows(collector *queryresults.Collector) []queryRow {
	names := collector.FieldNames()
	rows := make([]queryRow, len(names))
	for i, fields := range names {
		rows[i].data = make(map[string]any, len(fields))
		for _, name := range fields {
			rows[i].data[name] = nil
		}
	}
	return rows
}

// resultProvenance is the provenance meta.json records: the mode and the time,
// never a URL.
func resultProvenance(prov dalgo2http.Provenance) *queryresults.Provenance {
	mode := apicontract.ProvenanceModeLive
	if prov.Source == dalgo2http.SourceSnapshot {
		mode = apicontract.ProvenanceModeSnapshot
	}
	return &queryresults.Provenance{Mode: mode, ObservedAt: newQueryProvenance(prov).FetchedAt}
}

// adHocPolicy names where an ad-hoc run's policies came from, never a path.
func adHocPolicy(o queryOptions) string {
	switch {
	case o.noPolicies:
		return queryresults.PolicyNone
	case len(o.policies) > 0:
		return queryresults.PolicyExplicit
	case o.policiesDir != "" || os.Getenv(accesspolicies.DirEnv) != "":
		return queryresults.PolicyDir
	default:
		return queryresults.PolicyDiscovered
	}
}

// typedParameters is the --var values as typed values; a list is a set.
func typedParameters(variables map[string]any) map[string]apicontract.TypedValueOrSet {
	if len(variables) == 0 {
		return nil
	}
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Strings(names)
	typed := make(map[string]apicontract.TypedValueOrSet, len(variables))
	for _, name := range names {
		value := variables[name]
		if list, isList := value.([]any); isList {
			values := make([]apicontract.TypedValue, len(list))
			for i, item := range list {
				values[i] = queryresults.ConvertValue(item)
			}
			if set, err := apicontract.NewTypedValueSet(values...); err == nil {
				typed[name] = apicontract.SetValue(set)
				continue
			}
		}
		typed[name] = apicontract.ScalarValue(queryresults.ConvertValue(value))
	}
	return typed
}

// projectDB is the db of meta.json for a project source.
func projectDB(ref dbcopy.DatatugRef, driver string) queryresults.DB {
	text := ref.String()
	return queryresults.DB{Ref: &text, Project: &ref.Project, Env: &ref.Env, Source: &ref.DB, Driver: driver}
}

// fullDatatugRef is the datatug:// reference arg written in full: the
// registered project id and the environment, resolved from the registry and
// the project. It is false when arg is not a reference of a registered project.
func fullDatatugRef(ctx context.Context, arg string) (dbcopy.DatatugRef, bool) {
	if !dbcopy.IsDatatugRef(arg) {
		return dbcopy.DatatugRef{}, false
	}
	reference, err := dbcopy.ParseDatatugRef(arg)
	if err != nil {
		return dbcopy.DatatugRef{}, false
	}
	project, dir, err := projectNameOf(reference.Project)
	if err != nil {
		return dbcopy.DatatugRef{}, false
	}
	base, err := openProjectForRead(project, dir)
	if err != nil {
		return dbcopy.DatatugRef{}, false
	}
	id := base.projectID
	if project == "" {
		id = registeredProjectID(base.ProjectDir)
	}
	env := reference.Env
	if env == "" {
		envs, err := api.EnvironmentsListingSource(ctx, base.store.GetProjectStore(base.projectID), reference.DB)
		if err != nil || len(envs) != 1 {
			return dbcopy.DatatugRef{}, false
		}
		env = envs[0]
	}
	if id == "" {
		return dbcopy.DatatugRef{}, false
	}
	return dbcopy.DatatugRef{Project: id, DB: reference.DB, Env: env}, true
}

// registeredProjectID is the id under which the project at dir is registered,
// or "" when it is not.
func registeredProjectID(dir string) string {
	settings, err := dtconfig.GetSettings()
	if err != nil {
		return ""
	}
	for _, project := range settings.Projects {
		if project != nil && project.ID != "" && project.Path != "" && sameProjectDirectory(project.Path, dir) {
			return project.ID
		}
	}
	return ""
}

// savedQueryDB is the db of meta.json for a saved query: the project's
// registered id, the environment and the source the query ran on (for a
// federated query, its root's database). An HTTP query, and a project that is
// not registered, have no reference.
func savedQueryDB(ctx context.Context, store datatug.ProjectStore, projectDir, projectArg, envFlag string, queryDef *datatug.QueryDef) queryresults.DB {
	if queryDef.Type == datatug.QueryTypeHTTP {
		return queryresults.DB{Driver: "http"}
	}
	envID, err := resolveQueryEnvironment(ctx, store, envFlag)
	if err != nil {
		return queryresults.DB{}
	}
	database, err := savedQueryDatabase(ctx, store, envID, queryDef)
	if err != nil {
		return queryresults.DB{}
	}
	source, err := resolveQuerySourceURL(ctx, store, projectDir, envID, database)
	if err != nil {
		return queryresults.DB{}
	}
	driver, _, _ := strings.Cut(source, ":")
	id := registeredProjectID(projectDir)
	if id == "" {
		return queryresults.DB{Driver: driver}
	}
	return projectDB(dbcopy.DatatugRef{Project: id, DB: database, Env: envID}, driver)
}

// savedQueryDatabase is the database a saved query reads: the root's named
// database of a federated DTQL query, else the one it is resolved to.
func savedQueryDatabase(ctx context.Context, store datatug.ProjectStore, envID string, queryDef *datatug.QueryDef) (string, error) {
	if queryDef.Type == datatug.QueryTypeDTQL {
		if parsed, err := dtql.Deserialize([]byte(queryDef.Text)); err == nil {
			if root, ok := parsed.From().Base().(dal.CollectionRef); ok && root.Database() != "" {
				return root.Database(), nil
			}
		}
	}
	return resolveQueryDatabase(ctx, store, envID, queryDef)
}
