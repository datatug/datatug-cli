package commands

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/spf13/cobra"
)

// aiConfigDir points the user config directory of the AI choices at a
// temporary one and returns the path of the choices file.
func aiConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	covDSetVar(t, &chatUserConfigDir, func() (string, error) { return dir, nil })
	return filepath.Join(dir, "datatug", "ai-choices.json")
}

// aiKeyringStub replaces the OS keyring with a map.
func aiKeyringStub(t *testing.T) map[string]string {
	t.Helper()
	keys := map[string]string{}
	covDSetVar(t, &aiKeyGet, func(_, account string) (string, error) {
		key, ok := keys[account]
		if !ok {
			return "", errors.New("no such key")
		}
		return key, nil
	})
	covDSetVar(t, &aiKeySet, func(_, account, key string) error { keys[account] = key; return nil })
	return keys
}

func TestAIChoiceIsRememberedPerProject(t *testing.T) {
	path := aiConfigDir(t)
	project, other := t.TempDir(), t.TempDir()
	if _, ok := readAIChoice(project); ok {
		t.Fatal("a project has an AI choice before one was made")
	}
	want := aiChoice{Kind: aiKindEndpoint, Model: "llama3.2", BaseURL: "http://localhost:11434/v1"}
	if err := writeAIChoice(project, want); err != nil {
		t.Fatal(err)
	}
	if err := writeAIChoice(other, aiChoice{Kind: "telepathy"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := readAIChoice(project); !ok || got != want {
		t.Fatalf("readAIChoice = %+v, %v", got, ok)
	}
	if _, ok := readAIChoice(other); ok {
		t.Fatal("an unknown kind of AI was accepted")
	}
	if _, ok := readAIChoice(t.TempDir()); ok {
		t.Fatal("the choice of one project was applied to another")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the choices file is not private: %v %v", info, err)
	}

	// A file that is not JSON is no choice, and is never overwritten.
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readAIChoice(project); ok {
		t.Fatal("a broken choices file gave a choice")
	}
	if err := writeAIChoice(project, want); err == nil || !strings.Contains(err.Error(), "not valid JSON; fix or delete it first") {
		t.Fatalf("writing over a broken file: %v", err)
	}
	// Neither is one that cannot be read.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAIChoices(path); err == nil || err.Error() != "it cannot be opened" {
		t.Fatalf("a directory in place of the file: %v", err)
	}
}

func TestAIChoiceStoreFailures(t *testing.T) {
	covDSetVar(t, &chatUserConfigDir, func() (string, error) { return "", errors.New("no home") })
	if _, ok := readAIChoice("p"); ok {
		t.Fatal("a choice without a config directory")
	}
	if err := writeAIChoice("p", aiChoice{Kind: aiKindNone}); err == nil || err.Error() != "no home" {
		t.Fatalf("write without a config directory: %v", err)
	}

	// The config directory cannot be made under a plain file.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	chatUserConfigDir = func() (string, error) { return blocker, nil }
	if err := writeAIChoice("p", aiChoice{Kind: aiKindNone}); err == nil {
		t.Fatal("a choice was written under a plain file")
	}

	// Another process holds the lock.
	path := aiConfigDir(t)
	covDSetVar(t, &consentLockWait, time.Millisecond)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAIChoice("p", aiChoice{Kind: aiKindNone}); err == nil || !strings.Contains(err.Error(), "another datatug process") {
		t.Fatalf("write under a held lock: %v", err)
	}
}

func TestApplyProjectAIChoice(t *testing.T) {
	aiConfigDir(t)
	keys := aiKeyringStub(t)
	project := t.TempDir()
	start := chatOptions{model: defaultChatModel, ai: "profile", baseURL: "https://old.example/v1", apiKey: "old"}

	same := func(o chatOptions) bool {
		return o.model == start.model && o.ai == start.ai && o.baseURL == start.baseURL && o.apiKey == start.apiKey && !o.aiFromChoice
	}
	options := start
	if notice := applyProjectAIChoice(&options, project); notice != "" || !same(options) {
		t.Fatalf("no choice changed the options: %q %+v", notice, options)
	}

	for _, tc := range []struct {
		name                string
		choice              aiChoice
		model, base, apiKey string
	}{
		{"DataTug AI", aiChoice{Kind: aiKindHosted}, "cloud", "", ""},
		{"no AI", aiChoice{Kind: aiKindNone}, "none", "", ""},
		{"own key", aiChoice{Kind: aiKindKey, Model: "openai/gpt-x", KeyAccount: "ai-key:openai"}, "openai/gpt-x", "", "sk-saved"},
		{"own endpoint", aiChoice{Kind: aiKindEndpoint, Model: "llama3.2", BaseURL: "http://localhost:11434/v1"}, "llama3.2", "http://localhost:11434/v1", ""},
	} {
		keys["ai-key:openai"] = "sk-saved"
		if err := writeAIChoice(project, tc.choice); err != nil {
			t.Fatal(err)
		}
		options := start
		if notice := applyProjectAIChoice(&options, project); notice != "" {
			t.Fatalf("%s: notice %q", tc.name, notice)
		}
		if options.model != tc.model || options.baseURL != tc.base || options.apiKey != tc.apiKey || options.ai != "" || !options.aiFromChoice {
			t.Fatalf("%s: options = %+v", tc.name, options)
		}
	}

	// A key the keyring no longer has is said, and nothing is changed; one
	// the keyring never took is used for as long as the workspace runs.
	if err := writeAIChoice(project, aiChoice{Kind: aiKindKey, Model: "openai/gpt-x", KeyAccount: "ai-key:gone"}); err != nil {
		t.Fatal(err)
	}
	options = start
	if notice := applyProjectAIChoice(&options, project); !strings.Contains(notice, "could not be read from the OS keyring. Type /ai to enter it again.") || !same(options) {
		t.Fatalf("a lost key: %q %+v", notice, options)
	}
	options = start
	options.aiState = &aiRunState{sessionKeys: map[string]string{"ai-key:gone": "sk-session"}}
	if notice := applyProjectAIChoice(&options, project); notice != "" || options.apiKey != "sk-session" {
		t.Fatalf("a key of this run: %q %+v", notice, options)
	}
}

// --- the dialog -------------------------------------------------------------

type aiDoorsLog struct {
	signInErr, planErr, browserErr, choiceErr, keyErr error
	signInLines                                       string
	plan                                              cloudPlan
	opened                                            []string
	choices                                           []aiChoice
	keys                                              map[string]string
	signIns                                           int
}

func testPlan(effective string, left, limit int64) cloudPlan {
	plan := cloudPlan{V: 1, Payer: "personal", EffectivePlan: effective, CanUpgrade: effective == "free", UpgradeURL: "https://datatug.app/pro", ManageURL: "https://datatug.app/manage"}
	plan.AI.Left, plan.AI.Limit, plan.AI.ResetsAt = &left, &limit, "2026-12-01"
	return plan
}

func newTestAIDialog(t *testing.T, pending string) (*aiSetupDialog, *aiDoorsLog) {
	t.Helper()
	log := &aiDoorsLog{keys: map[string]string{}, plan: testPlan("free", 47, 50), signInLines: "Copy this one-time code: ABCD-1234\nOpen https://auth.sneat.co/device in your browser to continue.\n"}
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "test")
	d := &aiSetupDialog{ui: ui, state: &aiRunState{}, ctx: context.Background(), projectDir: "/projects/shop", projectTitle: "Shop", current: "no AI", pending: pending}
	d.doors = aiSetupDoors{
		signIn: func(_ context.Context, out io.Writer) (string, error) {
			log.signIns++
			_, _ = io.WriteString(out, log.signInLines)
			return "ann@example.com", log.signInErr
		},
		plan:        func(context.Context) (cloudPlan, error) { return log.plan, log.planErr },
		openBrowser: func(link string) error { log.opened = append(log.opened, link); return log.browserErr },
		saveChoice: func(dir string, choice aiChoice) error {
			if dir != "/projects/shop" {
				t.Errorf("the choice was saved for %q", dir)
			}
			log.choices = append(log.choices, choice)
			return log.choiceErr
		},
		saveKey: func(account, key string) error {
			if log.keyErr != nil {
				return log.keyErr
			}
			log.keys[account] = key
			return nil
		},
		now: func() time.Time { return time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC) },
	}
	return d, log
}

