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
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const (
	queryDBFlag          = "db"
	queryFileFlag        = "file"
	queryFromFlag        = "from"
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

	// Standard exit codes from the CLI umbrella feature.
	exitCodeUsage        = 2
	exitCodeDatabase     = 4
	exitCodeAccessDenied = 5
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
	return strings.Join(labeled, ", ")
}

// queryCommand is the `query` resource group; the bare group shows help.
func queryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Run queries through your access policies",
	}
	cmd.AddCommand(queryRunCommand())
	return cmd
}

func queryRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run an ad-hoc DTQL query as a policy-secured application would",
		Long: `Runs one DTQL query against a database URL through the DALgo access policies
found in ~/` + accesspolicies.DefaultDir + `/ (override with --policies-dir or $` + accesspolicies.DirEnv + `),
exactly as a secured application would for the principal named by --as/--role/--group.
Result rows go to stdout in the chosen format; the limitations the policies
applied (row conditions, field allow-lists, the deciding rule) go to stderr.

Examples:
  datatug query run --db ingitdb://./crm --from customers --as alice
  datatug query run --db sqlite:///tmp/crm.db -f report.dtql.yaml --as alice --role support --format csv
  datatug query run --db ingitdb://./crm --from products --var minPrice=10 --format json
  datatug query run --project ./demo-project-1 --query customers/customer-invoices --as alice --var CustomerId=5
  datatug query run --project demo-project-1 --query reference/country-facts --env local --var name=Brazil`,
		SilenceUsage: true,
		RunE:         queryRunCommandAction,
	}
	flags := cmd.Flags()
	flags.String(queryDBFlag, "", "Database URL ("+dbSchemesHelp()+")")
	flags.StringP(queryFileFlag, "f", "", "DTQL query document (YAML or JSON); '-' reads stdin")
	flags.String(queryFromFlag, "", "Select every row and field of this root collection (alternative to -f)")
	flags.String(queryProjectFlag, "", "Project directory or registered project ID (alternative to --db; runs a saved --query by ID)")
	flags.String(queryQueryFlag, "", "Saved query ID to run, e.g. customers/customer-invoices (requires --project)")
	flags.String(queryEnvFlag, "", "Environment ID the saved query runs against (default: the project's only environment)")
	flags.String(queryFormatFlag, "grid", "Output format: "+strings.Join(queryFormats, ", "))
	flags.String(queryAsFlag, "", "Principal user ID; also sets $currentUser")
	flags.StringArray(queryRoleFlag, nil, "Principal role (repeatable)")
	flags.StringArray(queryGroupFlag, nil, "Principal group (repeatable)")
	flags.StringArray(queryVarFlag, nil, "Policy or query variable name=value (repeatable); values are YAML scalars or [lists]")
	flags.StringArray(queryPolicyFlag, nil, "Additional policy document, applied after the directory (repeatable)")
	flags.String(queryPoliciesDirFlag, "", "Policies directory (default $"+accesspolicies.DirEnv+" or ~/"+accesspolicies.DefaultDir+")")
	flags.Bool(queryNoPoliciesFlag, false, "Run without any access policy")
	flags.BoolP(queryQuietFlag, "q", false, "Do not report applied policy limitations on stderr")
	return cmd
}

type queryOptions struct {
	db          string
	file        string
	from        string
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
}

func readQueryOptions(cmd *cobra.Command) (queryOptions, error) {
	flags := cmd.Flags()
	var o queryOptions
	o.db, _ = flags.GetString(queryDBFlag)
	o.file, _ = flags.GetString(queryFileFlag)
	o.from, _ = flags.GetString(queryFromFlag)
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

	savedQueryMode := o.project != "" || o.query != ""
	adHocMode := o.db != "" || o.file != "" || o.from != ""
	switch {
	case savedQueryMode && adHocMode:
		return o, Exit("--project/--query and --db/-f/--from are mutually exclusive", exitCodeUsage)
	case savedQueryMode && (o.project == "" || o.query == ""):
		return o, Exit("--project and --query must be given together", exitCodeUsage)
	case savedQueryMode && (len(o.policies) > 0 || o.policiesDir != "" || o.noPolicies):
		return o, Exit("--policy/--policies-dir/--no-policies are not supported with --project/--query; policies come from the project's own policies/ directory, exactly like `serve`", exitCodeUsage)
	case !savedQueryMode && strings.TrimSpace(o.db) == "":
		return o, Exit("one of --project+--query or --db is required", exitCodeUsage)
	case !savedQueryMode && o.file == "" && o.from == "":
		return o, Exit("one of -f/--file or --from is required", exitCodeUsage)
	case !savedQueryMode && o.file != "" && o.from != "":
		return o, Exit("-f/--file and --from are mutually exclusive", exitCodeUsage)
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
// Production code always runs with this default, which calls the real,
// https-only-enforcing BackendRef.Open.
var openBackend = func(ctx context.Context, backend dbcopy.BackendRef) (dal.DB, error) {
	return backend.Open(ctx)
}

func queryRunCommandAction(cmd *cobra.Command, _ []string) error {
	o, err := readQueryOptions(cmd)
	if err != nil {
		return err
	}
	if o.project != "" && o.query != "" {
		return runSavedQueryCommand(cmd, o)
	}
	query, err := buildQuery(o, cmd.InOrStdin())
	if err != nil {
		return err
	}
	backend, err := dbcopy.Parse(o.db)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
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
	db, err := openBackend(ctx, backend)
	if err != nil {
		return Exit(fmt.Sprintf("open %s: %v", o.db, err), exitCodeDatabase)
	}
	stderr := cmd.ErrOrStderr()
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
		return queryFailure(err)
	}
	rows, err := collectRows(result.Reader)
	if err != nil {
		return queryFailure(err)
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

func buildQuery(o queryOptions, stdin io.Reader) (dal.Query, error) {
	if o.from != "" {
		return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(o.from, ""))).SelectColumns(), nil
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
		return nil, Exit(fmt.Sprintf("read query %s: %v", o.file, err), exitCodeUsage)
	}
	if strings.TrimSpace(string(content)) == "" {
		return nil, Exit(fmt.Sprintf("query %s is empty", o.file), exitCodeUsage)
	}
	query, err := dtql.Deserialize(content)
	if err != nil {
		return nil, Exit(fmt.Sprintf("query %s: %v", o.file, err), exitCodeUsage)
	}
	return query, nil
}
