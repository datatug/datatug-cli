package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/spf13/cobra"
)

const compareAgentRequestTimeout = 75 * time.Second

// agentLoadSettings reads the DataTug settings used to resolve the default
// agent address. It is a package-level seam so tests can stay hermetic.
var agentLoadSettings = dtconfig.GetSettings

const (
	compareAgentFlag        = "agent"
	compareProjectFlag      = "project"
	compareStoreFlag        = "store"
	compareEnvironmentFlag  = "environment"
	compareQueryFlag        = "query"
	compareLeftFlag         = "left"
	compareRightFlag        = "right"
	compareKeyFlag          = "key"
	compareDistributionFlag = "distribution"
	compareLimitFlag        = "limit"
	compareIncidentFlag     = "incident"
	compareMutationFlag     = "mutation"
	compareJSONFlag         = "json"
	compareLeftSchemaFlag   = "left-schema"
	compareRightSchemaFlag  = "right-schema"
	compareDetailsFlag      = "details"
	compareLeftEnvFlag      = "left-environment"
	compareRightEnvFlag     = "right-environment"
	compareLeftSourceFlag   = "left-source"
	compareRightSourceFlag  = "right-source"
	compareTableMapFlag     = "table-map"
	compareKeyMapFlag       = "key-map"
	compareMappingFileFlag  = "mapping-file"
)

func compareArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 || len(args) == 2 {
		return nil
	}
	return fmt.Errorf("expected either no positional arguments for query comparison or exactly two database names")
}

func compareCommand() *cobra.Command {
	command := &cobra.Command{
		Use:          "compare",
		Short:        "Compare database schemas and records, or two query result sets",
		Long:         "Compare complete database contents from one or two environments in a DataTug project, or use the existing agent-backed query-result comparison flags. Database comparison always reports schema changes and fully reads every DALgo table; provider-reported views are compared as schema metadata. --details only limits how many per-record examples are printed. Renamed relations require explicit --table-map entries; keyless relations are compared as row multisets unless --key-map supplies a verified identity. Hosted-pending editions require an explicit --left-source/--right-source binding; their metadata URLs are never opened. BigQuery sources use the separately granted read-only Google identity and the DALgo physical-row API; no BigQuery query job is submitted.",
		Example:      "datatug compare local remote --project demo-project --environment dev\ndatatug compare chinook-sqlite chinook-postgresql --project ./demo-project-1 --left-environment dev --right-environment QA --left-source env:DEMO_CHINOOK_SQLITE_URL --right-source env:DEMO_CHINOOK_QA_PG_URL --right-schema public --details --limit 20\ndatatug compare source target --project ./project --table-map 'Production.Document=production_document' --key-map 'Production.Document=ProductID,DocumentID'\ndatatug compare --project demo-project --query orders --left env=before --right env=after --key id",
		Args:         compareArgs,
		SilenceUsage: true,
		RunE:         runCompareCommand,
	}
	flags := command.Flags()
	flags.String(compareAgentFlag, "", "DataTug agent base URL (default server.host/server.port or http://localhost:8989)")
	flags.String(compareProjectFlag, "", "Project ID (optional when the agent serves exactly one project)")
	flags.String(compareStoreFlag, api.LocalStoreID, "Source store ID for env and facts sides")
	flags.String(compareEnvironmentFlag, "", "Environment used by facts sides")
	flags.String(compareQueryFlag, "", "Saved query ID")
	flags.String(compareLeftFlag, "", "Left side: env=<id>, facts=affected|control, or execution=<store>/<project>/<execution>")
	flags.String(compareRightFlag, "", "Right side: env=<id>, facts=affected|control, or execution=<store>/<project>/<execution>")
	flags.StringSlice(compareKeyFlag, nil, "Key column (repeat the flag or use a comma-separated list)")
	flags.String(compareDistributionFlag, "", "Optional shared column for value distribution")
	flags.Int(compareLimitFlag, apicontract.CompareDefaultLimit, "Maximum diff rows to print or return")
	flags.String(compareIncidentFlag, "", "Optional qualified incident <store>/<incident>")
	flags.String(compareMutationFlag, "", "Required idempotency key when --incident is set")
	flags.Bool(compareJSONFlag, false, "Print the exact JSON response")
	flags.String(compareLeftSchemaFlag, "", "PostgreSQL schema to inspect on the left database")
	flags.String(compareRightSchemaFlag, "", "PostgreSQL schema to inspect on the right database")
	flags.Bool(compareDetailsFlag, false, "Print bounded per-record differences after the complete comparison")
	flags.String(compareLeftEnvFlag, "", "Environment for the left database (defaults to --environment or the project's only environment)")
	flags.String(compareRightEnvFlag, "", "Environment for the right database (defaults to --environment or the project's only environment)")
	flags.String(compareLeftSourceFlag, "", "Explicit DALgo source for the left database; use env:VARIABLE for a DSN so credentials stay out of command history")
	flags.String(compareRightSourceFlag, "", "Explicit DALgo source for the right database; use env:VARIABLE for a DSN so credentials stay out of command history")
	flags.StringSlice(compareTableMapFlag, nil, "Map a left relation name to a right relation name: LEFT=RIGHT (repeat for renamed relations)")
	flags.StringSlice(compareKeyMapFlag, nil, "Database comparison identity: TABLE=column[,column] (repeat per table); otherwise use matching primary keys or keyless multiset comparison")
	flags.String(compareMappingFileFlag, "", "Project-relative JSON file with explicit relation-name mappings and optional per-table keys")
	return command
}

