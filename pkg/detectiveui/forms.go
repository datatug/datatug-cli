package detectiveui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/google/uuid"
)

type field struct {
	label, value, help string
	readOnly           bool
	multiline          bool
}
type form struct {
	kind            string
	title           string
	fields          []field
	index           int
	check           detective.Check
	caseItem        detective.Case
	scenario        detective.Scenario
	validator       detective.PublishedValidator
	queryValidators detective.QueryValidators
	stepIndex       int
	validatorIndex  int
	proposal        KnowledgeProposal
	aiProposal      ai.Proposal
}

func f(label, value, help string) field { return field{label: label, value: value, help: help} }
func fm(label, value, help string) field {
	return field{label: label, value: value, help: help, multiline: true}
}
func (m *Model) begin(kind, title string, fields []field) {
	if m.screen != formScreen {
		m.returnScreen = m.screen
	}
	m.form = &form{kind: kind, title: title, fields: fields, stepIndex: -1, validatorIndex: -1}
	m.screen = formScreen
	m.feedback = ""
}
func (m *Model) field(name string) string {
	for _, f := range m.form.fields {
		if f.label == name {
			return strings.TrimSpace(f.value)
		}
	}
	return ""
}
func (m *Model) formView() string {
	f := m.form
	var b strings.Builder
	b.WriteString(f.title + "\n\n")
	visible := max(1, m.height-7)
	start := max(0, f.index-visible+1)
	if start > 0 {
		b.WriteString("↑ earlier fields\n")
	}
	end := min(len(f.fields), start+visible)
	for i := start; i < end; i++ {
		x := f.fields[i]
		mark := "  "
		if i == f.index {
			mark = "› "
		}
		label := x.label
		if x.readOnly {
			label += " [review]"
		}
		b.WriteString(mark + label + ": " + x.value + "\n")
		if i == f.index && x.help != "" {
			b.WriteString("    " + x.help + "\n")
		}
	}
	if end < len(f.fields) {
		b.WriteString("↓ more fields\n")
	}
	return b.String()
}
func (m *Model) updateForm(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.form == nil {
		m.screen = m.home
		return m, nil
	}
	f := m.form
	switch k.String() {
	case "esc":
		if f.kind == "run" || f.kind == "scenario-query-run" {
			m.pendingApplicationID, m.pendingStepID = "", ""
		}
		m.form = nil
		m.screen = m.returnScreen
		if m.screen == "" {
			m.screen = m.home
		}
		m.rebuild()
		return m, nil
	case "tab", "down", "enter":
		f.index = (f.index + 1) % len(f.fields)
		return m, nil
	case "shift+tab", "up":
		f.index = (f.index + len(f.fields) - 1) % len(f.fields)
		return m, nil
	case "ctrl+s":
		if cmd, handled, err := m.submitHostedForm(); handled {
			if err != nil {
				m.feedback = err.Error()
				return m, nil
			}
			return m, cmd
		}
		req, err := m.submitForm()
		if err != nil {
			m.feedback = err.Error()
			return m, nil
		}
		if req == nil {
			return m, nil
		}
		return m, m.act(*req)
	case "backspace":
		if f.fields[f.index].readOnly {
			return m, nil
		}
		x := &f.fields[f.index].value
		if *x != "" {
			_, n := utf8.DecodeLastRuneInString(*x)
			*x = (*x)[:len(*x)-n]
		}
		return m, nil
	}
	if k.Text != "" && k.Mod & ^tea.ModShift == 0 && !f.fields[f.index].readOnly {
		f.fields[f.index].value += k.Text
	}
	return m, nil
}
func (m *Model) startNew() (tea.Model, tea.Cmd) {
	switch m.screen {
	case checks:
		m.parent = checks
		m.begin("new-check", "New Check", []field{f("ID", "", "stable slug"), f("Title", "", "human-readable title"), f("Description", "", "optional"), f("Inputs", "", "name:type:scalar|set|range[:required], comma separated")})
		return m, nil
	case cases:
		m.parent = cases
		m.begin("new-case", "New Case", []field{f("ID", "", "stable slug"), f("Title", "", "investigation title")})
		return m, nil
	case scenarios:
		m.parent = scenarios
		m.begin("new-scenario", "New Scenario", []field{f("ID", "", "stable slug"), f("Title", "", "reusable guided investigation"), f("Description", "", "optional"), f("Inputs", "", "name:type:scalar|set|range[:required], comma separated")})
		return m, nil
	case validators:
		m.parent = validators
		m.begin("new-promoted", "New reusable Validator", validatorFields(detective.Validator{}, ""))
		return m, nil
	case queryValidators:
		m.parent = queryValidators
		m.begin("new-query-validators", "Add Validators to saved query", []field{f("Query ID", "", "saved query ID")})
		return m, nil
	}
	return m, nil
}
func (m *Model) detailKey(key string) (tea.Model, tea.Cmd) {
	switch m.parent {
	case checks:
		c := m.currentCheck()
		if c == nil {
			return m, nil
		}
		switch key {
		case "e":
			if len(m.rows) > 0 && m.rows[m.index].kind == "step" {
				step := m.selectedStep(c)
				m.begin("edit-step", "Edit Check step", stepFields(c.Steps[step]))
				m.form.check = *c
				m.form.stepIndex = step
				return m, nil
			}
			if len(m.rows) > 0 && m.rows[m.index].kind == "validator" {
				parts := strings.SplitN(m.rows[m.index].id, "/", 2)
				for i, s := range c.Steps {
					if s.ID == parts[0] {
						for j, v := range s.Validators {
							if v.ID == parts[1] {
								m.begin("edit-validator", "Edit local Validator", validatorFields(v, ""))
								m.form.check = *c
								m.form.stepIndex = i
								m.form.validatorIndex = j
								return m, nil
							}
						}
					}
				}
			}
			m.begin("edit-check", "Edit Check", []field{f("ID", c.ID, "stable ID cannot change"), f("Title", c.Title, ""), f("Description", c.Description, ""), f("Inputs", formatInputs(c.Inputs), "name:type:cardinality[:required], comma separated")})
			m.form.check = *c
			return m, nil
		case "a":
			m.begin("add-step", "Add query or nested Check step", stepFields(detective.Step{}))
			m.form.check = *c
			return m, nil
		case "v":
			step := m.selectedStep(c)
			if step < 0 || c.Steps[step].Kind != "query" {
				m.feedback = "Select a query step to attach a local Validator"
				return m, nil
			}
			m.begin("add-validator", "Add local Validator to "+c.Steps[step].ID, validatorFields(detective.Validator{}, ""))
			m.form.check = *c
			m.form.stepIndex = step
			return m, nil
		case "p":
			if len(m.rows) == 0 || m.rows[m.index].kind != "validator" {
				m.feedback = "Select a local Validator to promote"
				return m, nil
			}
			parts := strings.SplitN(m.rows[m.index].id, "/", 2)
			for _, s := range c.Steps {
				if s.ID == parts[0] {
					for _, v := range s.Validators {
						if v.ID == parts[1] {
							m.begin("promote-validator", "Promote reusable Validator", validatorFields(v, v.ID))
							m.form.validator = detective.PublishedValidator{ID: v.ID, Title: v.ID, Validator: v}
							return m, nil
						}
					}
				}
			}
		case "r":
			return m.startRun(c.ID, "")
		case "h":
			m.screen = history
			m.index = 0
			m.rebuild()
			return m, nil
		}
	case cases:
		c := m.currentCase()
		if c == nil {
			return m, nil
		}
		switch key {
		case "c":
			m.begin("context", "Edit typed context", []field{f("Name", "", "parameter name"), f("Type", "string", "string, integer, decimal, boolean, date, datetime"), f("Cardinality", "scalar", "scalar, set, range"), f("Value", "", "scalar value; set comma separated; range start..end")})
			m.form.caseItem = *c
			return m, nil
		case "e":
			m.begin("evidence", "Add evidence reference", []field{f("ID", "", "stable reference ID"), f("Kind", "query", "query, check-run, external"), f("Ref", "", "source ID or URL"), f("Note", "", "")})
			m.form.caseItem = *c
			return m, nil
		case "f":
			m.begin("finding", "Add finding", []field{f("ID", "", "stable finding ID"), f("Text", "", "observation or hypothesis"), f("Status", "hypothesis", "hypothesis or confirmed"), f("Evidence refs", "", "comma-separated evidence IDs")})
			m.form.caseItem = *c
			return m, nil
		case "i":
			return m.openIncidents()
		case "k":
			return m.openKnowledge()
		case "g":
			return m.openTimeline()
		case "enter":
			if len(m.rows) > 0 && m.rows[m.index].kind == "application-step" {
				return m.startScenarioStep(*c, m.rows[m.index].id)
			}
			return m, nil
		case "s":
			m.begin("link-scenario", "Apply reusable Scenario", []field{f("Scenario ID", "", "choose from F3 catalog")})
			m.form.caseItem = *c
			return m, nil
		case "r":
			m.begin("choose-check", "Run Check in this Case", []field{f("Check ID", "", "choose from F1 catalog")})
			m.form.caseItem = *c
			return m, nil
		case "t":
			m.begin("event", "Add timeline note", []field{f("Message", "", "meaningful investigation action or note")})
			m.form.caseItem = *c
			return m, nil
		case "x":
			m.begin("close", "Close Case", []field{f("Reason", "", "required closure reason")})
			m.form.caseItem = *c
			return m, nil
		case "o":
			return m.lifecycle(*c, "active", "")
		case "z":
			return m.lifecycle(*c, "archived", "")
		case "h":
			m.screen = history
			m.index = 0
			m.rebuild()
			return m, nil
		}
	case scenarios:
		s := m.currentScenario()
		if s == nil {
			return m, nil
		}
		switch key {
		case "e":
			if len(m.rows) > 0 && m.rows[m.index].kind == "scenario-step" {
				for i, step := range s.Steps {
					if step.ID == m.rows[m.index].id {
						m.begin("edit-scenario-step", "Edit Scenario step", scenarioStepFields(step))
						m.form.scenario = *s
						m.form.stepIndex = i
						return m, nil
					}
				}
			}
			m.begin("edit-scenario", "Edit Scenario", []field{f("ID", s.ID, "stable ID"), f("Title", s.Title, ""), f("Description", s.Description, ""), f("Inputs", formatInputs(s.Inputs), "name:type:cardinality[:required], comma separated")})
			m.form.scenario = *s
			return m, nil
		case "a":
			m.begin("add-scenario-step", "Add Scenario step", scenarioStepFields(detective.ScenarioStep{}))
			m.form.scenario = *s
			return m, nil
		}
	case validators:
		v := m.currentValidator()
		if v == nil {
			return m, nil
		}
		if key == "e" {
			fields := validatorFields(v.Validator, v.ID)
			fields[1].value = v.Title
			m.begin("edit-promoted", "Edit reusable Validator", fields)
			m.form.validator = *v
		}
		return m, nil
	case queryValidators:
		q := m.currentQueryValidators()
		if q == nil {
			return m, nil
		}
		switch key {
		case "q":
			if _, err := m.queryExecutionService(); err != nil {
				m.feedback = err.Error()
				return m, nil
			}
			m.begin("query-validators-exact", "Run saved query and validate its exact result", []field{f("Environment", m.defaultEnvironment(), "admitted logical environment"), f("Query ID", q.QueryID, "saved query ID"), f("Inputs", "", "name=type:cardinality:value; separated by semicolons")})
			return m, nil
		case "e":
			if len(m.rows) == 0 || m.rows[m.index].kind != "validator" {
				m.feedback = "Select a local Validator to edit"
				return m, nil
			}
			for i, v := range q.Validators {
				if v.ID == m.rows[m.index].id {
					m.begin("edit-query-validator", "Edit query-local Validator", validatorFields(v, ""))
					m.form.queryValidators = *q
					m.form.validatorIndex = i
					return m, nil
				}
			}
		case "v":
			m.begin("add-query-validator", "Add local Validator to "+q.QueryID, validatorFields(detective.Validator{}, ""))
			m.form.queryValidators = *q
			return m, nil
		case "p":
			if len(m.rows) == 0 || m.rows[m.index].kind != "validator" {
				m.feedback = "Select a local Validator to promote"
				return m, nil
			}
			for _, v := range q.Validators {
				if v.ID == m.rows[m.index].id {
					m.begin("promote-validator", "Promote reusable Validator", validatorFields(v, v.ID))
					m.form.validator = detective.PublishedValidator{ID: v.ID, Title: v.ID, Validator: v}
					return m, nil
				}
			}
		case "r":
			m.begin("run-query-validators", "Run query Validators", []field{f("Environment", "", "select logical environment explicitly"), f("Inputs", "", "name=type:cardinality:value; separate inputs with semicolons")})
			m.form.queryValidators = *q
			return m, nil
		case "h":
			m.screen = history
			m.index = 0
			m.rebuild()
			return m, nil
		}
	}
	return m, nil
}
func (m *Model) selectedStep(c *detective.Check) int {
	if len(m.rows) == 0 {
		return -1
	}
	r := m.rows[m.index]
	for i, s := range c.Steps {
		if s.ID == r.id && r.kind == "step" {
			return i
		}
	}
	return -1
}
func (m *Model) lifecycle(c detective.Case, status, reason string) (tea.Model, tea.Cmd) {
	c.Status = status
	if reason != "" {
		c.ClosureReason = reason
	}
	return m, m.act(detective.ActionRequest{Type: "update-case", Case: &c})
}
func (m *Model) startRun(checkID, caseID string) (tea.Model, tea.Cmd) {
	c := m.checkByID(checkID)
	if c == nil {
		m.feedback = "Check not in catalog: " + checkID
		return m, nil
	}
	if caseID != "" {
		for _, item := range m.state.Cases {
			if item.ID != caseID {
				continue
			}
			for name, value := range item.Context {
				if value.Set == nil && value.Range == nil {
					continue
				}
				found := false
				for _, input := range c.Inputs {
					if input.Name == name {
						found = true
						break
					}
				}
				if !found {
					m.feedback = "Check " + checkID + " has no input for confirmed Case cohort " + name
					return m, nil
				}
			}
		}
	}
	env := ""
	if len(m.state.Environments) == 1 {
		env = m.state.Environments[0]
	}
	fields := []field{f("Environment", env, "logical environment: "+strings.Join(m.state.Environments, ", "))}
	for _, in := range c.Inputs {
		value := ""
		if caseID != "" {
			for _, item := range m.state.Cases {
				if item.ID == caseID {
					if contextValue, ok := item.Context[in.Name]; ok {
						value = formatInputValue(contextValue)
					}
				}
			}
		}
		fields = append(fields, f(in.Name, value, string(in.Type)+"/"+in.Cardinality+"; Case context prefilled when available"))
	}
	m.begin("run", "Run "+checkID, fields)
	m.form.check = *c
	m.caseID = caseID
	return m, nil
}
func (m *Model) checkByID(id string) *detective.Check {
	for i := range m.state.Checks {
		if m.state.Checks[i].ID == id {
			return &m.state.Checks[i]
		}
	}
	return nil
}
func (m *Model) submitForm() (*detective.ActionRequest, error) {
	f := *m.form
	// Form drafts are read from cached State. Detach every nested map and slice
	// before building a request so a rejected or canceled action cannot change
	// the visible catalog while the server still holds the authoritative value.
	var err error
	if f.check, err = detachedFormValue(f.check); err != nil {
		return nil, err
	}
	if f.caseItem, err = detachedFormValue(f.caseItem); err != nil {
		return nil, err
	}
	if f.scenario, err = detachedFormValue(f.scenario); err != nil {
		return nil, err
	}
	if f.validator, err = detachedFormValue(f.validator); err != nil {
		return nil, err
	}
	if f.queryValidators, err = detachedFormValue(f.queryValidators); err != nil {
		return nil, err
	}
	req := &detective.ActionRequest{}
	switch f.kind {
	case "new-check", "edit-check":
		c := f.check
		if f.kind == "new-check" {
			c = detective.Check{}
		}
		c.ID = m.field("ID")
		c.Title = m.field("Title")
		c.Description = m.field("Description")
		if f.kind == "edit-check" && c.ID != f.check.ID {
			return nil, fmt.Errorf("check ID cannot change")
		}
		ins, err := parseInputs(m.field("Inputs"))
		if err != nil {
			return nil, err
		}
		c.Inputs = ins
		if c.ID == "" || c.Title == "" {
			return nil, fmt.Errorf("ID and title are required")
		}
		if f.kind == "new-check" {
			m.begin("first-step", "First step for new Check", stepFields(detective.Step{}))
			m.form.check = c
			return nil, nil
		}
		req.Type = "save-check"
		req.Check = &c
	case "first-step", "add-step", "edit-step":
		c := f.check
		s, err := m.parseStep()
		if err != nil {
			return nil, err
		}
		if f.kind == "edit-step" {
			s.Validators = c.Steps[f.stepIndex].Validators
			c.Steps[f.stepIndex] = s
		} else {
			c.Steps = append(c.Steps, s)
		}
		req.Type = "save-check"
		req.Check = &c
	case "add-validator", "edit-validator":
		c := f.check
		v, err := m.parseValidator()
		if err != nil {
			return nil, err
		}
		if f.kind == "edit-validator" {
			c.Steps[f.stepIndex].Validators[f.validatorIndex] = v
		} else {
			c.Steps[f.stepIndex].Validators = append(c.Steps[f.stepIndex].Validators, v)
		}
		req.Type = "save-check"
		req.Check = &c
	case "promote-validator", "new-promoted", "edit-promoted":
		v, err := m.parseValidator()
		if err != nil {
			return nil, err
		}
		p := f.validator
		p.ID = m.field("Catalog ID")
		p.Title = m.field("Title")
		p.Validator = v
		if p.ID == "" || p.Title == "" {
			return nil, fmt.Errorf("catalog ID and title required")
		}
		if f.kind == "edit-promoted" && p.ID != f.validator.ID {
			return nil, fmt.Errorf("reusable Validator ID cannot change")
		}
		req.Type = "save-validator"
		req.Validator = &p
	case "new-case":
		c := detective.Case{ID: m.field("ID"), Title: m.field("Title"), Status: "active"}
		if c.ID == "" || c.Title == "" {
			return nil, fmt.Errorf("ID and title required")
		}
		req.Type = "create-case"
		req.Case = &c
	case "new-query-validators":
		id := m.field("Query ID")
		if id == "" {
			return nil, fmt.Errorf("saved query ID required")
		}
		m.begin("first-query-validator", "First local Validator", validatorFields(detective.Validator{}, ""))
		m.form.queryValidators = detective.QueryValidators{QueryID: id}
		return nil, nil
	case "first-query-validator", "add-query-validator", "edit-query-validator":
		q := f.queryValidators
		v, err := m.parseValidator()
		if err != nil {
			return nil, err
		}
		if f.kind == "edit-query-validator" {
			q.Validators[f.validatorIndex] = v
		} else {
			q.Validators = append(q.Validators, v)
		}
		req.Type = "save-query-validators"
		req.QueryValidators = &q
	case "run-query-validators":
		env := m.field("Environment")
		if env == "" {
			return nil, fmt.Errorf("select a logical environment")
		}
		inputs, err := parseNamedValues(m.field("Inputs"))
		if err != nil {
			return nil, err
		}
		req.Type = "run-query-validators"
		req.QueryID = f.queryValidators.QueryID
		req.Environment = env
		req.Inputs = inputs
	case "context":
		c := f.caseItem
		name := m.field("Name")
		if name == "" {
			return nil, fmt.Errorf("context name required")
		}
		v, err := parseValue(m.field("Type"), m.field("Cardinality"), m.field("Value"))
		if err != nil {
			return nil, err
		}
		if c.Context == nil {
			c.Context = map[string]detective.InputValue{}
		}
		c.Context[name] = v
		req.Type = "update-case"
		req.Case = &c
	case "evidence":
		c := f.caseItem
		e := detective.EvidenceRef{ID: m.field("ID"), Kind: m.field("Kind"), Ref: m.field("Ref"), Note: m.field("Note")}
		if e.ID == "" || e.Ref == "" {
			return nil, fmt.Errorf("evidence ID and reference required")
		}
		c.Evidence = append(c.Evidence, e)
		req.Type = "update-case"
		req.Case = &c
	case "finding":
		c := f.caseItem
		v := detective.Finding{ID: m.field("ID"), Text: m.field("Text"), Status: m.field("Status"), EvidenceRefs: splitCSV(m.field("Evidence refs"))}
		if v.ID == "" || v.Text == "" {
			return nil, fmt.Errorf("finding ID and text required")
		}
		c.Findings = append(c.Findings, v)
		req.Type = "update-case"
		req.Case = &c
	case "link-incident":
		return nil, fmt.Errorf("link incidents through the verified Incidentius workflow")
	case "link-scenario":
		ref := m.field("Scenario ID")
		if ref == "" {
			return nil, fmt.Errorf("scenario ID required")
		}
		found := false
		for _, s := range m.state.Scenarios {
			if s.ID == ref {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("scenario not in catalog")
		}
		req.Type = "apply-scenario"
		req.CaseID = f.caseItem.ID
		req.ScenarioID = ref
		req.ApplicationID = uuid.NewString()
	case "choose-check":
		if m.field("Check ID") == "" {
			return nil, fmt.Errorf("check ID required")
		}
		m.startRun(m.field("Check ID"), f.caseItem.ID)
		return nil, nil
	case "event":
		req.Type = "append-event"
		req.CaseID = f.caseItem.ID
		req.Event = &detective.CaseEvent{Type: "note", Message: m.field("Message")}
		if req.Event.Message == "" {
			return nil, fmt.Errorf("message required")
		}
	case "close":
		reason := m.field("Reason")
		if reason == "" {
			return nil, fmt.Errorf("closure reason required")
		}
		c := f.caseItem
		c.Status = "closed"
		c.ClosureReason = reason
		req.Type = "update-case"
		req.Case = &c
	case "run":
		env := m.field("Environment")
		if env == "" {
			return nil, fmt.Errorf("select a logical environment")
		}
		req.Type = "run-check"
		req.CheckID = f.check.ID
		req.CaseID = m.caseID
		req.Environment = env
		req.Inputs = map[string]detective.InputValue{}
		for _, in := range f.check.Inputs {
			raw := m.field(in.Name)
			if raw == "" {
				if in.Required {
					return nil, fmt.Errorf("%s is required", in.Name)
				}
				continue
			}
			v, err := parseValue(string(in.Type), in.Cardinality, raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", in.Name, err)
			}
			req.Inputs[in.Name] = v
		}
	case "new-scenario", "edit-scenario":
		s := f.scenario
		s.ID = m.field("ID")
		s.Title = m.field("Title")
		s.Description = m.field("Description")
		inputs, err := parseInputs(m.field("Inputs"))
		if err != nil {
			return nil, err
		}
		s.Inputs = inputs
		if s.ID == "" || s.Title == "" {
			return nil, fmt.Errorf("ID and title required")
		}
		if f.kind == "edit-scenario" && s.ID != f.scenario.ID {
			return nil, fmt.Errorf("scenario ID cannot change")
		}
		if f.kind == "new-scenario" {
			m.begin("first-scenario-step", "First Scenario step", scenarioStepFields(detective.ScenarioStep{}))
			m.form.scenario = s
			return nil, nil
		}
		if err := detective.ValidateScenario(s); err != nil {
			return nil, fmt.Errorf("scenario inputs or bindings: %w", err)
		}
		req.Type = "save-scenario"
		req.Scenario = &s
	case "first-scenario-step", "add-scenario-step", "edit-scenario-step":
		s := f.scenario
		step := detective.ScenarioStep{ID: m.field("ID"), Kind: m.field("Kind"), Ref: m.field("Ref")}
		if step.ID == "" || step.Ref == "" || (step.Kind != "check" && step.Kind != "query") {
			return nil, fmt.Errorf("valid step ID, kind and ref required")
		}
		step.Bindings, err = parseBindings(m.field("Bindings"))
		if err != nil {
			return nil, err
		}
		if f.kind == "edit-scenario-step" {
			if step.ID != s.Steps[f.stepIndex].ID {
				return nil, fmt.Errorf("scenario step ID cannot change")
			}
			s.Steps[f.stepIndex] = step
		} else {
			s.Steps = append(s.Steps, step)
		}
		if err := detective.ValidateScenario(s); err != nil {
			return nil, fmt.Errorf("scenario step or bindings: %w", err)
		}
		req.Type = "save-scenario"
		req.Scenario = &s
	default:
		return nil, fmt.Errorf("unsupported form %s", f.kind)
	}
	return req, nil
}

func detachedFormValue[T any](value T) (T, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return value, fmt.Errorf("copy form resource: %w", err)
	}
	var detached T
	if err := json.Unmarshal(data, &detached); err != nil {
		return value, fmt.Errorf("copy form resource: %w", err)
	}
	return detached, nil
}
func stepFields(s detective.Step) []field {
	ref := s.QueryID
	if s.Kind == "check" {
		ref = s.CheckID
	}
	return []field{f("ID", s.ID, "stable step slug"), f("Kind", defaultString(s.Kind, "query"), "query or check"), f("Ref", ref, "saved query ID or globally reusable Check ID"), f("Bindings", formatBindings(s.Bindings), "child=parent; child=range.start; child=@row-column, comma separated"), f("On unavailable", s.OnUnavailable, "block, fail, warn, skip or empty")}
}
func scenarioStepFields(s detective.ScenarioStep) []field {
	return []field{f("ID", s.ID, "stable step ID"), f("Kind", defaultString(s.Kind, "check"), "check or query"), f("Ref", s.Ref, "saved query or Check catalog ID"), f("Bindings", formatBindings(s.Bindings), "query only: parameter=scenario_input or parameter=range.start/end; comma separated")}
}
func validatorFields(v detective.Validator, id string) []field {
	return []field{f("Catalog ID", id, "only for promoted Validators"), f("Title", id, "only for promoted Validators"), f("ID", v.ID, "local Validator slug"), f("Scope", defaultString(v.Scope, "row"), "row or set"), f("Kind", defaultString(v.Kind, "non-null"), "non-null, equals-fields, row-count, unique, sum-equals, check"), f("Severity", defaultString(v.Severity, "error"), "critical, error, warning, info"), f("Field", v.Field, "field to inspect"), f("Other field", v.OtherField, "comparison field"), f("Expected", formatExpected(v.Expected), "row-count expected integer"), f("Check ID", v.CheckID, "globally reusable Check for kind=check"), f("Bindings", formatBindings(v.Bindings), "child=@row-column for per-row Check"), f("On unavailable", v.OnUnavailable, "block, fail, warn, skip or empty"), f("Promoted ID", v.ValidatorID, "reference an existing reusable Validator instead of defining a local kind")}
}
func (m *Model) parseStep() (detective.Step, error) {
	s := detective.Step{ID: m.field("ID"), Kind: m.field("Kind"), OnUnavailable: m.field("On unavailable")}
	switch s.Kind {
	case "query":
		s.QueryID = m.field("Ref")
	case "check":
		s.CheckID = m.field("Ref")
	default:
		return s, fmt.Errorf("step kind must be query or check")
	}
	if s.ID == "" || m.field("Ref") == "" {
		return s, fmt.Errorf("step ID and reference required")
	}
	b, e := parseBindings(m.field("Bindings"))
	s.Bindings = b
	return s, e
}
func (m *Model) parseValidator() (detective.Validator, error) {
	v := detective.Validator{ID: m.field("ID"), Scope: m.field("Scope"), Kind: m.field("Kind"), Severity: m.field("Severity"), Field: m.field("Field"), OtherField: m.field("Other field"), CheckID: m.field("Check ID"), OnUnavailable: m.field("On unavailable")}
	if v.ID == "" {
		return v, fmt.Errorf("validator ID required")
	}
	if id := m.field("Promoted ID"); id != "" {
		v.ValidatorID = id
		v.Kind = ""
		v.Field = ""
		v.OtherField = ""
		v.CheckID = ""
		v.Bindings = nil
		v.Expected = nil
		return v, nil
	}
	if raw := m.field("Expected"); raw != "" {
		x, e := typedValue("integer", raw)
		if e != nil {
			return v, e
		}
		v.Expected = &x
	}
	b, e := parseBindings(m.field("Bindings"))
	v.Bindings = b
	return v, e
}
func parseInputs(raw string) ([]detective.Input, error) {
	var out []detective.Input
	for _, part := range splitCSV(raw) {
		p := strings.Split(part, ":")
		if len(p) < 3 || len(p) > 4 {
			return nil, fmt.Errorf("input %q: expected name:type:cardinality[:required]", part)
		}
		in := detective.Input{Name: p[0], Type: investigation.ValueType(p[1]), Cardinality: p[2], Required: len(p) == 4 && p[3] == "required"}
		if in.Name == "" || !oneOf(in.Cardinality, "scalar", "set", "range") {
			return nil, fmt.Errorf("invalid input %q", part)
		}
		out = append(out, in)
	}
	return out, nil
}
func formatInputs(inputs []detective.Input) string {
	var out []string
	for _, in := range inputs {
		s := in.Name + ":" + string(in.Type) + ":" + in.Cardinality
		if in.Required {
			s += ":required"
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}
func parseBindings(raw string) ([]detective.Binding, error) {
	var out []detective.Binding
	for _, part := range splitCSV(raw) {
		p := strings.SplitN(part, "=", 2)
		if len(p) != 2 || p[0] == "" || p[1] == "" {
			return nil, fmt.Errorf("binding %q: expected child=parent or child=@row-column", part)
		}
		b := detective.Binding{Input: p[0]}
		if strings.HasPrefix(p[1], "@") {
			b.FromColumn = strings.TrimPrefix(p[1], "@")
		} else if name, part, ok := strings.Cut(p[1], "."); ok {
			b.FromInput = name
			b.Part = part
		} else {
			b.FromInput = p[1]
		}
		out = append(out, b)
	}
	return out, nil
}
func formatBindings(bindings []detective.Binding) string {
	var out []string
	for _, b := range bindings {
		v := b.FromInput
		if b.Part != "" {
			v += "." + b.Part
		}
		if b.FromColumn != "" {
			v = "@" + b.FromColumn
		}
		out = append(out, b.Input+"="+v)
	}
	return strings.Join(out, ", ")
}
func parseValue(kind, cardinality, raw string) (detective.InputValue, error) {
	var out detective.InputValue
	switch cardinality {
	case "scalar":
		v, e := typedValue(kind, raw)
		if e != nil {
			return out, e
		}
		out.Scalar = &v
	case "set":
		var values []detective.Value
		for _, s := range splitCSV(raw) {
			v, e := typedValue(kind, s)
			if e != nil {
				return out, e
			}
			values = append(values, v)
		}
		set, e := apicontract.NewTypedValueSet(values...)
		if e != nil {
			return out, e
		}
		out.Set = &set
	case "range":
		parts := strings.SplitN(raw, "..", 2)
		if len(parts) != 2 {
			return out, fmt.Errorf("range requires start..end")
		}
		a, e := typedValue(kind, parts[0])
		if e != nil {
			return out, e
		}
		b, e := typedValue(kind, parts[1])
		if e != nil {
			return out, e
		}
		out.Range = &detective.ValueRange{Type: "range", Start: a, End: b}
	default:
		return out, fmt.Errorf("cardinality must be scalar, set or range")
	}
	return out, out.Validate()
}
func typedValue(kind, raw string) (detective.Value, error) {
	raw = strings.TrimSpace(raw)
	switch kind {
	case "string":
		return investigation.NewStringValue(raw), nil
	case "integer":
		v := investigation.NewIntegerValue(raw)
		return v, v.Validate()
	case "decimal":
		v := investigation.NewDecimalValue(raw)
		return v, v.Validate()
	case "date":
		v := investigation.NewDateValue(raw)
		return v, v.Validate()
	case "datetime":
		v := investigation.NewDatetimeValue(raw)
		return v, v.Validate()
	case "boolean":
		b, e := strconv.ParseBool(raw)
		if e != nil {
			return detective.Value{}, e
		}
		return investigation.NewBooleanValue(b), nil
	case "number":
		n, e := strconv.ParseFloat(raw, 64)
		if e != nil {
			return detective.Value{}, e
		}
		return investigation.NewNumberValue(n), nil
	}
	return detective.Value{}, fmt.Errorf("unsupported type %q", kind)
}
func describeValue(v detective.InputValue) string {
	b, e := json.Marshal(v)
	if e != nil {
		return "invalid"
	}
	return string(b)
}
func formatExpected(v *detective.Value) string {
	if v == nil {
		return ""
	}
	return v.Str
}
func splitCSV(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
func defaultString(s, defaultValue string) string {
	if s == "" {
		return defaultValue
	}
	return s
}
func oneOf(s string, opts ...string) bool {
	for _, v := range opts {
		if s == v {
			return true
		}
	}
	return false
}

func parseNamedValues(raw string) (map[string]detective.InputValue, error) {
	out := map[string]detective.InputValue{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	entries, err := splitOutsideQuotes(raw, ';')
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, spec, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("input %q: expected name=type:cardinality:value", entry)
		}
		parts := strings.SplitN(spec, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("input %q: expected type:cardinality:value", name)
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("duplicate input %q", name)
		}
		value, err := parseNamedValue(parts[0], parts[1], parts[2])
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", name, err)
		}
		out[name] = value
	}
	return out, nil
}

func parseNamedValue(kind, cardinality, raw string) (detective.InputValue, error) {
	if kind != "string" || cardinality == "range" {
		return parseValue(kind, cardinality, raw)
	}
	var result detective.InputValue
	if cardinality == "scalar" {
		text, err := parseQuotedString(raw)
		if err != nil {
			return result, err
		}
		value := investigation.NewStringValue(text)
		result.Scalar = &value
	} else if cardinality == "set" {
		parts, err := splitOutsideQuotes(raw, ',')
		if err != nil {
			return result, err
		}
		values := make([]detective.Value, 0, len(parts))
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				continue
			}
			text, err := parseQuotedString(part)
			if err != nil {
				return result, err
			}
			values = append(values, investigation.NewStringValue(text))
		}
		set, err := apicontract.NewTypedValueSet(values...)
		if err != nil {
			return result, err
		}
		result.Set = &set
	} else {
		return result, fmt.Errorf("cardinality must be scalar, set or range")
	}
	return result, result.Validate()
}

func parseQuotedString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "\"") {
		return raw, nil // Existing unquoted input remains available for simple values.
	}
	value, err := strconv.Unquote(raw)
	if err != nil {
		return "", fmt.Errorf("invalid quoted string: %w", err)
	}
	return value, nil
}

func splitOutsideQuotes(raw string, separator byte) ([]string, error) {
	var parts []string
	start := 0
	quoted, escaped := false, false
	for i := 0; i < len(raw); i++ {
		switch {
		case escaped:
			escaped = false
		case quoted && raw[i] == '\\':
			escaped = true
		case raw[i] == '"' && (quoted || startsQuotedValue(raw, start, i)):
			quoted = !quoted
		case raw[i] == separator && !quoted:
			parts = append(parts, raw[start:i])
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, fmt.Errorf("unterminated quoted input")
	}
	return append(parts, raw[start:]), nil
}

func startsQuotedValue(raw string, start, quote int) bool {
	for i := quote - 1; i >= start; i-- {
		if raw[i] == ' ' || raw[i] == '\t' {
			continue
		}
		return raw[i] == ':' || raw[i] == ',' || raw[i] == '='
	}
	return true
}
