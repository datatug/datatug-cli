package chat

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

// currentHTTPOrigin is ui.go's (*UI).currentHTTPOrigin.
func (u *ChatUI) currentHTTPOrigin() string {
	var latest time.Time
	origin := ""
	for _, response := range u.snapshot.HTTPResponses {
		if response.CreatedAt.After(latest) {
			if candidate, err := httpOrigin(response.URL); err == nil {
				origin, latest = candidate, response.CreatedAt
			}
		}
	}
	return origin
}

// httpSettingsCommand is ui.go's (*UI).httpSettingsCommand, ported to
// chatshell: a name=value draft pushes httpScopeOverlay (ui.go's
// httpScopeOverlay, as a real Overlay) to choose CLI-wide vs. this-project
// persistence instead of setting u.httpSettingDraft directly (checklist
// item — HTTP header/cookie settings).
func (u *ChatUI) httpSettingsCommand(kind, argument string) (tea.Cmd, error) {
	if u.sessions == nil || u.sessions.store == nil {
		return nil, fmt.Errorf("HTTP settings need an active chat session")
	}
	origin := u.currentHTTPOrigin()
	argument = strings.TrimSpace(argument)
	explicitOrigin := false
	if strings.HasPrefix(argument, "http://") || strings.HasPrefix(argument, "https://") {
		explicitOrigin = true
		urlText, remaining, _ := strings.Cut(argument, " ")
		var err error
		origin, err = httpOrigin(urlText)
		if err != nil {
			return nil, err
		}
		argument = strings.TrimSpace(remaining)
	}
	if argument == "" {
		return nil, u.listHTTPSettings(kind, origin)
	}
	if name, ok := strings.CutPrefix(argument, "remove "); ok {
		name = strings.TrimSpace(name)
		if !validHTTPSettingName(name) {
			return nil, fmt.Errorf("usage: /http %s remove <name>", kind)
		}
		settings, err := u.sessions.store.HTTPRequestSettings(u.ctx, origin)
		if err != nil {
			return nil, fmt.Errorf("couldn't load HTTP settings")
		}
		scope := ""
		if kind == "header" {
			name = http.CanonicalHeaderKey(name)
			scope, origin = settings.HeaderScopes[name], settings.HeaderOrigins[name]
		} else {
			scope = settings.CookieScopes[name]
		}
		if scope != "cli" && scope != "project" {
			return nil, fmt.Errorf("that HTTP %s is not saved", kind)
		}
		if err := u.sessions.store.RemoveHTTPRequestSetting(u.ctx, scope, kind, origin, name); err != nil {
			return nil, fmt.Errorf("couldn't remove HTTP %s", kind)
		}
		u.shell.AppendAssistant("Removed " + kind + " " + sanitizeTerminalText(name) + " (" + scope + ").")
		return nil, nil
	}
	name, value, ok := strings.Cut(argument, "=")
	if !ok {
		return nil, fmt.Errorf("usage: /http %s [https://host] name=value", kind)
	}
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if kind == "header" && (strings.EqualFold(name, "User-Agent") || strings.EqualFold(name, "Accept") || strings.EqualFold(name, "Accept-Language")) && !explicitOrigin {
		origin = ""
	}
	canonical, err := validateHTTPSetting(kind, origin, name, value)
	if err != nil {
		return nil, err
	}
	return u.shell.PushOverlay(&httpScopeOverlay{ui: u, draft: httpSettingDraft{kind: kind, origin: origin, name: canonical, value: value}}), nil
}

