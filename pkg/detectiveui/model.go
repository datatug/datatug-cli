// Package detectiveui provides native keyboard-driven Checks and Detective workspaces.
// All project state and execution are delegated to the current DataTug agent.
package detectiveui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

type Service interface {
	State(context.Context) (detective.State, error)
	Action(context.Context, detective.ActionRequest) (detective.ActionResponse, error)
}
type actionStatusService interface {
	ActionWithStatus(context.Context, detective.ActionRequest) (detective.ActionResponse, string, error)
}
type historyPager interface {
	StatePage(context.Context, string) (detective.State, error)
}

type screen string

const (
	checks          screen = "Checks"
	cases           screen = "Detective"
	scenarios       screen = "Scenarios"
	validators      screen = "Validators"
	queryValidators screen = "Query Validators"
	detail          screen = "Detail"
	runDetail       screen = "Run"
	queryDetail     screen = "Query Result"
	history         screen = "History"
	timeline        screen = "Timeline"
	incidentList    screen = "Incidents"
	knowledgeList   screen = "Knowledge"
	knowledgeDetail screen = "Proposal"
	exportDetail    screen = "Private Export"
	aiList          screen = "AI Proposals"
	aiDetail        screen = "AI Review"
	formScreen      screen = "Form"
)

type loadedMsg struct {
	state detective.State
	err   error
}
type moreRunsMsg struct {
	state detective.State
	err   error
}
type actedMsg struct {
	response       detective.ActionResponse
	err            error
	caseLinkStatus string
}
type eventsMsg struct {
	page  detective.CaseEventsPage
	err   error
	older bool
}
type incidentsMsg struct {
	links IncidentLinks
	err   error
}
type knowledgeMsg struct {
	page  KnowledgePage
	err   error
	older bool
}
type incidentActedMsg struct {
	result IncidentResponse
	err    error
}
type knowledgeActedMsg struct {
	result KnowledgeResponse
	err    error
}
type queryRunMsg struct {
	result                SavedQueryResult
	err                   error
	queryID, environment  string
	validators            bool
	applicationID, stepID string
}
type aiPageMsg struct {
	page  AIPage
	err   error
	older bool
}
type aiGeneratedMsg struct {
	response ai.Response
	err      error
}
type aiReviewedMsg struct {
	proposal ai.Proposal
	err      error
}
type aiConfirmedMsg struct {
	receipt ai.Receipt
	err     error
}

type Model struct {
	service                             Service
	state                               detective.State
	screen                              screen
	home                                screen
	parent                              screen
	returnScreen                        screen
	id                                  string
	checkID                             string
	caseID                              string
	runID                               string
	filter                              string
	index                               int
	offset                              int
	width, height                       int
	busy                                bool
	feedback                            string
	lastRunLinkStatus                   string
	failedOnly                          bool
	form                                *form
	rows                                []row
	events                              []detective.CaseEvent
	eventsBefore                        int
	eventsMore                          bool
	incidents                           IncidentLinks
	knowledge                           KnowledgePage
	proposalID                          string
	exported                            *KnowledgeHandoff
	aiPage                              AIPage
	aiProposalID                        string
	queryExecution                      *apicontract.ExecutionRef
	queryResult                         *SavedQueryResult
	queryID, queryEnvironment           string
	showQueryOnCase                     bool
	pendingApplicationID, pendingStepID string
	pendingRunID                        string
}

type row struct {
	label, kind, id string
	depth           int
}

