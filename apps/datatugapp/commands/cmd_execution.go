package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

const (
	executionAgentFlag       = "agent"
	executionProjectFlag     = "project"
	executionEnvironmentFlag = "environment"
	executionStoreFlag       = "store"
	executionJSONFlag        = "json"
	executionSnapshotFlag    = "snapshot"
)

func executionCommand() *cobra.Command {
	command := &cobra.Command{Use: "execution", Short: "Read recorded execution evidence from a DataTug agent"}
	command.PersistentFlags().String(executionAgentFlag, "", "DataTug agent base URL (default server.host/server.port or http://localhost:8989)")
	command.PersistentFlags().String(executionProjectFlag, "", "Project ID (optional when the agent serves exactly one project)")
	command.PersistentFlags().String(executionEnvironmentFlag, "", "Environment ID")
	command.PersistentFlags().String(executionStoreFlag, "", "Qualified evidence store ID (default: project ID)")
	command.PersistentFlags().Bool(executionJSONFlag, false, "Print the exact JSON response")
	command.AddCommand(executionListCommand(), executionShowCommand())
	return command
}

func executionListCommand() *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "List execution receipts", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, scope, err := newExecutionClient(cmd)
			if err != nil {
				return err
			}
			query := scope.values()
			var response apicontract.ExecutionListResponse
			raw, err := client.get(cmd.Context(), "/datatug/executions?"+query.Encode(), &response)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool(executionJSONFlag)
			if asJSON {
				_, err = cmd.OutOrStdout().Write(raw)
				return err
			}
			for _, execution := range response.Executions {
				identity := execution.QueryID
				if identity == "" {
					identity = "ad-hoc"
				}
				snapshot := ""
				if execution.SnapshotState != nil {
					snapshot = " snapshot=" + execution.SnapshotState.Availability
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  rows=%d%s\n", execution.Ref.ExecutionID, execution.ExecutedAt, identity, execution.RowCount, snapshot)
			}
			if response.Truncated {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "more executions are available")
			}
			return nil
		},
	}
}

func executionShowCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "show <id>", Short: "Show one execution receipt", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, scope, err := newExecutionClient(cmd)
			if err != nil {
				return err
			}
			snapshot, _ := cmd.Flags().GetBool(executionSnapshotFlag)
			path := "/datatug/executions/" + url.PathEscape(args[0])
			if snapshot {
				path += "/snapshot"
			}
			raw, err := client.get(cmd.Context(), path+"?"+scope.values().Encode(), nil)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool(executionJSONFlag)
			if asJSON {
				_, err = cmd.OutOrStdout().Write(raw)
				return err
			}
			if snapshot {
				var response apicontract.SnapshotReadResponse
				if err := apicontract.DecodeStrict(raw, &response); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "execution %s snapshot %s: %s\n", response.Execution.ExecutionID, response.SnapshotRef, response.SnapshotState.Availability)
				if response.Recordset != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d rows, %d columns\n", len(response.Recordset.Rows), len(response.Recordset.Columns))
				}
				return nil
			}
			var record apicontract.ExecutionRecord
			if err := apicontract.DecodeStrict(raw, &record); err != nil {
				return err
			}
			identity := record.QueryID
			if identity == "" {
				identity = "ad-hoc DTQL " + record.DTQLHash
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "execution %s\n%s\n%s / %s\nrows: %d\nfingerprint: %s\n", record.Ref.ExecutionID, identity, record.Scope.Project, record.Scope.Environment, record.RowCount, record.ResultFingerprint)
			return nil
		},
	}
	command.Flags().Bool(executionSnapshotFlag, false, "Read current-policy-filtered snapshot evidence instead of the receipt")
	return command
}

type executionScope struct {
	Project, Environment, StoreID, SecurityContextID string
}

func (s executionScope) values() url.Values {
	values := url.Values{"project": {s.Project}, "environment": {s.Environment}, "securityContextId": {s.SecurityContextID}}
	if s.StoreID != "" {
		values.Set("storeId", s.StoreID)
	}
	return values
}

type executionClient struct {
	baseURL string
	client  *http.Client
}

func newExecutionClient(cmd *cobra.Command) (executionClient, executionScope, error) {
	agent, _ := cmd.Flags().GetString(executionAgentFlag)
	if agent == "" {
		settings, err := dtconfig.GetSettings()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return executionClient{}, executionScope{}, err
		}
		host, port := resolveServeAddr("", 0, settings)
		agent = fmt.Sprintf("http://%s:%d", host, port)
	}
	client := executionClient{baseURL: strings.TrimRight(agent, "/"), client: &http.Client{Timeout: 15 * time.Second}}
	var info apicontract.AgentInfo
	if _, err := client.get(cmd.Context(), "/datatug/agent-info", &info); err != nil {
		return executionClient{}, executionScope{}, err
	}
	project, _ := cmd.Flags().GetString(executionProjectFlag)
	if project == "" {
		if len(info.Projects) != 1 {
			return executionClient{}, executionScope{}, Exit("--project is required when the agent does not serve exactly one project", exitCodeUsage)
		}
		project = info.Projects[0].ID
	}
	environment, _ := cmd.Flags().GetString(executionEnvironmentFlag)
	if environment == "" {
		return executionClient{}, executionScope{}, Exit("--environment is required", exitCodeUsage)
	}
	storeID, _ := cmd.Flags().GetString(executionStoreFlag)
	return client, executionScope{Project: project, Environment: environment, StoreID: storeID, SecurityContextID: info.SecurityContextID}, nil
}

func (c executionClient) get(ctx context.Context, path string, target any) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DataTug agent request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope apicontract.ErrorEnvelope
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return nil, fmt.Errorf("DataTug agent: %s: %s", envelope.Error.Code, envelope.Error.Message)
		}
		return nil, fmt.Errorf("DataTug agent: HTTP %d", response.StatusCode)
	}
	if target != nil {
		if err := apicontract.DecodeStrict(body, target); err != nil {
			return nil, fmt.Errorf("decode DataTug agent response: %w", err)
		}
	}
	return body, nil
}