var aiTestKeys = map[string]tea.KeyPressMsg{
	"enter": {Code: tea.KeyEnter}, "esc": {Code: tea.KeyEscape}, "up": {Code: tea.KeyUp}, "down": {Code: tea.KeyDown},
	"left": {Code: tea.KeyLeft}, "right": {Code: tea.KeyRight}, "tab": {Code: tea.KeyTab}, "shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
	"backspace": {Code: tea.KeyBackspace},
}

// press sends keys to the dialog: a named key, or a character. It follows the
// background work the dialog starts until it is quiet, and reports whether the
// dialog closed and the last command it returned.
func press(t *testing.T, d *aiSetupDialog, keys ...string) (done bool, last tea.Cmd) {
	t.Helper()
	for _, name := range keys {
		key, ok := aiTestKeys[name]
		if !ok {
			key = tea.KeyPressMsg{Code: []rune(name)[0], Text: name}
		}
		if key.String() != name {
			t.Fatalf("test key %q reads as %q", name, key.String())
		}
		var cmd tea.Cmd
		_, cmd, done = d.Update(key)
		last = cmd
		for cmd != nil && d.step == aiStepSignIn {
			msg, isOverlay := cmd().(chat.OverlayMsg)
			if !isOverlay || msg.Overlay != d {
				t.Fatalf("the sign-in sent %T", msg)
			}
			_, cmd, done = d.Update(msg.Msg)
		}
	}
	return done, last
}

func aiView(d *aiSetupDialog) string { return ansi.Strip(d.View(96, 40)) }

func wantView(t *testing.T, d *aiSetupDialog, wants ...string) {
	t.Helper()
	view := aiView(d)
	flat := strings.Join(strings.Fields(view), " ") // a long message wraps
	for _, want := range wants {
		if !strings.Contains(view, want) && !strings.Contains(flat, want) {
			t.Fatalf("the dialog lacks %q:\n%s", want, view)
		}
	}
}

