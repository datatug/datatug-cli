package chat

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

// --- ChatUI-level HTTP plumbing (ported from http_command.go) ------------

// runHTTPCommand ports ui.go's "/http" branch of runSessionCommand:
// "/http header|cookie ..." manages persisted request headers/cookies
// (chatui_http_settings.go); every other form opens the request dialog or
// sends a plain GET directly.
func (u *ChatUI) runHTTPCommand(argument string) (tea.Cmd, error) {
	command, rest, _ := strings.Cut(strings.TrimSpace(argument), " ")
	command = strings.ToLower(command)
	if command == "header" || command == "cookie" {
		return u.httpSettingsCommand(command, strings.TrimSpace(rest))
	}
	if command == "" || command == "new" {
		return u.shell.PushOverlay(newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: strings.TrimSpace(rest)})), nil
	}
	method := strings.ToUpper(command)
	if !supportedHTTPMethod(method) {
		return nil, fmt.Errorf("usage: /http [new|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS] [url]")
	}
	if method != http.MethodGet || strings.TrimSpace(rest) == "" {
		return u.shell.PushOverlay(newHTTPRequestOverlay(u, httpRequestSpec{Method: method, URL: strings.TrimSpace(rest)})), nil
	}
	return u.sendHTTPRequest(httpRequestSpec{Method: method, URL: strings.TrimSpace(rest)})
}

// httpDoneMsg reports a background HTTP request's outcome — the ChatUI
// analogue of http_command.go's httpMessage.
type httpDoneMsg struct {
	sessionID string
	snapshot  ChatSession
	err       error
}

// sendHTTPRequest ports http_command.go's sendHTTPRequest, minus its direct
// u.entries/u.busy mutation (now shell.AppendBlock/SetBusy).
func (u *ChatUI) sendHTTPRequest(spec httpRequestSpec) (tea.Cmd, error) {
	if !supportedHTTPMethod(spec.Method) {
		return nil, fmt.Errorf("unsupported HTTP method")
	}
	if (spec.Method == http.MethodGet || spec.Method == http.MethodHead) && spec.Body != "" {
		return nil, fmt.Errorf("GET and HEAD requests cannot have a body")
	}
	rawURL := spec.URL
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("/http needs an HTTP or HTTPS URL without embedded credentials")
	}
	displayURL := *parsed
	displayURL.RawQuery, displayURL.ForceQuery, displayURL.Fragment = "", false, ""
	requestText := "/http " + strings.ToLower(spec.Method) + " " + displayURL.String()
	if u.sessions == nil || u.sessions.store == nil {
		return nil, fmt.Errorf("HTTP requests need an active chat session")
	}
	origin, err := httpOrigin(rawURL)
	if err != nil {
		return nil, err
	}
	settings, err := u.sessions.store.HTTPRequestSettings(u.ctx, origin)
	if err != nil {
		return nil, fmt.Errorf("couldn't load HTTP request settings")
	}
	if spec.ReplaceHeaders {
		settings.Headers = make(map[string]string)
	}
	for name, value := range spec.Headers {
		canonical, validationErr := validateHTTPSetting("header", origin, name, value)
		if validationErr != nil {
			return nil, fmt.Errorf("invalid request header %q", name)
		}
		settings.Headers[canonical] = value
	}
	if len(spec.Body) > maxHTTPRequestBytes {
		return nil, fmt.Errorf("HTTP request body exceeds 1 MiB")
	}
	sessionID := u.sessionID
	store := u.sessions.store
	ctx := u.ctx
	u.appendKindedBlock("msg", transcriptEntryKindMessage, newUserMessageBlock(requestText))
	busyCmd := u.shell.SetBusy(true)
	runCmd := func() tea.Msg {
		response, query, failure := fetchHTTPRequestResult(ctx, spec, displayURL.String(), settings)
		origin, err := store.AppendUser(ctx, sessionID, requestText)
		if err != nil {
			return httpDoneMsg{sessionID: sessionID, err: err}
		}
		if failure != "" {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, displayURL.String(), Turn{Text: failure})
		} else {
			_, err = store.AppendHTTPResponse(ctx, sessionID, origin.ID, response, query)
		}
		if err != nil {
			return httpDoneMsg{sessionID: sessionID, err: err}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return httpDoneMsg{sessionID: sessionID, snapshot: snapshot, err: err}
	}
	if busyCmd != nil {
		return tea.Batch(busyCmd, runCmd), nil
	}
	return runCmd, nil
}