func New(service Service, start string) *Model {
	home := checks
	if start == "detective" {
		home = cases
	}
	return &Model{service: service, screen: home, home: home, width: 80, height: 24}
}
func (m *Model) Init() tea.Cmd { return m.load() }
func (m *Model) AtCatalog() bool {
	return m.screen == checks || m.screen == cases || m.screen == scenarios || m.screen == validators || m.screen == queryValidators
}
func (m *Model) CanLeave() bool { return m.AtCatalog() && m.filter == "" }
func (m *Model) load() tea.Cmd {
	return func() tea.Msg { s, e := m.service.State(context.Background()); return loadedMsg{s, e} }
}
func (m *Model) act(req detective.ActionRequest) tea.Cmd {
	m.busy = true
	return func() tea.Msg {
		if withStatus, ok := m.service.(actionStatusService); ok {
			r, status, e := withStatus.ActionWithStatus(context.Background(), req)
			return actedMsg{response: r, err: e, caseLinkStatus: status}
		}
		r, e := m.service.Action(context.Background(), req)
		return actedMsg{response: r, err: e}
	}
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(25, v.Width), max(8, v.Height)
		return m, nil
	case loadedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			m.pendingApplicationID, m.pendingStepID, m.pendingRunID = "", "", ""
		} else {
			m.state = v.state
			m.feedback = ""
			if m.screen == runDetail && m.lastRunLinkStatus != "" {
				m.feedback = "Case link: " + m.lastRunLinkStatus
			}
		}
		m.rebuild()
		if m.pendingRunID != "" {
			app, step, runID := m.pendingApplicationID, m.pendingStepID, m.pendingRunID
			m.pendingApplicationID, m.pendingStepID, m.pendingRunID = "", "", ""
			return m, m.act(detective.ActionRequest{Type: "record-scenario-step", CaseID: m.caseID, ApplicationID: app, StepID: step, StepStatus: "completed", RunID: runID})
		}
		return m, nil
	case moreRunsMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.state.Runs = append(m.state.Runs, v.state.Runs...)
		m.state.RunsHasMore = v.state.RunsHasMore
		m.state.RunsNextCursor = v.state.RunsNextCursor
		m.feedback = fmt.Sprintf("Loaded %d more runs", len(v.state.Runs))
		m.rebuild()
		return m, nil
	case actedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			if m.showQueryOnCase {
				m.showQueryOnCase = false
				m.form = nil
				m.screen = queryDetail
				m.rebuild()
			}
			return m, nil
		}
		m.feedback = "Saved in project"
		if v.response.Check != nil {
			m.id = v.response.Check.ID
			m.screen = detail
			m.parent = checks
		}
		if v.response.Case != nil {
			m.id = v.response.Case.ID
			m.screen = detail
			m.parent = cases
			if m.showQueryOnCase {
				m.screen = queryDetail
				m.showQueryOnCase = false
			}
		}
		if v.response.Scenario != nil {
			m.id = v.response.Scenario.ID
			m.screen = detail
			m.parent = scenarios
		}
		if v.response.Validator != nil {
			m.screen = validators
		}
		if v.response.QueryValidators != nil {
			m.id = v.response.QueryValidators.QueryID
			m.parent = queryValidators
			m.screen = detail
		}
		if v.response.Run != nil {
			m.lastRunLinkStatus = v.caseLinkStatus
			m.failedOnly = false
			m.runID = v.response.Run.ID
			m.screen = runDetail
			m.checkID = v.response.Run.CheckID
			m.caseID = v.response.Run.CaseID
			if m.pendingApplicationID != "" && v.response.Run.ExecutionStatus == "completed" && (v.caseLinkStatus == "linked" || v.caseLinkStatus == "already-linked") {
				// Attaching a Case-bound run advances the Case version. Refresh
				// before recording the Scenario step so hosted ifMatch is current.
				m.pendingRunID = v.response.Run.ID
				m.form = nil
				return m, m.load()
			}
			if m.pendingApplicationID != "" {
				m.pendingApplicationID, m.pendingStepID = "", ""
				m.feedback = "Scenario step remains pending: " + v.caseLinkStatus
				if v.caseLinkStatus == "" {
					m.feedback = "Scenario step remains pending: Case link unverified"
				}
			}
		}
		m.form = nil
		return m, m.load()
	case eventsMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		if !v.older {
			m.events = nil
		}
		m.events = append(m.events, v.page.Events...)
		m.eventsBefore, m.eventsMore = v.page.NextBefore, v.page.HasMore
		m.rebuild()
		return m, nil
	case incidentsMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.incidents = v.links
		m.rebuild()
		return m, nil
	case knowledgeMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		if !v.older {
			m.knowledge = v.page
		} else {
			m.knowledge.Proposals = append(m.knowledge.Proposals, v.page.Proposals...)
			m.knowledge.NextCursor = v.page.NextCursor
			m.knowledge.HasMore = v.page.HasMore
		}
		m.rebuild()
		return m, nil
	case incidentActedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.form = nil
		if v.result.CaseLinkStatus == "link-pending" {
			m.feedback = "Incident " + v.result.Incident.Ref.String() + " created; Case changed. Link it to the latest Case."
			return m, nil
		}
		m.feedback = "Incident " + v.result.Incident.Ref.String() + " linked"
		if v.result.Case != nil {
			m.id = v.result.Case.ID
			m.parent = cases
			m.screen = detail
		}
		return m, m.load()
	case knowledgeActedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.form = nil
		m.feedback = "Knowledge proposal saved privately"
		if v.result.Proposal != nil {
			for i := range m.knowledge.Proposals {
				if m.knowledge.Proposals[i].ID == v.result.Proposal.ID {
					m.knowledge.Proposals[i] = *v.result.Proposal
					break
				}
			}
		}
		if v.result.Export != nil {
			m.exported = v.result.Export
			m.screen = exportDetail
			m.rebuild()
			return m, nil
		}
		m.screen = knowledgeList
		return m, m.loadKnowledge(false)
	case queryRunMsg:
		m.busy = false
		// The message carries its own Scenario identity for successful proof
		// recording. Release the model's pending identity on every terminal path.
		m.pendingApplicationID, m.pendingStepID = "", ""
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.form = nil
		ref := v.result.Result.Execution
		if ref == nil {
			m.feedback = "Hosted query returned no execution receipt"
			return m, nil
		}
		m.queryExecution = ref
		m.queryResult = &v.result
		m.queryID, m.queryEnvironment = v.queryID, v.environment
		if v.validators {
			return m, m.act(detective.ActionRequest{Type: "run-query-validators", QueryID: v.queryID, Environment: v.environment, Execution: ref})
		}
		if v.applicationID != "" {
			m.showQueryOnCase = true
			return m, m.act(detective.ActionRequest{Type: "record-scenario-step", CaseID: m.id, ApplicationID: v.applicationID, StepID: v.stepID, StepStatus: "completed", QueryID: v.queryID, Environment: v.environment, Execution: ref})
		}
		m.feedback = "Saved query completed with execution receipt " + ref.ExecutionID
		m.screen = queryDetail
		m.rebuild()
		return m, nil
	case aiPageMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		if v.older {
			m.aiPage.Proposals = append(m.aiPage.Proposals, v.page.Proposals...)
			m.aiPage.HasMore, m.aiPage.NextCursor = v.page.HasMore, v.page.NextCursor
		} else {
			m.aiPage = v.page
		}
		m.rebuild()
		return m, nil
	case aiGeneratedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.form = nil
		m.feedback = fmt.Sprintf("%d private AI proposals ready for review", len(v.response.Proposals))
		if len(v.response.Proposals) > 0 {
			m.aiProposalID = v.response.Proposals[0].ID
			m.screen = aiDetail
		}
		return m, m.loadAI(false)
	case aiReviewedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.form = nil
		m.aiProposalID = v.proposal.ID
		m.screen = aiDetail
		m.feedback = "Reviewed draft saved privately; inspect before confirming"
		return m, m.loadAI(false)
	case aiConfirmedMsg:
		m.busy = false
		if v.err != nil {
			m.feedback = v.err.Error()
			return m, nil
		}
		m.form = nil
		m.feedback = "Confirmed with receipt " + v.receipt.OperationID
		return m, m.load()
	case tea.KeyPressMsg:
		if v.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		if m.screen == formScreen {
			return m.updateForm(v)
		}
		return m.updateKey(v)
	case tea.PasteMsg:
		if m.busy {
			return m, nil
		}
		value := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, v.Content)
		if m.screen == formScreen && m.form != nil && !m.form.fields[m.form.index].readOnly {
			m.form.fields[m.form.index].value += value
		} else if m.AtCatalog() {
			m.filter += value
			m.index = 0
			m.rebuild()
		}
		return m, nil
	}
	return m, nil
}
func (m *Model) updateKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "f1" {
		m.openCatalog(checks)
		return m, nil
	}
	if key == "f2" {
		m.openCatalog(cases)
		return m, nil
	}
	if key == "f3" {
		m.openCatalog(scenarios)
		return m, nil
	}
	if key == "f4" {
		m.openCatalog(validators)
		return m, nil
	}
	if key == "f5" {
		m.openCatalog(queryValidators)
		return m, nil
	}
	if key == "ctrl+a" && m.screen != formScreen {
		return m.openAI()
	}
	if key == "esc" {
		if m.filter != "" && m.screen != detail && m.screen != runDetail {
			m.filter = ""
			m.index = 0
			m.rebuild()
			return m, nil
		}
		switch m.screen {
		case detail:
			m.screen = m.parent
		case runDetail:
			m.screen = detail
		case queryDetail:
			m.screen = detail
		case history:
			m.screen = detail
		case timeline, incidentList, knowledgeList, aiList:
			m.screen = detail
		case knowledgeDetail:
			m.screen = knowledgeList
		case exportDetail:
			m.screen = knowledgeDetail
		case aiDetail:
			m.screen = aiList
		default:
			return m, tea.Quit
		}
		m.index = 0
		m.rebuild()
		return m, nil
	}
	if key == "up" {
		m.index = max(0, m.index-1)
		m.ensureScroll()
		return m, nil
	}
	if key == "down" {
		m.index = min(max(0, len(m.rows)-1), m.index+1)
		m.ensureScroll()
		return m, nil
	}
	if key == "pgdown" {
		m.index = min(max(0, len(m.rows)-1), m.index+max(1, m.height-7))
		m.ensureScroll()
		return m, nil
	}
	if key == "pgup" {
		m.index = max(0, m.index-max(1, m.height-7))
		m.ensureScroll()
		return m, nil
	}
	if m.screen == checks || m.screen == cases || m.screen == scenarios || m.screen == validators || m.screen == queryValidators {
		switch key {
		case "enter":
			if len(m.rows) > 0 {
				m.id = m.rows[m.index].id
				m.parent = m.screen
				m.screen = detail
				m.index = 0
				m.rebuild()
			}
			return m, nil
		case "ctrl+n":
			return m.startNew()
		case "backspace":
			if m.filter != "" {
				_, n := utf8.DecodeLastRuneInString(m.filter)
				m.filter = m.filter[:len(m.filter)-n]
				m.index = 0
				m.rebuild()
			}
			return m, nil
		}
		if k.Text != "" && k.Mod & ^tea.ModShift == 0 {
			m.filter += k.Text
			m.index = 0
			m.rebuild()
		}
		return m, nil
	}
	if m.screen == history {
		if key == "l" && m.state.RunsHasMore {
			if pager, ok := m.service.(historyPager); ok {
				m.busy = true
				cursor := m.state.RunsNextCursor
				return m, func() tea.Msg { s, e := pager.StatePage(context.Background(), cursor); return moreRunsMsg{s, e} }
			}
			m.feedback = "History paging is unavailable from this agent client"
			return m, nil
		}
		if key == "enter" && len(m.rows) > 0 {
			m.runID = m.rows[m.index].id
			m.lastRunLinkStatus = ""
			m.failedOnly = false
			m.screen = runDetail
			m.index = 0
			m.rebuild()
		}
		return m, nil
	}
	if m.screen == timeline || m.screen == incidentList || m.screen == knowledgeList || m.screen == knowledgeDetail || m.screen == exportDetail || m.screen == aiList || m.screen == aiDetail {
		return m.updateHostedKey(key)
	}
	if m.screen == runDetail {
		if key == "f" {
			m.failedOnly = !m.failedOnly
			m.index = 0
			m.offset = 0
			m.rebuild()
			return m, nil
		}
		if key == "enter" && len(m.rows) > 0 {
			m.feedback = m.rows[m.index].label
		}
		return m, nil
	}
	if m.screen == queryDetail {
		if key == "enter" && len(m.rows) > 0 {
			m.feedback = m.rows[m.index].label
		}
		return m, nil
	}
	if m.screen == detail {
		return m.detailKey(key)
	}
	return m, nil
}
func (m *Model) openCatalog(s screen) {
	m.screen = s
	m.home = s
	m.filter = ""
	m.index = 0
	m.rebuild()
}
func (m *Model) ensureScroll() {
	visible := max(1, m.height-7)
	if m.index < m.offset {
		m.offset = m.index
	}
	if m.index >= m.offset+visible {
		m.offset = m.index - visible + 1
	}
}
func (m *Model) rebuild() {
	m.rows = nil
	match := func(s string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(m.filter)) }
	switch m.screen {
	case checks:
		for _, c := range m.state.Checks {
			if match(c.ID + " " + c.Title) {
				m.rows = append(m.rows, row{c.ID + "  " + c.Title + fmt.Sprintf("  v%d", c.Version), "check", c.ID, 0})
			}
		}
	case cases:
		for _, c := range m.state.Cases {
			if match(c.ID + " " + c.Title + " " + c.Status) {
				m.rows = append(m.rows, row{c.ID + "  [" + c.Status + "] " + c.Title, "case", c.ID, 0})
			}
		}
	case scenarios:
		for _, s := range m.state.Scenarios {
			if match(s.ID + " " + s.Title) {
				m.rows = append(m.rows, row{s.ID + "  " + s.Title, "scenario", s.ID, 0})
			}
		}
	case validators:
		for _, v := range m.state.Validators {
			if match(v.ID + " " + v.Title) {
				m.rows = append(m.rows, row{v.ID + "  " + v.Title, "validator", v.ID, 0})
			}
		}
	case queryValidators:
		for _, q := range m.state.QueryValidators {
			if match(q.QueryID) {
				m.rows = append(m.rows, row{fmt.Sprintf("%s  ·  %d local Validators  ·  v%d", q.QueryID, len(q.Validators), q.Version), "query-validators", q.QueryID, 0})
			}
		}
	case detail:
		m.rebuildDetail()
	case history:
		for _, r := range m.state.Runs {
			if r.CheckID == m.id || r.CaseID == m.id || r.QueryID == m.id {
				m.rows = append(m.rows, row{fmt.Sprintf("%s  %s  %s/%s  %s", r.ID, r.Environment, r.ExecutionStatus, r.Outcome, r.StartedAt.Format("2006-01-02 15:04")), "run", r.ID, 0})
			}
		}
	case timeline:
		for _, e := range m.events {
			m.rows = append(m.rows, row{e.At.Format("2006-01-02 15:04") + "  " + e.Type + "  " + e.Message, "event", e.ID, 0})
		}
	case incidentList:
		for _, incident := range m.incidents.Incidents {
			m.rows = append(m.rows, row{incident.Ref.String() + "  [" + string(incident.Status) + "] " + incident.Title, "incident", incident.Ref.String(), 0})
		}
	case knowledgeList:
		for _, proposal := range m.knowledge.Proposals {
			if proposal.CaseID == m.id && match(proposal.Title+" "+proposal.ID) {
				m.rows = append(m.rows, row{proposal.ID + "  [" + proposal.Status + "] " + proposal.Title, "proposal", proposal.ID, 0})
			}
		}
	case knowledgeDetail:
		for _, proposal := range m.knowledge.Proposals {
			if proposal.ID == m.proposalID {
				m.rows = append(m.rows, row{"Proposal " + proposal.ID + "  [" + proposal.Status + "] " + proposal.Title, "header", proposal.ID, 0}, row{"Kind " + proposal.Kind + "  Case " + proposal.CaseID + " · " + proposal.ClaimStatus, "header", proposal.ID, 0}, row{proposal.Statement, "header", proposal.ID, 0}, row{"Evidence: " + strings.Join(proposal.EvidenceRefs, ", "), "header", proposal.ID, 0}, row{"Findings: " + strings.Join(proposal.FindingRefs, ", "), "header", proposal.ID, 0})
				for _, handoff := range proposal.Handoffs {
					m.rows = append(m.rows, row{fmt.Sprintf("Handoff %s · %s/%s · proposal v%d", handoff.Target.System, handoff.Target.Repository, handoff.Target.ResourceID, handoff.ProposalVersion), "handoff", proposal.ID, 0})
				}
			}
		}
	case exportDetail:
		if p := m.exported; p != nil {
			m.rows = append(m.rows, row{"Private knowledge handoff " + p.ProposalID + " · " + p.Status, "header", p.ProposalID, 0}, row{"Target " + p.Target.System + " · " + p.Target.Repository + "/" + p.Target.ResourceID, "header", p.ProposalID, 0}, row{"Expected revision " + p.Target.ExpectedRevision + " · proposal v" + fmt.Sprint(p.ProposalVersion), "header", p.ProposalID, 0}, row{"Statement " + p.Statement + " · " + p.ClaimStatus, "header", p.ProposalID, 0}, row{"Evidence " + strings.Join(p.EvidenceRefs, ", "), "header", p.ProposalID, 0}, row{"Findings " + strings.Join(p.FindingRefs, ", "), "header", p.ProposalID, 0})
		}
	case aiList:
		for _, proposal := range m.aiPage.Proposals {
			if match(proposal.Draft.Title + " " + proposal.ID + " " + proposal.Draft.Kind) {
				m.rows = append(m.rows, row{proposal.ID + "  [" + proposal.Draft.Kind + "] " + proposal.Draft.Title, "ai-proposal", proposal.ID, 0})
			}
		}
	case aiDetail:
		m.rebuildAI()
	case runDetail:
		m.rebuildRun()
	case queryDetail:
		m.rebuildQueryRows(nil)
	}
	if m.index >= len(m.rows) {
		m.index = max(0, len(m.rows)-1)
	}
	m.ensureScroll()
}
func (m *Model) rebuildDetail() {
	switch m.parent {
	case checks:
		c := m.currentCheck()
		if c == nil {
			return
		}
		m.rows = append(m.rows, row{"Check  " + c.ID + " · " + c.Title + fmt.Sprintf(" · version %d", c.Version), "header", c.ID, 0}, row{"Inputs: " + inputSummary(c.Inputs), "header", "", 0})
		for i, s := range c.Steps {
			ref := s.QueryID
			if s.Kind == "check" {
				ref = s.CheckID
			}
			m.rows = append(m.rows, row{fmt.Sprintf("%d. %s → %s · %s", i+1, s.ID, ref, s.Kind), "step", s.ID, 0})
			for _, v := range s.Validators {
				m.rows = append(m.rows, row{"Validator " + v.ID + " · " + v.Scope + "/" + v.Kind + " · " + v.Severity, "validator", s.ID + "/" + v.ID, 1})
			}
		}
	case cases:
		c := m.currentCase()
		if c == nil {
			return
		}
		m.rows = append(m.rows, row{"Case " + c.ID + " · " + c.Title + " · " + c.Status, "header", c.ID, 0})
		keys := make([]string, 0, len(c.Context))
		for k := range c.Context {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			m.rows = append(m.rows, row{"Context " + k + " = " + describeValue(c.Context[k]), "context", k, 0})
		}
		for _, e := range c.Evidence {
			m.rows = append(m.rows, row{"Evidence " + e.ID + " · " + e.Kind + " · " + e.Ref, "evidence", e.ID, 0})
		}
		for _, f := range c.Findings {
			m.rows = append(m.rows, row{"Finding " + f.ID + " · " + f.Status + " · " + f.Text, "finding", f.ID, 0})
		}
		for _, ref := range c.IncidentRefs {
			m.rows = append(m.rows, row{"Incident " + ref, "incident", ref, 0})
		}
		for _, ref := range c.ScenarioRefs {
			m.rows = append(m.rows, row{"Scenario " + ref, "scenario", ref, 0})
		}
		for _, application := range c.ScenarioApplications {
			m.rows = append(m.rows, row{fmt.Sprintf("Application %s · %s v%d", application.ID, application.ScenarioID, application.ScenarioVersion), "application", application.ID, 0})
			for _, step := range application.Steps {
				m.rows = append(m.rows, row{fmt.Sprintf("%s · %s · evidence %s", step.StepID, step.Status, defaultString(step.EvidenceOutcome, "unknown")), "application-step", application.ID + "/" + step.StepID, 1})
			}
		}
		for _, e := range c.Timeline {
			m.rows = append(m.rows, row{"Timeline " + e.At.Format("2006-01-02 15:04") + " · " + e.Type + " · " + e.Message, "event", e.ID, 0})
		}
	case scenarios:
		s := m.currentScenario()
		if s == nil {
			return
		}
		m.rows = append(m.rows, row{"Scenario " + s.ID + " · " + s.Title, "header", s.ID, 0})
		for _, step := range s.Steps {
			m.rows = append(m.rows, row{step.ID + " · " + step.Kind + " → " + step.Ref, "scenario-step", step.ID, 0})
		}
	case validators:
		v := m.currentValidator()
		if v == nil {
			return
		}
		m.rows = append(m.rows, row{"Promoted Validator " + v.ID + " · " + v.Title, "header", v.ID, 0}, row{v.Validator.Scope + " · " + v.Validator.Kind + " · " + v.Validator.Severity, "header", v.ID, 0})
	case queryValidators:
		q := m.currentQueryValidators()
		if q == nil {
			return
		}
		m.rows = append(m.rows, row{fmt.Sprintf("Saved query %s · local Validators v%d", q.QueryID, q.Version), "header", q.QueryID, 0})
		for _, v := range q.Validators {
			m.rows = append(m.rows, row{"Validator " + v.ID + " · " + v.Scope + "/" + v.Kind + " · " + v.Severity, "validator", v.ID, 0})
		}
	}
}
func (m *Model) rebuildRun() {
	for _, r := range m.state.Runs {
		if r.ID != m.runID {
			continue
		}
		ref := r.CheckID
		if r.Kind == "query-validators" {
			ref = r.QueryID
		}
		m.rows = append(m.rows, row{"Run " + r.ID + " · " + ref + " · " + r.Environment, "header", r.ID, 0}, row{fmt.Sprintf("Execution %s · Outcome %s · Severity %s · %d ms · actor %s", r.ExecutionStatus, r.Outcome, r.Severity, r.DurationMs, r.Actor), "header", r.ID, 0})
		if r.Kind == "query-validators" && r.QueryID == m.queryID && r.Environment == m.queryEnvironment {
			for i := range r.Steps {
				step := &r.Steps[i]
				if step.Kind == "query" && step.QueryID == r.QueryID && m.queryExecution != nil && step.Execution != nil && *step.Execution == *m.queryExecution {
					m.rebuildQueryRows(step)
					break
				}
			}
		}
		for _, s := range r.Steps {
			m.addStepResult(s, 0)
		}
		break
	}
}