// listHTTPSettings is ui.go's (*UI).listHTTPSettings, ported to
// shell.AppendAssistant.
func (u *ChatUI) listHTTPSettings(kind, origin string) error {
	settings, err := u.sessions.store.HTTPRequestSettings(u.ctx, origin)
	if err != nil {
		return fmt.Errorf("couldn't load HTTP settings")
	}
	label := "project-wide defaults"
	if origin != "" {
		label = origin
	}
	lines := []string{"HTTP " + kind + "s for " + label + ":"}
	if kind == "header" {
		names := make([]string, 0, len(settings.Headers))
		for name := range settings.Headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			lines = append(lines, sanitizeTerminalText(name)+"=[saved] ("+settings.HeaderScopes[name]+")")
		}
	} else {
		if origin == "" {
			lines = append(lines, "No current origin. Fetch a URL or specify an origin first.")
		}
		names := make([]string, 0, len(settings.Cookies))
		for name := range settings.Cookies {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			lines = append(lines, name+"=[saved] ("+settings.CookieScopes[name]+")")
		}
	}
	u.shell.AppendAssistant(strings.Join(lines, "\n"))
	return nil
}

// httpScopeOverlay is ui.go's httpScopeOverlay, as a chatshell.Overlay: "1"
// saves CLI-wide, "2" saves to this project, Esc cancels.
type httpScopeOverlay struct {
	ui    *ChatUI
	draft httpSettingDraft
	err   string
}

func (o *httpScopeOverlay) save(scope string) (chatshell.Overlay, tea.Cmd, bool) {
	d := o.draft
	if err := o.ui.sessions.store.SetHTTPRequestSetting(o.ui.ctx, scope, d.kind, d.origin, d.name, d.value); err != nil {
		o.err = "Couldn't save that setting."
		return o, nil, false
	}
	o.ui.shell.AppendAssistant(fmt.Sprintf("Saved %s %s (%s).", d.kind, sanitizeTerminalText(d.name), scope))
	return o, nil, true
}

func (o *httpScopeOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch key.String() {
	case "1":
		return o.save("cli")
	case "2":
		return o.save("project")
	case "esc":
		return o, nil, true
	}
	return o, nil, false
}

func (o *httpScopeOverlay) View(width, height int) string {
	draft := o.draft
	width = max(28, min(width, 68))
	target := "all HTTP origins"
	if draft.origin != "" {
		target = draft.origin
	}
	lines := []string{
		"Save HTTP " + draft.kind, "",
		"Name: " + draft.name,
		"Target: " + target,
		"Value: [hidden]", "",
		"1  CLI-wide (local to this machine)",
		"2  This project (local to this machine)", "",
		"Esc  Cancel",
	}
	if o.err != "" {
		lines = append(lines, "", o.err)
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(sanitizeTerminalText(line), max(1, width-6), "…")
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
}

var _ chatshell.Overlay = (*httpScopeOverlay)(nil)

// --- Alt+S table style cycling (checklist #18) ---------------------------

// cycleTableStyle is ui.go's (*UI).cycleTableStyle, ported: instead of a
// timed styleNotice field, it appends a transcript status line via
// AppendAssistant (chatshell.Model.SetStatus has no visible effect once
// WithStatusBar is set — ChatUI's own statusBar function always wins over
// chatshell's internal m.status, and has no way to read it back — so a
// bare SetStatus call here would silently confirm nothing to the user).
func (u *ChatUI) cycleTableStyle() tea.Cmd {
	next := nextTableStyle(u.tableStyle)
	if u.sessions != nil {
		if err := u.sessions.SetTableStyle(u.ctx, next.Name); err != nil {
			u.shell.AppendAssistant("Couldn't save table style.")
			return nil
		}
	}
	if u.tableStyle.Name != next.Name {
		u.tableStyle = next
		u.applyTableStyleToGrids()
	}
	u.shell.AppendAssistant("Table style: " + next.Name)
	return nil
}

// applyTableStyleToGrids restyles every currently-tracked grid — the
// ChatUI analogue of ui.go's setTableStyle grid-restyling loop, minus the
// dock/bookmark grids the workspace SidePanel already restyles itself via
// u.tableStyle on rebuild.
func (u *ChatUI) applyTableStyleToGrids() {
	for _, g := range u.gridsByRecordSetID {
		g.SetStyle(u.tableStyle)
	}
}
