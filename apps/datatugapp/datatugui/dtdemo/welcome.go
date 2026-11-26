package dtdemo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"golang.org/x/oauth2"
)

// WelcomeOutcome describes how the visitor left the demo welcome screen.
type WelcomeOutcome int

const (
	// OutcomeQuit means the visitor quit before entering the workspace.
	OutcomeQuit WelcomeOutcome = iota
	// OutcomeExplore means the visitor chose to explore the fresh demo template.
	OutcomeExplore
	// OutcomeOpenedFork means the visitor signed in and opened their fork into RepoDir.
	OutcomeOpenedFork
)

// WelcomeResult is the outcome of the welcome screen.
type WelcomeResult struct {
	Outcome WelcomeOutcome
	Fork    demogit.Repo
	Branch  string
}

type welcomeStep int

const (
	welcomeStepChoose welcomeStep = iota
	welcomeStepExplain
	welcomeStepAuth
	welcomeStepFindingFork
	welcomeStepOpening
	welcomeStepError
)

// Messages internal to WelcomeModel.
type (
	welcomeAuthDoneMsg struct {
		token *oauth2.Token
		err   error
	}
	welcomeAuthCanceledMsg struct{}
	welcomeForkFoundMsg    struct {
		fork demogit.Repo
		err  error
	}
	welcomeOpenDoneMsg struct {
		branch string
		err    error
	}
)

var welcomeChoices = []struct{ key, title, detail string }{
	{"1", "Explore the demo project", "Start immediately in a temporary workspace with local SQLite and inGitDB catalogs. No sign-in required."},
	{"2", "Open my GitHub project", "Sign in to GitHub to reopen the saved branch from your fork of the demo repository."},
}

// WelcomeModel is the Bubble Tea model shown at the start of an SSH demo session
// before the full DataTug workspace launches.
type WelcomeModel struct {
	cfg        Config
	step       welcomeStep
	index      int
	width      int
	height     int
	authScreen nav.Screen
	fork       demogit.Repo
	errMsg     string
	noFork     bool
	result     WelcomeResult
}

var _ tea.Model = WelcomeModel{}

// NewWelcomeModel creates the initial welcome screen model.
func NewWelcomeModel(cfg Config) WelcomeModel {
	return WelcomeModel{
		cfg:    cfg.normalized(),
		step:   welcomeStepChoose,
		width:  80,
		height: 24,
	}
}

// Result returns the visitor's outcome once the model quits.
func (m WelcomeModel) Result() WelcomeResult { return m.result }

// Init implements tea.Model.
func (m WelcomeModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m WelcomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = sz.Width, sz.Height
		if m.step == welcomeStepAuth && m.authScreen != nil {
			var cmd tea.Cmd
			m.authScreen, cmd = m.authScreen.Update(sz)
			return m, cmd
		}
		return m, nil
	}

	switch m.step {
	case welcomeStepChoose:
		return m.updateChoose(msg)
	case welcomeStepExplain:
		return m.updateExplain(msg)
	case welcomeStepAuth:
		return m.updateAuth(msg)
	case welcomeStepFindingFork:
		return m.updateFindingFork(msg)
	case welcomeStepOpening:
		return m.updateOpening(msg)
	default:
		return m.updateError(msg)
	}
}

func (m WelcomeModel) updateChoose(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		m.index = 0
	case "down", "j", "tab":
		if m.cfg.GitHubEnabled() {
			m.index = 1
		}
	case "1":
		m.result = WelcomeResult{Outcome: OutcomeExplore}
		return m, tea.Quit
	case "2":
		if !m.cfg.GitHubEnabled() {
			return m, nil
		}
		m.index = 1
		return m.startOpenGitHub()
	case "enter":
		if m.index == 0 || !m.cfg.GitHubEnabled() {
			m.result = WelcomeResult{Outcome: OutcomeExplore}
			return m, tea.Quit
		}
		return m.startOpenGitHub()
	case "q", "ctrl+c":
		m.result = WelcomeResult{Outcome: OutcomeQuit}
		return m, tea.Quit
	}
	return m, nil
}

func (m WelcomeModel) startOpenGitHub() (tea.Model, tea.Cmd) {
	if m.cfg.Auth.SignedIn() {
		m.step = welcomeStepFindingFork
		return m, m.findForkCmd()
	}
	m.step = welcomeStepExplain
	return m, nil
}

