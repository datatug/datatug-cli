// Package detective adapts Core's shared Checks and Cases to local storage.
package detective

import core "github.com/datatug/datatug-core/pkg/detective"

// The wire contract is owned by Core so terminal and hosted callers share it.
type Value = core.Value
type InputValue = core.InputValue
type ValueRange = core.ValueRange
type Input = core.Input
type Binding = core.Binding
type Validator = core.Validator
type PublishedValidator = core.PublishedValidator
type QueryValidators = core.QueryValidators
type Step = core.Step
type Check = core.Check
type EvidenceRef = core.EvidenceRef
type Finding = core.Finding
type CaseEvent = core.CaseEvent
type Case = core.Case
type CaseEventsPage = core.CaseEventsPage
type ScenarioStepReceipt = core.ScenarioStepReceipt
type ScenarioQueryProof = core.ScenarioQueryProof
type ScenarioApplication = core.ScenarioApplication
type ScenarioStep = core.ScenarioStep
type Scenario = core.Scenario
type ValidationResult = core.ValidationResult
type StepResult = core.StepResult
type CheckRun = core.CheckRun
type Capabilities = core.Capabilities
type State = core.State
type ActionRequest = core.ActionRequest
type ActionResponse = core.ActionResponse
