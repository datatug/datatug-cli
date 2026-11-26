package dtdemo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"golang.org/x/oauth2"
)

// SaveModule returns the Save-to-GitHub root module entry for the workspace menu.
func SaveModule(cfg Config) datatugui.Module {
	cfg = cfg.normalized()
	return datatugui.Module{
		ID:       ModuleID,
		Text:     ModuleText,
		Shortcut: 'g',
		Root: func() nav.Page {
			return nav.Page{Title: ModuleText, Content: NewSaveScreen(cfg)}
		},
	}
}

type saveStep int

const (
	saveStepLoadingChanges saveStep = iota
	saveStepDisabled
	saveStepOverview
	saveStepExplain
	saveStepAuth
	saveStepCheckingFork
	saveStepConfirmFork
	saveStepCreatingFork
	saveStepSaving
	saveStepDiverged
	saveStepSuccess
	saveStepError
)

// Messages internal to saveScreen.
type (
	changesLoadedMsg struct {
		changes []demogit.Change
		err     error
	}
	saveAuthDoneMsg struct {
		token *oauth2.Token
		err   error
	}
	saveAuthCanceledMsg struct{}
	forkCheckedMsg      struct {
		fork  demogit.Repo
		login string
		err   error
	}
	forkCreatedMsg struct {
		fork    demogit.Repo
		created bool
		err     error
	}
	saveDoneMsg struct {
		result demogit.SaveResult
		err    error
	}
)

type saveScreen struct {
	cfg           Config
	w, h          int
	focused       bool
	step          saveStep
	projectFiles  []demogit.Change
	dbFiles       []demogit.Change
	changesErr    string
	authScreen    nav.Screen
	fork          demogit.Repo
	recheckedFork bool
	targetBranch  string
	altBranch     string
	result        demogit.SaveResult
	errMsg        string
	canReauth     bool
}

var (
	_ nav.Screen       = saveScreen{}
	_ nav.Titled       = saveScreen{}
	_ nav.ShortHelper  = saveScreen{}
	_ widgets.Boundary = saveScreen{}
)

// NewSaveScreen constructs the Save-to-GitHub workspace screen.
func NewSaveScreen(cfg Config) nav.Screen {
	cfg = cfg.normalized()
	step := saveStepLoadingChanges
	if !cfg.GitHubEnabled() {
		step = saveStepDisabled
	}
	return saveScreen{
		cfg:          cfg,
		w:            80,
		h:            24,
		step:         step,
		targetBranch: cfg.SaveBranch,
	}
}

// Init implements nav.Screen.
func (s saveScreen) Init() tea.Cmd {
	if s.step == saveStepDisabled {
		return nil
	}
	return s.loadChangesCmd()
}

// Title implements nav.Titled.
func (s saveScreen) Title() string { return ModuleText }

// ShortHelp implements nav.ShortHelper.
func (s saveScreen) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save/confirm")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back/cancel")),
	}
}

// AtEdge implements widgets.Boundary.
func (s saveScreen) AtEdge(dir widgets.Direction) bool {
	if s.step == saveStepAuth && s.authScreen != nil {
		if b, ok := s.authScreen.(widgets.Boundary); ok {
			return b.AtEdge(dir)
		}
	}
	return true
}

func (s saveScreen) loadChangesCmd() tea.Cmd {
	repoDir := s.cfg.RepoDir
	return func() tea.Msg {
		changes, err := listChanges(repoDir)
		return changesLoadedMsg{changes: changes, err: err}
	}
}

func (s saveScreen) checkForkCmd() tea.Cmd {
	client := s.cfg.gitClient()
	needLogin := s.cfg.Auth.Login() == ""
	return func() tea.Msg {
		ctx := context.Background()
		login := ""
		if needLogin {
			u, err := client.User(ctx)
			if err != nil {
				return forkCheckedMsg{err: err}
			}
			login = u
		}
		fork, err := client.FindFork(ctx)
		return forkCheckedMsg{fork: fork, login: login, err: err}
	}
}