func runCompareCommand(cmd *cobra.Command, args []string) error {
	if len(args) == 2 {
		return runDatabaseCompareCommand(cmd, args[0], args[1])
	}
	if cmd.Flags().Changed(compareLeftSchemaFlag) || cmd.Flags().Changed(compareRightSchemaFlag) || cmd.Flags().Changed(compareDetailsFlag) ||
		cmd.Flags().Changed(compareLeftEnvFlag) || cmd.Flags().Changed(compareRightEnvFlag) || cmd.Flags().Changed(compareLeftSourceFlag) ||
		cmd.Flags().Changed(compareRightSourceFlag) || cmd.Flags().Changed(compareTableMapFlag) || cmd.Flags().Changed(compareKeyMapFlag) ||
		cmd.Flags().Changed(compareMappingFileFlag) {
		return Exit("database-specific schema, environment, and detail flags require two positional database names", exitCodeUsage)
	}
	queryID, _ := cmd.Flags().GetString(compareQueryFlag)
	leftText, _ := cmd.Flags().GetString(compareLeftFlag)
	rightText, _ := cmd.Flags().GetString(compareRightFlag)
	if queryID == "" || leftText == "" || rightText == "" {
		return Exit("query comparison requires --query, --left, and --right; database comparison uses `compare <left-db> <right-db> --project <project>`", exitCodeUsage)
	}
	client, info, err := newAgentHTTPClient(cmd, compareAgentFlag)
	if err != nil {
		return err
	}
	project, _ := cmd.Flags().GetString(compareProjectFlag)
	if project == "" && (compareSideNeedsProject(leftText) || compareSideNeedsProject(rightText)) {
		if len(info.Projects) != 1 {
			return Exit("--project is required for env or facts sides when the agent does not serve exactly one project", exitCodeUsage)
		}
		project = info.Projects[0].ID
	}
	storeID, _ := cmd.Flags().GetString(compareStoreFlag)
	defaultEnvironment, _ := cmd.Flags().GetString(compareEnvironmentFlag)
	left, err := parseCompareSide(leftText, project, storeID, defaultEnvironment)
	if err != nil {
		return Exit("--left: "+err.Error(), exitCodeUsage)
	}
	right, err := parseCompareSide(rightText, project, storeID, defaultEnvironment)
	if err != nil {
		return Exit("--right: "+err.Error(), exitCodeUsage)
	}
	key, _ := cmd.Flags().GetStringSlice(compareKeyFlag)
	distribution, _ := cmd.Flags().GetString(compareDistributionFlag)
	mutationID, _ := cmd.Flags().GetString(compareMutationFlag)
	request := apicontract.CompareRequest{
		SecurityContextID: info.SecurityContextID, QueryID: queryID, Left: left, Right: right,
		Key: key, DistributionColumn: distribution, MutationID: mutationID,
	}
	if cmd.Flags().Changed(compareLimitFlag) {
		limit, _ := cmd.Flags().GetInt(compareLimitFlag)
		request.Limit = &limit
	}
	incidentText, _ := cmd.Flags().GetString(compareIncidentFlag)
	if incidentText != "" {
		incident, parseErr := incidents.ParseIncidentRef(incidentText)
		if parseErr != nil {
			return Exit("--incident: "+parseErr.Error(), exitCodeUsage)
		}
		request.Incident = &incident
		leftProject, rightProject := compareSideProject(left), compareSideProject(right)
		if leftProject == "" || rightProject == "" || leftProject != rightProject {
			return Exit("--incident requires both sides to belong to one unambiguous project", exitCodeUsage)
		}
	}
	if err := request.Validate(); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	raw, _, err := client.post(cmd.Context(), "/datatug/compare", request)
	asJSON, _ := cmd.Flags().GetBool(compareJSONFlag)
	if asJSON && len(raw) != 0 {
		if _, writeErr := cmd.OutOrStdout().Write(raw); writeErr != nil {
			return writeErr
		}
	}
	if err != nil {
		if asJSON {
			return Exit("compare failed", 1)
		}
		return err
	}
	if asJSON {
		return nil
	}
	var result apicontract.CompareResult
	if err := apicontract.DecodeStrict(raw, &result); err != nil {
		return fmt.Errorf("decode DataTug compare response: %w", err)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate DataTug compare response: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s: +%d -%d ~%d, %d unchanged\n",
		result.Left.Execution.ExecutionID, result.Right.Execution.ExecutionID,
		result.Summary.Added, result.Summary.Removed, result.Summary.Changed, result.Summary.Unchanged)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "key: %s\n", strings.Join(result.Key, ", "))
	writeCompareRows(cmd.OutOrStdout(), "added", result.Columns, result.Key, result.Added)
	writeCompareRows(cmd.OutOrStdout(), "removed", result.Columns, result.Key, result.Removed)
	for _, row := range result.Changed {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "changed [%s]:", formatCompareKey(result.Key, row.Key))
		for _, change := range row.Columns {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " %s %s -> %s", change.Column, formatCompareValue(change.Left), formatCompareValue(change.Right))
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
	}
	if result.Distribution != nil {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "distribution %s:\n", result.Distribution.Column)
		for _, value := range result.Distribution.Values {
			ratio := "null"
			if value.Ratio != nil {
				ratio = strconv.FormatFloat(*value.Ratio, 'g', -1, 64)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s: left %d (%.2f%%), right %d (%.2f%%), ratio %s\n",
				formatCompareValue(value.Value), value.Left.Count, value.Left.Pct, value.Right.Count, value.Right.Pct, ratio)
		}
		if result.Distribution.Truncated {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  distribution values are truncated")
		}
	}
	if result.PolicyLimited {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "comparison is limited by the current access policy")
	}
	if result.Truncated {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "diff rows are truncated; summary counts remain complete")
	}
	return nil
}

