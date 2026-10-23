package endpoints

import (
	"context"
	"errors"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
)

// ExecuteWatchCheck is a trusted local daemon boundary. It preserves Detective's
// project asset admission, revision pinning and secured saved-query path. This
// function is never registered as an unauthenticated HTTP route.
func ExecuteWatchCheck(ctx context.Context, project, environment, checkID, checkVersion, fingerprint, operationID string, inputs map[string]detective.InputValue) (detective.CheckRun, string, error) {
	if api.ValidateIdentifier("environment", environment) != nil || operationID == "" {
		return detective.CheckRun{}, "", detective.ErrInvalid
	}
	store, actor, err := detectiveStore(project, api.SecurityContextID())
	if err != nil {
		return detective.CheckRun{}, "", errors.New("Watch Check project asset access is unavailable")
	}
	req := detective.ActionRequest{Type: "run-check", Project: project, Environment: environment, CheckID: checkID, Inputs: inputs}
	out, observedFingerprint, err := store.ExecuteWatchAction(ctx, operationID, actor, req, checkVersion, fingerprint, detectiveQueryRunner(project, api.SecurityContextID()), detectiveExecutionMetadata(project), detectiveQueryRevisionLookup(project))
	if err != nil {
		return detective.CheckRun{}, observedFingerprint, err
	}
	if out.Run == nil {
		return detective.CheckRun{}, observedFingerprint, detective.ErrInvalid
	}
	return *out.Run, observedFingerprint, nil
}
