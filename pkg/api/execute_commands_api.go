package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// ExecuteCommandsRequest is the body of POST /datatug/exec/execute_commands
// (datatug-apps' agent.service.ts, AgentService.execute): unlike
// pkg/sqlexecute.Request (a direct db-server connection carrying a raw
// driver/host ServerRef, still used by GetServerDatabases), every command's
// Env/DB here are project-relative identifiers resolved exactly the way
// ExecuteSelect/RunQuery resolve them (resolveSourceURL) — the web client
// never sends a driver/host ServerRef, and sqlexecute.RequestCommand's own
// Validate() (which requires one) would reject every request this endpoint
// actually receives. Project comes from the `?project=` query parameter,
// mirroring how execute_endpoints.go already reads it — the JSON body
// itself carries no project field (agent.service.ts's execute() deletes
// projectId from the body before POSTing it).
type ExecuteCommandsRequest struct {
	ID string `json:"id"`
	// Project is "omitempty": the real client never sends it in the body at
	// all (it is filled in from the `?project=` query parameter before the
	// body is decoded — see executeCommandsHandler), and omitempty keeps a
	// body that does not set it from clobbering that query-derived value
	// back to "" the way a bare `json:"project"` zero-value would on
	// decode. A body that DOES explicitly set "project" still overrides it,
	// same as before.
	Project string `json:"project,omitempty"`
	// Commands are decoded from the request body; StoreID is not part of the
	// body at all and is passed to ExecuteCommands separately (see
	// executeCommandsHandler), matching ExecuteSelect/RunQuery.
	Commands []ExecuteCommandRequest `json:"commands"`
}

// Validate returns an error if the request is not well-formed.
func (v ExecuteCommandsRequest) Validate() error {
	if v.Project == "" {
		return validation.NewErrRequestIsMissingRequiredField("project")
	}
	if len(v.Commands) == 0 {
		return validation.NewErrRequestIsMissingRequiredField("commands")
	}
	for i, c := range v.Commands {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("invalid command at index %v: %w", i, err)
		}
	}
	return nil
}

// ExecuteCommandRequest is one entry of ExecuteCommandsRequest.Commands,
// matching datatug-apps' ISqlCommandRequest wire shape exactly (id?, type,
// text, env, db, namedParams?). Only Type "SQL" is implemented: it is the
// only command type agent.service.ts's execute() ever actually constructs
// (IHttpCommand exists as a client-side type but nothing builds one to POST
// here).
type ExecuteCommandRequest struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
	Text string `json:"text"`
	Env  string `json:"env"`
	DB   string `json:"db"`
	// NamedParams is decoded so a request that sends it gets a clear
	// "not supported" error (see ExecuteCommands) rather than having the
	// parameters silently ignored — RunNativeSQL executes opaque SQL text
	// with no bind-parameter surface (REQ:opaque-sql-limitation). The web
	// client already avoids this combination itself: a single command with
	// namedParams routes through GET /exec/select instead (agent.service.ts,
	// AgentService.execute).
	NamedParams map[string]any `json:"namedParams,omitempty"`
}

// Validate returns an error if the command is not well-formed.
func (v ExecuteCommandRequest) Validate() error {
	if v.Type == "" {
		return validation.NewErrRequestIsMissingRequiredField("type")
	}
	if v.Type != "SQL" {
		return validation.NewErrBadRequestFieldValue("type", fmt.Sprintf("command type %q is not supported for execute_commands (only \"SQL\")", v.Type))
	}
	if v.Text == "" {
		return validation.NewErrRequestIsMissingRequiredField("text")
	}
	if v.Env == "" {
		return validation.NewErrRequestIsMissingRequiredField("env")
	}
	if v.DB == "" {
		return validation.NewErrRequestIsMissingRequiredField("db")
	}
	return nil
}

// ExecuteCommandsResponse is POST /datatug/exec/execute_commands's response,
// matching datatug-apps' IExecuteResponse wire shape (duration, commands:
// ICommandResponse[]).
type ExecuteCommandsResponse struct {
	// DurationMilliseconds is the wall-clock time every command in this
	// request took, combined.
	DurationMilliseconds int64                    `json:"duration"`
	Commands             []CommandExecutionResult `json:"commands"`
}

