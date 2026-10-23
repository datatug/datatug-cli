package detectiveui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

func (m *Model) aiService() (AIService, error) {
	service, ok := m.service.(AIService)
	if !ok {
		return nil, fmt.Errorf("this project does not expose Detective AI proposals")
	}
	return service, nil
}
func (m *Model) loadAI(older bool) tea.Cmd {
	service, err := m.aiService()
	if err != nil {
		m.feedback = err.Error()
		return nil
	}
	cursor := ""
	if older {
		cursor = m.aiPage.NextCursor
	}
	m.busy = true
	return func() tea.Msg {
		page, e := service.AIProposals(context.Background(), cursor)
		return aiPageMsg{page, e, older}
	}
}
func (m *Model) openAI() (tea.Model, tea.Cmd) {
	if _, err := m.aiService(); err != nil {
		m.feedback = err.Error()
		return m, nil
	}
	m.screen, m.index, m.offset, m.filter = aiList, 0, 0, ""
	return m, m.loadAI(false)
}
func (m *Model) defaultAICaseID() string {
	for _, c := range m.state.Cases {
		if c.ID == m.id {
			return c.ID
		}
	}
	if len(m.state.Cases) == 1 {
		return m.state.Cases[0].ID
	}
	return ""
}
func (m *Model) currentAIProposal() *ai.Proposal {
	for i := range m.aiPage.Proposals {
		if m.aiPage.Proposals[i].ID == m.aiProposalID {
			return &m.aiPage.Proposals[i]
		}
	}
	return nil
}
func (m *Model) rebuildAI() {
	p := m.currentAIProposal()
	if p == nil {
		return
	}
	m.rows = append(m.rows, row{"Proposal " + p.ID + " · " + p.Draft.Kind + " · " + p.Draft.Title, "header", p.ID, 0})
	m.rows = append(m.rows, row{"Scope " + p.Scope.Project + " · expires " + p.ExpiresAt.Format("2006-01-02 15:04"), "header", p.ID, 0})
	for _, ambiguity := range p.Draft.Ambiguities {
		m.rows = append(m.rows, row{"Uncertainty: " + ambiguity, "ambiguity", "", 0})
	}
	contextKeys := make([]string, 0, len(p.Draft.Context))
	for name := range p.Draft.Context {
		contextKeys = append(contextKeys, name)
	}
	sort.Strings(contextKeys)
	for _, name := range contextKeys {
		value := p.Draft.Context[name]
		m.rows = append(m.rows, row{"Context " + name + " = " + describeValue(value.Value), "context", name, 0})
		for _, citation := range value.Provenance {
			m.rows = append(m.rows, aiCitation(citation, 1))
		}
	}
	for _, citation := range p.Draft.Sources {
		m.rows = append(m.rows, aiCitation(citation, 0))
	}
	switch p.Draft.Kind {
	case ai.KindQuery:
		if q := p.Draft.Query; q != nil {
			m.rows = append(m.rows, row{"Query " + q.ID + " · " + q.Title + " · " + q.Type, "draft", q.ID, 0}, row{"Text " + q.Text, "draft", q.ID, 1})
		}
	case ai.KindCheck:
		if x := p.Draft.Check; x != nil {
			m.rows = append(m.rows, row{fmt.Sprintf("Check %s · %s · %d steps", x.ID, x.Title, len(x.Steps)), "draft", x.ID, 0})
			for i, step := range x.Steps {
				ref := step.QueryID
				if step.Kind == "check" {
					ref = step.CheckID
				}
				m.rows = append(m.rows, row{fmt.Sprintf("%d. %s · %s → %s · %s", i+1, step.ID, step.Kind, ref, formatBindings(step.Bindings)), "ai-check-step", step.ID, 1})
				for _, v := range step.Validators {
					m.rows = append(m.rows, row{"Validator " + v.ID + " · " + v.Kind + " · " + v.Field, "ai-check-validator", step.ID + "/" + v.ID, 2})
				}
			}
		}
	case ai.KindValidator:
		if x := p.Draft.Validator; x != nil {
			m.rows = append(m.rows, row{"Validator " + x.ID + " · " + x.Title + " · " + x.Validator.Kind, "draft", x.ID, 0})
		}
	case ai.KindScenario:
		if x := p.Draft.Scenario; x != nil {
			m.rows = append(m.rows, row{fmt.Sprintf("Scenario %s · %s · %d steps", x.ID, x.Title, len(x.Steps)), "draft", x.ID, 0})
			for i, step := range x.Steps {
				m.rows = append(m.rows, row{fmt.Sprintf("%d. %s · %s → %s", i+1, step.ID, step.Kind, step.Ref), "ai-scenario-step", step.ID, 1})
			}
		}
	case ai.KindCase:
		if x := p.Draft.Case; x != nil {
			m.rows = append(m.rows, row{fmt.Sprintf("Case %s · expected v%d · %s", x.CaseID, x.ExpectedVersion, x.Title), "draft", x.CaseID, 0})
			m.rows = append(m.rows, row{"Status " + defaultString(x.Status, "unchanged") + " · closure reason " + defaultString(x.ClosureReason, "unchanged"), "draft", x.CaseID, 1})
			keys := make([]string, 0, len(x.Context))
			for name := range x.Context {
				keys = append(keys, name)
			}
			sort.Strings(keys)
			for _, name := range keys {
				m.rows = append(m.rows, row{"Context " + name + " = " + describeValue(x.Context[name]), "draft", name, 1})
			}
			if x.Findings != nil {
				m.rows = append(m.rows, row{fmt.Sprintf("Replace findings with %d reviewed items", len(x.Findings)), "draft", x.CaseID, 1})
			}
			for _, finding := range x.Findings {
				m.rows = append(m.rows, row{"Finding " + finding.ID + " · " + finding.Status + " · " + finding.Text + " · evidence " + strings.Join(finding.EvidenceRefs, ", "), "draft", finding.ID, 2})
			}
		}
	case ai.KindRun:
		if x := p.Draft.Run; x != nil {
			m.rows = append(m.rows, row{"Run Check " + x.CheckID + " · " + x.Environment + " · Case " + x.CaseID, "draft", x.CheckID, 0})
			keys := make([]string, 0, len(x.Inputs))
			for name := range x.Inputs {
				keys = append(keys, name)
			}
			sort.Strings(keys)
			for _, name := range keys {
				m.rows = append(m.rows, row{"Input " + name + " = " + describeValue(x.Inputs[name]), "draft", name, 1})
			}
		}
	case ai.KindExplain:
		if x := p.Draft.Explanation; x != nil {
			m.rows = append(m.rows, row{"Explanation " + x.Summary, "draft", x.RunID, 0}, row{"Basis " + x.Basis, "draft", x.RunID, 1})
		}
	}
}
func aiCitation(c ai.Provenance, depth int) row {
	uncertainty := ""
	if c.Ambiguous {
		uncertainty = " · ambiguous"
	}
	return row{fmt.Sprintf("Citation %s [%d:%d] confidence %.2f%s · %s", c.SourceID, c.Start, c.End, c.Confidence, uncertainty, c.Quote), "citation", c.SourceID, depth}
}
func aiValueShape(v detective.InputValue) (string, string) {
	if v.Scalar != nil {
		return string(v.Scalar.Type), "scalar"
	}
	if v.Set != nil && len(v.Set.Values) > 0 {
		return string(v.Set.Values[0].Type), "set"
	}
	if v.Range != nil {
		return string(v.Range.Start.Type), "range"
	}
	return "string", "scalar"
}