func compareSideNeedsProject(value string) bool {
	kind, _, ok := strings.Cut(value, "=")
	return ok && (kind == "env" || kind == "facts")
}

func compareSideProject(side apicontract.CompareSideSpec) string {
	if side.Execution != nil {
		return side.Execution.ProjectID
	}
	return side.Project
}

func writeCompareRows(w io.Writer, label string, columns []apicontract.Column, keyNames []string, rows []apicontract.CompareRow) {
	for _, row := range rows {
		_, _ = fmt.Fprintf(w, "%s [%s]:", label, formatCompareKey(keyNames, row.Key))
		for i, value := range row.Row {
			if i < len(columns) {
				_, _ = fmt.Fprintf(w, " %s=%s", columns[i].Name, formatCompareValue(value))
			}
		}
		_, _ = fmt.Fprintln(w)
	}
}

func formatCompareKey(names []string, values []apicontract.TypedValue) string {
	parts := make([]string, 0, len(values))
	for i, value := range values {
		name := fmt.Sprintf("key%d", i+1)
		if i < len(names) {
			name = names[i]
		}
		parts = append(parts, name+"="+formatCompareValue(value))
	}
	return strings.Join(parts, ", ")
}

func formatCompareValue(value apicontract.TypedValue) string {
	switch value.Type {
	case apicontract.ValueTypeString, apicontract.ValueTypeDate, apicontract.ValueTypeDatetime:
		return strconv.Quote(value.Str)
	case apicontract.ValueTypeInteger, apicontract.ValueTypeDecimal:
		return value.Str
	case apicontract.ValueTypeNumber:
		return strconv.FormatFloat(value.Num, 'g', -1, 64)
	case apicontract.ValueTypeBoolean:
		return strconv.FormatBool(value.Bool)
	case apicontract.ValueTypeNull:
		return "null"
	default:
		return "<invalid>"
	}
}

