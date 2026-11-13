package commands

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/strongo/deviceauth"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

// aiCommand opens the AI setup dialog from the composer.
const aiCommand = chat.AISetupCommand

// aiSetupAdvice is the one way AI is set up, said wherever AI is missing. It
// never sends a person out of the workspace.
const aiSetupAdvice = "To add AI, type " + aiCommand + ": choose DataTug AI (free, limited), DataTug Pro, your own API key, or your own endpoint. Nothing is sent anywhere until you choose."

// aiConsentLines is what a person agrees to before DataTug AI is used for a
// project. It is shown every time the choice is made, never implied.
var aiConsentLines = []string{
	"With DataTug AI, each question you ask leaves this machine. Sent to DataTug's",
	"cloud service, and from there to the AI model provider it uses:",
	"",
	"  • your questions and the conversation so far",
	"  • the names and types of this project's tables and columns",
	"  • data read from the database to answer them",
	"  • your DataTug account, an installation ID, and this app's version and OS",
	"",
	"Columns labelled sensitive are never read. Your database password and",
	"connection details are not sent. Unless telemetry is off, a usage report per",
	"question (its length and outcome, never its text) goes to DataTug.",
	"",
	"The free plan is for trying DataTug: a limited number of questions a month.",
	"DataTug Pro raises that limit. Your own API key or endpoint has no DataTug",
	"limit and sends nothing to DataTug.",
}

// aiProvider is a provider a person can bring their own key to.
type aiProvider struct {
	family, title, model string
}

// aiProviders lists the providers of "your own API key". A model is prefilled
// only where this CLI documents one; the person can always change it.
var aiProviders = []aiProvider{
	{"openai", "OpenAI", defaultChatModel},
	{"anthropic", "Anthropic", "claude-haiku"},
	{"gemini", "Google Gemini", ""},
	{"mistral", "Mistral", ""},
	{"xai", "xAI", ""},
	{"openrouter", "OpenRouter", ""},
}

const aiDefaultEndpoint = "http://localhost:11434/v1"

// aiSetupDoors are the outside world of the dialog; tests replace them.
type aiSetupDoors struct {
	// signIn returns the saved DataTug login, or runs the browser sign-in,
	// writing its one-time code and URL to out.
	signIn      func(ctx context.Context, out io.Writer) (email string, err error)
	plan        func(ctx context.Context) (cloudPlan, error)
	openBrowser func(string) error
	saveChoice  func(projectDir string, choice aiChoice) error
	saveKey     func(account, key string) error
	now         func() time.Time
}

// aiEnsureSignedIn is device.EnsureSignedIn; a seam so no test starts a real sign-in.
var aiEnsureSignedIn = device.EnsureSignedIn

var newAISetupDoors = func(options chatOptions) aiSetupDoors {
	return aiSetupDoors{
		signIn: func(ctx context.Context, out io.Writer) (string, error) {
			session, err := aiEnsureSignedIn(ctx, device.SignInOptions{Insecure: options.insecureStorage, Interactive: true, Out: out, ErrOut: out})
			return session.Email, err
		},
		plan: func(ctx context.Context) (cloudPlan, error) {
			session, err := loadCloudSession(ctx, chatOptions{insecureStorage: options.insecureStorage})
			if err != nil {
				return cloudPlan{}, err
			}
			plan, err := session.getPlan(ctx, "")
			if err != nil {
				return cloudPlan{}, err
			}
			return plan, plan.requirePersonalFreeOrPro()
		},
		openBrowser: deviceauth.OpenBrowser,
		saveChoice:  writeAIChoice,
		saveKey:     func(account, key string) error { return aiKeySet(aiKeyringService, account, key) },
		now:         time.Now,
	}
}

type aiStep int

const (
	aiStepMenu aiStep = iota
	aiStepConsent
	aiStepSignIn
	aiStepPlan
	aiStepKey
	aiStepEndpoint
)

// The menu's entries, in order.
const (
	aiMenuFree = iota
	aiMenuPro
	aiMenuKey
	aiMenuEndpoint
	aiMenuNone
)