func (m *Model) rebuildQueryRows(step *detective.StepResult) {
	if m.queryResult == nil || m.queryResult.Result.Execution == nil || m.queryExecution == nil || *m.queryResult.Result.Execution != *m.queryExecution {
		return
	}
	result := m.queryResult
	ref := result.Result.Execution
	m.rows = append(m.rows, row{fmt.Sprintf("Saved query %s · %s · receipt %s", m.queryID, m.queryEnvironment, ref.ExecutionID), "header", ref.ExecutionID, 0})
	if result.QueryRevision != "" {
		m.rows = append(m.rows, row{"Query revision " + result.QueryRevision, "header", ref.ExecutionID, 0})
	}
	if result.ResultFingerprint != "" {
		m.rows = append(m.rows, row{"Result fingerprint " + result.ResultFingerprint, "header", ref.ExecutionID, 0})
	}
	rowCount := len(result.Result.Recordset.Rows)
	rowLabel := fmt.Sprintf("%d retained rows", rowCount)
	if result.Result.Truncated {
		rowLabel += " · truncated"
	}
	m.rows = append(m.rows, row{rowLabel, "header", ref.ExecutionID, 0})
	for index, cells := range result.Result.Recordset.Rows {
		parts := make([]string, 0, len(cells))
		for column, cell := range cells {
			name := fmt.Sprintf("column %d", column+1)
			if column < len(result.Result.Recordset.Columns) {
				name = result.Result.Recordset.Columns[column].Name
			}
			parts = append(parts, name+"="+queryCellText(cell))
		}
		label := fmt.Sprintf("Row %d · %s", index+1, strings.Join(parts, " · "))
		if step != nil {
			for _, validation := range step.Validators {
				if validation.RowIndex != nil && *validation.RowIndex == index {
					label += " · Validator " + validation.ValidatorID + " " + validation.Outcome
				}
			}
		}
		m.rows = append(m.rows, row{label, "query-row", strconv.Itoa(index), 1})
	}
}

