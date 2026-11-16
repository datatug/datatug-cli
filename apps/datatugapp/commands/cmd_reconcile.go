package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/reconcileapi"
	"github.com/datatug/datatug-cli/pkg/reconcileprofiles"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Exit codes of `datatug reconcile` (REQ:cli-reconcile).
const (
	reconcileExitDiscrepancies = 1
	reconcileExitInconclusive  = 2
	reconcileExitError         = 3
)

const (
	reconcileProfileFlag      = "profile"
	reconcileQueryFlag        = "query"
	reconcileKeyFlag          = "key"
	reconcileFamilyFlag       = "family"
	reconcileKindFlag         = "kind"
	reconcileTenantFlag       = "tenant"
	reconcileEnvFlag          = "env"
	reconcileBaselineFlag     = "baseline"
	reconcilePresenceFlag     = "presence"
	reconcileFieldFlag        = "field"
	reconcileDefaultFieldFlag = "default-field"
	reconcileGroupsOnlyFlag   = "groups-only"
	reconcileLimitFlag        = "limit"
	reconcileJSONFlag         = "json"
	reconcileAgentFlag        = "agent"
	reconcileProjectFlag      = "project"
	reconcileStoreFlag        = "store"
	reconcileTitleFlag        = "title"
)

func reconcileError(err error) error { return Exit(err.Error(), reconcileExitError) }

func reconcileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile one dataset across many instances of one application",
		Long: "Runs one saved query in every selected instance (one catalog of one environment), each read under its own " +
			"policy-enforced execution, aligns the rows by a semantic key in one pass and classifies every difference against the " +
			"consistency rule: expected, unexpected or inconclusive. Read-only: it never writes to an instance.\n\n" +
			"Exit codes: 0 consistent, 1 discrepancies, 2 partial or inconclusive, 3 errors.",
		Example: "datatug reconcile --profile reference-countries\n" +
			"datatug reconcile --query countries --key code --family factory --kind production\n" +
			"datatug reconcile --query products --key sku --env factory-a-prod,factory-b-prod --baseline factory-a-prod --field price=instance-specific\n" +
			"datatug reconcile save reference-countries --query countries --key code --family factory --kind production",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runReconcileCommand,
	}
	addReconcileRuleFlags(cmd.Flags())
	cmd.Flags().String(reconcileProfileFlag, "", "Reconciliation profile id (reconciliations/<id>.json); other flags override it")
	cmd.Flags().Bool(reconcileJSONFlag, false, "Print the engine result unchanged as JSON")
	cmd.Flags().String(reconcileAgentFlag, "", "DataTug agent base URL (default server.host/server.port or http://localhost:8989)")
	cmd.Flags().String(reconcileStoreFlag, api.LocalStoreID, "Source store ID")
	cmd.AddCommand(reconcileSaveCommand(), reconcileListCommand())
	return cmd
}

// addReconcileRuleFlags adds the flags that select instances and state the
// rule, shared by reconcile and reconcile save.
func addReconcileRuleFlags(flags *pflag.FlagSet) {
	flags.String(reconcileProjectFlag, "", "Project ID (optional when the agent serves exactly one project), or folder for save and list")
	flags.String(reconcileQueryFlag, "", "Saved query ID of the dataset")
	flags.StringSlice(reconcileKeyFlag, nil, "Semantic key columns (comma-separated or repeated); default: the confirmed data-role classification")
	flags.String(reconcileFamilyFlag, "", "Database family (DbModel) of the instances")
	flags.String(reconcileKindFlag, "", "Environment kind of the instances (production, test, …)")
	flags.StringArray(reconcileTenantFlag, nil, "Tenant label of the instances (repeat for several)")
	flags.StringSlice(reconcileEnvFlag, nil, "Environment ids of the instances (comma-separated); with --family they narrow its match")
	flags.String(reconcileBaselineFlag, "", "Authoritative baseline environment (baseline mode; default consensus)")
	flags.String(reconcilePresenceFlag, "", "Presence rule: required, subset, superset or any")
	flags.StringArray(reconcileFieldFlag, nil, "Field policy <col>=identical|normalized|instance-specific|ignored (repeat)")
	flags.String(reconcileDefaultFieldFlag, "", "Default field policy")
	flags.Bool(reconcileGroupsOnlyFlag, false, "Report which instances agree without values")
	flags.Int(reconcileLimitFlag, 0, "Maximum records per detail list (summary counts stay complete)")
}

