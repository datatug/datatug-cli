package detectiveui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func (m *Model) caseEventsService() (CaseEventsService, error) {
	service, ok := m.service.(CaseEventsService)
	if !ok {
		return nil, fmt.Errorf("this agent does not expose Case event paging")
	}
	return service, nil
}
func (m *Model) incidentService() (IncidentService, error) {
	service, ok := m.service.(IncidentService)
	if !ok {
		return nil, fmt.Errorf("this agent does not expose Incidentius Case links")
	}
	return service, nil
}
func (m *Model) knowledgeService() (KnowledgeService, error) {
	service, ok := m.service.(KnowledgeService)
	if !ok {
		return nil, fmt.Errorf("this agent does not expose private knowledge proposals")
	}
	return service, nil
}
func (m *Model) queryExecutionService() (QueryExecutionService, error) {
	service, ok := m.service.(QueryExecutionService)
	if !ok {
		return nil, fmt.Errorf("this agent does not expose saved-query execution receipts")
	}
	return service, nil
}
func (m *Model) openTimeline() (tea.Model, tea.Cmd) {
	service, err := m.caseEventsService()
	if err != nil {
		m.feedback = err.Error()
		return m, nil
	}
	m.screen = timeline
	m.index = 0
	m.offset = 0
	m.busy = true
	return m, func() tea.Msg {
		page, e := service.CaseEvents(context.Background(), m.id, 0)
		return eventsMsg{page: page, err: e}
	}
}
func (m *Model) openIncidents() (tea.Model, tea.Cmd) {
	service, err := m.incidentService()
	if err != nil {
		m.feedback = err.Error()
		return m, nil
	}
	m.screen = incidentList
	m.index = 0
	m.offset = 0
	m.busy = true
	return m, func() tea.Msg {
		links, e := service.LinkedIncidents(context.Background(), m.id)
		return incidentsMsg{links: links, err: e}
	}
}
func (m *Model) loadKnowledge(older bool) tea.Cmd {
	service, err := m.knowledgeService()
	if err != nil {
		m.feedback = err.Error()
		return nil
	}
	m.busy = true
	cursor := ""
	if older {
		cursor = m.knowledge.NextCursor
	}
	return func() tea.Msg {
		page, e := service.KnowledgeProposals(context.Background(), cursor)
		return knowledgeMsg{page: page, err: e, older: older}
	}
}
func (m *Model) openKnowledge() (tea.Model, tea.Cmd) {
	if _, err := m.knowledgeService(); err != nil {
		m.feedback = err.Error()
		return m, nil
	}
	m.screen = knowledgeList
	m.index = 0
	m.offset = 0
	return m, m.loadKnowledge(false)
}
func (m *Model) updateHostedKey(key string) (tea.Model, tea.Cmd) {
	if m.screen == aiList || m.screen == aiDetail {
		return m.updateAIKey(key)
	}
	switch m.screen {
	case timeline:
		if key == "l" && m.eventsMore {
			service, err := m.caseEventsService()
			if err != nil {
				m.feedback = err.Error()
				return m, nil
			}
			before := m.eventsBefore
			m.busy = true
			return m, func() tea.Msg {
				page, e := service.CaseEvents(context.Background(), m.id, before)
				return eventsMsg{page: page, err: e, older: true}
			}
		}
	case incidentList:
		switch key {
		case "c":
			m.begin("incident-create", "Create Incidentius incident", []field{f("Environment", m.defaultEnvironment(), "admitted logical environment"), f("Title", "", "incident title"), f("Description", "", "")})
			return m, nil
		case "l":
			m.begin("incident-link", "Link existing Incidentius incident", []field{f("Environment", m.defaultEnvironment(), "admitted logical environment"), f("Incident ref", "", "qualified store/incident ID")})
			return m, nil
		case "s":
			if len(m.rows) == 0 {
				return m, nil
			}
			ref := m.rows[m.index].id
			m.begin("incident-start-case", "Start independent Case from incident", []field{f("Environment", m.defaultEnvironment(), "incident environment"), f("Incident ref", ref, "verified server-side"), f("Case ID", "", "stable slug"), f("Title", "", "investigation title")})
			return m, nil
		}
	case knowledgeList:
		switch key {
		case "n":
			m.begin("knowledge-draft", "Draft private knowledge proposal", []field{f("ID", "", "stable slug"), f("Kind", "invariant", "field-meaning, relationship, invariant, query or scenario"), f("Title", "", ""), f("Statement", "", "candidate knowledge"), f("Claim status", "hypothesis", "hypothesis or confirmed"), f("Evidence refs", "", "comma-separated Case evidence IDs"), f("Finding refs", "", "comma-separated Case finding IDs")})
			return m, nil
		case "enter":
			if len(m.rows) > 0 {
				m.proposalID = m.rows[m.index].id
				m.screen = knowledgeDetail
				m.index = 0
				m.rebuild()
			}
			return m, nil
		case "l":
			if m.knowledge.HasMore {
				return m, m.loadKnowledge(true)
			}
		}
	case knowledgeDetail:
		proposal := m.currentProposal()
		if proposal == nil {
			return m, nil
		}
		switch key {
		case "r":
			m.begin("knowledge-review", "Review knowledge proposal", []field{f("Review note", "", "required reviewer note")})
			m.form.proposal = *proposal
			return m, nil
		case "x":
			m.begin("knowledge-reject", "Reject knowledge proposal", []field{f("Review note", "", "required reason")})
			m.form.proposal = *proposal
			return m, nil
		case "e":
			m.begin("knowledge-export", "Prepare private target handoff", []field{f("System", "meaninggraph", "meaninggraph or modelspec"), f("Repository", "", "owner/repository"), f("Resource ID", "", "target resource identifier"), f("Expected revision", "", "target revision for reviewer")})
			m.form.proposal = *proposal
			return m, nil
		}
	}
	return m, nil
}
func (m *Model) currentProposal() *KnowledgeProposal {
	for i := range m.knowledge.Proposals {
		if m.knowledge.Proposals[i].ID == m.proposalID {
			return &m.knowledge.Proposals[i]
		}
	}
	return nil
}
func (m *Model) defaultEnvironment() string {
	if len(m.state.Environments) == 1 {
		return m.state.Environments[0]
	}
	return ""
}
func (m *Model) validateEnvironment(value string) error {
	if value == "" {
		return fmt.Errorf("select a logical environment")
	}
	if len(m.state.Environments) > 0 {
		for _, allowed := range m.state.Environments {
			if allowed == value {
				return nil
			}
		}
		return fmt.Errorf("environment %q is not admitted for this project", value)
	}
	return nil
}
func (m *Model) startScenarioStep(c detective.Case, rowID string) (tea.Model, tea.Cmd) {
	appID, stepID, ok := strings.Cut(rowID, "/")
	if !ok {
		return m, nil
	}
	for _, app := range c.ScenarioApplications {
		if app.ID != appID {
			continue
		}
		for _, receipt := range app.Steps {
			if receipt.StepID != stepID {
				continue
			}
			if receipt.Status == "completed" {
				m.feedback = "Step already completed"
				return m, nil
			}
		}
		for _, step := range app.Snapshot.Steps {
			if step.ID != stepID {
				continue
			}
			if step.Kind == "check" {
				m.pendingApplicationID, m.pendingStepID = appID, stepID
				model, cmd := m.startRun(step.Ref, c.ID)
				if m.form == nil || m.form.kind != "run" {
					m.pendingApplicationID, m.pendingStepID = "", ""
				}
				return model, cmd
			}
			if _, err := m.queryExecutionService(); err != nil {
				m.feedback = err.Error()
				return m, nil
			}
			inputs, err := scenarioQueryInputs(c, app.Snapshot.Inputs, step)
			if err != nil {
				m.feedback = err.Error()
				return m, nil
			}
			m.pendingApplicationID, m.pendingStepID = appID, stepID
			m.begin("scenario-query-run", "Run saved query for Scenario step", []field{f("Environment", m.defaultEnvironment(), "admitted logical environment"), f("Query ID", step.Ref, "saved query from Scenario"), f("Inputs", inputs, "query_parameter=type:cardinality:value; quote strings with punctuation or edge spaces")})
			return m, nil
		}
	}
	m.feedback = "Scenario application step is unavailable"
	return m, nil
}
func scenarioQueryInputs(c detective.Case, schema []detective.Input, step detective.ScenarioStep) (string, error) {
	if err := detective.ValidateScenario(detective.Scenario{ID: "binding-preview", Title: "Binding preview", Inputs: schema, Steps: []detective.ScenarioStep{step}}); err != nil {
		return "", fmt.Errorf("Scenario query bindings: %w", err)
	}
	var entries []string
	boundSources := make(map[string]bool, len(step.Bindings))
	seenTargets := make(map[string]bool, len(step.Bindings))
	appendValue := func(name string, value detective.InputValue) error {
		if err := value.Validate(); err != nil || value.Range != nil {
			return fmt.Errorf("Scenario query input %q has an invalid bound value", name)
		}
		kind, cardinality := "", "scalar"
		if value.Scalar != nil {
			kind = string(value.Scalar.Type)
		} else if value.Set != nil && len(value.Set.Values) > 0 {
			kind = string(value.Set.Values[0].Type)
			cardinality = "set"
		}
		if kind == "" {
			return fmt.Errorf("Scenario query input %q is empty", name)
		}
		entries = append(entries, name+"="+kind+":"+cardinality+":"+formatScenarioQueryValue(value))
		return nil
	}
	for _, binding := range step.Bindings {
		if binding.Input == "" || binding.FromInput == "" || binding.FromColumn != "" || seenTargets[binding.Input] {
			return "", fmt.Errorf("Scenario query has invalid input bindings")
		}
		seenTargets[binding.Input] = true
		boundSources[binding.FromInput] = true
		value, ok := c.Context[binding.FromInput]
		if !ok {
			continue
		}
		if value.Range != nil {
			var endpoint detective.Value
			switch binding.Part {
			case "start":
				endpoint = value.Range.Start
			case "end":
				endpoint = value.Range.End
			default:
				return "", fmt.Errorf("Scenario range binding %q needs start or end", binding.Input)
			}
			value = detective.InputValue{Scalar: &endpoint}
		} else if binding.Part != "" {
			return "", fmt.Errorf("Scenario binding %q selects an endpoint of a non-range input", binding.Input)
		}
		if err := appendValue(binding.Input, value); err != nil {
			return "", err
		}
	}
	for _, input := range schema {
		if boundSources[input.Name] || seenTargets[input.Name] {
			continue
		}
		value, ok := c.Context[input.Name]
		if !ok {
			continue
		}
		if value.Range != nil {
			return "", fmt.Errorf("Scenario range input %q needs start and end query bindings", input.Name)
		}
		if err := appendValue(input.Name, value); err != nil {
			return "", err
		}
	}
	return strings.Join(entries, "; "), nil
}
func formatScenarioQueryValue(value detective.InputValue) string {
	if value.Scalar != nil && value.Scalar.Type == investigation.ValueTypeString {
		return strconv.Quote(value.Scalar.Str)
	}
	if value.Set != nil && len(value.Set.Values) > 0 && value.Set.Values[0].Type == investigation.ValueTypeString {
		items := make([]string, 0, len(value.Set.Values))
		for _, member := range value.Set.Values {
			items = append(items, strconv.Quote(member.Str))
		}
		return strings.Join(items, ", ")
	}
	return formatInputValue(value)
}
func (m *Model) caseContextAsInputs(c detective.Case, schema []detective.Input) string {
	var entries []string
	for _, input := range schema {
		name := input.Name
		value, ok := c.Context[name]
		if !ok {
			continue
		}
		if value.Range != nil {
			continue
		}
		kind, cardinality := "", "scalar"
		if value.Scalar != nil {
			kind = string(value.Scalar.Type)
		} else if value.Set != nil && len(value.Set.Values) > 0 {
			kind = string(value.Set.Values[0].Type)
			cardinality = "set"
		}
		if kind != "" {
			entries = append(entries, name+"="+kind+":"+cardinality+":"+formatInputValue(value))
		}
	}
	return strings.Join(entries, "; ")
}
func formatInputValue(value detective.InputValue) string {
	if value.Scalar != nil {
		return scalarText(*value.Scalar)
	}
	if value.Set != nil {
		items := make([]string, 0, len(value.Set.Values))
		for _, v := range value.Set.Values {
			items = append(items, scalarText(v))
		}
		return strings.Join(items, ", ")
	}
	if value.Range != nil {
		return scalarText(value.Range.Start) + ".." + scalarText(value.Range.End)
	}
	return ""
}
func scalarText(v detective.Value) string {
	switch string(v.Type) {
	case "boolean":
		return strconv.FormatBool(v.Bool)
	case "number":
		return strconv.FormatFloat(v.Num, 'g', -1, 64)
	default:
		return v.Str
	}
}