func (s saveScreen) ensureForkCmd() tea.Cmd {
	client := s.cfg.gitClient()
	return func() tea.Msg {
		fork, created, err := client.EnsureFork(context.Background())
		return forkCreatedMsg{fork: fork, created: created, err: err}
	}
}

func (s saveScreen) saveCmd(fork demogit.Repo, branch string) tea.Cmd {
	client := s.cfg.gitClient()
	repoDir := s.cfg.RepoDir
	return func() tea.Msg {
		res, err := client.Save(context.Background(), repoDir, fork, demogit.SaveOptions{
			Branch: branch,
		})
		return saveDoneMsg{result: res, err: err}
	}
}

// Update implements nav.Screen.
func (s saveScreen) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.w, s.h = msg.Width, msg.Height
		if s.step == saveStepAuth && s.authScreen != nil {
			var cmd tea.Cmd
			s.authScreen, cmd = s.authScreen.Update(msg)
			return s, cmd
		}
		return s, nil
	case nav.ScreenFocusMsg:
		s.focused = msg.Focused
		if s.step == saveStepAuth && s.authScreen != nil {
			var cmd tea.Cmd
			s.authScreen, cmd = s.authScreen.Update(msg)
			return s, cmd
		}
		return s, nil
	}

	switch s.step {
	case saveStepDisabled:
		return s, nil
	case saveStepLoadingChanges:
		return s.updateLoadingChanges(msg)
	case saveStepOverview:
		return s.updateOverview(msg)
	case saveStepExplain:
		return s.updateExplain(msg)
	case saveStepAuth:
		return s.updateAuth(msg)
	case saveStepCheckingFork:
		return s.updateCheckingFork(msg)
	case saveStepConfirmFork:
		return s.updateConfirmFork(msg)
	case saveStepCreatingFork:
		return s.updateCreatingFork(msg)
	case saveStepSaving:
		return s.updateSaving(msg)
	case saveStepDiverged:
		return s.updateDiverged(msg)
	case saveStepSuccess:
		return s.updateSuccess(msg)
	default:
		return s.updateError(msg)
	}
}

func (s saveScreen) updateLoadingChanges(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if loaded, ok := msg.(changesLoadedMsg); ok {
		s.step = saveStepOverview
		if loaded.err != nil {
			s.projectFiles, s.dbFiles = nil, nil
			s.changesErr = FormatErrorFor(loaded.err, s.cfg.Canonical)
			return s, nil
		}
		s.changesErr = ""
		s.projectFiles, s.dbFiles = ClassifyChanges(s.cfg.RepoDir, loaded.changes)
		return s, nil
	}
	return s, nil
}

func (s saveScreen) updateOverview(msg tea.Msg) (nav.Screen, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "r":
		s.step = saveStepLoadingChanges
		return s, s.loadChangesCmd()
	case "enter", "s":
		if s.changesErr != "" {
			s.step = saveStepLoadingChanges
			return s, s.loadChangesCmd()
		}
		if len(s.projectFiles) == 0 && len(s.dbFiles) == 0 {
			s.errMsg = FormatErrorFor(demogit.ErrNothingToSave, s.cfg.Canonical)
			s.canReauth = false
			s.step = saveStepError
			return s, nil
		}
		s.targetBranch = s.cfg.SaveBranch
		s.recheckedFork = false
		if !s.cfg.Auth.SignedIn() {
			s.step = saveStepExplain
			return s, nil
		}
		s.step = saveStepCheckingFork
		return s, s.checkForkCmd()
	}
	return s, nil
}

func (s saveScreen) updateExplain(msg tea.Msg) (nav.Screen, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "enter", "y", "c":
		auth := dtproject.NewGitHubDeviceAuth(dtproject.GitHubAuthOptions{
			ClientID:          s.cfg.GitHubClientID,
			Scope:             GitHubScope,
			Explanation:       fmt.Sprintf("Scope: %s (fork %s and push your saved branch). Forgotten on disconnect.", GitHubScope, s.cfg.Canonical.FullName()),
			NoSecret:          true,
			OnDone:            func(tok *oauth2.Token, err error) tea.Msg { return saveAuthDoneMsg{token: tok, err: err} },
			OnCancel:          func() tea.Msg { return saveAuthCanceledMsg{} },
			RequestDeviceCode: s.cfg.RequestDeviceCode,
			PollForToken:      s.cfg.PollForToken,
			SaveToken:         discardDeviceToken,
		})
		auth, _ = auth.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
		auth, _ = auth.Update(nav.ScreenFocusMsg{Focused: s.focused})
		s.authScreen = auth
		s.step = saveStepAuth
		return s, auth.Init()
	case "esc", "n", "b":
		s.step = saveStepOverview
		return s, nil
	}
	return s, nil
}