// The free AI is used only after the person read what leaves the machine and
// agreed, and signed in without leaving the workspace.
func TestAISetupFreeAIAsksConsentThenSignsIn(t *testing.T) {
	d, log := newTestAIDialog(t, "who buys the most?")
	if !d.WorkspaceBorderlessCard() {
		t.Fatal("the dialog is not the quiet card")
	}
	wantView(t, d, "Set up AI", "for Shop", "1  DataTug AI · free", "2  DataTug Pro", "3  Your own API key", "4  Your own endpoint", "5  No AI", "Now: no AI. The choice is remembered for this project only.")
	if _, _, done := d.Update("not a key"); done {
		t.Fatal("an unknown message closed the dialog")
	}

	press(t, d, "down", "j", "up", "k", "up", "enter")
	if d.step != aiStepConsent || log.signIns != 0 {
		t.Fatalf("the free AI did not ask for consent first: step=%v sign-ins=%d", d.step, log.signIns)
	}
	wantView(t, d, "what leaves this machine", "your questions and the conversation so far", "the names and types of this project's tables and columns", "data read from the database to answer them", "a limited number of questions a month", "This applies to Shop only", "Y I agree, sign in")

	// Declining goes back and nothing was contacted.
	press(t, d, "n")
	if d.step != aiStepMenu || log.signIns != 0 || len(log.choices) != 0 {
		t.Fatal("declining did not go back untouched")
	}
	// Neither an unrelated key nor a second Enter is consent.
	press(t, d, "1", "x", "enter")
	if d.step != aiStepConsent || log.signIns != 0 {
		t.Fatal("a key other than Y answered the consent")
	}

	press(t, d, "y")
	if d.step != aiStepPlan || log.signIns != 1 {
		t.Fatalf("agreeing did not sign in: step=%v", d.step)
	}
	if got := strings.Join(d.lines, "|"); got != "Copy this one-time code: ABCD-1234|Open https://auth.sneat.co/device in your browser to continue." {
		t.Fatalf("the sign-in code and URL were not shown: %q", got)
	}
	wantView(t, d, "DataTug AI is ready", "Signed in as ann@example.com", "Free plan · 47 of 50 AI questions left · resets 2026-12-01", "Enter start asking", "U get Pro", "R refresh plan")

	done, cmd := press(t, d, "enter")
	if !done || cmd == nil {
		t.Fatal("starting did not reopen the workspace")
	}
	if len(log.choices) != 1 || log.choices[0] != (aiChoice{Kind: aiKindHosted, ConsentAt: "2026-11-13T09:00:00Z"}) {
		t.Fatalf("the consent was not recorded: %+v", log.choices)
	}
	if reopen, pending := d.ui.Reopened(); !reopen || pending != "who buys the most?" {
		t.Fatalf("the question was not kept: %v %q", reopen, pending)
	}
}

func TestAISetupSignInScreens(t *testing.T) {
	d, log := newTestAIDialog(t, "")
	d.step = aiStepSignIn
	wantView(t, d, "Sign in to DataTug", "Checking your sign-in…", "Esc cancel")
	d.Update(aiSignInLine{text: "Copy this one-time code: ABCD-1234"})
	wantView(t, d, "Your browser opens the sign-in page", "Copy this one-time code: ABCD-1234", "Waiting for you to finish in the browser…")

	// Esc cancels the sign-in; its late result changes nothing.
	cancelled := false
	d.cancel = func() { cancelled = true }
	press(t, d, "esc")
	if !cancelled || d.step != aiStepMenu {
		t.Fatal("Esc did not cancel the sign-in")
	}
	d.Update(aiSignedIn{email: "late@example.com"})
	if d.step != aiStepMenu || d.email != "" {
		t.Fatal("a cancelled sign-in still signed in")
	}
	// Nor does the late result of an earlier attempt answer a newer one, and
	// nothing waits for an attempt that was abandoned.
	d.step, d.attempt, d.lines = aiStepSignIn, 2, nil
	d.Update(aiSignedIn{attempt: 1, email: "late@example.com"})
	_, stale, _ := d.Update(aiSignInLine{attempt: 1, text: "old code"})
	if d.step != aiStepSignIn || d.email != "" || len(d.lines) != 0 || stale != nil {
		t.Fatalf("an abandoned sign-in reached the new one: step=%v lines=%q", d.step, d.lines)
	}
	d.step, d.attempt = aiStepMenu, 0

	// A sign-in that fails says why, without sending the person to a command.
	log.signInErr = errors.New("DataTug cloud chat requires 'datatug auth login': denied\n in the browser.")
	press(t, d, "1", "y")
	if d.step != aiStepMenu {
		t.Fatalf("a failed sign-in did not return to the choices: %v", d.step)
	}
	wantView(t, d, "DataTug AI could not be set up: DataTug cloud chat: denied in the browser. Try again, or choose another way.")
	if strings.Contains(aiView(d), "auth login") {
		t.Fatal("the dialog sends the person out to a command")
	}
	log.signInErr, log.planErr = nil, errors.New("this account is unavailable for your sign-in")
	press(t, d, "1", "y")
	wantView(t, d, "could not be set up: this account is unavailable for your sign-in")
	// The next key clears the message.
	press(t, d, "down")
	if d.problem != "" {
		t.Fatal("the message stayed after the next key")
	}
	// Esc on the choices closes the dialog.
	if done, _ := press(t, d, "esc"); !done {
		t.Fatal("Esc did not close the dialog")
	}
	// Ctrl+C quits the workspace from the dialog too, ending a sign-in.
	cancelled = false
	d.cancel = func() { cancelled = true }
	_, quit, done := d.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !done || quit == nil || !cancelled {
		t.Fatal("Ctrl+C did not quit from the dialog")
	}
	if _, isQuit := quit().(tea.QuitMsg); !isQuit {
		t.Fatal("Ctrl+C did not quit the program")
	}
}

