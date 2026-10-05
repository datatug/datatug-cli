package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/datatug/datatug-cli/pkg/bigqueryread"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

type bigQueryControl struct {
	bigquery.Page
	Status *bigquery.JobStatus    `json:"status,omitempty"`
	Cancel *bigquery.CancelResult `json:"cancel,omitempty"`
}

type bigQueryDependencies struct {
	ledger          func(string) (bigquery.Ledger, error)
	provider        func(string, bool, string) (bigquery.Provider, error)
	transport       http.RoundTripper
	clock           bigquery.Clock
	connect         func(context.Context, []string) (*oauth2.Token, error)
	consentIdentity func(context.Context, string, bool, *oauth2.Token) (bigquery.Identity, error)
}

var bigQueryDeps = bigQueryDependencies{
	ledger: func(dir string) (bigquery.Ledger, error) { return bigquery.NewFileLedger(dir) },
	provider: func(dir string, cancel bool, auth string) (bigquery.Provider, error) {
		if auth == "adc" {
			return bigqueryread.NewADCProvider(dir, cancel)
		}
		return bigqueryread.NewGoogleProvider(dir, cancel)
	},
	transport: http.DefaultTransport,
	connect:   gauth.StartInteractiveLogin,
	consentIdentity: func(ctx context.Context, dir string, cancel bool, token *oauth2.Token) (bigquery.Identity, error) {
		p, err := bigqueryread.NewGoogleProvider(dir, cancel)
		if err != nil {
			return bigquery.Identity{}, err
		}
		identity, _, err := p.AuthorizeToken(ctx, token, http.DefaultTransport)
		return identity, err
	},
}

func queryBigQueryCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "bigquery", Short: "Preview and explicitly run a capped native BigQuery read"}
	for _, operation := range []string{"connect", "preview", "run", "page", "status", "cancel"} {
		op := operation
		child := &cobra.Command{Use: op, Short: map[string]string{"connect": "Explicitly connect Google with separate BigQuery grants", "preview": "Show policy-effective query, verified identity, estimate and approval digest", "run": "Approve an exact preview digest and submit once", "page": "Resume the same job and read its next page", "status": "Observe authoritative status for an existing job", "cancel": "Request cancellation with separately enabled granted permission"}[op], Args: cobra.NoArgs, SilenceUsage: true, RunE: func(c *cobra.Command, _ []string) error { return runBigQueryCommand(c, op) }}
		f := child.Flags()
		f.StringP("file", "f", "", "Bounded JSON source profile and scalar DTQL query; '-' reads stdin")
		f.String("auth", "", "Explicit credential source: adc (Google-user ADC) or google (existing stored Google OAuth grant)")
		f.String("source-profile", "", "Separate reviewed source-profile JSON; --file then contains scalar DTQL JSON")
		f.String("ledger", "", "Absolute private directory shared by every operation in this session")
		f.String("format", "json", "Result envelope format (json only)")
		f.String("preview-out", "", "Private preview-*.json output in --ledger directory")
		f.String("receipt-out", "", "Private receipt-*.json continuation output in --ledger directory")
		f.String("cursor", "", "Opaque cursor override for same-job continuation")
		f.String("execution-project", "", "Google execution project (distinct from saved DataTug --project)")
		f.String("maximum-bytes-billed", "", "Exact positive maximum bytes billed; required for preview")
		f.String("session-budget-bytes", "", "Exact session budget (defaults to the explicit maximum bytes billed)")
		f.Int("page-size", 100, "Rows per same-job page (1..1000)")
		f.String("preview", "", "JSON preview printed by preview; required for run")
		f.String("receipt", "", "JSON page/partial receipt printed by run/page; required for page/status/cancel")
		f.String("approve-digest", "", "Exact displayed approval digest; required for run")
		f.Bool("enable-cancellation", false, "Use already granted broader permission to explicitly cancel this job")
		f.Bool("reconnect", false, "Explicitly rebind an existing job to newly verified same-subject grants")
		f.String(queryAsFlag, "", "DataTug policy principal; independent of verified Google execution identity")
		f.StringArray(queryRoleFlag, nil, "DataTug policy role (repeatable)")
		f.StringArray(queryGroupFlag, nil, "DataTug policy group (repeatable)")
		f.StringArray(queryVarFlag, nil, "Policy/query variable name=value (repeatable)")
		f.StringArray(queryPolicyFlag, nil, "Additional access policy file (repeatable)")
		f.String(queryPoliciesDirFlag, "", "Access policy directory")
		f.Bool(queryNoPoliciesFlag, false, "Explicitly run without DataTug access policies")
		cmd.AddCommand(child)
	}
	return cmd
}
func decodeBigQueryFile(name string, stdin io.Reader, target any) error {
	return decodeBigQueryFileLimit(name, stdin, target, bigqueryread.MaxInputBytes)
}
func decodeBigQueryFileLimit(name string, stdin io.Reader, target any, limit int) error {
	if name == "" {
		return bigqueryread.ErrInput
	}
	if name == "-" {
		return bigqueryread.DecodeLimit(stdin, target, limit)
	}
	f, err := os.Open(name)
	if err != nil {
		return bigqueryread.ErrInput
	}
	defer func() { _ = f.Close() }()
	return bigqueryread.DecodeLimit(f, target, limit)
}
func bigQueryPolicyOptions(cmd *cobra.Command) (accesspolicies.Options, error) {
	f := cmd.Flags()
	dir, _ := f.GetString(queryPoliciesDirFlag)
	files, _ := f.GetStringArray(queryPolicyFlag)
	none, _ := f.GetBool(queryNoPoliciesFlag)
	loaded, err := accesspolicies.Load(accesspolicies.LoadOptions{Dir: dir, Files: files, None: none})
	if err != nil {
		return accesspolicies.Options{}, err
	}
	vars, _ := f.GetStringArray(queryVarFlag)
	variables, err := accesspolicies.ParseVariables(vars)
	if err != nil {
		return accesspolicies.Options{}, err
	}
	as, _ := f.GetString(queryAsFlag)
	roles, _ := f.GetStringArray(queryRoleFlag)
	groups, _ := f.GetStringArray(queryGroupFlag)
	var principal *access.Principal
	if as != "" || len(roles) > 0 || len(groups) > 0 {
		principal = &access.Principal{Roles: roles, Groups: groups}
		if as != "" {
			principal.ID = as
		}
	}
	return accesspolicies.Options{Principal: principal, Variables: variables, Policies: loaded, Unrestricted: none}, nil
}
func runBigQueryCommand(cmd *cobra.Command, operation string) error {
	f := cmd.Flags()
	file, _ := f.GetString("file")
	dir, _ := f.GetString("ledger")
	auth, _ := f.GetString("auth")
	if auth != "adc" && auth != "google" {
		return Exit("--auth adc or --auth google is required", exitCodeUsage)
	}
	cancelPermission, _ := f.GetBool("enable-cancellation")
	reconnect, _ := f.GetBool("reconnect")
	if dir == "" {
		return Exit("--ledger is required", exitCodeUsage)
	}
	if operation == "cancel" && !cancelPermission {
		return Exit("cancel requires explicit --enable-cancellation and a verified broader grant", exitCodeUsage)
	}
	if reconnect && operation != "page" && operation != "status" && operation != "cancel" {
		return Exit("--reconnect requires an existing job", exitCodeUsage)
	}
	for _, flag := range []string{"maximum-bytes-billed", "session-budget-bytes", "page-size"} {
		if f.Changed(flag) && operation != "preview" {
			return Exit("--"+flag+" requires preview; existing run bounds cannot be renewed", exitCodeUsage)
		}
	}
	if f.Changed("approve-digest") && operation != "run" {
		return Exit("--approve-digest requires run", exitCodeUsage)
	}
	format, _ := f.GetString("format")
	if format != "json" {
		return Exit("BigQuery supports --format json only", exitCodeUsage)
	}
	previewOut, _ := f.GetString("preview-out")
	receiptOut, _ := f.GetString("receipt-out")
	if previewOut != "" && (operation != "preview" || !validBigQueryExport(previewOut, dir, "preview-")) {
		return Exit("--preview-out requires preview-*.json in the private ledger directory", exitCodeUsage)
	}
	if receiptOut != "" && (!validBigQueryExport(receiptOut, dir, "receipt-") || operation == "preview" || operation == "connect") {
		return Exit("--receipt-out requires receipt-*.json in the private ledger directory", exitCodeUsage)
	}
	if operation == "connect" {
		if auth != "google" {
			return Exit("connect uses --auth google; configure user ADC explicitly outside DataTug for --auth adc", exitCodeUsage)
		}
		if _, err := bigQueryDeps.ledger(dir); err != nil {
			return bigQueryFailure(err)
		}
		scopes := []string{"https://www.googleapis.com/auth/bigquery.readonly", "openid"}
		if cancelPermission {
			scopes[0] = "https://www.googleapis.com/auth/bigquery"
		}
		ctx, stop := context.WithTimeout(cmd.Context(), 5*time.Minute)
		defer stop()
		token, err := bigQueryDeps.connect(ctx, scopes)
		if err != nil {
			return bigQueryFailure(err)
		}
		consent, err := bigQueryDeps.consentIdentity(ctx, dir, cancelPermission, token)
		if err != nil {
			return bigQueryFailure(err)
		}
		provider, err := bigQueryDeps.provider(dir, cancelPermission, auth)
		if err != nil {
			return bigQueryFailure(err)
		}
		identity, _, err := provider.Authorize(ctx, bigQueryDeps.transport)
		if err != nil {
			return bigQueryFailure(err)
		}
		// Storage failure or a concurrent different-account login cannot silently
		// connect the prior stored grant. Both tokens must attest the same binding.
		if identity.Principal != consent.Principal || identity.Read != consent.Read || identity.Cancel != consent.Cancel {
			return bigQueryFailure(bigqueryread.ErrIdentity)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Principal bigquery.Principal `json:"principal"`
			ExpiresAt time.Time          `json:"expiresAt"`
			Read      bool               `json:"read"`
			Cancel    bool               `json:"cancel"`
		}{identity.Principal, identity.ExpiresAt, identity.Read, identity.Cancel})
	}
	var previous bigQueryControl
	if operation == "page" || operation == "status" || operation == "cancel" {
		path, _ := f.GetString("receipt")
		if err := decodeBigQueryFileLimit(path, cmd.InOrStdin(), &previous, 5<<20); err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
		previous.Rows = nil
		if previous.Receipt.RunID == "" {
			return Exit("missing existing run receipt", exitCodeUsage)
		}
	}
	recoverError := func(e error) error {
		if previous.Receipt.RunID != "" {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(previous)
		}
		return bigQueryFailure(e)
	}
	var input bigqueryread.Input
	sourceFile, _ := f.GetString("source-profile")
	if sourceFile != "" {
		if err := decodeBigQueryFile(sourceFile, cmd.InOrStdin(), &input.Profile); err != nil {
			return recoverError(err)
		}
		if err := decodeBigQueryFile(file, cmd.InOrStdin(), &input.Query); err != nil {
			return recoverError(err)
		}
	} else if err := decodeBigQueryFile(file, cmd.InOrStdin(), &input); err != nil {
		return recoverError(err)
	}
	if project, _ := f.GetString("execution-project"); project != "" && previous.Receipt.RunID != "" {
		if previous.Receipt.Job == nil || previous.Receipt.Job.ProjectID != project {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(previous)
			return Exit("execution project changed; existing job cannot be rerouted", exitCodeUsage)
		}
	}
	cursorOverride, _ := f.GetString("cursor")
	if cursorOverride != "" {
		previous.Cursor = cursorOverride
	}
	var preview bigquery.Preview
	if operation == "run" {
		path, _ := f.GetString("preview")
		if err := decodeBigQueryFileLimit(path, cmd.InOrStdin(), &preview, 2<<20); err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
		digest, _ := f.GetString("approve-digest")
		if digest == "" || digest != preview.ApprovalDigest {
			return Exit("--approve-digest must equal the displayed preview digest", exitCodeUsage)
		}
	}
	// Guard and policy preparation precede private ledger, authentication or I/O.
	preparer := bigqueryread.Preparer{Input: input, Options: func() (accesspolicies.Options, error) { return bigQueryPolicyOptions(cmd) }}
	plan, _, lines, err := preparer.PrepareWithReport(cmd.Context())
	if err != nil {
		return recoverError(err)
	}
	for _, line := range lines {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), line.String())
	}
	ledger, err := bigQueryDeps.ledger(dir)
	if err != nil {
		return recoverError(err)
	}
	provider, err := bigQueryDeps.provider(dir, cancelPermission, auth)
	if err != nil {
		return recoverError(err)
	}
	client, err := bigquery.NewClient(bigquery.Config{Profiles: []bigquery.SourceProfile{input.Profile}, Provider: provider, Transport: bigQueryDeps.transport, Ledger: ledger, Prepare: preparer.Prepare, Clock: bigQueryDeps.clock})
	if err != nil {
		return recoverError(err)
	}
	write := func(value any) error {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(value); err != nil {
			return err
		}
		if previewOut != "" {
			return writeBigQueryExport(previewOut, value, dir)
		}
		if receiptOut != "" {
			var page bigquery.Page
			switch v := value.(type) {
			case bigquery.Page:
				page = v
			case bigQueryControl:
				page = v.Page
			}
			page.Rows = nil
			page.Schema = nil
			return writeBigQueryExport(receiptOut, page, dir)
		}
		return nil
	}
	if reconnect {
		rotated, e := client.RebindJob(cmd.Context(), previous.Receipt, previous.Cursor)
		if e != nil {
			_ = write(previous)
			return bigQueryFailure(e)
		}
		previous.Receipt = rotated.Receipt
		previous.Cursor = rotated.Cursor
	}
	switch operation {
	case "preview":
		project, _ := f.GetString("execution-project")
		cap, _ := f.GetString("maximum-bytes-billed")
		budget, _ := f.GetString("session-budget-bytes")
		if budget == "" {
			budget = cap
		}
		if project == "" || cap == "" {
			return Exit("preview requires --execution-project and --maximum-bytes-billed", exitCodeUsage)
		}
		ctx, finish := context.WithTimeout(cmd.Context(), 15*time.Second)
		identity, _, e := provider.Authorize(ctx, bigQueryDeps.transport)
		finish()
		if e != nil {
			return bigQueryFailure(e)
		}
		bounds := bigquery.DefaultBounds()
		bounds.PageSize, _ = f.GetInt("page-size")
		preview, e = client.Preview(cmd.Context(), plan, bigquery.Execution{JobProject: project, Principal: identity.Principal, MaximumBytesBilled: cap, SessionBudgetBytes: budget}, bounds)
		if e != nil {
			return bigQueryFailure(e)
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "execution: Google subject %s; project %s; estimate %s bytes; maximum %s bytes; approval %s\n", preview.Execution.Principal.Subject, project, preview.EstimatedBytes, cap, preview.ApprovalDigest)
		return write(preview)
	case "run":
		project, _ := f.GetString("execution-project")
		if project != "" && project != preview.Execution.JobProject {
			return Exit("execution project changed; preview again", exitCodeUsage)
		}
		digest, _ := f.GetString("approve-digest")
		approval, e := client.Approve(preview, digest)
		if e != nil {
			return bigQueryFailure(e)
		}
		run, e := client.Execute(cmd.Context(), approval)
		return writeBigQueryRun(run, e, write)
	case "page":
		run, e := client.Resume(cmd.Context(), previous.Receipt, previous.Cursor)
		if run == nil {
			_ = write(previous)
			return bigQueryFailure(e)
		}
		return writeBigQueryRun(run, e, write)
	case "status":
		if previous.Receipt.Job == nil {
			return Exit("submission is unresolved; no verified job reference exists", exitCodeDatabase)
		}
		status, e := client.Status(cmd.Context(), *previous.Receipt.Job)
		if e != nil {
			_ = write(previous)
			return bigQueryFailure(e)
		}
		previous.Status = &status
		return write(previous)
	case "cancel":
		if previous.Receipt.Job == nil {
			return Exit("no verified job reference to cancel", exitCodeDatabase)
		}
		result, e := client.CancelJob(cmd.Context(), *previous.Receipt.Job)
		if e != nil {
			_ = write(previous)
			return bigQueryFailure(e)
		}
		previous.Cancel = &result
		return write(previous)
	}
	return errors.New("unknown BigQuery operation")
}
func writeBigQueryRun(run *bigquery.Run, operationError error, write func(any) error) error {
	if run == nil {
		if operationError != nil {
			return bigQueryFailure(operationError)
		}
		return Exit("missing run receipt", exitCodeDatabase)
	}
	page := bigquery.Page{}
	if operationError == nil {
		page, operationError = run.NextPage()
		if errors.Is(operationError, dal.ErrNoMoreRecords) {
			operationError = nil
		}
	}
	// One invocation reads one page then stops local waiting; the same job remains
	// recoverable. Preserve the freshest receipt even when parsing/page access fails.
	if operationError == nil {
		operationError = run.Close()
	}
	page.Receipt = run.Receipt()
	if len(page.Schema) == 0 {
		page.Schema = run.Schema()
	}
	cursor, cursorErr := run.Cursor()
	if cursorErr == nil {
		page.Cursor = cursor
	}
	if err := write(page); err != nil {
		return err
	}
	if operationError != nil {
		return bigQueryFailure(operationError)
	}
	return cursorErr
}