func queryCellText(value apicontract.TypedValue) string {
	var shown string
	switch value.Type {
	case apicontract.ValueTypeBoolean:
		shown = strconv.FormatBool(value.Bool)
	case apicontract.ValueTypeNumber:
		shown = strconv.FormatFloat(value.Num, 'g', -1, 64)
	case apicontract.ValueTypeNull:
		shown = "null"
	default:
		shown = value.Str
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, shown)
}
func (m *Model) addStepResult(s detective.StepResult, depth int) {
	label := fmt.Sprintf("%s %s · %s/%s · %s · rows %d", s.Kind, s.StepID, s.ExecutionStatus, s.Outcome, s.Severity, s.RowCount)
	if s.Message != "" {
		label += " · " + s.Message
	}
	if !m.failedOnly || problematic(s.ExecutionStatus, s.Outcome) {
		m.rows = append(m.rows, row{label, "result", s.StepID, depth})
	}
	for _, v := range s.Validators {
		m.addValidation(v, depth+1)
	}
	for _, c := range s.Children {
		m.addStepResult(c, depth+1)
	}
}
func (m *Model) addValidation(v detective.ValidationResult, depth int) {
	label := fmt.Sprintf("Validator %s · %s/%s · %s", v.ValidatorID, v.ExecutionStatus, v.Outcome, v.Severity)
	if v.RowIndex != nil {
		label += fmt.Sprintf(" · row %d", *v.RowIndex+1)
	}
	if v.Message != "" {
		label += " · " + v.Message
	}
	if !m.failedOnly || problematic(v.ExecutionStatus, v.Outcome) {
		m.rows = append(m.rows, row{label, "validation", v.ValidatorID, depth})
	}
	for _, s := range v.Children {
		m.addStepResult(s, depth+1)
	}
}
func problematic(execution, outcome string) bool {
	return execution == "error" || outcome == "fail" || outcome == "unknown"
}
func (m *Model) View() tea.View {
	title := "DataTug  /  " + string(m.screen)
	if m.screen == detail {
		title += "  /  " + m.id
	}
	if m.screen == runDetail {
		title += "  /  " + m.runID
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	if m.screen == formScreen && m.form != nil {
		b.WriteString(m.formView())
	} else {
		if m.filter != "" {
			b.WriteString("Search: " + m.filter + "\n")
		}
		visible := max(1, m.height-7)
		start := min(m.offset, len(m.rows))
		end := min(len(m.rows), start+visible)
		for i := start; i < end; i++ {
			r := m.rows[i]
			mark := "  "
			if i == m.index {
				mark = "› "
			}
			line := mark + strings.Repeat("  ", min(r.depth, 8)) + r.label
			b.WriteString(ansi.Truncate(line, max(1, m.width-1), "…") + "\n")
		}
		if len(m.rows) == 0 {
			b.WriteString("No items. Ctrl+N creates one where available.\n")
		}
		if (m.screen == runDetail || m.screen == queryDetail) && len(m.rows) > 0 {
			wrapped := strings.Split(ansi.Wrap(m.rows[m.index].label, max(1, m.width-1), " "), "\n")
			b.WriteString("\n" + strings.Join(wrapped[max(0, len(wrapped)-2):], "\n") + "\n")
		}
	}
	if m.busy {
		b.WriteString("Working with project…\n")
	}
	if m.feedback != "" {
		b.WriteString("\n" + ansi.Truncate(m.feedback, max(1, m.width-1), "…") + "\n")
	}
	b.WriteString("\n" + ansi.Truncate(m.hints(), max(1, m.width-1), "…"))
	return tea.NewView(b.String())
}
func (m *Model) hints() string {
	if m.screen == formScreen {
		return "Tab next · Shift+Tab previous · Ctrl+S save · Esc cancel · type to edit"
	}
	if m.screen == detail {
		switch m.parent {
		case checks:
			return "a add step · v validator · p promote · e edit · r run · h history · Esc catalog"
		case cases:
			return "Enter Scenario step · c context · e evidence · f finding · i incidents · k knowledge · g timeline · s apply · r run"
		case scenarios:
			return "e edit · a add step · Esc catalog"
		case queryValidators:
			return "v add Validator · p promote · q run query with exact result · r fresh validation · h history"
		}
	}
	if m.screen == runDetail {
		return "↑↓ inspect nested results · F toggle failures · Enter detail · Esc back"
	}
	if m.screen == queryDetail {
		return "↑↓ inspect retained rows · Enter selected row · Esc back"
	}
	if m.screen == history {
		if m.state.RunsHasMore {
			return "Enter run · L load older runs · Esc back"
		}
		return "Enter run · Esc back"
	}
	if m.screen == timeline {
		return "↑↓ events · L older events · Esc Case"
	}
	if m.screen == incidentList {
		return "C create · L link existing · S start independent Case · Esc Case"
	}
	if m.screen == knowledgeList {
		return "N draft · Enter review · L older proposals · Esc Case"
	}
	if m.screen == knowledgeDetail {
		return "R mark reviewed · X reject · E export private record · Esc proposals"
	}
	if m.screen == exportDetail {
		return "Private portable proposal · Esc proposal"
	}
	if m.screen == aiList {
		return "N ask AI · Enter inspect proposal · L older proposals · Esc Case"
	}
	if m.screen == aiDetail {
		return "R edit draft · E selected step/Validator · A add step · V add Validator · C confirm · Esc proposals"
	}
	return "F1 Checks · F2 Detective · F3 Scenarios · F4 Reusable · F5 Query Validators · Ctrl+A AI · type to search"
}
func inputSummary(inputs []detective.Input) string {
	if len(inputs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(inputs))
	for _, in := range inputs {
		parts = append(parts, in.Name+":"+string(in.Type)+"/"+in.Cardinality)
	}
	return strings.Join(parts, ", ")
}
func (m *Model) currentCheck() *detective.Check {
	for i := range m.state.Checks {
		if m.state.Checks[i].ID == m.id {
			return &m.state.Checks[i]
		}
	}
	return nil
}
func (m *Model) currentCase() *detective.Case {
	for i := range m.state.Cases {
		if m.state.Cases[i].ID == m.id {
			return &m.state.Cases[i]
		}
	}
	return nil
}
func (m *Model) currentScenario() *detective.Scenario {
	for i := range m.state.Scenarios {
		if m.state.Scenarios[i].ID == m.id {
			return &m.state.Scenarios[i]
		}
	}
	return nil
}
func (m *Model) currentValidator() *detective.PublishedValidator {
	for i := range m.state.Validators {
		if m.state.Validators[i].ID == m.id {
			return &m.state.Validators[i]
		}
	}
	return nil
}
func (m *Model) currentQueryValidators() *detective.QueryValidators {
	for i := range m.state.QueryValidators {
		if m.state.QueryValidators[i].QueryID == m.id {
			return &m.state.QueryValidators[i]
		}
	}
	return nil
}