func (s saveScreen) updateAuth(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case saveAuthCanceledMsg:
		s.authScreen = nil
		s.step = saveStepOverview
		return s, nil
	case saveAuthDoneMsg:
		s.authScreen = nil
		if msg.err != nil {
			s.errMsg = FormatErrorFor(msg.err, s.cfg.Canonical)
			s.canReauth = true
			s.step = saveStepError
			return s, nil
		}
		if msg.token == nil || msg.token.AccessToken == "" {
			s.errMsg = "GitHub sign-in did not return an access token."
			s.canReauth = true
			s.step = saveStepError
			return s, nil
		}
		s.cfg.Auth.Set(msg.token.AccessToken, "")
		s.step = saveStepCheckingFork
		return s, s.checkForkCmd()
	}
	var cmd tea.Cmd
	s.authScreen, cmd = s.authScreen.Update(msg)
	return s, cmd
}

func (s saveScreen) handleGitError(err error) saveScreen {
	s.canReauth = errors.Is(err, demogit.ErrAuth)
	if s.canReauth {
		s.cfg.Auth.Clear()
	}
	s.errMsg = FormatErrorFor(err, s.cfg.Canonical)
	s.step = saveStepError
	return s
}

func (s saveScreen) updateCheckingFork(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if res, ok := msg.(forkCheckedMsg); ok {
		if res.login != "" {
			s.cfg.Auth.SetLogin(res.login)
		}
		if errors.Is(res.err, demogit.ErrNoFork) {
			s.step = saveStepConfirmFork
			return s, nil
		}
		if res.err != nil {
			return s.handleGitError(res.err), nil
		}
		s.fork = res.fork
		s.step = saveStepSaving
		return s, s.saveCmd(s.fork, s.targetBranch)
	}
	return s, nil
}

func (s saveScreen) updateConfirmFork(msg tea.Msg) (nav.Screen, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "y", "enter":
		s.step = saveStepCreatingFork
		return s, s.ensureForkCmd()
	case "n", "esc":
		s.step = saveStepOverview
		return s, nil
	}
	return s, nil
}

func (s saveScreen) updateCreatingFork(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if res, ok := msg.(forkCreatedMsg); ok {
		if res.err != nil {
			return s.handleGitError(res.err), nil
		}
		s.fork = res.fork
		s.step = saveStepSaving
		return s, s.saveCmd(s.fork, s.targetBranch)
	}
	return s, nil
}

func (s saveScreen) updateSaving(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if res, ok := msg.(saveDoneMsg); ok {
		if errors.Is(res.err, demogit.ErrNoFork) && !s.recheckedFork {
			s.recheckedFork = true
			s.fork = demogit.Repo{}
			s.step = saveStepCheckingFork
			return s, s.checkForkCmd()
		}
		if errors.Is(res.err, demogit.ErrDiverged) {
			branch := s.cfg.gitClient().NewBranchName()
			if branch == "" {
				branch = fmt.Sprintf("%s-%s", s.cfg.SaveBranch, nowFunc().UTC().Format("20060102-150405"))
			}
			s.altBranch = branch
			s.step = saveStepDiverged
			return s, nil
		}
		if res.err != nil {
			return s.handleGitError(res.err), nil
		}
		s.result = res.result
		s.step = saveStepSuccess
		return s, nil
	}
	return s, nil
}