func parseCompareSide(value, project, storeID, defaultEnvironment string) (apicontract.CompareSideSpec, error) {
	if strings.Count(value, "=") != 1 {
		return apicontract.CompareSideSpec{}, errors.New("expected exactly one kind=value expression")
	}
	kind, argument, _ := strings.Cut(value, "=")
	if argument == "" {
		return apicontract.CompareSideSpec{}, errors.New("side value is required")
	}
	var side apicontract.CompareSideSpec
	switch kind {
	case "env":
		side = apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: storeID, Project: project, Environment: argument}
	case "facts":
		if defaultEnvironment == "" {
			return apicontract.CompareSideSpec{}, errors.New("--environment is required for a facts side")
		}
		side = apicontract.CompareSideSpec{Kind: apicontract.CompareSideFacts, StoreID: storeID, Project: project, Environment: defaultEnvironment, CohortRole: argument}
	case "execution":
		parts := strings.Split(argument, "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return apicontract.CompareSideSpec{}, errors.New("execution must be <store>/<project>/<execution>")
		}
		ref := apicontract.ExecutionRef{StoreID: parts[0], ProjectID: parts[1], ExecutionID: parts[2]}
		side = apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &ref}
	default:
		return apicontract.CompareSideSpec{}, fmt.Errorf("unknown side kind %q", kind)
	}
	if err := side.Validate(); err != nil {
		return apicontract.CompareSideSpec{}, err
	}
	return side, nil
}

type agentHTTPClient struct {
	baseURL string
	client  *http.Client
}

func newAgentHTTPClient(cmd *cobra.Command, flagName string) (agentHTTPClient, apicontract.AgentInfo, error) {
	agent, _ := cmd.Flags().GetString(flagName)
	if agent == "" {
		settings, err := agentLoadSettings()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return agentHTTPClient{}, apicontract.AgentInfo{}, err
		}
		host, port := resolveServeAddr("", 0, settings)
		agent = fmt.Sprintf("http://%s:%d", host, port)
	}
	// Compare may execute two sides sequentially, each using the server's
	// valid 30-second execution budget, followed by snapshot persistence and
	// an optional incident append. Preserve caller cancellation while allowing
	// that complete server-side budget to finish.
	client := agentHTTPClient{baseURL: strings.TrimRight(agent, "/"), client: &http.Client{Timeout: compareAgentRequestTimeout}}
	// do already turns every non-2xx status into an error.
	raw, _, err := client.get(cmd.Context(), "/datatug/agent-info")
	if err != nil {
		return agentHTTPClient{}, apicontract.AgentInfo{}, err
	}
	var info apicontract.AgentInfo
	if err := apicontract.DecodeStrict(raw, &info); err != nil {
		return agentHTTPClient{}, apicontract.AgentInfo{}, fmt.Errorf("decode DataTug agent-info: %w", err)
	}
	return client, info, nil
}

func (c agentHTTPClient) get(ctx context.Context, path string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	return c.do(request)
}

func (c agentHTTPClient) post(ctx context.Context, path string, input any) ([]byte, int, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	return c.do(request)
}

func (c agentHTTPClient) do(request *http.Request) ([]byte, int, error) {
	response, err := c.client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("DataTug agent request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return body, response.StatusCode, agentResponseError(response.StatusCode, body)
	}
	return body, response.StatusCode, nil
}

func agentResponseError(status int, body []byte) error {
	var response apicontract.CompareErrorResponse
	if apicontract.DecodeStrict(body, &response) == nil && response.Error.Message != "" {
		return fmt.Errorf("DataTug agent: %s: %s", response.Error.Code, response.Error.Message)
	}
	return fmt.Errorf("DataTug agent: HTTP %d", status)
}