// handleHTTPDone is called from OnMsg for an httpDoneMsg. r1b item 5b: when
// the request was submitted from httpRequestOverlay (pendingHTTPRequest set
// by submit()), a failure keeps that overlay open with its draft and shows
// the error there instead of losing the draft and posting a generic
// transcript message; success closes it via CloseOverlay (identity-based --
// safe even if another overlay has since been pushed on top). A request
// sent without a dialog (runHTTPCommand's direct "/http GET url" path)
// leaves pendingHTTPRequest nil, so it keeps the prior transcript-message
// behavior unchanged.
func (u *ChatUI) handleHTTPDone(msg httpDoneMsg) {
	u.shell.SetBusy(false)
	overlay := u.pendingHTTPRequest
	u.pendingHTTPRequest = nil
	if msg.err != nil {
		if overlay != nil {
			overlay.err = conciseError(msg.err)
			return
		}
		u.shell.AppendAssistant(conciseError(msg.err))
		return
	}
	if overlay != nil {
		u.shell.CloseOverlay(overlay)
	}
	if msg.sessionID == u.sessionID {
		u.loadSession(msg.snapshot)
	}
}

// --- httpRequestOverlay (checklist item #49) -------------------------------

// httpFormMethods is defined in http_request_ui.go and reused here as-is.

// httpRequestOverlay is ChatUI's chatshell.Overlay port of the retired
// legacy UI's httpRequestDialog: method/URL/headers/body fields,
// Ctrl+Enter submits. "Save as project query" (saveAsProjectQuery below)
// pushes ChatUI's own saveQueryOverlay on top of this one -- GET-only,
// custom-header-free requests, same constraints ui.go's saveHTTPRequestFromDialog
// enforced -- rather than reporting "not yet available".
type httpRequestOverlay struct {
	ui *ChatUI

	method       string
	url          textinput.Model
	body         textarea.Model
	headerName   textinput.Model
	headerValue  textinput.Model
	headers      []httpHeaderField
	selected     int
	editing      int
	loadedOrigin string
	focus        int // method, URL, headers, name, value, body, send, save
	err          string
}

func newHTTPRequestOverlay(ui *ChatUI, spec httpRequestSpec) *httpRequestOverlay {
	d := &httpRequestOverlay{ui: ui, method: spec.Method, editing: -1}
	if !supportedHTTPMethod(d.method) {
		d.method = http.MethodGet
	}
	d.url = textinput.New()
	d.url.Prompt = ""
	d.url.SetValue(spec.URL)
	d.headerName = textinput.New()
	d.headerName.Prompt = ""
	d.headerValue = textinput.New()
	d.headerValue.Prompt = ""
	d.headerValue.EchoMode = textinput.EchoPassword
	d.body = textarea.New()
	d.body.Prompt = ""
	d.body.Placeholder = "Optional request body"
	d.body.ShowLineNumbers = false
	d.body.SetHeight(3)
	d.body.SetValue(spec.Body)
	if ui.sessions != nil && ui.sessions.store != nil {
		origin, _ := httpOrigin(spec.URL)
		if settings, err := ui.sessions.store.HTTPRequestSettings(ui.ctx, origin); err == nil {
			d.loadedOrigin = origin
			for name, value := range settings.Headers {
				d.headers = append(d.headers, httpHeaderField{name: name, value: value, configured: true})
			}
			sort.Slice(d.headers, func(i, j int) bool { return d.headers[i].name < d.headers[j].name })
		}
	}
	for name, value := range spec.Headers {
		d.headers = append(d.headers, httpHeaderField{name: name, value: value})
	}
	if spec.URL == "" {
		d.focus = 1
		d.url.Focus()
	}
	return d
}

func (d *httpRequestOverlay) setFocus(next int) {
	d.url.Blur()
	d.headerName.Blur()
	d.headerValue.Blur()
	d.body.Blur()
	d.focus = (next + 8) % 8
	switch d.focus {
	case 1:
		d.url.Focus()
	case 3:
		d.headerName.Focus()
	case 4:
		d.headerValue.Focus()
	case 5:
		d.body.Focus()
	}
}