func aiCaseEditFields(x ai.CaseEdit) []field {
	fields := []field{
		f("Case title", x.Title, "empty keeps the current title"),
		f("Status", x.Status, "empty keeps current status; active, closed or archived"),
		f("Closure reason", x.ClosureReason, "required when closing a Case"),
	}
	keys := make([]string, 0, len(x.Context))
	for key := range x.Context {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		value := x.Context[key]
		typeName, cardinality := aiValueShape(value)
		prefix := "Context " + strconv.Itoa(i+1) + " "
		fields = append(fields, f(prefix+"name", key, "empty omits this proposed context value"), f(prefix+"type", typeName, "string, integer, decimal, boolean, date, datetime"), f(prefix+"cardinality", cardinality, "scalar, set, range"), f(prefix+"value", formatInputValue(value), "review the exact typed cohort"))
	}
	findingsAction := "keep"
	if x.Findings != nil {
		findingsAction = "replace"
	}
	fields = append(fields, f("Findings action", findingsAction, "keep or replace; replace with no items clears findings"))
	for i, finding := range x.Findings {
		prefix := "Finding " + strconv.Itoa(i+1) + " "
		fields = append(fields, f(prefix+"ID", finding.ID, "empty omits this proposed finding"), f(prefix+"status", finding.Status, "hypothesis or confirmed"), f(prefix+"text", finding.Text, "reviewed finding"), f(prefix+"evidence refs", strings.Join(finding.EvidenceRefs, ", "), "authorized evidence IDs, comma separated"))
	}
	return fields
}

