package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/spf13/cobra"
)

type sessionRequestError struct {
	StatusCode int
	Code       string
}

func (e *sessionRequestError) Error() string {
	return fmt.Sprintf("session request failed: HTTP %d (%s)", e.StatusCode, e.Code)
}

// recoverClientRun only reads accepted work. A pending run can represent a
// provider call whose reply was lost, so it must never restart that call.
// Both a pin receipt and its outcome receipt resolve to the same durable run.
func (c sessionCLI) recoverClientRun(cmd *cobra.Command, operationID, sessionID, kind string) (bool, error) {
	var receipt sessioncontext.Receipt
	_, err := c.request(cmd.Context(), http.MethodGet, "/operations/"+url.PathEscape(operationID), nil, &receipt)
	if err != nil {
		var response *sessionRequestError
		if errors.As(err, &response) && response.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return true, err
	}
	if receipt.SessionID != sessionID || (receipt.Action != "pinInput" && receipt.Action != "reportClientRun" && receipt.Action != "reportDashboard" && receipt.Action != "recordRunExecution") {
		return true, errors.New("operation belongs to different work; choose a new --op for an intentional new run")
	}
	var run sessioncontext.Run
	if _, err = c.request(cmd.Context(), http.MethodGet, "/runs/"+url.PathEscape(receipt.ResourceID), nil, &run); err != nil {
		return true, err
	}
	if run.SessionID != sessionID || run.Kind != kind || run.ExecutionAuthority != "client" {
		return true, errors.New("operation belongs to a different run; choose a new --op for an intentional new run")
	}
	var interpretation *chat.InterpretResult
	if kind == "chat" && run.State == "completed" {
		text, readErr := c.retainedAssistantText(cmd.Context(), run)
		if readErr != nil {
			return true, readErr
		}
		interpretation = &chat.InterpretResult{TugQL: text}
	}
	response := struct {
		Receipt        sessioncontext.Receipt `json:"receipt"`
		Run            sessioncontext.Run     `json:"run"`
		Replayed       bool                   `json:"replayed"`
		Interpretation *chat.InterpretResult  `json:"interpretation,omitempty"`
	}{receipt, run, true, interpretation}
	if err = json.NewEncoder(cmd.OutOrStdout()).Encode(response); err != nil {
		return true, err
	}
	if run.State == "accepted" {
		return true, fmt.Errorf("operation %s is accepted; outcome unknown; inspect session run %s; an intentional new run requires a new --op", operationID, run.ID)
	}
	return true, nil
}

func (c sessionCLI) retainedAssistantText(ctx context.Context, run sessioncontext.Run) (string, error) {
	cursor := ""
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		pageClient := c
		pageClient.query = c.query.Clone()
		pageClient.query.Set("cursor", cursor)
		pageClient.query.Set("limit", "100")
		var page sessioncontext.Page[sessioncontext.Message]
		if _, err := pageClient.request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(run.SessionID)+"/messages", nil, &page); err != nil {
			return "", err
		}
		for _, message := range page.Items {
			if message.Role == "assistant" && message.InputSnapshotID == run.InputSnapshotID && message.SessionID == run.SessionID {
				return message.Text, nil
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return "", errors.New("completed run has unavailable response history; inspect its run before choosing a new --op")
		}
		cursor = page.NextCursor
	}
	return "", errors.New("completed run has unavailable response history; inspect its run before choosing a new --op")
}
