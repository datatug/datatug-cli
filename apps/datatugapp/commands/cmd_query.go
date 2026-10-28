package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/tugql"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/spf13/cobra"
)

const (
	queryDBFlag          = "db"
	queryFileFlag        = "file"
	queryFromFlag        = "from"
	queryLimitFlag       = "limit"
	queryOffsetFlag      = "offset"
	queryFormatFlag      = "format"
	queryAsFlag          = "as"
	queryRoleFlag        = "role"
	queryGroupFlag       = "group"
	queryVarFlag         = "var"
	queryPolicyFlag      = "policy"
	queryPoliciesDirFlag = "policies-dir"
	queryNoPoliciesFlag  = "no-policies"
	queryQuietFlag       = "quiet"
	queryProjectFlag     = "project"
	queryQueryFlag       = "query"
	queryEnvFlag         = "env"
	queryResultIDFlag    = "result-id"
	queryResultRowsFlag  = "result-rows"

	// Standard exit codes from the CLI umbrella feature.
	exitCodeUsage        = 2
	exitCodeDatabase     = 4
	exitCodeAccessDenied = 5

	// couchDBSourceDefaultLimit is the bound an exploratory `--from` read of a
	// CouchDB source gets when the person gave no --limit. The CouchDB adapter
	// refuses an unbounded read (no silent full scan), so exactly this one
	// exploration path needs a visible default; a -f or saved query keeps its own
	// limit, and an explicit --limit (even 0) is never replaced.
	couchDBSourceDefaultLimit = 100

	// postgRESTSourceDefaultLimit is the same visible bound for an exploratory
	// `--from` read of a PostgREST source: the adapter refuses an unbounded read,
	// and DALgo's local aggregation fallback also asks for unbounded source rows
	// (which the adapter refuses rather than returning a partial aggregate).
	postgRESTSourceDefaultLimit = 100

	// libSQLSourceDefaultLimit is the same visible bound for an exploratory
	// `--from` read of a libSQL source. The adapter has its own hard response,
	// row, request and deadline bounds, but a read still needs a positive
	// semantic limit: an exploration never silently reads a whole table, and an
	// explicit --limit (even 0) is never replaced. This is an output bound, not
	// the adapter's transport budget: a result the adapter truncates at a hard
	// bound fails instead of returning a partial page.
	libSQLSourceDefaultLimit = 100
)

// dbSchemesHelp lists every URL scheme dbcopy.Parse accepts, generated from
// dbcopy.SupportedSchemes() so the --db flag's help text cannot silently
// drift from what it actually dispatches to (as it did when http/https were
// wired in without this text being updated).
func dbSchemesHelp() string {
	schemes := dbcopy.SupportedSchemes()
	labeled := make([]string, len(schemes))
	for i, scheme := range schemes {
		labeled[i] = scheme + "://"
	}
	return strings.Join(labeled, ", ") + ", or " + dbcopy.EnvSourceForm + " naming an environment variable that holds one"
}

// queryCommand is the `query` resource group; the bare group shows help.
func queryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Run queries through your access policies",
	}
	cmd.AddCommand(queryRunCommand(), queryBigQueryCommand(), queryRedisCommand(), queryResultsCommand())
	return cmd
}

func queryRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run an ad-hoc TugQL query as a policy-secured application would",
		Long: `Runs one TugQL query against a database URL through the DALgo access policies
found in ~/` + accesspolicies.DefaultDir + `/ (override with --policies-dir or $` + accesspolicies.DirEnv + `),
exactly as a secured application would for the principal named by --as/--role/--group.
Result rows go to stdout in the chosen format; the limitations the policies
applied (row conditions, field allow-lists, the deciding rule) go to stderr.

For saved federated TugQL, --format jsonl and csv stream one flat equality
join without top-level ordering or aggregation. The joined dimension must
declare a stable scan order and limit of at most 10000 rows; it is indexed
in memory while fact rows and HTTP lookups stream in bounded batches.
CSV additionally requires explicit result columns. Other federated query
shapes can use json, yaml or grid, which collect the result before output.

Examples:
  datatug query run --db ingitdb://./crm --from customers --as alice
  datatug query run --db sqlite:///tmp/crm.db -f report.tugql.yaml --as alice --role support --format csv
  datatug query run --db ingitdb://./crm --from products --var minPrice=10 --format json
  datatug query run --db env:SHOP_PG_URL -f report.tugql.yaml --as alice   # SHOP_PG_URL holds the whole connection URL
  datatug query run --db env:DATATUG_POSTGREST_ITEMS --from items --limit 10 --no-policies   # a PostgREST relation; credentials stay in DATATUG_POSTGREST_*
  datatug query run --db env:DATATUG_LIBSQL_DB --from items --limit 10 --no-policies   # a libSQL table; the token stays in DATATUG_LIBSQL_BEARER
  datatug query run --db datatug://shop/crm --from customers --no-policies   # crm is a source of the registered project shop
  datatug query run --project ./demo-project-1 --query customers/customer-invoices --as alice --var CustomerId=5
  datatug query run --project demo-project-1 --query reference/country-facts --env local --var name=Brazil`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         queryRunCommandAction,
	}
	flags := cmd.Flags()
	flags.String(queryDBFlag, "", "Database URL ("+dbSchemesHelp()+"), or datatug://<project>/<db>[?env=<ENV>] for a database that is a source of a registered project (datatug:///<db> for the current project)")
	flags.StringP(queryFileFlag, "f", "", "TugQL query document (YAML or JSON); '-' reads stdin")
	flags.String(queryFromFlag, "", "Select every row and field of this root collection (alternative to -f)")
	flags.String(querySQLFlag, "", "SQL query to run against --db (alternative to -f): on PostgreSQL, MySQL, Oracle and SQL Server one SELECT or WITH query, read-only; '-' reads stdin")
	flags.String(queryProjectFlag, "", "Project directory or registered project ID (alternative to --db; runs a saved --query by ID)")
	flags.String(queryQueryFlag, "", "Saved query ID to run, e.g. customers/customer-invoices (requires --project)")
	flags.String(queryEnvFlag, "", "Environment ID the saved query runs against (default: the project's only environment)")
	flags.Int(queryLimitFlag, 0, "Read at most this many rows (only with --from; a -f query states its own limit); a CouchDB, PostgREST or libSQL --from read defaults to "+fmt.Sprint(couchDBSourceDefaultLimit)+" when no --limit is given")
	flags.Int(queryOffsetFlag, 0, "Skip this many rows first (only with --from); rows are ordered by primary key, or by rowid in SQLite, so pages are stable")
	flags.String(queryFormatFlag, "grid", "Output format: "+strings.Join(queryFormats, ", "))
	flags.String(queryAsFlag, "", "Principal user ID; also sets $currentUser")
	flags.StringArray(queryRoleFlag, nil, "Principal role (repeatable)")
	flags.StringArray(queryGroupFlag, nil, "Principal group (repeatable)")
	flags.StringArray(queryVarFlag, nil, "Policy or query variable name=value (repeatable); values are YAML scalars or [lists]")
	flags.StringArray(queryPolicyFlag, nil, "Additional policy document, applied after the directory (repeatable)")
	flags.String(queryPoliciesDirFlag, "", "Policies directory (default $"+accesspolicies.DirEnv+" or ~/"+accesspolicies.DefaultDir+")")
	flags.Bool(queryNoPoliciesFlag, false, "Run without any access policy")
	flags.BoolP(queryQuietFlag, "q", false, "Do not report applied policy limitations on stderr")
	flags.String(queryResultIDFlag, "", "Write the result to private files under ~/"+queryresults.DefaultDir+"/<id>/ instead of printing rows; stdout gets one path-free JSON line")
	flags.Int(queryResultRowsFlag, queryresults.DefaultRows, fmt.Sprintf("Rows kept with --result-id (%d to %d)", queryresults.MinRows, queryresults.MaxRows))
	return cmd
}