func aiConfirmationFields(p ai.Proposal) []field {
	fields := []field{{label: "Proposal", value: p.ID + " · " + p.Draft.Kind, readOnly: true}}
	switch p.Draft.Kind {
	case ai.KindCase:
		if x := p.Draft.Case; x != nil {
			fields = append(fields, field{label: "Case", value: fmt.Sprintf("%s · expected v%d", x.CaseID, x.ExpectedVersion), readOnly: true})
			for _, item := range aiCaseEditFields(*x) {
				item.readOnly = true
				fields = append(fields, item)
			}
		}
	case ai.KindRun:
		if x := p.Draft.Run; x != nil {
			fields = append(fields, field{label: "Check", value: x.CheckID, readOnly: true}, field{label: "Case", value: x.CaseID, readOnly: true}, field{label: "Environment", value: x.Environment, readOnly: true})
			keys := make([]string, 0, len(x.Inputs))
			for key := range x.Inputs {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fields = append(fields, field{label: "Input " + key, value: describeValue(x.Inputs[key]), readOnly: true})
			}
		}
	default:
		fields = append(fields, field{label: "Reviewed title", value: p.Draft.Title, readOnly: true})
	}
	return append(fields, f("Confirmation", "", "type CONFIRM to authorize this exact reviewed proposal"))
}
func (m *Model) updateAIKey(key string) (tea.Model, tea.Cmd) {
	if m.screen == aiList {
		switch key {
		case "n":
			m.begin("ai-generate", "Ask Detective AI for private proposals", []field{fm("Prompt", "", "describe the evidence and desired proposal; no action runs yet"), f("Case ID", m.defaultAICaseID(), "optional existing Case"), f("Expected counts", "", "optional name=count pairs, comma separated")})
			return m, nil
		case "l":
			if m.aiPage.HasMore {
				return m, m.loadAI(true)
			}
		case "enter":
			if len(m.rows) > 0 {
				m.aiProposalID = m.rows[m.index].id
				m.screen = aiDetail
				m.index = 0
				m.rebuild()
			}
			return m, nil
		}
		return m, nil
	}
	p := m.currentAIProposal()
	if p == nil {
		return m, nil
	}
	switch key {
	case "r":
		if p.Draft.Kind == ai.KindContext {
			keys := make([]string, 0, len(p.Draft.Context))
			for key := range p.Draft.Context {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			fields := []field{f("Case ID", m.defaultAICaseID(), "existing Case; all reviewed values commit together")}
			for i, key := range keys {
				value := p.Draft.Context[key].Value
				typeName, cardinality := aiValueShape(value)
				prefix := "Context " + strconv.Itoa(i+1) + " "
				fields = append(fields, f(prefix+"name", key, "editable context parameter name"), f(prefix+"type", typeName, "string, integer, decimal, boolean, date, datetime"), f(prefix+"cardinality", cardinality, "scalar, set, range"), f(prefix+"value", formatInputValue(value), "review extracted value; set comma separated, range start..end"))
			}
			m.begin("ai-context-review", "Review all extracted typed context", fields)
			m.form.aiProposal = *p
			return m, nil
		}
		if p.Draft.Kind == ai.KindExplain {
			m.feedback = "Failure explanations are review evidence; they cannot mutate project resources"
			return m, nil
		}
		fields := []field{f("Proposal title", p.Draft.Title, "reviewed proposal title")}
		switch p.Draft.Kind {
		case ai.KindQuery:
			if x := p.Draft.Query; x != nil {
				fields = append(fields, f("Query ID", x.ID, "saved query ID"), f("Query title", x.Title, ""), f("Query type", x.Type, ""), fm("Query text", x.Text, "editable SQL or TugQL"), f("Connection ID", x.ConnectionID, "admitted source"), f("Inputs", formatInputs(x.Inputs), "scalar or set query parameters"), f("Expected revision", x.ExpectedRevision, "empty creates; existing revision edits"))
			}
		case ai.KindCheck:
			if x := p.Draft.Check; x != nil {
				fields = append(fields, f("Check title", x.Title, ""), f("Description", x.Description, ""), f("Inputs", formatInputs(x.Inputs), "name:type:cardinality[:required], comma separated"))
			}
		case ai.KindValidator:
			if x := p.Draft.Validator; x != nil {
				fields = append(fields, validatorFields(x.Validator, x.ID)...)
			}
		case ai.KindScenario:
			if x := p.Draft.Scenario; x != nil {
				fields = append(fields, f("Scenario title", x.Title, ""), f("Description", x.Description, ""), f("Inputs", formatInputs(x.Inputs), "name:type:cardinality[:required], comma separated"))
			}
		case ai.KindCase:
			if x := p.Draft.Case; x != nil {
				fields = append(fields, aiCaseEditFields(*x)...)
			}
		case ai.KindRun:
			if x := p.Draft.Run; x != nil {
				fields = append(fields, f("Environment", x.Environment, "admitted logical environment"), f("Check ID", x.CheckID, "saved Check"), f("Case ID", x.CaseID, "optional Case; confirmed cohort applies"))
				if check := m.checkByID(x.CheckID); check != nil {
					for _, input := range check.Inputs {
						fields = append(fields, f("Input "+input.Name, formatInputValue(x.Inputs[input.Name]), string(input.Type)+"/"+input.Cardinality))
					}
				}
			}
		}
		m.begin("ai-draft-review", "Edit reviewed AI proposal", fields)
		m.form.aiProposal = *p
		return m, nil
	case "e":
		if len(m.rows) == 0 {
			return m, nil
		}
		selected := m.rows[m.index]
		if p.Draft.Kind == ai.KindCheck && p.Draft.Check != nil {
			for i, step := range p.Draft.Check.Steps {
				if selected.kind == "ai-check-step" && selected.id == step.ID {
					m.begin("ai-step-edit", "Edit reviewed Check step", stepFields(step))
					m.form.aiProposal = *p
					m.form.stepIndex = i
					return m, nil
				}
				for j, v := range step.Validators {
					if selected.kind == "ai-check-validator" && selected.id == step.ID+"/"+v.ID {
						m.begin("ai-validator-edit", "Edit reviewed local Validator", validatorFields(v, ""))
						m.form.aiProposal = *p
						m.form.stepIndex = i
						m.form.validatorIndex = j
						return m, nil
					}
				}
			}
		}
		if p.Draft.Kind == ai.KindScenario && p.Draft.Scenario != nil && selected.kind == "ai-scenario-step" {
			for i, step := range p.Draft.Scenario.Steps {
				if step.ID == selected.id {
					m.begin("ai-scenario-step-edit", "Edit reviewed Scenario step", scenarioStepFields(step))
					m.form.aiProposal = *p
					m.form.stepIndex = i
					return m, nil
				}
			}
		}
	case "a":
		if p.Draft.Kind == ai.KindCheck && p.Draft.Check != nil {
			m.begin("ai-step-add", "Add reviewed Check step", stepFields(detective.Step{}))
			m.form.aiProposal = *p
			return m, nil
		}
		if p.Draft.Kind == ai.KindScenario && p.Draft.Scenario != nil {
			m.begin("ai-scenario-step-add", "Add reviewed Scenario step", scenarioStepFields(detective.ScenarioStep{}))
			m.form.aiProposal = *p
			return m, nil
		}
	case "v":
		if p.Draft.Kind == ai.KindCheck && p.Draft.Check != nil && len(m.rows) > 0 && m.rows[m.index].kind == "ai-check-step" {
			for i, step := range p.Draft.Check.Steps {
				if step.ID == m.rows[m.index].id && step.Kind == "query" {
					m.begin("ai-validator-add", "Add reviewed local Validator", validatorFields(detective.Validator{}, ""))
					m.form.aiProposal = *p
					m.form.stepIndex = i
					return m, nil
				}
			}
		}
	case "c":
		if p.Draft.Kind == ai.KindContext || p.Draft.Kind == ai.KindExplain {
			m.feedback = "Review this proposal into a concrete action before confirmation"
			return m, nil
		}
		if p.Draft.Kind == ai.KindCase && p.ParentProposalID == "" {
			m.feedback = "Review the Case title, context, findings and lifecycle before confirmation"
			return m, nil
		}
		m.begin("ai-confirm", "Confirm reviewed AI action", aiConfirmationFields(*p))
		m.form.aiProposal = *p
		return m, nil
	}
	return m, nil
}
func (m *Model) submitAIForm() (tea.Cmd, bool, error) {
	if m.form == nil || !strings.HasPrefix(m.form.kind, "ai-") {
		return nil, false, nil
	}
	service, err := m.aiService()
	if err != nil {
		return nil, true, err
	}
	switch m.form.kind {
	case "ai-generate":
		prompt := m.field("Prompt")
		if prompt == "" {
			return nil, true, fmt.Errorf("prompt required")
		}
		counts, err := parseExpectedCounts(m.field("Expected counts"))
		if err != nil {
			return nil, true, err
		}
		a := AIGenerate{Text: prompt, CaseID: m.field("Case ID"), ExpectedCounts: counts}
		m.busy = true
		return func() tea.Msg {
			result, e := service.GenerateAI(context.Background(), a)
			return aiGeneratedMsg{result, e}
		}, true, nil
	case "ai-context-review":
		p := m.form.aiProposal
		if p.Draft.Kind != ai.KindContext {
			return nil, true, fmt.Errorf("context proposal required")
		}
		caseID := m.field("Case ID")
		if caseID == "" {
			return nil, true, fmt.Errorf("case ID required")
		}
		values := map[string]detective.InputValue{}
		for i := 0; i < len(p.Draft.Context); i++ {
			prefix := "Context " + strconv.Itoa(i+1) + " "
			name := m.field(prefix + "name")
			if !detective.ValidParameterName(name) {
				return nil, true, fmt.Errorf("context %d requires a valid name", i+1)
			}
			if _, exists := values[name]; exists {
				return nil, true, fmt.Errorf("duplicate context name %q", name)
			}
			value, err := parseValue(m.field(prefix+"type"), m.field(prefix+"cardinality"), m.field(prefix+"value"))
			if err != nil {
				return nil, true, fmt.Errorf("%s: %w", name, err)
			}
			values[name] = value
		}
		version := int64(0)
		for _, c := range m.state.Cases {
			if c.ID == caseID {
				version = c.Version
				break
			}
		}
		if version == 0 {
			return nil, true, fmt.Errorf("case %q is not in the authorized catalog", caseID)
		}
		request := ai.ContextReviewRequest{ProposalID: p.ID, Digest: p.Digest, CaseID: caseID, ExpectedVersion: version, Context: values}
		m.busy = true
		return func() tea.Msg {
			result, e := service.ReviewAI(context.Background(), AIReview{Type: "context", Context: &request})
			return aiReviewedMsg{result, e}
		}, true, nil
	case "ai-draft-review":
		p := m.form.aiProposal
		draft := p.Draft
		draft.Title = m.field("Proposal title")
		switch draft.Kind {
		case ai.KindQuery:
			if draft.Query == nil {
				return nil, true, fmt.Errorf("query draft unavailable")
			}
			q := *draft.Query
			q.ID = m.field("Query ID")
			q.Title = m.field("Query title")
			q.Type = m.field("Query type")
			q.Text = m.field("Query text")
			q.ConnectionID = m.field("Connection ID")
			inputs, err := parseInputs(m.field("Inputs"))
			if err != nil {
				return nil, true, err
			}
			q.Inputs = inputs
			q.ExpectedRevision = m.field("Expected revision")
			draft.Query = &q
		case ai.KindCheck:
			if draft.Check == nil {
				return nil, true, fmt.Errorf("check draft unavailable")
			}
			x := *draft.Check
			x.Title = m.field("Check title")
			x.Description = m.field("Description")
			inputs, err := parseInputs(m.field("Inputs"))
			if err != nil {
				return nil, true, err
			}
			x.Inputs = inputs
			draft.Check = &x
		case ai.KindValidator:
			if draft.Validator == nil {
				return nil, true, fmt.Errorf("validator draft unavailable")
			}
			x := *draft.Validator
			x.ID = m.field("Catalog ID")
			x.Title = m.field("Title")
			validator, err := m.parseValidator()
			if err != nil {
				return nil, true, err
			}
			x.Validator = validator
			draft.Validator = &x
		case ai.KindScenario:
			if draft.Scenario == nil {
				return nil, true, fmt.Errorf("scenario draft unavailable")
			}
			x := *draft.Scenario
			x.Title = m.field("Scenario title")
			x.Description = m.field("Description")
			inputs, err := parseInputs(m.field("Inputs"))
			if err != nil {
				return nil, true, err
			}
			x.Inputs = inputs
			draft.Scenario = &x
		case ai.KindCase:
			if draft.Case == nil {
				return nil, true, fmt.Errorf("case edit unavailable")
			}
			x := *draft.Case
			x.Title = m.field("Case title")
			x.Status = m.field("Status")
			x.ClosureReason = m.field("Closure reason")
			if draft.Case.Context != nil {
				x.Context = make(map[string]detective.InputValue, len(draft.Case.Context))
				for i := 0; i < len(draft.Case.Context); i++ {
					prefix := "Context " + strconv.Itoa(i+1) + " "
					name := m.field(prefix + "name")
					if name == "" {
						continue
					}
					if !detective.ValidParameterName(name) {
						return nil, true, fmt.Errorf("context %d requires a valid name", i+1)
					}
					if _, exists := x.Context[name]; exists {
						return nil, true, fmt.Errorf("duplicate context name %q", name)
					}
					value, err := parseValue(m.field(prefix+"type"), m.field(prefix+"cardinality"), m.field(prefix+"value"))
					if err != nil {
						return nil, true, fmt.Errorf("%s: %w", name, err)
					}
					x.Context[name] = value
				}
			}
			switch m.field("Findings action") {
			case "keep":
				x.Findings = nil
			case "replace":
				x.Findings = make([]detective.Finding, 0, len(draft.Case.Findings))
				for i := 0; i < len(draft.Case.Findings); i++ {
					prefix := "Finding " + strconv.Itoa(i+1) + " "
					id := m.field(prefix + "ID")
					if id == "" {
						continue
					}
					finding := detective.Finding{ID: id, Status: m.field(prefix + "status"), Text: m.field(prefix + "text"), EvidenceRefs: splitCSV(m.field(prefix + "evidence refs"))}
					if !detective.ValidSlug(finding.ID) || finding.Text == "" || !oneOf(finding.Status, "hypothesis", "confirmed") {
						return nil, true, fmt.Errorf("finding %d requires a valid ID, status and text", i+1)
					}
					x.Findings = append(x.Findings, finding)
				}
			default:
				return nil, true, fmt.Errorf("findings action must be keep or replace")
			}
			draft.Case = &x
		case ai.KindRun:
			if draft.Run == nil {
				return nil, true, fmt.Errorf("run draft unavailable")
			}
			x := *draft.Run
			x.Environment = m.field("Environment")
			x.CheckID = m.field("Check ID")
			x.CaseID = m.field("Case ID")
			if err := m.validateEnvironment(x.Environment); err != nil {
				return nil, true, err
			}
			check := m.checkByID(x.CheckID)
			if check == nil {
				return nil, true, fmt.Errorf("check %q is not in the authorized catalog", x.CheckID)
			}
			x.Inputs = map[string]detective.InputValue{}
			for _, input := range check.Inputs {
				raw := m.field("Input " + input.Name)
				if raw == "" {
					if input.Required {
						return nil, true, fmt.Errorf("%s is required", input.Name)
					}
					continue
				}
				value, err := parseValue(string(input.Type), input.Cardinality, raw)
				if err != nil {
					return nil, true, fmt.Errorf("%s: %w", input.Name, err)
				}
				x.Inputs[input.Name] = value
			}
			draft.Run = &x
		default:
			return nil, true, fmt.Errorf("this proposal cannot be confirmed")
		}
		request := ai.ReviewedDraftRequest{ProposalID: p.ID, Digest: p.Digest, Draft: draft}
		m.busy = true
		return func() tea.Msg {
			result, e := service.ReviewAI(context.Background(), AIReview{Type: "draft", Draft: &request})
			return aiReviewedMsg{result, e}
		}, true, nil
	case "ai-step-edit", "ai-step-add", "ai-validator-edit", "ai-validator-add", "ai-scenario-step-edit", "ai-scenario-step-add":
		p := m.form.aiProposal
		draft := p.Draft
		switch m.form.kind {
		case "ai-step-edit", "ai-step-add":
			if draft.Check == nil {
				return nil, true, fmt.Errorf("check draft unavailable")
			}
			item := *draft.Check
			item.Steps = append([]detective.Step(nil), item.Steps...)
			step, err := m.parseStep()
			if err != nil {
				return nil, true, err
			}
			if m.form.kind == "ai-step-edit" {
				i := m.form.stepIndex
				if i < 0 || i >= len(item.Steps) {
					return nil, true, fmt.Errorf("step unavailable")
				}
				step.Validators = item.Steps[i].Validators
				item.Steps[i] = step
			} else {
				item.Steps = append(item.Steps, step)
			}
			draft.Check = &item
		case "ai-validator-edit", "ai-validator-add":
			if draft.Check == nil {
				return nil, true, fmt.Errorf("check draft unavailable")
			}
			item := *draft.Check
			item.Steps = append([]detective.Step(nil), item.Steps...)
			i := m.form.stepIndex
			if i < 0 || i >= len(item.Steps) || item.Steps[i].Kind != "query" {
				return nil, true, fmt.Errorf("query step unavailable")
			}
			step := item.Steps[i]
			step.Validators = append([]detective.Validator(nil), step.Validators...)
			validator, err := m.parseValidator()
			if err != nil {
				return nil, true, err
			}
			if m.form.kind == "ai-validator-edit" {
				j := m.form.validatorIndex
				if j < 0 || j >= len(step.Validators) {
					return nil, true, fmt.Errorf("validator unavailable")
				}
				step.Validators[j] = validator
			} else {
				step.Validators = append(step.Validators, validator)
			}
			item.Steps[i] = step
			draft.Check = &item
		case "ai-scenario-step-edit", "ai-scenario-step-add":
			if draft.Scenario == nil {
				return nil, true, fmt.Errorf("scenario draft unavailable")
			}
			item := *draft.Scenario
			item.Steps = append([]detective.ScenarioStep(nil), item.Steps...)
			step := detective.ScenarioStep{ID: m.field("ID"), Kind: m.field("Kind"), Ref: m.field("Ref")}
			if !detective.ValidSlug(step.ID) || step.Ref == "" || (step.Kind != "check" && step.Kind != "query") {
				return nil, true, fmt.Errorf("valid Scenario step ID, kind and ref required")
			}
			bindings, err := parseBindings(m.field("Bindings"))
			if err != nil {
				return nil, true, err
			}
			step.Bindings = bindings
			if m.form.kind == "ai-scenario-step-edit" {
				i := m.form.stepIndex
				if i < 0 || i >= len(item.Steps) {
					return nil, true, fmt.Errorf("scenario step unavailable")
				}
				item.Steps[i] = step
			} else {
				item.Steps = append(item.Steps, step)
			}
			draft.Scenario = &item
		}
		request := ai.ReviewedDraftRequest{ProposalID: p.ID, Digest: p.Digest, Draft: draft}
		m.busy = true
		return func() tea.Msg {
			result, e := service.ReviewAI(context.Background(), AIReview{Type: "draft", Draft: &request})
			return aiReviewedMsg{result, e}
		}, true, nil
	case "ai-confirm":
		if m.field("Confirmation") != "CONFIRM" {
			return nil, true, fmt.Errorf("type CONFIRM to authorize this action")
		}
		p := m.form.aiProposal
		request := ai.ConfirmRequest{ProposalID: p.ID, Digest: p.Digest}
		m.busy = true
		return func() tea.Msg {
			receipt, e := service.ConfirmAI(context.Background(), request)
			return aiConfirmedMsg{receipt, e}
		}, true, nil
	}
	return nil, false, nil
}
func parseExpectedCounts(raw string) (map[string]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	values := map[string]int{}
	for _, item := range strings.Split(raw, ",") {
		name, countText, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || !detective.ValidParameterName(name) {
			return nil, fmt.Errorf("expected counts require name=count")
		}
		var count int
		if _, err := fmt.Sscan(countText, &count); err != nil || count < 1 || count > 100 {
			return nil, fmt.Errorf("expected count for %s must be 1..100", name)
		}
		values[name] = count
	}
	return values, nil
}