func TestAISetupProLeadsToTheSubscriptionPage(t *testing.T) {
	d, log := newTestAIDialog(t, "")
	press(t, d, "2", "y")
	if d.step != aiStepPlan || !d.wantPro {
		t.Fatalf("DataTug Pro did not reach the plan: step=%v", d.step)
	}
	wantView(t, d, "Press U to subscribe to DataTug Pro in your browser, then R to refresh.")
	press(t, d, "u")
	if len(log.opened) != 1 || log.opened[0] != "https://datatug.app/pro" {
		t.Fatalf("the subscription page was not opened: %v", log.opened)
	}
	wantView(t, d, "The subscription page is open in your browser. When you are done there, press R to refresh")

	// R reads the plan again: now Pro.
	log.plan = testPlan("pro", 990, 1000)
	press(t, d, "r")
	if log.signIns != 2 || d.step != aiStepPlan {
		t.Fatalf("R did not refresh the plan: sign-ins=%d step=%v", log.signIns, d.step)
	}
	wantView(t, d, "Pro plan · 990 of 1000 AI questions left", "U manage Pro in your browser")
	press(t, d, "U")
	if log.opened[1] != "https://datatug.app/manage" {
		t.Fatalf("manage page: %v", log.opened)
	}

	// A browser that does not open leaves the link to open by hand.
	log.browserErr = errors.New("no display")
	press(t, d, "u")
	wantView(t, d, "The browser did not open. Open this page yourself: https://datatug.app/manage")
	// Only an https page of the service is ever opened.
	log.browserErr, d.plan.ManageURL = nil, "file:///etc/passwd"
	press(t, d, "u")
	if len(log.opened) != 3 {
		t.Fatalf("a page that is not https was opened: %v", log.opened)
	}
	wantView(t, d, "This account has no subscription page to open.")

	// An account that cannot be upgraded says so, and one whose allowance
	// this version cannot read says that.
	d.plan = testPlan("free", 1, 50)
	d.plan.CanUpgrade, d.plan.AI.ResetsAt = false, ""
	wantView(t, d, "Free plan · 1 of 50 AI questions left", "This account cannot be upgraded from here. The free allowance still works.")
	d.plan.V = 2
	wantView(t, d, "Your allowance cannot be shown by this version of DataTug.")
	d.email = ""
	if strings.Contains(aiView(d), "Signed in as") {
		t.Fatal("an unknown account was named")
	}

	// A choice that cannot be saved keeps the dialog open and says why.
	log.choiceErr = errors.New("disk\nfull")
	if done, _ := press(t, d, "enter"); done {
		t.Fatal("the dialog closed although the choice was not saved")
	}
	wantView(t, d, "The choice could not be saved: disk full")
	press(t, d, "x") // a key that means nothing here
	press(t, d, "esc")
	if d.step != aiStepMenu {
		t.Fatal("Esc did not go back to the choices")
	}
}