func (s saveScreen) updateDiverged(msg tea.Msg) (nav.Screen, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "y", "enter":
		s.targetBranch = s.altBranch
		s.step = saveStepSaving
		return s, s.saveCmd(s.fork, s.targetBranch)
	case "n", "esc":
		s.step = saveStepOverview
		return s, nil
	}
	return s, nil
}

func (s saveScreen) updateSuccess(msg tea.Msg) (nav.Screen, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "enter", "r", "esc":
		s.step = saveStepLoadingChanges
		return s, s.loadChangesCmd()
	}
	return s, nil
}

func (s saveScreen) updateError(msg tea.Msg) (nav.Screen, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "enter", "s":
		if s.canReauth {
			s.step = saveStepExplain
			return s, nil
		}
		s.step = saveStepLoadingChanges
		return s, s.loadChangesCmd()
	case "r":
		s.step = saveStepLoadingChanges
		return s, s.loadChangesCmd()
	case "esc", "b":
		s.step = saveStepOverview
		return s, nil
	}
	return s, nil
}

// View implements nav.Screen.
func (s saveScreen) View() string {
	if s.step == saveStepAuth && s.authScreen != nil {
		return s.authScreen.View()
	}

	w := max(24, s.w)
	inner := max(20, w-2)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.AccentColor())
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor())
	mutedStyle := lipgloss.NewStyle().Foreground(theme.MutedColor())
	okStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Green)
	errStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.ErrorColor())

	var lines []string
	switch s.step {
	case saveStepDisabled:
		lines = append(lines, titleStyle.Render(ModuleText), "")
		for _, l := range strings.Split(ansi.Wrap(SaveNotConfiguredNotice, inner, " "), "\n") {
			lines = append(lines, mutedStyle.Render(l))
		}

	case saveStepLoadingChanges:
		lines = append(lines, titleStyle.Render(ModuleText), "", "Checking workspace changes...")

	case saveStepOverview:
		lines = append(lines, titleStyle.Render(ModuleText))
		status := "Not signed in to GitHub"
		if s.cfg.Auth.SignedIn() {
			status = "Signed in to GitHub"
			if login := s.cfg.Auth.Login(); login != "" {
				status += " as @" + login
			}
		}
		lines = append(lines,
			mutedStyle.Render(fmt.Sprintf("Target: fork of %s (branch %s)", s.cfg.Canonical.FullName(), s.cfg.SaveBranch)),
			mutedStyle.Render(status),
			"",
		)
		if s.changesErr != "" {
			for _, l := range strings.Split(ansi.Wrap(s.changesErr, inner, " "), "\n") {
				lines = append(lines, errStyle.Render(l))
			}
			lines = append(lines, "", theme.WorkspaceKeyHint("r")+" retry")
		} else if len(s.projectFiles) == 0 && len(s.dbFiles) == 0 {
			lines = append(lines, "Nothing to save: the workspace has no changes.", "")
			for _, l := range strings.Split(ansi.Wrap(ExternalDatabasesNotice, inner, " "), "\n") {
				lines = append(lines, mutedStyle.Render(l))
			}
			lines = append(lines, "", theme.WorkspaceKeyHint("r")+" refresh")
		} else {
			lines = append(lines, sectionStyle.Render(fmt.Sprintf("Project files (%d)", len(s.projectFiles))))
			if len(s.projectFiles) == 0 {
				lines = append(lines, mutedStyle.Render("  (none)"))
			} else {
				for _, ch := range s.projectFiles {
					lines = append(lines, fmt.Sprintf("  [%s] %s", ch.Kind, ch.Path))
				}
			}
			lines = append(lines, "", sectionStyle.Render(fmt.Sprintf("Database files (%d)", len(s.dbFiles))))
			if len(s.dbFiles) == 0 {
				lines = append(lines, mutedStyle.Render("  (none)"))
			} else {
				for _, ch := range s.dbFiles {
					lines = append(lines, fmt.Sprintf("  [%s] %s", ch.Kind, ch.Path))
				}
			}
			lines = append(lines, "")
			for _, l := range strings.Split(ansi.Wrap(ExternalDatabasesNotice, inner, " "), "\n") {
				lines = append(lines, mutedStyle.Render(l))
			}
			action := "save to GitHub"
			if !s.cfg.Auth.SignedIn() {
				action = "sign in and save to GitHub"
			}
			lines = append(lines, "", theme.WorkspaceKeyHint("Enter")+" "+action+"   "+theme.WorkspaceKeyHint("r")+" refresh")
		}

	case saveStepExplain:
		lines = append(lines, titleStyle.Render("Sign in to GitHub"), "")
		lines = append(lines, strings.Split(ansi.Wrap(ScopeExplanation(s.cfg.Canonical), inner, " "), "\n")...)
		lines = append(lines, "",
			theme.WorkspaceKeyHint("Enter")+" continue to GitHub sign-in   "+
				theme.WorkspaceKeyHint("Esc")+" cancel",
		)

	case saveStepCheckingFork:
		lines = append(lines, titleStyle.Render(ModuleText), "",
			sectionStyle.Render(fmt.Sprintf("Looking for your fork of %s...", s.cfg.Canonical.FullName())),
		)

	case saveStepConfirmFork:
		lines = append(lines, titleStyle.Render("Create a GitHub fork"), "",
			fmt.Sprintf("Create a fork of %s in your account?", s.cfg.Canonical.FullName()),
			"",
		)
		for _, l := range strings.Split(ansi.Wrap("Your workspace changes will be pushed to branch "+s.targetBranch+" on your new fork. The canonical repository is never modified.", inner, " "), "\n") {
			lines = append(lines, mutedStyle.Render(l))
		}
		lines = append(lines, "",
			theme.WorkspaceKeyHint("y/Enter")+" create fork and save   "+theme.WorkspaceKeyHint("n/Esc")+" cancel",
		)

	case saveStepCreatingFork:
		lines = append(lines, titleStyle.Render(ModuleText), "",
			sectionStyle.Render(fmt.Sprintf("Creating your fork of %s...", s.cfg.Canonical.FullName())),
		)

	case saveStepSaving:
		lines = append(lines, titleStyle.Render(ModuleText), "",
			sectionStyle.Render(fmt.Sprintf("Saving changes to %s (branch %s)...", s.fork.FullName(), s.targetBranch)),
		)

	case saveStepDiverged:
		lines = append(lines, titleStyle.Render("Branch has diverged"), "")
		msg := fmt.Sprintf(
			"The branch %s on %s has changes this workspace does not have. DataTug never force-pushes.",
			s.targetBranch, s.fork.FullName(),
		)
		for _, l := range strings.Split(ansi.Wrap(msg, inner, " "), "\n") {
			lines = append(lines, errStyle.Render(l))
		}
		lines = append(lines, "",
			fmt.Sprintf("Save to new branch %s?", s.altBranch),
			"",
			theme.WorkspaceKeyHint("y/Enter")+" save to new branch "+s.altBranch+"   "+theme.WorkspaceKeyHint("n/Esc")+" cancel",
		)

	case saveStepSuccess:
		lines = append(lines, okStyle.Render("Saved to GitHub"), "",
			fmt.Sprintf("Repository: %s", s.result.Fork.FullName()),
			fmt.Sprintf("Branch:     %s", s.result.Branch),
			fmt.Sprintf("Commit:     %s", s.result.CommitURL),
			"",
			theme.WorkspaceKeyHint("Enter/r")+" refresh changes",
		)

	case saveStepError:
		lines = append(lines, titleStyle.Render(ModuleText), "")
		for _, l := range strings.Split(ansi.Wrap(s.errMsg, inner, " "), "\n") {
			lines = append(lines, errStyle.Render(l))
		}
		if s.canReauth {
			lines = append(lines, "",
				theme.WorkspaceKeyHint("Enter/s")+" sign in again   "+theme.WorkspaceKeyHint("Esc")+" back",
			)
		} else {
			lines = append(lines, "",
				theme.WorkspaceKeyHint("Enter/r")+" refresh   "+theme.WorkspaceKeyHint("Esc")+" back",
			)
		}
	}

	return widgets.Fit(strings.Join(lines, "\n"), w, s.h)
}