func (d *httpRequestOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return d, nil, false
	}
	d.err = ""
	switch key.String() {
	case "ctrl+c":
		return d, tea.Quit, false
	case "esc":
		return d, nil, true
	case "tab", "shift+tab":
		if d.focus == 1 {
			d.loadDefaults()
		}
		delta := 1
		if key.String() == "shift+tab" {
			delta = -1
		}
		d.setFocus(d.focus + delta)
		return d, nil, false
	case "ctrl+enter":
		return d.submit()
	case "shift+enter":
		if d.focus == 5 {
			d.body.InsertString("\n")
		}
		return d, nil, false
	case "left", "right":
		if d.focus == 0 {
			index := 0
			for i, method := range httpFormMethods {
				if method == d.method {
					index = i
					break
				}
			}
			if key.String() == "left" {
				index = (index + len(httpFormMethods) - 1) % len(httpFormMethods)
			} else {
				index = (index + 1) % len(httpFormMethods)
			}
			d.method = httpFormMethods[index]
			return d, nil, false
		}
	case "up", "down":
		if d.focus == 2 && len(d.headers) > 0 {
			if key.String() == "up" {
				d.selected = max(0, d.selected-1)
			} else {
				d.selected = min(len(d.headers)-1, d.selected+1)
			}
			return d, nil, false
		}
	case "a":
		if d.focus == 2 {
			d.editing = -1
			d.headerName.Reset()
			d.headerValue.Reset()
			d.setFocus(3)
			return d, nil, false
		}
	case "e":
		if d.focus == 2 && len(d.headers) > 0 {
			d.editing = d.selected
			d.headerName.SetValue(d.headers[d.selected].name)
			d.headerValue.SetValue(d.headers[d.selected].value)
			d.setFocus(3)
			return d, nil, false
		}
	case "delete":
		if d.focus == 2 && len(d.headers) > 0 {
			d.headers = append(d.headers[:d.selected], d.headers[d.selected+1:]...)
			d.selected = max(0, min(d.selected, len(d.headers)-1))
			return d, nil, false
		}
	}
	if key.String() == "enter" {
		switch d.focus {
		case 0, 1:
			if d.focus == 1 {
				d.loadDefaults()
			}
			d.setFocus(d.focus + 1)
		case 2:
			if len(d.headers) > 0 {
				d.editing = d.selected
				d.headerName.SetValue(d.headers[d.selected].name)
				d.headerValue.SetValue(d.headers[d.selected].value)
				d.setFocus(3)
			}
		case 3:
			d.setFocus(4)
		case 4:
			d.commitHeader()
		case 5:
			d.body.InsertString("\n")
		case 6:
			return d.submit()
		case 7:
			return d.saveAsProjectQuery()
		}
		return d, nil, false
	}
	var cmd tea.Cmd
	switch d.focus {
	case 1:
		d.url, cmd = d.url.Update(key)
	case 3:
		d.headerName, cmd = d.headerName.Update(key)
	case 4:
		d.headerValue, cmd = d.headerValue.Update(key)
	case 5:
		d.body, cmd = d.body.Update(key)
	}
	return d, cmd, false
}

func (d *httpRequestOverlay) commitHeader() {
	name := strings.TrimSpace(d.headerName.Value())
	value := d.headerValue.Value()
	if !validHTTPSettingName(name) || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		d.err = "Enter a valid header name and one-line value."
		return
	}
	name = http.CanonicalHeaderKey(name)
	for i, header := range d.headers {
		if i != d.editing && strings.EqualFold(header.name, name) {
			d.err = "That header already exists; edit it in the list."
			return
		}
	}
	if d.editing >= 0 && d.editing < len(d.headers) {
		d.headers[d.editing] = httpHeaderField{name: name, value: value}
	} else {
		d.headers = append(d.headers, httpHeaderField{name: name, value: value})
	}
	d.editing = -1
	d.headerName.Reset()
	d.headerValue.Reset()
	d.setFocus(2)
}

// saveAsProjectQuery is ui.go's saveHTTPRequestFromDialog, ported to push
// ChatUI's saveQueryOverlay (chatui_inspector.go) on top of this overlay
// instead of switching u.saveQueryDialog. The underlying HTTP form stays on
// the overlay stack (done=false) so Esc from the save dialog returns to it,
// matching the query_parameters_dialog.go lookup dialog's own
// stacked-Overlay idiom.
func (d *httpRequestOverlay) saveAsProjectQuery() (chatshell.Overlay, tea.Cmd, bool) {
	d.loadDefaults()
	spec, err := d.spec()
	if err != nil {
		d.err = err.Error()
		return d, nil, false
	}
	if d.ui.savedQueryService == nil {
		d.err = "Project query saving is unavailable."
		return d, nil, false
	}
	if spec.Method != http.MethodGet || spec.Body != "" {
		d.err = "Project HTTP queries currently support GET only; submit this request without saving."
		return d, nil, false
	}
	for name, value := range spec.Headers {
		if name != "User-Agent" || value != "DataTug" {
			d.err = "Project HTTP queries cannot safely store custom headers yet. Submit this request without saving."
			return d, nil, false
		}
	}
	return d, d.ui.shell.PushOverlay(newSaveQueryOverlay(d.ui, SavedQuerySaveRequest{Type: "HTTP", Text: spec.URL, Title: spec.URL})), false
}