var aiMenu = []struct{ title, detail string }{
	{"DataTug AI · free", "no key needed · a limited number of questions a month · sign in with your browser"},
	{"DataTug Pro", "a larger monthly AI allowance · subscribe in your browser"},
	{"Your own API key", "OpenAI, Anthropic, Gemini and others · billed by your provider"},
	{"Your own endpoint", "Ollama, LM Studio or any OpenAI-compatible URL · can stay on this machine"},
	{"No AI", "open tables and run queries yourself"},
}

// aiSetupDialog chooses the AI of one project without leaving the workspace.
type aiSetupDialog struct {
	ui           *chat.ChatUI
	doors        aiSetupDoors
	state        *aiRunState
	ctx          context.Context
	projectDir   string
	projectTitle string
	pending      string // the question that opened the dialog, put back afterwards
	current      string // what answers questions now, in words

	step    aiStep
	index   int  // the menu entry
	wantPro bool // DataTug Pro was chosen: the plan step leads with the upgrade
	problem string

	// Sign-in.
	attempt int // counts sign-ins: the result of an abandoned one is dropped
	cancel  context.CancelFunc
	events  map[int]chan tea.Msg // the channel of the current sign-in, by attempt
	lines   []string
	email   string
	plan    cloudPlan

	// The forms of "own key" and "own endpoint".
	provider int
	field    int
	values   [3]string // (provider), model, key | base URL, model, key
}

type (
	aiSignInLine struct {
		attempt int
		text    string
	}
	aiSignedIn struct {
		attempt int // the sign-in this is the result of
		email   string
		plan    cloudPlan
		err     error
	}
)

func newAISetupDialog(ctx context.Context, ui *chat.ChatUI, options chatOptions, projectDir, projectTitle, current, pending string) *aiSetupDialog {
	return &aiSetupDialog{ui: ui, doors: newAISetupDoors(options), state: options.aiState, ctx: ctx, projectDir: projectDir, projectTitle: projectTitle, current: current, pending: pending}
}

func (d *aiSetupDialog) WorkspaceBorderlessCard() bool { return true }

// --- update ---------------------------------------------------------------

func (d *aiSetupDialog) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case aiSignInLine:
		if msg.attempt == d.attempt {
			d.lines = append(d.lines, msg.text)
		}
		return d, d.nextEvent(msg.attempt), false
	case aiSignedIn:
		return d.signedIn(msg)
	case tea.PasteMsg:
		d.typeText(msg.Content)
		return d, nil, false
	case tea.KeyPressMsg:
		return d.key(msg)
	}
	return d, nil, false
}

func (d *aiSetupDialog) key(key tea.KeyPressMsg) (chatshell.Overlay, tea.Cmd, bool) {
	name := key.String()
	switch name {
	case "esc":
		return d.back()
	case "ctrl+c":
		d.stopSignIn()
		return d, tea.Quit, true
	}
	d.problem = ""
	switch d.step {
	case aiStepMenu:
		switch name {
		case "up", "k":
			d.index = max(0, d.index-1)
		case "down", "j":
			d.index = min(len(aiMenu)-1, d.index+1)
		case "1", "2", "3", "4", "5":
			d.index = int(name[0] - '1')
			return d.choose()
		case "enter":
			return d.choose()
		}
	case aiStepConsent:
		switch name {
		case "y", "Y": // never Enter: a second Enter from the menu is not consent
			d.step, d.lines = aiStepSignIn, nil
			return d, d.startSignIn(), false
		case "n", "N":
			return d.back()
		}
	case aiStepPlan:
		switch name {
		case "enter":
			return d.finish(aiChoice{Kind: aiKindHosted, ConsentAt: d.doors.now().UTC().Format(time.RFC3339)}, "")
		case "u", "U":
			return d.upgrade()
		case "r", "R":
			d.step, d.lines = aiStepSignIn, nil
			return d, d.startSignIn(), false
		}
	case aiStepKey, aiStepEndpoint:
		return d.formKey(key)
	}
	return d, nil, false
}