func (m *Model) submitHostedForm() (tea.Cmd, bool, error) {
	if m.form == nil {
		return nil, false, nil
	}
	if cmd, handled, err := m.submitAIForm(); handled {
		return cmd, handled, err
	}
	switch m.form.kind {
	case "incident-create", "incident-link", "incident-start-case":
		service, err := m.incidentService()
		if err != nil {
			return nil, true, err
		}
		env := m.field("Environment")
		if e := m.validateEnvironment(env); e != nil {
			return nil, true, e
		}
		a := IncidentAction{Environment: env}
		switch m.form.kind {
		case "incident-create":
			a.Type = "create"
			a.CaseID = m.id
			a.Title = m.field("Title")
			a.Description = m.field("Description")
			if c := m.currentCase(); c != nil {
				a.IfMatch = c.Version
			}
			if a.Title == "" {
				return nil, true, fmt.Errorf("incident title required")
			}
		case "incident-link":
			a.Type = "link"
			a.CaseID = m.id
			a.IncidentRef = m.field("Incident ref")
			if c := m.currentCase(); c != nil {
				a.IfMatch = c.Version
			}
			if a.IncidentRef == "" {
				return nil, true, fmt.Errorf("incident reference required")
			}
		case "incident-start-case":
			a.Type = "start-case"
			a.IncidentRef = m.field("Incident ref")
			a.Case = &detective.Case{ID: m.field("Case ID"), Title: m.field("Title"), Status: "active"}
			if a.IncidentRef == "" || a.Case.ID == "" || a.Case.Title == "" {
				return nil, true, fmt.Errorf("incident reference, Case ID and title required")
			}
		}
		m.busy = true
		return func() tea.Msg {
			result, e := service.IncidentAction(context.Background(), a)
			return incidentActedMsg{result, e}
		}, true, nil
	case "knowledge-draft", "knowledge-review", "knowledge-reject", "knowledge-export":
		service, err := m.knowledgeService()
		if err != nil {
			return nil, true, err
		}
		a := KnowledgeAction{}
		if m.form.kind == "knowledge-draft" {
			a.Type = "save-draft"
			a.Proposal = &KnowledgeProposal{ID: m.field("ID"), CaseID: m.id, Kind: m.field("Kind"), Title: m.field("Title"), Statement: m.field("Statement"), ClaimStatus: m.field("Claim status"), EvidenceRefs: splitCSV(m.field("Evidence refs")), FindingRefs: splitCSV(m.field("Finding refs"))}
			if a.Proposal.ID == "" || a.Proposal.Title == "" || a.Proposal.Statement == "" || len(a.Proposal.EvidenceRefs) == 0 {
				return nil, true, fmt.Errorf("proposal ID, title, statement and Case evidence references required")
			}
		}
		if m.form.kind == "knowledge-review" || m.form.kind == "knowledge-reject" {
			a.Type = "review"
			if m.form.kind == "knowledge-reject" {
				a.Type = "reject"
			}
			a.ProposalID = m.form.proposal.ID
			a.IfMatch = m.form.proposal.Version
			a.ReviewNote = m.field("Review note")
			if a.ReviewNote == "" {
				return nil, true, fmt.Errorf("review note required")
			}
		}
		if m.form.kind == "knowledge-export" {
			a.Type = "export"
			a.ProposalID = m.form.proposal.ID
			a.IfMatch = m.form.proposal.Version
			a.Target = &KnowledgeTarget{System: m.field("System"), Repository: m.field("Repository"), ResourceID: m.field("Resource ID"), ExpectedRevision: m.field("Expected revision")}
			if a.Target.System != "meaninggraph" && a.Target.System != "modelspec" || a.Target.Repository == "" || a.Target.ResourceID == "" || a.Target.ExpectedRevision == "" {
				return nil, true, fmt.Errorf("target system, repository, resource ID and expected revision required")
			}
		}
		m.busy = true
		return func() tea.Msg {
			result, e := service.KnowledgeAction(context.Background(), a)
			return knowledgeActedMsg{result, e}
		}, true, nil
	case "query-validators-exact", "scenario-query-run":
		service, err := m.queryExecutionService()
		if err != nil {
			return nil, true, err
		}
		env := m.field("Environment")
		if e := m.validateEnvironment(env); e != nil {
			return nil, true, e
		}
		query := m.field("Query ID")
		if query == "" {
			return nil, true, fmt.Errorf("saved query ID required")
		}
		values, e := parseNamedValues(m.field("Inputs"))
		if e != nil {
			return nil, true, e
		}
		inputs := map[string]apicontract.TypedValueOrSet{}
		for name, value := range values {
			if value.Range != nil {
				return nil, true, fmt.Errorf("range input %q requires explicit start/end query bindings", name)
			}
			inputs[name] = apicontract.TypedValueOrSet{Scalar: value.Scalar, Set: value.Set}
		}
		validators := m.form.kind == "query-validators-exact"
		app, step := m.pendingApplicationID, m.pendingStepID
		m.busy = true
		return func() tea.Msg {
			result, e := service.RunSavedQuery(context.Background(), SavedQueryRun{QueryID: query, Environment: env, Inputs: inputs})
			return queryRunMsg{result: result, err: e, queryID: query, environment: env, validators: validators, applicationID: app, stepID: step}
		}, true, nil
	}
	return nil, false, nil
}