func TestAISetupOwnKey(t *testing.T) {
	d, log := newTestAIDialog(t, "")
	press(t, d, "3")
	wantView(t, d, "Your own API key", "Provider", "‹ OpenAI ›   ←→ change", "Model", defaultChatModel, "API key", "go to this provider, not to DataTug", "operating system's keyring, never in a file")

	// ←→ changes the provider on its own row, and its documented model with
	// it. Nothing can be typed there.
	press(t, d, "right", "x", "backspace")
	if aiProviders[d.provider].family != "anthropic" || d.values != [3]string{"", "claude-haiku", ""} {
		t.Fatalf("the model did not follow the provider: %q", d.values)
	}
	press(t, d, "left", "left")
	if aiProviders[d.provider].family != "openrouter" || d.values[1] != "" {
		t.Fatalf("left of the first provider: %+v %q", aiProviders[d.provider], d.values[1])
	}
	// A model the person typed is kept when the provider changes.
	press(t, d, "down", "m", "1", "up", "right")
	if aiProviders[d.provider].family != "openai" || d.values[1] != "m1" {
		t.Fatalf("a typed model was replaced: %q", d.values[1])
	}
	press(t, d, "down", "backspace", "backspace", "backspace")
	if done, _ := press(t, d, "down", "enter"); done || d.field != 1 {
		t.Fatal("an empty model was accepted")
	}
	wantView(t, d, "Type the model name as OpenAI lists it.")
	press(t, d, "g", "p", "t", "-", "x", "tab", "shift+tab", "enter")
	if d.field != 2 {
		t.Fatalf("Enter did not move to the key: field=%d", d.field)
	}
	if done, _ := press(t, d, "enter"); done {
		t.Fatal("an empty key was accepted")
	}
	wantView(t, d, "Paste your OpenAI API key.")

	// The key is pasted, never shown, and kept in the keyring only.
	d.Update(tea.PasteMsg{Content: " sk-secret-1\n"})
	if view := aiView(d); strings.Contains(view, "sk-secret") || !strings.Contains(view, "•••••••••••▏") {
		t.Fatalf("the key is shown:\n%s", view)
	}
	// An arrow pressed on the key never changes where the key is sent.
	press(t, d, "left", "right", "right")
	if aiProviders[d.provider].family != "openai" {
		t.Fatalf("an arrow on the key row changed the provider to %s", aiProviders[d.provider].family)
	}
	done, cmd := press(t, d, "enter")
	if !done || cmd == nil {
		t.Fatal("the key was not saved")
	}
	want := aiChoice{Kind: aiKindKey, Model: "openai/gpt-x", KeyAccount: "ai-key:openai"}
	if len(log.choices) != 1 || log.choices[0] != want || log.keys["ai-key:openai"] != "sk-secret-1" || d.state.keyNote != "" {
		t.Fatalf("saved %+v keys=%v", log.choices, log.keys)
	}

	// A model already named for the provider is kept as typed, and a keyring
	// that cannot store the key leaves it for this run only, and says so.
	d, log = newTestAIDialog(t, "")
	log.keyErr = errors.New("no keyring")
	press(t, d, "3")
	d.values = [3]string{"", "OpenAI/gpt-y", "sk-2"}
	d.field = 2
	if done, _ := press(t, d, "enter"); !done {
		t.Fatal("a key without a keyring was refused")
	}
	if log.choices[0].Model != "OpenAI/gpt-y" || d.state.sessionKeys["ai-key:openai"] != "sk-2" || len(log.keys) != 0 || !strings.Contains(d.state.keyNote, "works until you close DataTug") {
		t.Fatalf("choice=%+v session=%v note=%q", log.choices, d.state.sessionKeys, d.state.keyNote)
	}
	// Pasting outside a form types nothing.
	d.step = aiStepMenu
	d.Update(tea.PasteMsg{Content: "stray"})
	if d.values[2] != "sk-2" {
		t.Fatal("a paste outside a form changed a field")
	}
}

func TestAISetupOwnEndpoint(t *testing.T) {
	d, log := newTestAIDialog(t, "")
	press(t, d, "down", "down", "down", "enter")
	wantView(t, d, "Your own endpoint", "Any OpenAI-compatible URL", "Base URL", aiDefaultEndpoint, "API key (optional)", "go to this URL, not to DataTug")
	press(t, d, "left") // no provider to change here
	if d.values[0] != aiDefaultEndpoint {
		t.Fatal("←→ changed the endpoint")
	}
	// A key with Ctrl is not text.
	d.Update(tea.KeyPressMsg{Code: 'v', Text: "v", Mod: tea.ModCtrl})
	if d.values[0] != aiDefaultEndpoint {
		t.Fatal("a control key was typed")
	}

	d.values[0] = "localhost:11434"
	d.field = 2
	if done, _ := press(t, d, "enter"); done || d.field != 0 {
		t.Fatal("a base URL without a scheme was accepted")
	}
	wantView(t, d, "The base URL must start with http:// or https://")
	d.values[0], d.field = "https://me:secret@ai.corp.example/v1", 2
	if done, _ := press(t, d, "enter"); done || d.field != 0 {
		t.Fatal("a base URL with a password in it was accepted")
	}
	wantView(t, d, "Leave the user name and password out of the URL")
	d.values[0], d.field = aiDefaultEndpoint, 2
	if done, _ := press(t, d, "enter"); done || d.field != 1 {
		t.Fatal("an empty model was accepted")
	}
	wantView(t, d, "Type the model name your endpoint serves")

	d.values[1], d.field = "llama3.2", 2
	if done, _ := press(t, d, "enter"); !done {
		t.Fatal("the endpoint was not saved")
	}
	if log.choices[0] != (aiChoice{Kind: aiKindEndpoint, Model: "llama3.2", BaseURL: aiDefaultEndpoint}) || len(log.keys) != 0 {
		t.Fatalf("saved %+v keys=%v", log.choices, log.keys)
	}

	// With a key, it is kept under the endpoint's host, never under the
	// entry of a provider.
	d, log = newTestAIDialog(t, "")
	press(t, d, "4")
	d.values, d.field = [3]string{"https://ai.corp.example/v1", "big", "tok"}, 2
	press(t, d, "enter")
	if log.choices[0].KeyAccount != "ai-endpoint:ai.corp.example" || log.keys["ai-endpoint:ai.corp.example"] != "tok" {
		t.Fatalf("saved %+v keys=%v", log.choices, log.keys)
	}

	// "No AI" is a choice too.
	d, log = newTestAIDialog(t, "")
	if done, _ := press(t, d, "5"); !done || log.choices[0] != (aiChoice{Kind: aiKindNone}) {
		t.Fatalf("No AI: %+v", log.choices)
	}
}

