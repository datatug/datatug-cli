package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/spf13/cobra"
)

// Exit codes of `datatug checks run`.
const (
	checksRunExitFail    = 1
	checksRunExitUnknown = 2
	checksRunExitError   = 3
)

// checksRunCommand runs one project Check headlessly through the agent's
// Detective action endpoint, the same path the interactive Checks UI uses.
func checksRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <checkId>",
		Short: "Run one Check without the interactive UI and print its outcome",
		Long: "Runs the project Check through the running DataTug agent and prints its outcome and each step's result; a " +
			"reconcile step also prints its reconciliation summary. The agent must be served with --allow-writes, because a run " +
			"is recorded.\n\nExit codes: 0 pass, 1 fail, 2 unknown (inconclusive or an execution error), 3 errors.",
		Example:      "datatug checks run reference-data-agrees\ndatatug checks run reference-data-agrees --project datatug-factories --json",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE:         runChecksRun,
	}
	cmd.Flags().String("agent", "", "DataTug agent base URL (default configured server host/port)")
	cmd.Flags().StringP("project", "p", "", "Project ID (optional when the agent serves one project)")
	cmd.Flags().String("env", "", "Environment the Check's query steps run in (default: the only environment; any one for a Check of reconcile steps only)")
	cmd.Flags().Bool("json", false, "Print the run as JSON")
	return cmd
}

func runChecksRun(cmd *cobra.Command, args []string) error {
	fail := func(err error) error { return Exit(err.Error(), checksRunExitError) }
	client, info, err := newAgentHTTPClient(cmd, "agent")
	if err != nil {
		return fail(fmt.Errorf("checks run requires a running DataTug agent: %w", err))
	}
	project, _ := cmd.Flags().GetString("project")
	if project == "" {
		if len(info.Projects) != 1 {
			return fail(fmt.Errorf("--project is required when the agent serves %d projects", len(info.Projects)))
		}
		project = info.Projects[0].ID
	}
	service := detectiveUIService{client: client, project: project, securityContextID: info.SecurityContextID, pending: &detectiveOperationTracker{ids: map[string]string{}}}
	checkID := args[0]
	environment, _ := cmd.Flags().GetString("env")
	if environment == "" {
		state, err := service.State(cmd.Context())
		if err != nil {
			return fail(err)
		}
		if environment, err = checkRunEnvironment(state, checkID); err != nil {
			return fail(err)
		}
	}
	response, err := service.Action(cmd.Context(), detective.ActionRequest{Type: "run-check", CheckID: checkID, Environment: environment})
	if err != nil {
		return fail(err)
	}
	if response.Run == nil {
		return fail(errors.New("the agent returned no run"))
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		body, _ := json.Marshal(response.Run) // plain values
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", body)
	} else {
		writeCheckRun(cmd.OutOrStdout(), *response.Run)
	}
	switch response.Run.Outcome {
	case "pass":
		return nil
	case "fail":
		return Reported("check failed", checksRunExitFail)
	default:
		return Reported("check outcome is unknown", checksRunExitUnknown)
	}
}

// checkRunEnvironment picks the run's environment when none was given: the
// project's only one, or, for a Check whose steps are all reconcile steps
// (each profile selects its own instances), the first one.
func checkRunEnvironment(state detective.State, checkID string) (string, error) {
	if len(state.Environments) == 1 {
		return state.Environments[0], nil
	}
	for _, check := range state.Checks {
		if check.ID != checkID {
			continue
		}
		reconcileOnly := len(check.Steps) > 0
		for _, step := range check.Steps {
			reconcileOnly = reconcileOnly && step.Kind == "reconcile"
		}
		if reconcileOnly && len(state.Environments) > 0 {
			return state.Environments[0], nil
		}
	}
	return "", errors.New("--env is required: the project has several environments")
}

func writeCheckRun(w io.Writer, run detective.CheckRun) {
	_, _ = fmt.Fprintf(w, "check %s: %s (execution %s)\n", run.CheckID, run.Outcome, run.ExecutionStatus)
	for _, step := range run.Steps {
		line := fmt.Sprintf("  %s (%s): %s", step.StepID, step.Kind, step.Outcome)
		if step.ExecutionStatus != "completed" {
			line += " [" + step.ExecutionStatus
			if step.ErrorCode != "" {
				line += " " + step.ErrorCode
			}
			line += "]"
		}
		if step.Message != "" {
			line += " - " + step.Message
		}
		_, _ = fmt.Fprintln(w, line)
		if r := step.Reconciliation; r != nil {
			s := r.Summary
			_, _ = fmt.Fprintf(w, "    %s: %s; %d records, %d consistent, %d unexpected, %d expected only, %d inconclusive; %d of %d instances compared\n",
				step.Profile, r.Status, s.Records, s.Consistent, s.WithUnexpected, s.WithExpectedOnly, s.WithInconclusive, s.ComparedInstances, s.Instances)
			for _, instance := range r.Instances {
				if instance.Status == "consistent" {
					continue
				}
				detail := ""
				if instance.Unavailable != nil {
					detail = ": " + string(instance.Unavailable.Reason)
				}
				_, _ = fmt.Fprintf(w, "    %s %s%s\n", instance.Instance.ID, instance.Status, detail)
			}
		}
	}
}