// back leaves the current step: to the menu, or out of the dialog from it.
func (d *aiSetupDialog) back() (chatshell.Overlay, tea.Cmd, bool) {
	d.stopSignIn()
	if d.step == aiStepMenu {
		return d, nil, true
	}
	d.step, d.problem = aiStepMenu, ""
	return d, nil, false
}

// stopSignIn abandons the sign-in in progress, if any.
func (d *aiSetupDialog) stopSignIn() {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

func (d *aiSetupDialog) choose() (chatshell.Overlay, tea.Cmd, bool) {
	d.field, d.values = 0, [3]string{}
	switch d.index {
	case aiMenuFree, aiMenuPro:
		d.wantPro = d.index == aiMenuPro
		d.step = aiStepConsent
	case aiMenuKey:
		d.step = aiStepKey
		d.values[1] = aiProviders[d.provider].model
	case aiMenuEndpoint:
		d.step = aiStepEndpoint
		d.values[0] = aiDefaultEndpoint
	case aiMenuNone:
		return d.finish(aiChoice{Kind: aiKindNone}, "")
	}
	return d, nil, false
}

// startSignIn signs in and reads the plan in the background. What the sign-in
// prints (the one-time code and the URL) arrives line by line.
func (d *aiSetupDialog) startSignIn() tea.Cmd {
	d.stopSignIn()
	ctx, cancel := context.WithCancel(d.ctx)
	d.cancel = cancel
	d.attempt++
	attempt := d.attempt
	// Each sign-in has its own channel, read by its own commands only.
	events := make(chan tea.Msg, 64)
	doors := d.doors
	go func() {
		email, err := doors.signIn(ctx, aiLineWriter{events: events, attempt: attempt})
		var plan cloudPlan
		if err == nil {
			plan, err = doors.plan(ctx)
		}
		events <- aiSignedIn{attempt: attempt, email: email, plan: plan, err: err}
	}()
	d.events = map[int]chan tea.Msg{attempt: events}
	return d.nextEvent(attempt)
}

// nextEvent waits for the next thing the sign-in says.
func (d *aiSetupDialog) nextEvent(attempt int) tea.Cmd {
	events, ok := d.events[attempt]
	if !ok {
		return nil // an abandoned sign-in: nothing waits for it
	}
	return func() tea.Msg { return chat.OverlayMsg{Overlay: d, Msg: <-events} }
}

// aiLineWriter turns what the sign-in prints into lines of the dialog. A line
// that does not fit is dropped rather than blocking the sign-in.
type aiLineWriter struct {
	events  chan<- tea.Msg
	attempt int
}

func (w aiLineWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			select {
			case w.events <- aiSignInLine{attempt: w.attempt, text: line}:
			default:
			}
		}
	}
	return len(p), nil
}

func (d *aiSetupDialog) signedIn(msg aiSignedIn) (chatshell.Overlay, tea.Cmd, bool) {
	if msg.attempt != d.attempt || d.step != aiStepSignIn {
		return d, nil, false // the person went back, or started again, meanwhile
	}
	d.stopSignIn() // it is over: release its context
	if msg.err != nil {
		d.step = aiStepMenu
		d.problem = "DataTug AI could not be set up: " + aiPublicError(msg.err) + ". Try again, or choose another way."
		return d, nil, false
	}
	d.step, d.email, d.plan = aiStepPlan, msg.email, msg.plan
	return d, nil, false
}

// aiPublicError is an error on one line, without the advice to run a command
// outside: the dialog itself is the way to sign in.
func aiPublicError(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	for _, outside := range []string{"; run 'datatug auth login'", " requires 'datatug auth login'"} {
		text = strings.ReplaceAll(text, outside, "")
	}
	return strings.TrimSuffix(text, ".")
}