func TestAILineWriterNeverBlocksTheSignIn(t *testing.T) {
	events := make(chan tea.Msg, 1)
	n, err := aiLineWriter{events: events, attempt: 3}.Write([]byte("first\n\n  second\n"))
	if n != 16 || err != nil || len(events) != 1 || <-events != (aiSignInLine{attempt: 3, text: "first"}) {
		t.Fatalf("Write = %d, %v", n, err)
	}
}

// The real doors: the sign-in, the plan and the keyring, each stood in for at
// its own seam.
func TestAISetupRealDoors(t *testing.T) {
	path := aiConfigDir(t)
	keys := aiKeyringStub(t)
	covDSetVar(t, &aiEnsureSignedIn, func(_ context.Context, opts device.SignInOptions) (device.Session, error) {
		if !opts.Interactive || !opts.Insecure || opts.Out == nil {
			t.Errorf("sign-in options = %+v", opts)
		}
		return device.Session{Email: "ann@example.com"}, nil
	})
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "test")
	d := newAISetupDialog(context.Background(), ui, chatOptions{insecureStorage: true, aiState: &aiRunState{}}, "/p", "P", "no AI", "q")
	if email, err := d.doors.signIn(context.Background(), io.Discard); err != nil || email != "ann@example.com" {
		t.Fatalf("signIn = %q, %v", email, err)
	}
	if d.doors.openBrowser == nil || d.doors.now().IsZero() {
		t.Fatal("a door is missing")
	}
	if err := d.doors.saveKey("ai-key:x", "k"); err != nil || keys["ai-key:x"] != "k" {
		t.Fatalf("saveKey: %v %v", err, keys)
	}
	if err := d.doors.saveChoice("/p", aiChoice{Kind: aiKindNone}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	status := http.StatusInternalServerError
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = io.WriteString(w, `{"v":1,"payer":"personal","effectivePlan":"free","ai":{"used":3,"limit":50,"left":47}}`)
	}))
	defer server.Close()
	sessionErr := errors.New("not signed in")
	covDSetVar(t, &loadCloudSession, func(_ context.Context, options chatOptions) (cloudSession, error) {
		if !options.insecureStorage {
			t.Error("the plan was read from another credential store than the sign-in used")
		}
		return fakeCloudSession(server.URL + "/v0/"), sessionErr
	})
	if _, err := d.doors.plan(context.Background()); !errors.Is(err, sessionErr) {
		t.Fatalf("plan without a session: %v", err)
	}
	sessionErr = nil
	if _, err := d.doors.plan(context.Background()); err == nil {
		t.Fatal("a failed plan request was accepted")
	}
	status = http.StatusOK
	if plan, err := d.doors.plan(context.Background()); err != nil || *plan.AI.Left != 47 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
}

// --- the chat that opens with the choice -----------------------------------

func TestChatAISetupOfAProject(t *testing.T) {
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "test")
	setup := chatAISetup(context.Background(), chatOptions{model: "cloud", aiState: &aiRunState{}}, "/p", "Shop", false, false, false)
	if setup.Missing || setup.AnswersWithoutAI != nil {
		t.Fatalf("setup = %+v", setup)
	}
	dialog := setup.Open(ui, "why?").(*aiSetupDialog)
	if dialog.current != "DataTug AI" || dialog.pending != "why?" || dialog.projectTitle != "Shop" || dialog.projectDir != "/p" {
		t.Fatalf("dialog = %+v", dialog)
	}
	// With "No AI" chosen a question no longer opens the dialog by itself.
	if none := chatAISetup(context.Background(), chatOptions{}, "/p", "Shop", true, true, false); none.Missing || none.Open == nil {
		t.Fatal("No AI still opens the dialog on every question")
	}
	sample := chatAISetup(context.Background(), chatOptions{}, "/p", "Sample", true, false, true)
	if !sample.Missing || !sample.AnswersWithoutAI("1") || sample.AnswersWithoutAI("who buys the most?") {
		t.Fatal("the sample does not answer its ready-made questions by itself")
	}
	for want, options := range map[string]chatOptions{
		"llama3.2 at your own endpoint":  {model: "llama3.2", baseURL: "http://localhost:11434/v1"},
		"openai/gpt-x with your own key": {model: "openai/gpt-x"},
	} {
		if got := currentAI(options, false); got != want {
			t.Fatalf("currentAI = %q, want %q", got, want)
		}
	}
	if got := currentAI(chatOptions{model: "cloud"}, true); got != "no AI" {
		t.Fatalf("currentAI without AI = %q", got)
	}
}

// DataTug AI chosen in the workspace never closes the project when the
// sign-in has expired: the project opens without AI and says how to get it
// back, inside the workspace.
func TestSampleOpensWhenChosenDataTugAIIsAway(t *testing.T) {
	for _, env := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(env, "")
	}
	aiConfigDir(t)
	dir := installedSample(t)
	if err := writeAIChoice(dir, aiChoice{Kind: aiKindHosted}); err != nil {
		t.Fatal(err)
	}
	covDSetVar(t, &loadCloudSession, func(context.Context, chatOptions) (cloudSession, error) {
		return cloudSession{}, errors.New("DataTug cloud chat requires 'datatug auth login': stored session is empty")
	})
	notices, conversation := openSampleChat(t, dir)
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "DataTug AI is not available right now: DataTug cloud chat: stored session is empty. Type /ai to sign in again or choose another AI.") {
		t.Fatalf("notices = %q", joined)
	}
	if strings.Contains(joined, "auth login") || strings.Contains(joined, "No AI is set up yet") {
		t.Fatalf("the notices send the person out, or say AI was never set up: %q", joined)
	}
	if _, ok := conversation.(sampleConversation); !ok {
		t.Fatalf("conversation = %T, want the ready-made questions", conversation)
	}

	// Named on the command line, the same failure is still an error.
	_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: sampleEnvironment, model: "cloud", thinking: "low", modelExplicit: true})
	if err == nil || !strings.Contains(err.Error(), "configure cloud chat") {
		t.Fatalf("explicit --model cloud without a sign-in: %v", err)
	}
}