// CommandExecutionResult is one ExecuteCommandsResponse.Commands entry,
// matching datatug-apps' ICommandResponse (commandId, elapsed?, items).
type CommandExecutionResult struct {
	CommandID           string                `json:"commandId"`
	ElapsedMilliseconds int64                 `json:"elapsed,omitempty"`
	Items               []CommandResponseItem `json:"items"`
}

// CommandResponseItem is one CommandExecutionResult.Items entry, matching
// datatug-apps' ICommandResponseItem (type, elapsed?, value?). Value is
// always a QueryResultResponse for the one command type this endpoint
// implements (SQL) — the same columns/rows/limitations[] shape
// exec/select and run_query already return (REQ:limitation-visible), so
// the web UI's recordset rendering has one wire shape to handle regardless
// of which endpoint produced it.
type CommandResponseItem struct {
	Type  string      `json:"type"`
	Value interface{} `json:"value,omitempty"`
}

// ExecuteCommands runs every command in request through the
// policy-enforced secureread.Executor (REQ:server-acl-all-reads), the same
// path api.ExecuteSelect and api.RunQuery already use — see routes.go's
// executeRoutes / execute_endpoints.go. It replaces the
// `panic("not implemented yet")` this function used to be: the previous
// signature took a pkg/sqlexecute.Request, whose RequestCommand embeds a
// datatug.ServerRef requiring a concrete driver/host, which is not the
// shape datatug-apps' web client actually sends (see ExecuteCommandsRequest's
// doc comment) — a real implementation against that old signature would
// have rejected every real request anyway.
//
// A command's own error (an unsupported source scheme, a policy refusal, a
// database that can't be resolved) aborts the whole request with that
// error — matching sqlexecute's original all-or-nothing Response.Commands
// contract, and (for a secureread.ErrAccessDenied) letting
// util_error_handling.go's existing ACCESS_DENIED/403 mapping apply exactly
// as it does for exec/select and run_query, with no separate handling
// needed here.
func ExecuteCommands(ctx context.Context, storeID string, request ExecuteCommandsRequest) (ExecuteCommandsResponse, error) {
	if err := request.Validate(); err != nil {
		return ExecuteCommandsResponse{}, err
	}
	executor, ok := SecureExecutor()
	if !ok {
		return ExecuteCommandsResponse{}, errors.New("execute_commands: server has no policy-enforced session configured")
	}
	dtStore, err := storage.NewDatatugStore(storeID)
	if err != nil {
		return ExecuteCommandsResponse{}, err
	}
	projStore := dtStore.GetProjectStore(request.Project)

	start := time.Now()
	response := ExecuteCommandsResponse{Commands: make([]CommandExecutionResult, len(request.Commands))}
	for i, command := range request.Commands {
		commandStart := time.Now()
		if len(command.NamedParams) > 0 {
			return ExecuteCommandsResponse{}, validation.NewErrBadRequestFieldValue("namedParams", "not supported for execute_commands; use GET /exec/select for a parameterized single query")
		}
		sourceURL, err := resolveSourceURL(ctx, projStore, command.Env, command.DB)
		if err != nil {
			return ExecuteCommandsResponse{}, fmt.Errorf("command %d: %w", i, err)
		}
		result, err := executor.RunNativeSQL(ctx, sourceURL, command.Text)
		if err != nil {
			return ExecuteCommandsResponse{}, fmt.Errorf("command %d: %w", i, err)
		}
		commandID := command.ID
		if commandID == "" {
			commandID = request.ID
		}
		response.Commands[i] = CommandExecutionResult{
			CommandID:           commandID,
			ElapsedMilliseconds: time.Since(commandStart).Milliseconds(),
			Items: []CommandResponseItem{
				{Type: "recordset", Value: resultToResponse(result)},
			},
		}
	}
	response.DurationMilliseconds = time.Since(start).Milliseconds()
	return response, nil
}