// upgrade opens the subscription page of the signed-in account.
func (d *aiSetupDialog) upgrade() (chatshell.Overlay, tea.Cmd, bool) {
	link := d.plan.UpgradeURL
	if d.plan.EffectivePlan == "pro" {
		link = d.plan.ManageURL
	}
	if parsed, err := url.Parse(link); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		d.problem = "This account has no subscription page to open."
		return d, nil, false
	}
	if err := d.doors.openBrowser(link); err != nil {
		d.problem = "The browser did not open. Open this page yourself: " + link
		return d, nil, false
	}
	d.problem = "The subscription page is open in your browser. When you are done there, press R to refresh your plan."
	return d, nil, false
}

// form describes the fields of the step: their labels and which hide input.
func (d *aiSetupDialog) form() (labels []string, secret int) {
	if d.step == aiStepKey {
		return []string{"Provider", "Model", "API key"}, 2
	}
	return []string{"Base URL", "Model", "API key (optional)"}, 2
}

func (d *aiSetupDialog) formKey(key tea.KeyPressMsg) (chatshell.Overlay, tea.Cmd, bool) {
	labels, _ := d.form()
	switch key.String() {
	case "up", "shift+tab":
		d.field = max(0, d.field-1)
	case "down", "tab":
		d.field = min(len(labels)-1, d.field+1)
	case "left", "right":
		// Only on the provider's own row: an arrow pressed on the key must
		// never change where that key is sent.
		if d.providerRow() {
			step := 1
			if key.String() == "left" {
				step = len(aiProviders) - 1
			}
			known := d.values[1] == aiProviders[d.provider].model
			d.provider = (d.provider + step) % len(aiProviders)
			if known {
				// A model the person typed themself is kept.
				d.values[1] = aiProviders[d.provider].model
			}
		}
	case "backspace":
		if d.providerRow() {
			break
		}
		_, size := utf8.DecodeLastRuneInString(d.values[d.field])
		d.values[d.field] = d.values[d.field][:len(d.values[d.field])-size]
	case "enter":
		if d.field < len(labels)-1 {
			d.field++
			return d, nil, false
		}
		return d.submitForm()
	default:
		if key.Text != "" && key.Mod&^tea.ModShift == 0 {
			d.typeText(key.Text)
		}
	}
	return d, nil, false
}

// typeText adds typed or pasted text to the field of a form.
func (d *aiSetupDialog) typeText(text string) {
	if d.step != aiStepKey && d.step != aiStepEndpoint {
		return
	}
	if d.providerRow() {
		if utf8.RuneCountInString(text) < 8 {
			return // a stray character: the provider is chosen with ←→
		}
		d.field = 2 // a key pasted as the form opens goes to the key field
	}
	d.values[d.field] += strings.Join(strings.Fields(text), "")
}

// providerRow reports that the keyboard is on the provider of the own-key form.
func (d *aiSetupDialog) providerRow() bool { return d.step == aiStepKey && d.field == 0 }

func (d *aiSetupDialog) submitForm() (chatshell.Overlay, tea.Cmd, bool) {
	if d.step == aiStepKey {
		provider := aiProviders[d.provider]
		model, key := strings.TrimSpace(d.values[1]), strings.TrimSpace(d.values[2])
		switch {
		case model == "":
			d.field, d.problem = 1, "Type the model name as "+provider.title+" lists it."
			return d, nil, false
		case key == "":
			d.field, d.problem = 2, "Paste your "+provider.title+" API key."
			return d, nil, false
		}
		if !strings.HasPrefix(strings.ToLower(model), provider.family+"/") {
			model = provider.family + "/" + model // the key goes to this provider only
		}
		return d.finish(aiChoice{Kind: aiKindKey, Model: model, KeyAccount: "ai-key:" + provider.family}, key)
	}
	base, model, key := strings.TrimSpace(d.values[0]), strings.TrimSpace(d.values[1]), strings.TrimSpace(d.values[2])
	parsed, err := url.Parse(base)
	switch {
	case err == nil && parsed.User != nil:
		d.field, d.problem = 0, "Leave the user name and password out of the URL: put the key in the API key field, which is kept in the keyring."
		return d, nil, false
	case err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "":
		d.field, d.problem = 0, "The base URL must start with http:// or https://, for example "+aiDefaultEndpoint
		return d, nil, false
	case model == "":
		d.field, d.problem = 1, "Type the model name your endpoint serves, for example llama3.2"
		return d, nil, false
	}
	choice := aiChoice{Kind: aiKindEndpoint, Model: model, BaseURL: base}
	if key != "" {
		choice.KeyAccount = "ai-endpoint:" + parsed.Host // never the entry of a provider
	}
	return d.finish(choice, key)
}

