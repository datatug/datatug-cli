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
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/spf13/cobra"
)

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
)

func compareCommand() *cobra.Command {
	command := &cobra.Command{
		Use:          "compare",
		Short:        "Compare two query result sets through a DataTug agent",
		Args:         cobra.NoArgs,
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
	_ = command.MarkFlagRequired(compareQueryFlag)
	_ = command.MarkFlagRequired(compareLeftFlag)
	_ = command.MarkFlagRequired(compareRightFlag)
	return command
}

func runCompareCommand(cmd *cobra.Command, _ []string) error {
	client, info, err := newAgentHTTPClient(cmd, compareAgentFlag)
	if err != nil {
		return err
	}
	project, _ := cmd.Flags().GetString(compareProjectFlag)
	if project == "" {
		if len(info.Projects) != 1 {
			return Exit("--project is required when the agent does not serve exactly one project", exitCodeUsage)
		}
		project = info.Projects[0].ID
	}
	storeID, _ := cmd.Flags().GetString(compareStoreFlag)
	defaultEnvironment, _ := cmd.Flags().GetString(compareEnvironmentFlag)
	leftText, _ := cmd.Flags().GetString(compareLeftFlag)
	rightText, _ := cmd.Flags().GetString(compareRightFlag)
	left, err := parseCompareSide(leftText, project, storeID, defaultEnvironment)
	if err != nil {
		return Exit("--left: "+err.Error(), exitCodeUsage)
	}
	right, err := parseCompareSide(rightText, project, storeID, defaultEnvironment)
	if err != nil {
		return Exit("--right: "+err.Error(), exitCodeUsage)
	}
	queryID, _ := cmd.Flags().GetString(compareQueryFlag)
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
	}
	if err := request.Validate(); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	raw, status, err := client.post(cmd.Context(), "/datatug/compare", request)
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
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return Exit("compare failed", 1)
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
	if result.PolicyLimited {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "comparison is limited by the current access policy")
	}
	if result.Truncated {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "diff rows are truncated; summary counts remain complete")
	}
	return nil
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
		settings, err := dtconfig.GetSettings()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return agentHTTPClient{}, apicontract.AgentInfo{}, err
		}
		host, port := resolveServeAddr("", 0, settings)
		agent = fmt.Sprintf("http://%s:%d", host, port)
	}
	client := agentHTTPClient{baseURL: strings.TrimRight(agent, "/"), client: &http.Client{Timeout: 30 * time.Second}}
	raw, status, err := client.get(cmd.Context(), "/datatug/agent-info")
	if err != nil {
		return agentHTTPClient{}, apicontract.AgentInfo{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return agentHTTPClient{}, apicontract.AgentInfo{}, agentResponseError(status, raw)
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
	var envelope apicontract.ErrorEnvelope
	if apicontract.DecodeStrict(body, &envelope) == nil && envelope.Error.Message != "" {
		return fmt.Errorf("DataTug agent: %s: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return fmt.Errorf("DataTug agent: HTTP %d", status)
}
