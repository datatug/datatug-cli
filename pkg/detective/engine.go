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
	catalog, err := s.loadCatalog(ctx, checkID, "", caseID)
	if err != nil {
		return CheckRun{}, err
	}
	var lookup ExecutionMetadata
	if len(metadata) > 0 {
		lookup = metadata[0]
	}
	run, err := core.EvaluateCheck(ctx, core.RunRequest{ID: "run-" + strings.ReplaceAll(uuid.NewString(), "-", ""), CheckID: checkID, CaseID: caseID, Environment: environment, Actor: actor, Inputs: inputs}, catalog, runner, lookup)
	if err != nil {
		return CheckRun{}, err
	}
	if err := s.SaveRun(ctx, run); err != nil {
		return CheckRun{}, err
	}
	if caseID != "" {
		if _, err = s.appendServerCaseRun(ctx, caseID, run.ID, actor); err != nil {
			return run, fmt.Errorf("%w: case link was not saved", ErrOutcomeUncertain)
		}
	}
	return run, nil
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
	catalog, err := s.loadCatalog(ctx, "", queryID, caseID)
	if err != nil {
		return CheckRun{}, err
	}
	var lookup ExecutionMetadata
	if len(metadata) > 0 {
		lookup = metadata[0]
	}
	req := core.RunRequest{ID: "run-" + strings.ReplaceAll(uuid.NewString(), "-", ""), QueryID: queryID, CaseID: caseID, Environment: environment, Actor: actor, Inputs: inputs}
	var run CheckRun
	if result == nil {
		run, err = core.EvaluateQueryValidators(ctx, req, catalog, runner, lookup)
	} else {
		run, err = core.EvaluateQueryValidatorsForResult(ctx, req, catalog, *result, runner, lookup)
	}
	if err != nil {
		return CheckRun{}, err
	}
	if err := s.SaveRun(ctx, run); err != nil {
		return CheckRun{}, err
	}
	if caseID != "" {
		if _, err = s.appendServerCaseRun(ctx, caseID, run.ID, actor); err != nil {
			return run, fmt.Errorf("%w: case link was not saved", ErrOutcomeUncertain)
		}
	}
	return run, nil
}
