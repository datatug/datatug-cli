package chat

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type httpSettingDraft struct {
	kind, origin, name, value string
}

func (u *UI) currentHTTPOrigin() string {
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

func (u *UI) httpSettingsCommand(kind, argument string) error {
	if u.sessions == nil || u.sessions.store == nil {
		return fmt.Errorf("HTTP settings need an active chat session")
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
			return err
		}
		argument = strings.TrimSpace(remaining)
	}
	if argument == "" {
		return u.listHTTPSettings(kind, origin)
	}
	if name, ok := strings.CutPrefix(argument, "remove "); ok {
		name = strings.TrimSpace(name)
		if !validHTTPSettingName(name) {
			return fmt.Errorf("usage: /http %s remove <name>", kind)
		}
		settings, err := u.sessions.store.HTTPRequestSettings(u.ctx, origin)
		if err != nil {
			return fmt.Errorf("couldn't load HTTP settings")
		}
		scope := ""
		if kind == "header" {
			name = http.CanonicalHeaderKey(name)
			scope, origin = settings.HeaderScopes[name], settings.HeaderOrigins[name]
		} else {
			scope = settings.CookieScopes[name]
		}
		if scope != "cli" && scope != "project" {
			return fmt.Errorf("that HTTP %s is not saved", kind)
		}
		if err := u.sessions.store.RemoveHTTPRequestSetting(u.ctx, scope, kind, origin, name); err != nil {
			return fmt.Errorf("couldn't remove HTTP %s", kind)
		}
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Removed " + kind + " " + sanitizeTerminalText(name) + " (" + scope + ")."})
		return nil
	}
	name, value, ok := strings.Cut(argument, "=")
	if !ok {
		return fmt.Errorf("usage: /http %s [https://host] name=value", kind)
	}
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if kind == "header" && (strings.EqualFold(name, "User-Agent") || strings.EqualFold(name, "Accept") || strings.EqualFold(name, "Accept-Language")) && !explicitOrigin {
		origin = ""
	}
	canonical, err := validateHTTPSetting(kind, origin, name, value)
	if err != nil {
		return err
	}
	u.httpSettingDraft = &httpSettingDraft{kind: kind, origin: origin, name: canonical, value: value}
	return nil
}

func (u *UI) listHTTPSettings(kind, origin string) error {
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
	u.entries = append(u.entries, historyEntry{role: "DataTug", text: strings.Join(lines, "\n")})
	return nil
}

func (u *UI) httpScopeOverlay(background string) string {
	if u.httpSettingDraft == nil {
		return background
	}
	draft := u.httpSettingDraft
	width := max(28, min(u.width-4, 68))
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
	for i, line := range lines {
		lines[i] = ansi.Truncate(sanitizeTerminalText(line), max(1, width-6), "…")
	}
	box := lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}