// Errors never disclose query/parameter/token values or raw provider responses.
func bigQueryFailure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, access.ErrAccessDenied):
		return Exit("BigQuery policy access denied", exitCodeAccessDenied)
	case errors.Is(err, accesspolicies.ErrNoPolicies):
		return Exit(accesspolicies.ErrNoPolicies.Error(), exitCodeUsage)
	case errors.Is(err, accesspolicies.ErrInvalidQuery), errors.Is(err, bigqueryread.ErrInput):
		return Exit("unsupported or invalid BigQuery query", exitCodeUsage)
	case errors.Is(err, bigqueryread.ErrIdentity):
		return Exit(err.Error(), exitCodeDatabase)
	}
	var stable *bigquery.Error
	if errors.As(err, &stable) {
		code := exitCodeDatabase
		switch stable.Code {
		case "invalid_input", "unsupported_query", "unsupported_type", "unsupported_value", "approval_required":
			code = exitCodeUsage
		case "policy_denied":
			code = exitCodeAccessDenied
		}
		return Exit(stable.Code, code)
	}
	return Exit("BigQuery setup or execution failed", exitCodeDatabase)
}

func validBigQueryExport(path, dir, prefix string) bool {
	return filepath.IsAbs(path) && filepath.Clean(filepath.Dir(path)) == filepath.Clean(dir) && strings.HasPrefix(filepath.Base(path), prefix) && strings.HasSuffix(path, ".json")
}
func writeBigQueryExport(path string, value any, dir string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return bigqueryread.ErrInput
	}
	f, err := os.CreateTemp(dir, ".bq-export-")
	if err != nil {
		return bigqueryread.ErrInput
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return bigqueryread.ErrInput
	}
	if err = os.Rename(name, path); err != nil {
		return bigqueryread.ErrInput
	}
	return nil
}