// finish records the choice and reopens the workspace on it, in place.
func (d *aiSetupDialog) finish(choice aiChoice, key string) (chatshell.Overlay, tea.Cmd, bool) {
	if key != "" {
		if err := d.doors.saveKey(choice.KeyAccount, key); err != nil {
			// No keyring on this machine: the key works until the workspace
			// is closed, and is asked for again next time.
			if d.state.sessionKeys == nil {
				d.state.sessionKeys = map[string]string{}
			}
			d.state.sessionKeys[choice.KeyAccount] = key
			d.state.keyNote = "The key could not be kept in the OS keyring, so it works until you close DataTug and is asked for again next time."
		}
	}
	if err := d.doors.saveChoice(d.projectDir, choice); err != nil {
		d.problem = "The choice could not be saved: " + strings.Join(strings.Fields(err.Error()), " ")
		return d, nil, false
	}
	return d, d.ui.Reopen(d.pending), true
}

// --- view -----------------------------------------------------------------

func (d *aiSetupDialog) View(width, height int) string {
	title := lipgloss.NewStyle().Bold(true).Foreground(theme.AccentColor())
	muted := lipgloss.NewStyle().Foreground(theme.MutedColor())
	chosen := lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor())
	bold := lipgloss.NewStyle().Bold(true)
	hint := func(pairs ...string) string {
		parts := make([]string, 0, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			parts = append(parts, theme.WorkspaceKeyHint(pairs[i])+" "+pairs[i+1])
		}
		return strings.Join(parts, "   ")
	}
	var lines []string
	switch d.step {
	case aiStepMenu:
		lines = append(lines, title.Render("✦ Set up AI")+muted.Render("  for "+d.projectTitle), "")
		lines = append(lines, "Ask about your data in plain words. Choose who answers:", "")
		for i, item := range aiMenu {
			marker, style := "  ", bold
			if i == d.index {
				marker, style = "▸ ", chosen
			}
			lines = append(lines, marker+style.Render(fmt.Sprintf("%d  %s", i+1, item.title)), "     "+muted.Render(item.detail))
		}
		lines = append(lines, "", muted.Render("Now: "+d.current+". The choice is remembered for this project only."))
		lines = append(lines, "", hint("↑↓", "choose", "Enter", "select", "1-5", "pick", "Esc", "close"))
	case aiStepConsent:
		lines = append(lines, title.Render("✦ DataTug AI")+muted.Render("  what leaves this machine"), "")
		lines = append(lines, aiConsentLines...)
		lines = append(lines, "", "This applies to "+bold.Render(d.projectTitle)+" only. You can change it any time with "+aiCommand+".")
		lines = append(lines, "", hint("Y", "I agree, sign in", "N", "back"))
	case aiStepSignIn:
		lines = append(lines, title.Render("✦ Sign in to DataTug"), "")
		if len(d.lines) == 0 {
			lines = append(lines, "Checking your sign-in…")
		} else {
			lines = append(lines, "Your browser opens the sign-in page. Approve it there:", "")
			for _, line := range d.lines {
				lines = append(lines, "  "+bold.Render(line))
			}
			lines = append(lines, "", "Waiting for you to finish in the browser…")
		}
		lines = append(lines, "", hint("Esc", "cancel"))
	case aiStepPlan:
		lines = append(lines, title.Render("✦ DataTug AI is ready"), "")
		if d.email != "" {
			lines = append(lines, "Signed in as "+bold.Render(d.email))
		}
		lines = append(lines, d.planLine())
		pairs := []string{"Enter", "start asking"}
		if d.plan.EffectivePlan == "pro" && d.plan.ManageURL != "" {
			pairs = append(pairs, "U", "manage Pro in your browser")
		} else if d.plan.CanUpgrade && d.plan.UpgradeURL != "" {
			if d.wantPro {
				lines = append(lines, "", "Press "+bold.Render("U")+" to subscribe to DataTug Pro in your browser, then "+bold.Render("R")+" to refresh.")
			}
			pairs = append(pairs, "U", "get Pro", "R", "refresh plan")
		} else if d.wantPro {
			lines = append(lines, "", "This account cannot be upgraded from here. The free allowance still works.")
		}
		lines = append(lines, "", hint(append(pairs, "Esc", "back")...))
	case aiStepKey, aiStepEndpoint:
		labels, secret := d.form()
		if d.step == aiStepKey {
			lines = append(lines, title.Render("✦ Your own API key"), "")
		} else {
			lines = append(lines, title.Render("✦ Your own endpoint"), "")
			lines = append(lines, "Any OpenAI-compatible URL: Ollama, LM Studio, a gateway, your company's.", "")
		}
		for i, label := range labels {
			value := d.values[i]
			if d.step == aiStepKey && i == 0 {
				value = "‹ " + aiProviders[d.provider].title + " ›"
				if d.field == 0 {
					value = chosen.Render(value) + muted.Render("   ←→ change")
				}
				marker := "  "
				if d.field == 0 {
					marker = "▸ "
				}
				lines = append(lines, marker+bold.Render(fmt.Sprintf("%-19s", label))+value)
				continue
			}
			if i == secret && value != "" {
				value = strings.Repeat("•", min(utf8.RuneCountInString(value), 24))
			}
			marker := "  "
			if i == d.field {
				marker, value = "▸ ", value+"▏"
			}
			lines = append(lines, marker+bold.Render(fmt.Sprintf("%-19s", label))+value)
		}
		note := "Questions and the data read to answer them go to this provider, not to DataTug."
		if d.step == aiStepEndpoint {
			note = "Questions and the data read to answer them go to this URL, not to DataTug."
		}
		lines = append(lines, "", muted.Render(note), muted.Render("A key is kept in your operating system's keyring, never in a file."))
		lines = append(lines, "", hint("↑↓", "field", "Enter", "next / save", "Esc", "back"))
	}
	if d.problem != "" {
		lines = append(lines, "")
		warn := lipgloss.NewStyle().Foreground(theme.FocusColor())
		for _, line := range strings.Split(ansi.Wordwrap(d.problem, max(20, width-2), ""), "\n") {
			lines = append(lines, warn.Render(line))
		}
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(1, width), "…")
	}
	return strings.Join(lines, "\n")
}