type reconcileFlags struct {
	queryID   string
	selector  *reconcile.InstanceSelector
	rules     *reconcile.Rules
	hasQuery  bool
	hasTarget bool
}

// parseReconcileFlags reads the selection and rule flags; zero values mean
// "not given", so a profile keeps its own.
func parseReconcileFlags(cmd *cobra.Command) (reconcileFlags, error) {
	flags := cmd.Flags()
	var out reconcileFlags
	out.queryID, _ = flags.GetString(reconcileQueryFlag)
	out.hasQuery = out.queryID != ""
	family, _ := flags.GetString(reconcileFamilyFlag)
	kind, _ := flags.GetString(reconcileKindFlag)
	tenants, _ := flags.GetStringArray(reconcileTenantFlag)
	envs, _ := flags.GetStringSlice(reconcileEnvFlag)
	if family != "" || kind != "" || len(tenants) > 0 || len(envs) > 0 {
		selector := reconcile.InstanceSelector{Family: family, Kind: datatug.EnvironmentKind(kind), Tenants: tenants, Environments: envs}
		if err := selector.Kind.Validate(); err != nil {
			return out, fmt.Errorf("--kind: %w", err)
		}
		out.selector, out.hasTarget = &selector, true
	}
	var rules reconcile.Rules
	changed := false
	if key, _ := flags.GetStringSlice(reconcileKeyFlag); len(key) > 0 {
		rules.Key, changed = key, true
	}
	if baseline, _ := flags.GetString(reconcileBaselineFlag); baseline != "" {
		rules.Baseline, rules.Mode, changed = baseline, reconcile.ModeBaseline, true
	}
	if presence, _ := flags.GetString(reconcilePresenceFlag); presence != "" {
		rules.Presence, changed = reconcile.Presence(presence), true
	}
	fields, _ := flags.GetStringArray(reconcileFieldFlag)
	for _, field := range fields {
		column, policy, ok := strings.Cut(field, "=")
		if !ok || column == "" || policy == "" {
			return out, fmt.Errorf("--field %q: want <col>=<policy>", field)
		}
		if rules.Fields == nil {
			rules.Fields = map[string]reconcile.FieldPolicy{}
		}
		rules.Fields[column], changed = reconcile.FieldPolicy(policy), true
	}
	if policy, _ := flags.GetString(reconcileDefaultFieldFlag); policy != "" {
		rules.DefaultField, changed = reconcile.FieldPolicy(policy), true
	}
	if groupsOnly, _ := flags.GetBool(reconcileGroupsOnlyFlag); groupsOnly {
		rules.Disclosure, changed = reconcile.DiscloseGroupsOnly, true
	}
	if flags.Changed(reconcileLimitFlag) {
		rules.Limit, _ = flags.GetInt(reconcileLimitFlag)
		if rules.Limit < 1 {
			return out, errors.New("--limit must be positive")
		}
		changed = true
	}
	if changed {
		out.rules = &rules
	}
	return out, nil
}