type queryOptions struct {
	db     string
	file   string
	from   string
	sql    string
	limit  int
	offset int
	// limitSet is whether --limit was typed, so a CouchDB --from read can tell
	// "no bound given" (use the visible exploration default) from an explicit
	// --limit 0 (pass it through; the adapter then refuses it).
	limitSet    bool
	format      string
	as          string
	roles       []string
	groups      []string
	vars        []string
	policies    []string
	policiesDir string
	noPolicies  bool
	quiet       bool
	project     string
	query       string
	env         string
	// resultID and resultRows are --result-id and --result-rows; sink is the
	// result being written when resultID is set.
	resultID   string
	resultRows int
	sink       *resultSink
}

func readQueryOptions(cmd *cobra.Command) (queryOptions, error) {
	flags := cmd.Flags()
	var o queryOptions
	o.db, _ = flags.GetString(queryDBFlag)
	o.file, _ = flags.GetString(queryFileFlag)
	o.from, _ = flags.GetString(queryFromFlag)
	o.sql, _ = flags.GetString(querySQLFlag)
	o.limit, _ = flags.GetInt(queryLimitFlag)
	o.limitSet = flags.Changed(queryLimitFlag)
	o.offset, _ = flags.GetInt(queryOffsetFlag)
	o.format, _ = flags.GetString(queryFormatFlag)
	o.as, _ = flags.GetString(queryAsFlag)
	o.roles, _ = flags.GetStringArray(queryRoleFlag)
	o.groups, _ = flags.GetStringArray(queryGroupFlag)
	o.vars, _ = flags.GetStringArray(queryVarFlag)
	o.policies, _ = flags.GetStringArray(queryPolicyFlag)
	o.policiesDir, _ = flags.GetString(queryPoliciesDirFlag)
	o.noPolicies, _ = flags.GetBool(queryNoPoliciesFlag)
	o.quiet, _ = flags.GetBool(queryQuietFlag)
	o.project, _ = flags.GetString(queryProjectFlag)
	o.query, _ = flags.GetString(queryQueryFlag)
	o.env, _ = flags.GetString(queryEnvFlag)
	o.resultID, _ = flags.GetString(queryResultIDFlag)
	o.resultRows, _ = flags.GetInt(queryResultRowsFlag)
	if err := checkResultFlags(o, flags.Changed(queryFormatFlag), flags.Changed(queryResultRowsFlag)); err != nil {
		return o, err
	}

	savedQueryMode := o.project != "" || o.query != ""
	adHocMode := o.db != "" || o.file != "" || o.from != "" || o.sql != ""
	queryInputs := 0
	for _, given := range []bool{o.file != "", o.from != "", o.sql != ""} {
		if given {
			queryInputs++
		}
	}
	switch {
	case savedQueryMode && adHocMode:
		return o, Exit("--project/--query and --db/-f/--from/--sql are mutually exclusive", exitCodeUsage)
	case savedQueryMode && (o.project == "" || o.query == ""):
		return o, Exit("--project and --query must be given together", exitCodeUsage)
	case savedQueryMode && (len(o.policies) > 0 || o.policiesDir != "" || o.noPolicies):
		return o, Exit("--policy/--policies-dir/--no-policies are not supported with --project/--query; policies come from the project's own policies/ directory, exactly like `serve`", exitCodeUsage)
	case !savedQueryMode && strings.TrimSpace(o.db) == "":
		return o, Exit("one of --project+--query or --db is required", exitCodeUsage)
	case !savedQueryMode && queryInputs == 0:
		return o, Exit("one of -f/--file, --from or --sql is required", exitCodeUsage)
	case !savedQueryMode && queryInputs > 1:
		return o, Exit("-f/--file, --from and --sql are mutually exclusive", exitCodeUsage)
	case o.sql != "" && o.resultID != "":
		return o, Exit("--result-id is not supported with --sql yet", exitCodeUsage)
	}
	switch {
	case o.limit < 0 || o.offset < 0:
		return o, Exit("--limit and --offset must be zero or more", exitCodeUsage)
	case (o.limit > 0 || o.offset > 0) && o.from == "":
		return o, Exit("--limit and --offset work with --from; a -f query or a saved query states its own", exitCodeUsage)
	}
	valid := false
	for _, format := range queryFormats {
		valid = valid || format == o.format
	}
	if !valid {
		return o, Exit(fmt.Sprintf("unsupported --format %q: want one of %s", o.format, strings.Join(queryFormats, ", ")), exitCodeUsage)
	}
	return o, nil
}

