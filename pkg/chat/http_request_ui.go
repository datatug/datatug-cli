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
)

var httpFormMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions}

type httpRequestDialog struct {
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

type httpHeaderField struct {
	name       string
	value      string
	configured bool
}

func (u *UI) openHTTPRequestDialog(spec httpRequestSpec) {
	if u.busy {
		return
	}
	d := &httpRequestDialog{method: spec.Method, editing: -1}
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
	if u.sessions != nil && u.sessions.store != nil {
		origin, _ := httpOrigin(spec.URL)
		if settings, err := u.sessions.store.HTTPRequestSettings(u.ctx, origin); err == nil {
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
	u.httpRequestDialog = d
}

func (d *httpRequestDialog) setFocus(next int) {
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

func (u *UI) updateHTTPRequestDialog(key tea.KeyPressMsg) tea.Cmd {
	d := u.httpRequestDialog
	if d == nil {
		return nil
	}
	d.err = ""
	switch key.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		u.httpRequestDialog = nil
		return nil
	case "tab", "shift+tab":
		if d.focus == 1 {
			u.loadHTTPRequestDefaults(d)
		}
		delta := 1
		if key.String() == "shift+tab" {
			delta = -1
		}
		d.setFocus(d.focus + delta)
		return nil
	case "ctrl+enter":
		return u.submitHTTPRequestDialog()
	case "shift+enter":
		if d.focus == 5 {
			d.body.InsertString("\n")
		}
		return nil
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
			return nil
		}
	case "up", "down":
		if d.focus == 2 && len(d.headers) > 0 {
			if key.String() == "up" {
				d.selected = max(0, d.selected-1)
			} else {
				d.selected = min(len(d.headers)-1, d.selected+1)
			}
			return nil
		}
	case "a":
		if d.focus == 2 {
			d.editing = -1
			d.headerName.Reset()
			d.headerValue.Reset()
			d.setFocus(3)
			return nil
		}
	case "e", "enter":
		if d.focus == 2 && len(d.headers) > 0 {
			d.editing = d.selected
			d.headerName.SetValue(d.headers[d.selected].name)
			d.headerValue.SetValue(d.headers[d.selected].value)
			d.setFocus(3)
			return nil
		}
	case "d", "delete":
		if d.focus == 2 && len(d.headers) > 0 {
			d.headers = append(d.headers[:d.selected], d.headers[d.selected+1:]...)
			d.selected = max(0, min(d.selected, len(d.headers)-1))
			return nil
		}
	}
	if key.String() == "enter" {
		switch d.focus {
		case 0, 1:
			if d.focus == 1 {
				u.loadHTTPRequestDefaults(d)
			}
			d.setFocus(d.focus + 1)
		case 3:
			d.setFocus(4)
		case 4:
			d.commitHeader()
		case 5:
			d.body.InsertString("\n")
		case 6:
			return u.submitHTTPRequestDialog()
		case 7:
			u.saveHTTPRequestFromDialog()
		}
		return nil
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
	return cmd
}

func (d *httpRequestDialog) commitHeader() {
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

func (u *UI) loadHTTPRequestDefaults(d *httpRequestDialog) {
	if u.sessions == nil || u.sessions.store == nil {
		return
	}
	origin, err := httpOrigin(strings.TrimSpace(d.url.Value()))
	if err != nil || origin == d.loadedOrigin {
		return
	}
	settings, err := u.sessions.store.HTTPRequestSettings(u.ctx, origin)
	if err != nil {
		d.err = "Could not load configured HTTP headers."
		return
	}
	// Changing origin replaces configured values. Edited fields remain local to
	// this request and take precedence over configured defaults.
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

func (d *httpRequestDialog) spec() (httpRequestSpec, error) {
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

func (u *UI) submitHTTPRequestDialog() tea.Cmd {
	d := u.httpRequestDialog
	u.loadHTTPRequestDefaults(d)
	spec, err := d.spec()
	if err != nil {
		d.err = err.Error()
		return nil
	}
	cmd, err := u.sendHTTPRequest(spec)
	if err != nil {
		d.err = err.Error()
		return nil
	}
	u.httpRequestDialog = nil
	u.rebuildHistory(true)
	return cmd
}

func (u *UI) saveHTTPRequestFromDialog() {
	d := u.httpRequestDialog
	u.loadHTTPRequestDefaults(d)
	spec, err := d.spec()
	if err != nil {
		d.err = err.Error()
		return
	}
	if u.savedQueryService == nil {
		d.err = "Project query saving is unavailable."
		return
	}
	if spec.Method != http.MethodGet || spec.Body != "" {
		d.err = "Project HTTP queries currently support GET only; submit this request without saving."
		return
	}
	for name, value := range spec.Headers {
		if name != "User-Agent" || value != "DataTug" {
			d.err = "Project HTTP queries cannot safely store custom headers yet. Submit this request without saving."
			return
		}
	}
	u.httpRequestDialog = nil
	u.openSaveQueryForRequest(SavedQuerySaveRequest{Type: "HTTP", Text: spec.URL, Title: spec.URL})
}

func (u *UI) httpRequestOverlay(background string) string {
	d := u.httpRequestDialog
	if d == nil {
		return background
	}
	width := max(40, min(u.width-4, 84))
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
	box := lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}