func (m WelcomeModel) updateExplain(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "enter", "y", "c":
		auth := dtproject.NewGitHubDeviceAuth(dtproject.GitHubAuthOptions{
			ClientID:          m.cfg.GitHubClientID,
			Scope:             GitHubScope,
			Explanation:       fmt.Sprintf("Scope: %s (fork %s and read/save your branch). Forgotten on disconnect.", GitHubScope, m.cfg.Canonical.FullName()),
			NoSecret:          true,
			OnDone:            func(tok *oauth2.Token, err error) tea.Msg { return welcomeAuthDoneMsg{token: tok, err: err} },
			OnCancel:          func() tea.Msg { return welcomeAuthCanceledMsg{} },
			RequestDeviceCode: m.cfg.RequestDeviceCode,
			PollForToken:      m.cfg.PollForToken,
			SaveToken:         m.cfg.SaveToken,
		})
		auth, _ = auth.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		auth, _ = auth.Update(nav.ScreenFocusMsg{Focused: true})
		m.authScreen = auth
		m.step = welcomeStepAuth
		return m, auth.Init()
	case "esc", "b", "n":
		m.step = welcomeStepChoose
		return m, nil
	case "ctrl+c":
		m.result = WelcomeResult{Outcome: OutcomeQuit}
		return m, tea.Quit
	}
	return m, nil
}

func (m WelcomeModel) updateAuth(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case welcomeAuthCanceledMsg:
		m.authScreen = nil
		m.step = welcomeStepChoose
		return m, nil
	case welcomeAuthDoneMsg:
		m.authScreen = nil
		if msg.err != nil {
			m.errMsg = FormatErrorFor(msg.err, m.cfg.Canonical)
			m.noFork = false
			m.step = welcomeStepError
			return m, nil
		}
		if msg.token == nil || msg.token.AccessToken == "" {
			m.errMsg = "GitHub sign-in did not return an access token."
			m.noFork = false
			m.step = welcomeStepError
			return m, nil
		}
		m.cfg.Auth.Set(msg.token.AccessToken, "")
		m.step = welcomeStepFindingFork
		return m, m.findForkCmd()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.result = WelcomeResult{Outcome: OutcomeQuit}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.authScreen, cmd = m.authScreen.Update(msg)
	return m, cmd
}

func (m WelcomeModel) findForkCmd() tea.Cmd {
	client := m.cfg.gitClient()
	return func() tea.Msg {
		fork, err := client.FindFork(context.Background())
		return welcomeForkFoundMsg{fork: fork, err: err}
	}
}

