package detective

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/google/uuid"
)

type QueryRunner = core.QueryRunner
type ExecutionMetadata = core.ExecutionMetadata
type QueryFailure = core.QueryFailure

func validateCheck(c Check) error { return core.ValidateCheck(c) }
func validateGraph(checks map[string]Check, validators map[string]PublishedValidator, locals map[string]QueryValidators) error {
	return core.ValidateCatalog(core.Catalog{Checks: checks, Validators: validators, QueryValidators: locals})
}

func (s *LocalStore) Run(ctx context.Context, checkID, environment, caseID, actor string, inputs map[string]InputValue, runner QueryRunner, metadata ...ExecutionMetadata) (CheckRun, error) {
	if !validSlug(checkID) || environment == "" || runner == nil {
		return CheckRun{}, ErrInvalid
	}
	var lookup ExecutionMetadata
	if len(metadata) > 0 {
		lookup = metadata[0]
	}
	operationID := "legacy-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	req := ActionRequest{Type: "run-check", Project: s.projectID, CheckID: checkID, CaseID: caseID, Environment: environment, Inputs: inputs}
	response, err := s.ExecuteAction(ctx, operationID, actor, req, runner, lookup, nil, nil)
	return s.legacyRunResult(ctx, operationID, actor, req, response, err)
}

func (s *LocalStore) RunQueryValidators(ctx context.Context, queryID, environment, caseID, actor string, inputs map[string]InputValue, runner QueryRunner, metadata ...ExecutionMetadata) (CheckRun, error) {
	return s.runQueryValidators(ctx, queryID, environment, caseID, actor, inputs, nil, runner, metadata...)
}

// RunQueryValidatorsForResult evaluates the authorized retained rows of an
// existing execution. Its caller must verify the receipt and current policy.
func (s *LocalStore) RunQueryValidatorsForResult(ctx context.Context, queryID, environment, caseID, actor string, inputs map[string]InputValue, result apicontract.Result, runner QueryRunner, metadata ...ExecutionMetadata) (CheckRun, error) {
	return s.runQueryValidators(ctx, queryID, environment, caseID, actor, inputs, &result, runner, metadata...)
}

func (s *LocalStore) runQueryValidators(ctx context.Context, queryID, environment, caseID, actor string, inputs map[string]InputValue, result *apicontract.Result, runner QueryRunner, metadata ...ExecutionMetadata) (CheckRun, error) {
	if !core.ValidQueryRef(queryID) || environment == "" || runner == nil {
		return CheckRun{}, ErrInvalid
	}
	var lookup ExecutionMetadata
	if len(metadata) > 0 {
		lookup = metadata[0]
	}
	operationID := "legacy-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	req := ActionRequest{Type: "run-query-validators", Project: s.projectID, QueryID: queryID, CaseID: caseID, Environment: environment, Inputs: inputs}
	response, err := s.ExecuteAction(ctx, operationID, actor, req, runner, lookup, result, nil)
	return s.legacyRunResult(ctx, operationID, actor, req, response, err)
}

func (s *LocalStore) legacyRunResult(ctx context.Context, operationID, actor string, req ActionRequest, response ActionResponse, err error) (CheckRun, error) {
	if response.Run == nil {
		return CheckRun{}, err
	}
	if err != nil || req.CaseID == "" {
		return *response.Run, err
	}
	status, err := s.ActionLinkStatus(ctx, operationID, actor, req)
	if err != nil {
		return *response.Run, err
	}
	if status != "linked" {
		return *response.Run, fmt.Errorf("%w: completed run %s; Case link %s", ErrOutcomeUncertain, response.Run.ID, status)
	}
	return *response.Run, nil
}