// The workspace reopened after the AI setup continues in the chat with the
// question back, on the AI of this project only, which is never remembered
// for the next project.
func TestChatReopensOnTheChosenAI(t *testing.T) {
	aiConfigDir(t)
	keys := aiKeyringStub(t)
	dir := installedSample(t)
	if err := writeAIChoice(dir, aiChoice{Kind: aiKindEndpoint, Model: "llama3.2", BaseURL: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
	var notices []string
	covDSetVar(t, &setSessionNotices, func(ui *chat.ChatUI, lines []string) { notices = append(notices, lines...) })
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	state := &aiRunState{chosen: true, pending: "who buys the most?"}
	options := chatOptions{project: dir, env: sampleEnvironment, model: defaultChatModel, thinking: "low", aiState: state}
	if _, err := runChatProject(chatCommand(), options); err != nil {
		t.Fatal(err)
	}
	want := "AI is set up: llama3.2 at your own endpoint answers your questions in this project. Change it any time with /ai. Your question is in the composer: press Enter to ask it."
	if len(notices) == 0 || notices[0] != want {
		t.Fatalf("notices = %q", notices)
	}
	// A key the keyring did not take is said once, on the reopening.
	notices = nil
	state.chosen, state.keyNote = true, "The key works until you close DataTug."
	if _, err := runChatProject(chatCommand(), options); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(notices[0], "Change it any time with /ai. The key works until you close DataTug.") || state.keyNote != "" {
		t.Fatalf("notices = %q, keyNote = %q", notices, state.keyNote)
	}
	if state.chosen || state.pending != "" || state.reopen {
		t.Fatalf("the state of the reopening was kept: %+v", state)
	}

	// A key that can no longer be read is said, with the way to fix it here.
	notices = nil
	if err := writeAIChoice(dir, aiChoice{Kind: aiKindKey, Model: "openai/gpt-x", KeyAccount: "ai-key:openai"}); err != nil {
		t.Fatal(err)
	}
	state.chosen = true
	if _, err := runChatProject(chatCommand(), options); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(notices, "\n"); !strings.Contains(joined, "could not be read from the OS keyring. Type /ai to enter it again.") || strings.Contains(joined, "AI is set up:") {
		t.Fatalf("notices = %q", joined)
	}
	_ = keys
}

func TestChatLoopsReopenAfterTheAISetup(t *testing.T) {
	t.Run("datatug chat", func(t *testing.T) {
		calls := 0
		covDSetVar(t, &runChatProjectFunc, func(_ *cobra.Command, options chatOptions) (string, error) {
			calls++
			if calls == 1 {
				if !options.modelExplicit {
					t.Error("the first opening lost the command line's model")
				}
				options.aiState.reopen = true
				return "", nil
			}
			if calls == 2 {
				if options.modelExplicit || options.aiState.reopen {
					t.Errorf("the reopening still prefers the command line: %+v", options)
				}
				return "other", nil // F3: another project
			}
			if !options.modelExplicit || options.project != "other" {
				t.Errorf("the command line's model was lost for another project: %+v", options)
			}
			return "", nil
		})
		if err := runChat(chatCommand(), chatOptions{project: "p", model: "none", modelExplicit: true, aiState: &aiRunState{}}); err != nil || calls != 3 {
			t.Fatalf("runChat: calls=%d err=%v", calls, err)
		}
	})
	t.Run("the workspace", func(t *testing.T) {
		calls := 0
		covDSetVar(t, &runChatProjectFunc, func(_ *cobra.Command, options chatOptions) (string, error) {
			calls++
			if calls == 1 {
				if options.notice != "first" {
					t.Errorf("notice = %q", options.notice)
				}
				options.aiState.reopen = true
				return "p", nil
			}
			if options.notice != "" || options.openAtStart != "" || options.project != "p" {
				t.Errorf("the reopening repeats the first opening: %+v", options)
			}
			return "", nil
		})
		options := chatOptions{project: "p", notice: "first", openAtStart: "Invoice", workspace: &workspaceStart{}, aiState: &aiRunState{}}
		if err := runWorkspaceLoop(chatCommand(), options); err != nil || calls != 2 {
			t.Fatalf("runWorkspaceLoop: calls=%d err=%v", calls, err)
		}
	})
}

// The two promises of the per-project choice: DataTug AI agreed to for one
// project is not remembered as the model of the next chat, and another
// project opens with no AI and no request for a sign-in.
func TestDataTugAIOfOneProjectIsNotAppliedToAnother(t *testing.T) {
	for _, env := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(env, "")
	}
	aiConfigDir(t)
	dir := installedSample(t)
	if err := writeAIChoice(dir, aiChoice{Kind: aiKindHosted}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"v":1,"payer":"personal","effectivePlan":"free","ai":{"used":3,"limit":50,"left":47}}`)
	}))
	defer server.Close()
	signIns := 0
	covDSetVar(t, &loadCloudSession, func(context.Context, chatOptions) (cloudSession, error) {
		signIns++
		return fakeCloudSession(server.URL + "/v0/"), nil
	})
	var saved []chatOptions
	covDSetVar(t, &saveLastChatOptionsFunc, func(_ *cobra.Command, options chatOptions) error {
		saved = append(saved, options)
		return nil
	})
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	cmd := chatCommand()
	if err := cmd.Flags().Set("model", defaultChatModel); err != nil { // as a remembered model marks it
		t.Fatal(err)
	}
	options := chatOptions{project: dir, env: sampleEnvironment, model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err != nil {
		t.Fatal(err)
	}
	if signIns != 1 {
		t.Fatalf("the chosen DataTug AI was not used: sign-in lookups = %d", signIns)
	}
	if len(saved) != 1 || saved[0].model != defaultChatModel || saved[0].aiFromChoice {
		t.Fatalf("the choice of one project was remembered for the next chat: %+v", saved)
	}

	// The same sample installed elsewhere is another project: no choice, no
	// sign-in looked for, the no-AI answer.
	options.project = installedSample(t) // a second install, in another folder
	if _, err := runChatProject(cmd, options); err != nil {
		t.Fatal(err)
	}
	if signIns != 1 {
		t.Fatalf("another project looked for a DataTug sign-in: lookups = %d", signIns)
	}

	// DataTug AI remembered from an old command line is not consent either:
	// it is offered, and nothing is contacted.
	var notices []string
	covDSetVar(t, &setSessionNotices, func(_ *chat.ChatUI, lines []string) { notices = append(notices, lines...) })
	options.model, options.modelRemembered = "cloud", true
	if _, err := runChatProject(cmd, options); err != nil {
		t.Fatal(err)
	}
	if signIns != 1 || !strings.Contains(strings.Join(notices, "\n"), "it is now chosen per project. Type /ai to choose it for this one.") {
		t.Fatalf("a remembered DataTug AI was switched on: lookups=%d notices=%q", signIns, notices)
	}
	// Named on this command line it is used as before.
	options.modelExplicit = true
	if _, err := runChatProject(cmd, options); err != nil || signIns != 2 {
		t.Fatalf("--model cloud: lookups=%d err=%v", signIns, err)
	}
}

// A key or endpoint typed in the workspace is used exactly: no key of the
// environment goes to the typed endpoint.
func TestOwnEndpointNeverGetsAKeyOfTheEnvironment(t *testing.T) {
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "sk-from-the-environment")
	aiConfigDir(t)
	dir := installedSample(t)
	if err := writeAIChoice(dir, aiChoice{Kind: aiKindEndpoint, Model: "llama3.2", BaseURL: server.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	_, conversation := openSampleChat(t, dir)
	_, _ = conversation.AskWithContext(context.Background(), "who buys the most?", "")
	if len(auth) == 0 {
		t.Fatal("the typed endpoint was not asked")
	}
	for _, header := range auth {
		if strings.Contains(header, "sk-from-the-environment") {
			t.Fatalf("the environment's key was sent to the typed endpoint: %q", header)
		}
	}
}

func TestSignInExpiredSaysWhereToSignIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	session := fakeCloudSession(server.URL + "/v0/")
	// Inside the workspace: /ai.
	if err := writeHostedPlan(context.Background(), session, "", io.Discard); err == nil || err.Error() != "DataTug sign-in expired; sign in again: type /ai" {
		t.Fatalf("/plan with an expired sign-in: %v", err)
	}
	// On the command line: the command.
	covDSetVar(t, &loadCloudSession, func(context.Context, chatOptions) (cloudSession, error) { return session, nil })
	cmd := planCommand()
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || err.Error() != "DataTug sign-in expired; sign in again: run 'datatug auth login'" {
		t.Fatalf("datatug plan with an expired sign-in: %v", err)
	}
}

// A project chosen with F3 that cannot be opened says why on the setup
// screen, without the advice that belongs to the command line.
func TestSetupScreenNamesNoCommandToRunOutside(t *testing.T) {
	var reasons []string
	choices := []string{"shop", ""}
	covDSetVar(t, &runSetupWorkspaceFunc, func(_ workspaceStart, reason string, _ []tea.ProgramOption) (string, error) {
		reasons = append(reasons, reason)
		return choices[len(reasons)-1], nil
	})
	covDSetVar(t, &runChatProjectFunc, func(*cobra.Command, chatOptions) (string, error) {
		return "", Exit("project not found"+projectNotFoundAdvice, exitCodeUsage)
	})
	if err := runWorkspaceLoop(chatCommand(), chatOptions{project: ".", workspace: &workspaceStart{}}); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 2 || reasons[1] != `Could not open project "shop" for chat: project not found` {
		t.Fatalf("reasons = %q", reasons)
	}
}