// openBackend opens backend into a dal.DB for the direct (--db) `query run`
// path; a package var so this package's own tests can substitute
// dbcopy.BackendRef.OpenForTest (dal-go/dalgo2http v0.2.0's TEST-ONLY
// Collection.InsecureAllowLoopback) for an HTTP(S) source backed by a
// loopback httptest.Server, without any project descriptor file ever being
// able to request that itself — see cmd_query_http_provenance_test.go.
// Production code always runs with this default. SQLite uses the validated
// structured-query dialect so grouped TugQL is rendered as native SQL (and
// literals remain bound parameters), and PostgreSQL, MySQL, Oracle and SQL Server open as a
// protected read: a read-only session; other sources keep their
// existing open behavior. Both paths retain the HTTPS-only HTTP-source
// enforcement.
var openBackend = func(ctx context.Context, backend dbcopy.BackendRef) (dal.DB, error) {
	if backend.Scheme == "sqlite" || dbcopy.IsServerScheme(backend.Scheme) || dbcopy.IsCouchDBScheme(backend.Scheme) || dbcopy.IsPostgRESTScheme(backend.Scheme) || dbcopy.IsLibSQLScheme(backend.Scheme) {
		return backend.OpenProtected(ctx)
	}
	return backend.Open(ctx)
}

// queryRunCommandAction runs `query run`. With --result-id every failure
// after the flags were parsed also prints the path-free failure line.
func queryRunCommandAction(cmd *cobra.Command, _ []string) error {
	// A read is cancellable: the first SIGINT/SIGTERM cancels its context so the
	// command returns through its own error path (a safe "read was cancelled"
	// classification for a source that has one) instead of being killed in the
	// middle of a read. A second signal still ends the process, and stop gives
	// the signals back when the command returns.
	original := cmd.Context()
	// The helper always needs a parent context, but the command keeps the
	// caller's own one (even nil) and gets it back when this invocation ends: a
	// reused Cobra command must not inherit a cancelled per-invocation context.
	parent := original
	if parent == nil {
		parent = context.Background()
	}
	ctx, _, stop := interruptContext(parent)
	cmd.SetContext(ctx)
	defer func() {
		cmd.SetContext(original)
		stop()
	}()

	o, err := readQueryOptions(cmd)
	if err == nil {
		err = runQueryCommand(cmd, o)
	}
	if err != nil && o.resultID != "" {
		_, _ = cmd.OutOrStdout().Write(queryresults.FailureLine(resultFailureReason(err)))
	}
	return err
}