func (m WelcomeModel) updateFindingFork(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case welcomeForkFoundMsg:
		if msg.err != nil {
			if errors.Is(msg.err, demogit.ErrAuth) {
				m.cfg.Auth.Clear()
			}
			m.noFork = errors.Is(msg.err, demogit.ErrNoFork)
			m.errMsg = FormatErrorFor(msg.err, m.cfg.Canonical)
			m.step = welcomeStepError
			return m, nil
		}
		m.fork = msg.fork
		m.step = welcomeStepOpening
		return m, m.openForkCmd(msg.fork)
	case tea.KeyPressMsg:
		if msg.String() == "esc" {
			m.step = welcomeStepChoose
			return m, nil
		}
		if msg.String() == "ctrl+c" {
			m.result = WelcomeResult{Outcome: OutcomeQuit}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m WelcomeModel) openForkCmd(fork demogit.Repo) tea.Cmd {
	client := m.cfg.gitClient()
	destDir := m.cfg.RepoDir
	projectDir := m.cfg.ProjectDir
	return func() tea.Msg {
		branch, err := client.Open(context.Background(), fork, destDir, projectDir)
		return welcomeOpenDoneMsg{branch: branch, err: err}
	}
}

func (m WelcomeModel) updateOpening(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case welcomeOpenDoneMsg:
		if msg.err != nil {
			if errors.Is(msg.err, demogit.ErrAuth) {
				m.cfg.Auth.Clear()
			}
			m.noFork = false
			m.errMsg = FormatErrorFor(msg.err, m.cfg.Canonical)
			m.step = welcomeStepError
			return m, nil
		}
		m.result = WelcomeResult{
			Outcome: OutcomeOpenedFork,
			Fork:    m.fork,
			Branch:  msg.branch,
		}
		return m, tea.Quit
	case tea.KeyPressMsg:
		if msg.String() == "esc" {
			m.step = welcomeStepChoose
			return m, nil
		}
		if msg.String() == "ctrl+c" {
			m.result = WelcomeResult{Outcome: OutcomeQuit}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m WelcomeModel) updateError(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "enter", "1", "e":
		m.result = WelcomeResult{Outcome: OutcomeExplore}
		return m, tea.Quit
	case "esc", "b":
		m.step = welcomeStepChoose
		return m, nil
	case "q", "ctrl+c":
		m.result = WelcomeResult{Outcome: OutcomeQuit}
		return m, tea.Quit
	}
	return m, nil
}

// View implements tea.Model.
func (m WelcomeModel) View() tea.View {
	var body string
	if m.step == welcomeStepAuth && m.authScreen != nil {
		body = m.authScreen.View()
	} else {
		body = m.renderBody()
	}
	v := tea.NewView(body)
	v.AltScreen = true
	return v
}

func (m WelcomeModel) renderBody() string {
	width := max(24, m.width)
	inner := max(20, min(width-4, 76))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.AccentColor())
	mutedStyle := lipgloss.NewStyle().Foreground(theme.MutedColor())
	chosenStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor())
	errStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.ErrorColor())

	var lines []string
	switch m.step {
	case welcomeStepChoose:
		lines = append(lines, "", titleStyle.Render("Welcome to the DataTug demo"))
		sub := "Work in a temporary DataTug workspace right in your terminal."
		if !m.cfg.Deadline.IsZero() {
			sub += fmt.Sprintf(" Session ends %s.", m.cfg.Deadline.UTC().Format("15:04 UTC"))
		}
		for _, l := range strings.Split(ansi.Wrap(sub, inner, " "), "\n") {
			lines = append(lines, mutedStyle.Render(l))
		}
		lines = append(lines, "")
		choices := welcomeChoices
		if !m.cfg.GitHubEnabled() {
			choices = welcomeChoices[:1]
		}
		for i, c := range choices {
			marker, style := "  ", lipgloss.NewStyle().Bold(true)
			if i == m.index {
				marker, style = "▸ ", chosenStyle
			}
			lines = append(lines, style.Render(fmt.Sprintf("%s[%s] %s", marker, c.key, c.title)))
			for _, d := range strings.Split(ansi.Wrap(c.detail, inner-4, " "), "\n") {
				lines = append(lines, mutedStyle.Render("    "+d))
			}
			lines = append(lines, "")
		}
		if m.cfg.GitHubEnabled() {
			lines = append(lines,
				theme.WorkspaceKeyHint("↑↓")+" choose   "+
					theme.WorkspaceKeyHint("Enter")+" select   "+
					theme.WorkspaceKeyHint("1/2")+" pick   "+
					theme.WorkspaceKeyHint("q")+" quit",
			)
		} else {
			lines = append(lines,
				theme.WorkspaceKeyHint("Enter")+" select   "+
					theme.WorkspaceKeyHint("1")+" pick   "+
					theme.WorkspaceKeyHint("q")+" quit",
			)
		}

	case welcomeStepExplain:
		lines = append(lines, "", titleStyle.Render("Sign in to GitHub"), "")
		for _, l := range strings.Split(ansi.Wrap(ScopeExplanation(m.cfg.Canonical), inner, " "), "\n") {
			lines = append(lines, l)
		}
		lines = append(lines, "",
			theme.WorkspaceKeyHint("Enter")+" continue to GitHub sign-in   "+
				theme.WorkspaceKeyHint("Esc")+" back to welcome",
		)

	case welcomeStepFindingFork:
		lines = append(lines, "", titleStyle.Render("Open my GitHub project"), "",
			chosenStyle.Render(fmt.Sprintf("Looking for your fork of %s...", m.cfg.Canonical.FullName())),
			"", theme.WorkspaceKeyHint("Esc")+" cancel",
		)

	case welcomeStepOpening:
		lines = append(lines, "", titleStyle.Render("Open my GitHub project"), "",
			chosenStyle.Render(fmt.Sprintf("Opening %s into your temporary workspace...", m.fork.FullName())),
			"", theme.WorkspaceKeyHint("Esc")+" cancel",
		)

	case welcomeStepError:
		lines = append(lines, "", titleStyle.Render("Could not open your GitHub project"), "")
		for _, l := range strings.Split(ansi.Wrap(m.errMsg, inner, " "), "\n") {
			lines = append(lines, errStyle.Render(l))
		}
		if m.noFork {
			lines = append(lines, "")
			for _, l := range strings.Split(ansi.Wrap("You can explore the demo project now and use Save to GitHub from the module menu to create a fork later.", inner, " "), "\n") {
				lines = append(lines, mutedStyle.Render(l))
			}
		}
		lines = append(lines, "",
			theme.WorkspaceKeyHint("Enter")+" explore the demo project   "+
				theme.WorkspaceKeyHint("Esc")+" back to welcome   "+
				theme.WorkspaceKeyHint("q")+" quit",
		)
	}

	for i, line := range lines {
		lines[i] = ansi.Truncate("  "+line, width, "…")
	}
	if m.height > 0 && len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

// runWelcomeProgram runs the welcome Bubble Tea program; replaced in tests.
var runWelcomeProgram = func(m WelcomeModel, opts ...tea.ProgramOption) (WelcomeModel, error) {
	final, err := tea.NewProgram(m, opts...).Run()
	if err != nil {
		return WelcomeModel{}, err
	}
	return final.(WelcomeModel), nil
}

// RunWelcome displays the demo welcome screen and returns the visitor's choice.
func RunWelcome(cfg Config, opts ...tea.ProgramOption) (WelcomeResult, error) {
	final, err := runWelcomeProgram(NewWelcomeModel(cfg), opts...)
	if err != nil {
		return WelcomeResult{}, err
	}
	return final.Result(), nil
}