// planLine says the allowance of the signed-in account in one line.
func (d *aiSetupDialog) planLine() string {
	plan := d.plan
	if plan.V != 1 || plan.AI.Left == nil || plan.AI.Limit == nil {
		return "Your allowance cannot be shown by this version of DataTug."
	}
	name := "Free plan"
	if plan.EffectivePlan == "pro" {
		name = "Pro plan"
	}
	line := fmt.Sprintf("%s · %d of %d AI questions left", name, *plan.AI.Left, *plan.AI.Limit)
	if plan.AI.ResetsAt != "" {
		line += " · resets " + plan.AI.ResetsAt
	}
	return line
}

// chatAISetup is the AI setup of one project's chat: the dialog, and what
// opens it by itself.
//
// With "No AI" chosen a question is answered with the way back to this
// dialog instead of opening it every time.
func chatAISetup(ctx context.Context, options chatOptions, projectDir, projectTitle string, missing, noneChosen, isSample bool) chat.AISetup {
	setup := chat.AISetup{Missing: missing && !noneChosen, Open: func(ui *chat.ChatUI, pending string) chatshell.Overlay {
		return newAISetupDialog(ctx, ui, options, projectDir, projectTitle, currentAI(options, missing), pending)
	}}
	if isSample {
		// The ready-made questions of the sample need no AI.
		setup.AnswersWithoutAI = func(message string) bool {
			_, ok := matchSampleQuestion(message)
			return ok
		}
	}
	return setup
}