func runReconcileCommand(cmd *cobra.Command, _ []string) error {
	profileID, _ := cmd.Flags().GetString(reconcileProfileFlag)
	parsed, err := parseReconcileFlags(cmd)
	if err != nil {
		return reconcileError(err)
	}
	if profileID == "" && (!parsed.hasQuery || !parsed.hasTarget) {
		return reconcileError(errors.New("reconcile needs --profile, or --query with --family or --env"))
	}
	client, info, err := newAgentHTTPClient(cmd, reconcileAgentFlag)
	if err != nil {
		return reconcileError(err)
	}
	project, _ := cmd.Flags().GetString(reconcileProjectFlag)
	if project == "" {
		if len(info.Projects) != 1 {
			return reconcileError(errors.New("--project is required when the agent does not serve exactly one project"))
		}
		project = info.Projects[0].ID
	}
	storeID, _ := cmd.Flags().GetString(reconcileStoreFlag)
	request := reconcileapi.Request{
		SecurityContextID: info.SecurityContextID, StoreID: storeID, Project: project, ProfileID: profileID,
		QueryID: parsed.queryID, Instances: parsed.selector, Rules: parsed.rules,
	}
	raw, _, err := client.post(cmd.Context(), "/datatug"+reconcileapi.Path, request)
	if err != nil {
		return reconcileError(err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	var response reconcileapi.Response
	if err := json.Unmarshal(raw, &envelope); err != nil || json.Unmarshal(raw, &response) != nil {
		return reconcileError(errors.New("decode DataTug reconcile response: the agent answered something else"))
	}
	if asJSON, _ := cmd.Flags().GetBool(reconcileJSONFlag); asJSON {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", envelope.Result)
	} else {
		label := profileID
		if label == "" {
			label = parsed.queryID
		}
		writeReconcileResult(cmd.OutOrStdout(), label, response.Result)
	}
	return reconcileExit(response.Result.Status)
}

// reconcileExit maps a verdict to the command's exit code.
func reconcileExit(status reconcile.Status) error {
	switch status {
	case reconcile.StatusConsistent:
		return nil
	case reconcile.StatusDiscrepancies:
		return Reported("reconciliation found discrepancies", reconcileExitDiscrepancies)
	default:
		return Reported("reconciliation is "+string(status), reconcileExitInconclusive)
	}
}

// writeReconcileResult prints the summary per instance, then the unexpected
// record discrepancies with their field value groups, then the expected and
// the inconclusive ones, then column drift and caveats.
func writeReconcileResult(w io.Writer, label string, result reconcile.Result) {
	mode := string(result.Mode)
	if result.Baseline != "" {
		mode += " " + result.Baseline
	}
	_, _ = fmt.Fprintf(w, "%s: %s (%s, key %s, presence %s)\n", label, result.Status, mode, strings.Join(result.Key, ","), result.Presence)
	width := 0
	for _, instance := range result.Instances {
		width = max(width, len(instance.Instance.ID))
	}
	for _, instance := range result.Instances {
		_, _ = fmt.Fprintf(w, "  %-*s  %-13s", width, instance.Instance.ID, instance.Status)
		if instance.Unavailable != nil {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", instance.Unavailable.Reason, instance.Unavailable.Message)
			continue
		}
		_, _ = fmt.Fprintf(w, "  missing %d  extra %d  conflicts %d  expected %d  inconclusive %d", instance.Missing, instance.Extra, instance.Conflicts, instance.Expected, instance.Inconclusive)
		if instance.Authoritative {
			_, _ = fmt.Fprint(w, "  (baseline)")
		}
		if len(instance.DriftColumns) > 0 {
			_, _ = fmt.Fprintf(w, "  drift: %s", strings.Join(instance.DriftColumns, ", "))
		}
		_, _ = fmt.Fprintln(w)
	}
	s := result.Summary
	_, _ = fmt.Fprintf(w, "records: %d, consistent %d, unexpected %d, expected only %d, inconclusive %d\n", s.Records, s.Consistent, s.WithUnexpected, s.WithExpectedOnly, s.WithInconclusive)
	writeReconcileRecords(w, "unexpected", result.Key, result.Unexpected, s.WithUnexpected)
	writeReconcileRecords(w, "expected", result.Key, result.Expected, s.WithExpectedOnly)
	writeReconcileRecords(w, "inconclusive", result.Key, result.Inconclusive, s.WithInconclusive)
	if len(result.Columns) > 0 {
		_, _ = fmt.Fprintln(w, "columns:")
		for _, column := range result.Columns {
			var parts []string
			if len(column.MissingIn) > 0 {
				parts = append(parts, "missing in "+strings.Join(column.MissingIn, ", "))
			}
			if len(column.HiddenIn) > 0 {
				parts = append(parts, "hidden in "+strings.Join(column.HiddenIn, ", "))
			}
			if !column.Compared {
				parts = append(parts, "conflicting types, not compared")
			}
			_, _ = fmt.Fprintf(w, "  %s: %s\n", column.Column, strings.Join(parts, "; "))
		}
	}
	if result.Truncated {
		_, _ = fmt.Fprintln(w, "detail lists are truncated; summary counts remain complete")
	}
	if len(result.Caveats) > 0 {
		_, _ = fmt.Fprintln(w, "caveats:")
		for _, caveat := range result.Caveats {
			_, _ = fmt.Fprintf(w, "  - %s\n", caveat)
		}
	}
}

func writeReconcileRecords(w io.Writer, label string, key []string, records []reconcile.RecordDiscrepancy, total int) {
	if len(records) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "%s (%d):\n", label, total)
	for _, record := range records {
		if record.Key == nil {
			_, _ = fmt.Fprintf(w, "  #%d\n", record.Ordinal)
		} else {
			_, _ = fmt.Fprintf(w, "  [%s]\n", formatCompareKey(key, record.Key))
		}
		if record.PresenceNoConsensus {
			_, _ = fmt.Fprintln(w, "    presence: no consensus")
		}
		if len(record.Missing) > 0 {
			_, _ = fmt.Fprintf(w, "    missing in %s%s\n", strings.Join(record.Missing, ", "), expectedSuffix(record.MissingExpected))
		}
		if len(record.Extra) > 0 {
			_, _ = fmt.Fprintf(w, "    extra in %s%s\n", strings.Join(record.Extra, ", "), expectedSuffix(record.ExtraExpected))
		}
		if len(record.Inconclusive) > 0 {
			_, _ = fmt.Fprintf(w, "    cannot judge absence in %s\n", strings.Join(record.Inconclusive, ", "))
		}
		for _, field := range record.Fields {
			groups := make([]string, 0, len(field.Groups))
			for _, group := range field.Groups {
				value := "<group>"
				if group.Value != nil {
					value = formatCompareValue(*group.Value)
				}
				if group.Authoritative {
					value += " (baseline)"
				}
				groups = append(groups, fmt.Sprintf("%s {%s}", value, strings.Join(group.Instances, ", ")))
			}
			line := fmt.Sprintf("    %s (%s): %s", field.Column, field.Policy, strings.Join(groups, " | "))
			switch {
			case field.NoConsensus:
				line += "; no consensus"
			case len(field.Deviating) > 0:
				line += "; deviating: " + strings.Join(field.Deviating, ", ")
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

func expectedSuffix(expected bool) string {
	if expected {
		return " (expected)"
	}
	return ""
}

func reconcileSaveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "save <profileId>",
		Short:        "Save a reconciliation profile as reconciliations/<profileId>.json",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := parseReconcileFlags(cmd)
			if err != nil {
				return reconcileError(err)
			}
			if !parsed.hasQuery || !parsed.hasTarget || parsed.rules == nil || len(parsed.rules.Key) == 0 {
				return reconcileError(errors.New("reconcile save needs --query, --key and --family or --env"))
			}
			dir, err := localProjectDir(cmd)
			if err != nil {
				return reconcileError(err)
			}
			title, _ := cmd.Flags().GetString(reconcileTitleFlag)
			if title == "" {
				title = args[0]
			}
			profile := reconcile.Profile{ID: args[0], Title: title, QueryID: parsed.queryID, Instances: *parsed.selector, Rules: *parsed.rules}
			store := reconcileprofiles.Store{Dir: dir}
			if err := store.Save(profile); err != nil {
				return reconcileError(err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "saved %s\n", store.Path(profile.ID))
			return nil
		},
	}
	addReconcileRuleFlags(cmd.Flags())
	cmd.Flags().String(reconcileTitleFlag, "", "Profile title (default: the id)")
	return cmd
}

func reconcileListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List the project's reconciliation profiles",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := localProjectDir(cmd)
			if err != nil {
				return reconcileError(err)
			}
			profiles, err := reconcileprofiles.Store{Dir: dir}.List()
			if err != nil {
				return reconcileError(err)
			}
			if asJSON, _ := cmd.Flags().GetBool(reconcileJSONFlag); asJSON {
				body, _ := json.Marshal(profiles) // plain values
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", body)
				return nil
			}
			for _, profile := range profiles {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\tquery %s\t%s\t%s\n", profile.ID, profile.QueryID, describeSelector(profile.Instances), profile.Title)
			}
			return nil
		},
	}
	cmd.Flags().String(reconcileProjectFlag, "", "Project folder or registered ID")
	cmd.Flags().Bool(reconcileJSONFlag, false, "Print the profiles as JSON")
	return cmd
}

func describeSelector(s reconcile.InstanceSelector) string {
	var parts []string
	if s.Family != "" {
		parts = append(parts, "family "+s.Family)
	}
	if s.Kind != "" {
		parts = append(parts, "kind "+string(s.Kind))
	}
	if len(s.Tenants) > 0 {
		parts = append(parts, "tenants "+strings.Join(s.Tenants, ","))
	}
	if len(s.Environments) > 0 {
		parts = append(parts, "envs "+strings.Join(s.Environments, ","))
	}
	return strings.Join(parts, " ")
}