func (d *httpRequestOverlay) loadDefaults() {
	if d.ui.sessions == nil || d.ui.sessions.store == nil {
		return
	}
	origin, err := httpOrigin(strings.TrimSpace(d.url.Value()))
	if err != nil || origin == d.loadedOrigin {
		return
	}
	settings, err := d.ui.sessions.store.HTTPRequestSettings(d.ui.ctx, origin)
	if err != nil {
		d.err = "Could not load configured HTTP headers."
		return
	}
	manual := make([]httpHeaderField, 0, len(d.headers))
	if d.loadedOrigin == "" {
		for _, field := range d.headers {
			if !field.configured {
				manual = append(manual, field)
			}
		}
	} else {
		d.err = "The URL origin changed; request headers were reset to that origin's defaults."
	}
	d.headers = manual
	for name, value := range settings.Headers {
		found := false
		for _, field := range d.headers {
			if strings.EqualFold(field.name, name) {
				found = true
				break
			}
		}
		if !found {
			d.headers = append(d.headers, httpHeaderField{name: name, value: value, configured: true})
		}
	}
	sort.Slice(d.headers, func(i, j int) bool { return d.headers[i].name < d.headers[j].name })
	d.loadedOrigin = origin
}

func (d *httpRequestOverlay) spec() (httpRequestSpec, error) {
	raw := strings.TrimSpace(d.url.Value())
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return httpRequestSpec{}, fmt.Errorf("enter an HTTP or HTTPS URL without embedded credentials")
	}
	if (d.method == http.MethodGet || d.method == http.MethodHead) && strings.TrimSpace(d.body.Value()) != "" {
		return httpRequestSpec{}, fmt.Errorf("GET and HEAD cannot have a request body")
	}
	spec := httpRequestSpec{Method: d.method, URL: raw, Body: d.body.Value(), Headers: make(map[string]string), ReplaceHeaders: true}
	for _, header := range d.headers {
		spec.Headers[header.name] = header.value
	}
	return spec, nil
}

// submit stays open (done=false) once the request is actually in flight:
// r1b item 5b (async-safe dialogs, via strongo/aichat's CloseOverlay) keeps
// the form -- method/URL/headers/body draft included -- on screen until
// handleHTTPDone knows whether it succeeded, instead of closing
// optimistically and losing the draft/showing a generic transcript error on
// a failure that had nothing to do with what the user typed (a storage
// write failing, a stale session). d.ui.pendingHTTPRequest names this
// overlay so handleHTTPDone can reach it; only a SYNCHRONOUS validation
// failure (an invalid spec, sendHTTPRequest's own upfront checks) sets
// d.err and returns immediately, as before.
func (d *httpRequestOverlay) submit() (chatshell.Overlay, tea.Cmd, bool) {
	d.loadDefaults()
	spec, err := d.spec()
	if err != nil {
		d.err = err.Error()
		return d, nil, false
	}
	cmd, err := d.ui.sendHTTPRequest(spec)
	if err != nil {
		d.err = err.Error()
		return d, nil, false
	}
	d.ui.pendingHTTPRequest = d
	return d, cmd, false
}

func (d *httpRequestOverlay) View(width, height int) string {
	width = max(40, min(width, 84))
	inside := max(28, width-6)
	d.url.SetWidth(max(12, inside-8))
	d.headerName.SetWidth(max(8, inside/3))
	d.headerValue.SetWidth(max(8, inside/2))
	d.body.SetWidth(inside - 4)
	active := lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true)
	line := func(index int, label string) string {
		if d.focus == index {
			return active.Render("› " + label)
		}
		return "  " + label
	}
	lines := []string{"HTTP request", "", line(0, "Method: "+d.method+"  ←/→ change"), line(1, "URL: "+d.url.View()), line(2, "Headers: ↑/↓ choose · Enter edit · a add · d delete")}
	start := max(0, d.selected-4)
	if start > 0 {
		lines = append(lines, fmt.Sprintf("    ↑ %d more headers", start))
	}
	for i := start; i < min(len(d.headers), start+5); i++ {
		header := d.headers[i]
		prefix := "    "
		if d.focus == 2 && d.selected == i {
			prefix = "  › "
		}
		lines = append(lines, prefix+sanitizeTerminalText(header.name)+": [hidden]")
	}
	if remaining := len(d.headers) - min(len(d.headers), start+5); remaining > 0 {
		lines = append(lines, fmt.Sprintf("    ↓ %d more headers", remaining))
	}
	lines = append(lines, line(3, "Header name: "+d.headerName.View()), line(4, "Header value: "+d.headerValue.View()), line(5, "Body:"), d.body.View(), line(6, "Submit request"), line(7, "Save as project query"), "", "Tab moves · Ctrl+Enter submits · Esc cancels")
	if d.err != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(d.err))
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], inside, "…")
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
}

var _ chatshell.Overlay = (*httpRequestOverlay)(nil)