func runQueryCommand(cmd *cobra.Command, o queryOptions) error {
	if o.resultID != "" {
		sink, err := openResultSink(o)
		if err != nil {
			return err
		}
		defer sink.discardUnlessWritten()
		o.sink = sink
	}
	if o.project != "" && o.query != "" {
		return runSavedQueryCommand(cmd, o)
	}
	if o.sql != "" {
		return runSQLQueryCommand(cmd, o)
	}
	query, queryText, err := buildQuery(o, cmd.InOrStdin())
	if err != nil {
		return err
	}
	backend, _, err := parseSourceArg(cmd.Context(), o.db)
	if err != nil {
		return keepExit(err, func(err error) error { return Exit(err.Error(), exitCodeUsage) })
	}
	// The flag the source was typed on is what a failure of a PostgreSQL source says its connection string is read
	// from (an env:NAME source says the variable).
	backend = backend.WithFlag("--db")
	loaded, err := accesspolicies.Load(accesspolicies.LoadOptions{Dir: o.policiesDir, Files: o.policies, None: o.noPolicies})
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	variables, err := accesspolicies.ParseVariables(o.vars)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	var principal *access.Principal
	if o.as != "" || len(o.roles) > 0 || len(o.groups) > 0 {
		principal = &access.Principal{Roles: o.roles, Groups: o.groups}
		if o.as != "" {
			principal.ID = o.as
		}
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// An exploratory `--from` read of a CouchDB or PostgREST source needs an
	// explicit bound because the adapter refuses an unbounded read instead of
	// silently scanning the whole service. Only a missing --limit gets the visible
	// default: a -f or saved query keeps its own limit, and an explicit --limit 0
	// reaches the adapter and fails there rather than being replaced here.
	defaultedSourceLimit := ""
	if o.from != "" && !o.limitSet {
		switch {
		case dbcopy.IsCouchDBScheme(backend.Scheme):
			o.limit = couchDBSourceDefaultLimit
			defaultedSourceLimit = "CouchDB"
		case dbcopy.IsPostgRESTScheme(backend.Scheme):
			o.limit = postgRESTSourceDefaultLimit
			defaultedSourceLimit = "PostgREST"
		case dbcopy.IsLibSQLScheme(backend.Scheme):
			o.limit = libSQLSourceDefaultLimit
			defaultedSourceLimit = "libSQL"
		}
	}
	// A read of a PostgreSQL, MySQL, Oracle or SQL Server source through policies is not available yet: that is
	// answered before the source is opened.
	if err = dbcopy.CheckServerRead(backend, len(loaded)); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	// This ad-hoc `datatug query run --db http://...` path predates
	// pkg/httpsource.Open's fail-closed ModeLive default (see Open's own doc
	// comment) and is documented, tested, offline-friendly terminal UX
	// outside api-contract.md's scope (that appendix only binds `datatug
	// serve`'s exec/run_query, which always names an explicit mode from its
	// ExecutionRequest) — provenanceLine's own doc comment already explains
	// this command shows the caller whether an answer was live or a
	// recorded fixtures/http/ snapshot, so a silent live-then-snapshot
	// fallback is a feature here, not the contract's forbidden "silent
	// fallback ... claim". Opting in explicitly (rather than relying on a
	// package default) keeps that behavior unchanged and visible at this
	// exact call site. A no-op for every non-HTTP backend.
	ctx = httpsource.ContextWithDispatch(ctx, dalgo2http.ModeLiveThenSnapshot, false, 0)
	db, err := openBackend(ctx, backend)
	if err != nil {
		// OpenFailure names the source (its display form: a password given in the
		// URL never reaches the message, and env:NAME names the variable without
		// its value) and says what failed in a fixed sentence: a driver's own text
		// can quote the URL it was given. It already says which source failed, so
		// nothing is put in front of it. A PostgreSQL source is not named by its
		// display form (that holds the host and the port): its text is the adapter's
		// sentence and where the connection string is read from (the --db flag, or
		// the variable of an env:NAME source).
		return Exit(backend.OpenFailure(err).Error(), exitCodeDatabase)
	}
	// This command owns the handle it just opened: release it on success, a
	// query failure after open, cancellation, or a write failure. A cached
	// PostgreSQL handle's Close is a no-op; a CouchDB/PostgREST transport is
	// released through DALgo's optional-capability lookup.
	defer dbcopy.CloseSource(db)
	if o.from != "" && (o.limit > 0 || o.offset > 0) {
		query = fromQuery(o, pagingOrder(ctx, db, backend.Scheme, o.from))
	}
	if o.sink != nil {
		o.sink.describeAdHoc(ctx, o, backend, db, queryText, variables)
	}
	stderr := cmd.ErrOrStderr()
	if defaultedSourceLimit != "" {
		_, _ = fmt.Fprintf(stderr, "limit: reading at most %d rows (%s exploration default; pass --limit N to change it)\n", o.limit, defaultedSourceLimit)
	}
	if len(loaded) == 0 {
		_, _ = fmt.Fprintln(stderr, "access: running without access policies")
	}
	// rec observes the dalgo2http.Provenance (live vs snapshot) of an
	// HTTP-source query, exactly as pkg/httpsource.ExecuteQuery does
	// internally; every other backend never calls the observer, so rec.Last
	// simply reports "not observed" for them.
	rec := dalgo2http.NewRecorder()
	result, err := accesspolicies.Run(rec.WithContext(ctx), db, query, accesspolicies.Options{Principal: principal, Variables: variables, Policies: loaded, Unrestricted: o.noPolicies})
	if !o.quiet {
		for _, line := range result.Lines {
			_, _ = fmt.Fprintln(stderr, line.String())
		}
	}
	prov, provOK := rec.Last()
	if provOK {
		_, _ = fmt.Fprintln(stderr, provenanceLine(prov))
	}
	if err != nil {
		return queryFailureFor(backend, err)
	}
	if o.sink != nil {
		if provOK {
			o.sink.meta.Provenance = resultProvenance(prov)
		}
		if err = o.sink.collectAndWrite(cmd.OutOrStdout(), result.Reader, result.Query); err != nil {
			return queryFailureFor(backend, err)
		}
		return nil
	}
	rows, err := collectRows(result.Reader)
	if err != nil {
		return queryFailureFor(backend, err)
	}
	columns, err := rowColumns(result.Query, rows)
	if err != nil {
		return Exit(err.Error(), 1)
	}
	var provenance *queryProvenance
	if provOK {
		provenance = newQueryProvenance(prov)
	}
	if err := writeQueryRows(cmd.OutOrStdout(), o.format, columns, rows, provenance); err != nil {
		return Exit(err.Error(), 1)
	}
	return nil
}

// provenanceLine renders the stderr note printed after an HTTP-source query
// so a demo or interactive run always shows whether the rows just printed
// came from the live endpoint or a recorded fixtures/http/ snapshot (see
// pkg/httpsource's Mode: ModeLiveThenSnapshot fallback) — built only from
// what dalgo2http.Provenance actually reports (Collection, Source,
// FetchedAt), never a fabricated endpoint URL it doesn't carry.
func provenanceLine(prov dalgo2http.Provenance) string {
	fetchedAt := prov.FetchedAt.UTC()
	if prov.Source == dalgo2http.SourceSnapshot {
		return fmt.Sprintf("source: snapshot fixtures/http/%s.json (captured %s)", prov.Collection, fetchedAt.Format("2006-01-02"))
	}
	return fmt.Sprintf("source: live %s (%s)", prov.Collection, fetchedAt.Format(time.RFC3339))
}

// queryFailureFor is queryFailure with what only a CouchDB index refusal can
// say: the adapter refuses a filtered read that no Mango index serves rather
// than falling back to a full scan that could hide documents. The refusal stays
// explicit; it is never turned into a partial result. An unfiltered read does
// not reach this: it is served by CouchDB's native primary index.
func queryFailureFor(backend dbcopy.BackendRef, err error) error {
	if dbcopy.IsCouchDBScheme(backend.Scheme) && dbcopy.IsCouchDBIndexRequired(err) {
		return Exit("CouchDB requires a Mango index for a filtered query: the read is refused rather than falling back to a full scan that could hide documents. Create an index outside DataTug that covers the filter and the order, then run the query again.", exitCodeDatabase)
	}
	if dbcopy.IsLibSQLScheme(backend.Scheme) {
		switch {
		case errors.Is(err, access.ErrAccessDenied),
			errors.Is(err, accesspolicies.ErrInvalidQuery),
			errors.Is(err, accesspolicies.ErrNoPolicies):
			// A policy refusal or an unusable policy set is decided outside the
			// adapter and keeps its own exit code and text; it is not a libSQL
			// read failure.
			return queryFailure(err)
		}
		// The adapter seals its own errors, but an outer wrapper is not that
		// boundary: show only a fixed libSQL classification so no SQL, response
		// body, wrapper text or credential can reach the message.
		return Exit("libSQL read failed: "+dbcopy.LibSQLFailureReason(err)+"; check "+dbcopy.LibSQLBearerEnv+" and the token's read grant on the database", exitCodeDatabase)
	}
	return queryFailure(err)
}

func queryFailure(err error) error {
	switch {
	case errors.Is(err, access.ErrAccessDenied):
		return Exit(err.Error(), exitCodeAccessDenied)
	case errors.Is(err, accesspolicies.ErrInvalidQuery), errors.Is(err, accesspolicies.ErrNoPolicies):
		return Exit(err.Error(), exitCodeUsage)
	default:
		return Exit(err.Error(), exitCodeDatabase)
	}
}

// buildQuery is the query to run and its TugQL text: the document as given,
// or for --from the equivalent one-collection document.
func buildQuery(o queryOptions, stdin io.Reader) (dal.Query, string, error) {
	if o.from != "" {
		query := fromQuery(o, nil)
		text, _ := tugql.Serialize(query.(dal.StructuredQuery))
		return query, string(text), nil
	}
	var (
		content []byte
		err     error
	)
	if o.file == "-" {
		content, err = io.ReadAll(stdin)
	} else {
		content, err = os.ReadFile(o.file)
	}
	if err != nil {
		return nil, "", Exit(fmt.Sprintf("read query %s: %v", o.file, err), exitCodeUsage)
	}
	if strings.TrimSpace(string(content)) == "" {
		return nil, "", Exit(fmt.Sprintf("query %s is empty", o.file), exitCodeUsage)
	}
	query, err := tugql.Deserialize(content)
	if err != nil {
		return nil, "", Exit(fmt.Sprintf("query %s: %v", o.file, err), exitCodeUsage)
	}
	return query, string(content), nil
}

// fromQuery selects every row of the --from collection, paged by --limit and
// --offset and ordered by order (nil leaves the source's own order).
func fromQuery(o queryOptions, order []dal.OrderExpression) dal.Query {
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(o.from, "")))
	if len(order) > 0 {
		builder = builder.OrderBy(order...)
	}
	if o.offset > 0 {
		builder = builder.Offset(o.offset)
	}
	if o.limit > 0 {
		builder = builder.Limit(o.limit)
	}
	return builder.SelectColumns()
}

// pagingOrder is the order a paged --from read uses so that pages do not
// move: the primary key of the table when it has one, else rowid for a SQLite
// table; any other source, and a SQLite view, keeps its own order.
func pagingOrder(ctx context.Context, db dal.DB, scheme, collection string) []dal.OrderExpression {
	ref := dal.NewRootCollectionRef(collection, "")
	def, err := dbschema.DescribeCollection(ctx, db, &ref)
	if err != nil {
		return nil
	}
	var order []dal.OrderExpression
	for _, name := range def.PrimaryKey {
		order = append(order, dal.AscendingField(string(name)))
	}
	if len(order) == 0 && scheme == "sqlite" {
		order = append(order, dal.AscendingField("rowid"))
	}
	return order
}
