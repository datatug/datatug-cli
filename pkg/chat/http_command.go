package chat

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"gopkg.in/yaml.v3"
)

const maxHTTPResponseBytes = 16 << 20
const maxHTTPRequestBytes = 1 << 20

type httpRequestSpec struct {
	Method         string
	URL            string
	Headers        map[string]string
	ReplaceHeaders bool
	Body           string
}

type httpMessage struct {
	sessionID string
	snapshot  ChatSession
	err       error
}

func (u *UI) httpCommand(argument string) (tea.Cmd, error) {
	command, rest, _ := strings.Cut(strings.TrimSpace(argument), " ")
	command = strings.ToLower(command)
	if command == "header" || command == "cookie" {
		return nil, u.httpSettingsCommand(command, strings.TrimSpace(rest))
	}
	if command == "" || command == "new" {
		u.openHTTPRequestDialog(httpRequestSpec{Method: http.MethodGet, URL: strings.TrimSpace(rest)})
		return nil, nil
	}
	method := strings.ToUpper(command)
	if !supportedHTTPMethod(method) {
		return nil, fmt.Errorf("usage: /http [new|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS] [url] | /http header|cookie [name=value]")
	}
	if method != http.MethodGet || strings.TrimSpace(rest) == "" {
		u.openHTTPRequestDialog(httpRequestSpec{Method: method, URL: strings.TrimSpace(rest)})
		return nil, nil
	}
	return u.sendHTTPRequest(httpRequestSpec{Method: method, URL: strings.TrimSpace(rest)})
}

func supportedHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func (u *UI) sendHTTPRequest(spec httpRequestSpec) (tea.Cmd, error) {
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
	// Display and persistence never include query parameters, which may contain secrets.
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
	u.busy = true
	u.entries = append(u.entries, historyEntry{role: "You", text: requestText}, historyEntry{role: "DataTug", text: "Fetching…"})
	return func() tea.Msg {
		response, query, failure := fetchHTTPRequestResult(ctx, spec, displayURL.String(), settings)
		origin, err := store.AppendUser(ctx, sessionID, requestText)
		if err != nil {
			return httpMessage{sessionID: sessionID, err: err}
		}
		if failure != "" {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, displayURL.String(), Turn{Text: failure})
		} else {
			_, err = store.AppendHTTPResponse(ctx, sessionID, origin.ID, response, query)
		}
		if err != nil {
			return httpMessage{sessionID: sessionID, err: err}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return httpMessage{sessionID: sessionID, snapshot: snapshot, err: err}
	}, nil
}

func fetchHTTPResult(ctx context.Context, rawURL, displayURL string, options ...HTTPRequestSettings) (HTTPResponse, *QueryResult, string) {
	return fetchHTTPRequestResult(ctx, httpRequestSpec{Method: http.MethodGet, URL: rawURL}, displayURL, options...)
}

func fetchHTTPRequestResult(ctx context.Context, spec httpRequestSpec, displayURL string, options ...HTTPRequestSettings) (HTTPResponse, *QueryResult, string) {
	hops := make([]HTTPRedirect, 0, 3)
	settings := HTTPRequestSettings{Headers: map[string]string{"User-Agent": "DataTug"}}
	if len(options) > 0 {
		settings = options[0]
	}
	initialOrigin, _ := httpOrigin(spec.URL)
	client := &http.Client{Timeout: 15 * time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		started := time.Now()
		response, err := http.DefaultTransport.RoundTrip(request)
		if err == nil {
			hops = append(hops, HTTPRedirect{URL: sanitizedHTTPURL(request.URL), StatusCode: response.StatusCode, Elapsed: time.Since(started)})
		}
		return response, err
	}), CheckRedirect: func(request *http.Request, via []*http.Request) error {
		// net/http synthesizes Referer from the previous URL, including its query.
		request.Header.Del("Referer")
		if spec.Method != http.MethodGet && spec.Method != http.MethodHead {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 || request.URL.User != nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
			return errors.New("unsafe redirect")
		}
		redirectOrigin, _ := httpOrigin(request.URL.String())
		if redirectOrigin != initialOrigin {
			for name := range settings.Headers {
				request.Header.Del(name)
			}
			request.Header.Del("Cookie")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, spec.Method, spec.URL, strings.NewReader(spec.Body))
	if err != nil {
		return HTTPResponse{}, nil, "Couldn't fetch that URL: invalid request."
	}
	for name, value := range settings.Headers {
		request.Header.Set(name, value)
	}
	for name, value := range settings.Cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	requestHeaders := safeResponseHeaders(request.Header.Clone())
	requestStarted := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return HTTPResponse{}, nil, "Couldn't fetch that URL: network request failed."
	}
	responseArrived := time.Now()
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return HTTPResponse{}, nil, "Couldn't read the HTTP response."
	}
	if len(content) > maxHTTPResponseBytes {
		return HTTPResponse{}, nil, "HTTP response is too large to store (limit: 16 MiB)."
	}
	downloadFinished := time.Now()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	redirects := hops[:max(0, len(hops)-1)]
	stored := HTTPResponse{Method: spec.Method, URL: displayURL, FinalURL: sanitizedHTTPURL(response.Request.URL), Redirects: redirects, StatusCode: response.StatusCode, ContentType: mediaType, Headers: safeResponseHeaders(response.Header), RequestHeaders: requestHeaders, TimeToResponse: responseArrived.Sub(requestStarted), DownloadTime: downloadFinished.Sub(responseArrived), RequestHasQuery: request.URL.RawQuery != "", Body: content}
	if response.StatusCode < 200 || response.StatusCode >= 300 || stored.isMarkdown() {
		return stored, nil, ""
	}
	result, _, _ := formatHTTPBody(content, mediaType, response.Request.URL.Path)
	if len(result.Columns) > 0 {
		return stored, &QueryResult{Title: spec.Method + " " + displayURL, Result: result, Source: displayURL, SourceID: "http"}, ""
	}
	return stored, nil, ""
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func sanitizedHTTPURL(source *url.URL) string {
	if source == nil {
		return ""
	}
	copy := *source
	copy.User, copy.RawQuery, copy.Fragment, copy.ForceQuery = nil, "", "", false
	return copy.String()
}

// safeResponseHeaders stores values only for known metadata fields. A custom
// header name gives no assurance that its value is safe to persist.
func safeResponseHeaders(source http.Header) map[string][]string {
	result := make(map[string][]string, len(source))
	for name, values := range source {
		switch strings.ToLower(name) {
		case "content-type", "content-length", "content-encoding", "transfer-encoding":
			result[name] = append([]string(nil), values...)
		case "location", "content-location":
			for _, value := range values {
				parsed, err := url.Parse(value)
				if err != nil {
					result[name] = append(result[name], "[redacted]")
					continue
				}
				result[name] = append(result[name], sanitizedHTTPURL(parsed))
			}
		default:
			result[name] = []string{"[redacted]"}
		}
	}
	return result
}

func (response HTTPResponse) isMarkdown() bool {
	ext := strings.ToLower(path.Ext(response.URL))
	return response.ContentType == "text/markdown" || ext == ".md" || ext == ".markdown"
}

func (response HTTPResponse) summary() string {
	_, _, kind := formatHTTPBody(response.Body, response.ContentType, response.URL)
	if response.isMarkdown() {
		kind = "Markdown"
	}
	return fmt.Sprintf("%s · HTTP %d %s · %s", nonempty(response.Method, http.MethodGet), response.StatusCode, http.StatusText(response.StatusCode), kind)
}

func (response HTTPResponse) displayText() string {
	_, text, _ := formatHTTPBody(response.Body, response.ContentType, response.URL)
	if text == "" {
		return "(empty response)"
	}
	return text
}

func formatHTTPBody(content []byte, mediaType, urlPath string) (secureread.Result, string, string) {
	extension := strings.ToLower(path.Ext(urlPath))
	switch {
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || extension == ".json" || json.Valid(content):
		var value any
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err == nil {
			if result, ok := tabularJSON(value); ok {
				return result, "", "JSON"
			}
			pretty, _ := json.MarshalIndent(value, "", "  ")
			return secureread.Result{}, boundedText(pretty), "JSON"
		}
		return secureread.Result{}, "Invalid JSON response:\n" + boundedText(content), "JSON"
	case mediaType == "text/csv" || mediaType == "application/csv" || extension == ".csv":
		if result, err := tabularCSV(content); err == nil {
			return result, "", "CSV"
		}
		return secureread.Result{}, "Invalid CSV response:\n" + boundedText(content), "CSV"
	case mediaType == "application/yaml" || mediaType == "text/yaml" || mediaType == "application/x-yaml" || extension == ".yaml" || extension == ".yml":
		var value any
		if err := yaml.Unmarshal(content, &value); err == nil {
			if result, ok := tabularJSON(normalizeYAML(value)); ok {
				return result, "", "YAML"
			}
			return secureread.Result{}, boundedText(content), "YAML"
		}
		return secureread.Result{}, "Invalid YAML response:\n" + boundedText(content), "YAML"
	}
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "+xml") || mediaType == "application/xml" {
		return secureread.Result{}, boundedText(content), "Text"
	}
	return secureread.Result{}, "This response format cannot be displayed as text or a table.", nonempty(mediaType, "Unknown format")
}

func tabularJSON(value any) (secureread.Result, bool) {
	if object, ok := value.(map[string]any); ok {
		if rows, ok := object["data"].([]any); ok {
			value = rows
		} else if rows, ok := object["rows"].([]any); ok {
			if columnValues, ok := object["columns"].([]any); ok && len(columnValues) > 0 {
				columns := make([]string, len(columnValues))
				for i, column := range columnValues {
					name, ok := column.(string)
					if !ok || name == "" {
						return secureread.Result{}, false
					}
					columns[i] = name
				}
				if result, ok := tabularJSONArrays(columns, rows); ok {
					return result, true
				}
			}
			value = rows
		}
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return secureread.Result{}, false
	}
	columnsSet := map[string]bool{}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return secureread.Result{}, false
		}
		for column := range object {
			columnsSet[column] = true
		}
	}
	columns := make([]string, 0, len(columnsSet))
	for column := range columnsSet {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	rows := make([]secureread.Row, len(items))
	for i, item := range items {
		data := make(map[string]any, len(columns))
		for name, value := range item.(map[string]any) {
			data[name] = normalizeHTTPValue(value)
		}
		rows[i] = secureread.Row{Data: data}
	}
	return secureread.Result{Columns: columns, Rows: rows}, true
}

func tabularJSONArrays(columns []string, values []any) (secureread.Result, bool) {
	rows := make([]secureread.Row, 0, len(values))
	for _, value := range values {
		cells, ok := value.([]any)
		if !ok {
			return secureread.Result{}, false
		}
		data := make(map[string]any, len(columns))
		for i, column := range columns {
			if i < len(cells) {
				data[column] = normalizeHTTPValue(cells[i])
			}
		}
		rows = append(rows, secureread.Row{Data: data})
	}
	return secureread.Result{Columns: columns, Rows: rows}, true
}

func tabularCSV(content []byte) (secureread.Result, error) {
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) == 0 {
		return secureread.Result{}, errors.New("invalid CSV")
	}
	columns := make([]string, len(records[0]))
	used := map[string]int{}
	for i, column := range records[0] {
		column = strings.TrimSpace(column)
		if column == "" {
			column = "Column " + strconv.Itoa(i+1)
		}
		used[column]++
		if used[column] > 1 {
			column += " " + strconv.Itoa(used[column])
		}
		columns[i] = column
	}
	rows := make([]secureread.Row, 0, len(records)-1)
	for _, record := range records[1:] {
		data := make(map[string]any, len(columns))
		for i, column := range columns {
			if i < len(record) {
				data[column] = record[i]
			}
		}
		rows = append(rows, secureread.Row{Data: data})
	}
	return secureread.Result{Columns: columns, Rows: rows}, nil
}

func normalizeYAML(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			value[key] = normalizeYAML(item)
		}
	case map[any]any:
		converted := make(map[string]any, len(value))
		for key, item := range value {
			converted[fmt.Sprint(key)] = normalizeYAML(item)
		}
		return converted
	case []any:
		for i, item := range value {
			value[i] = normalizeYAML(item)
		}
	}
	return value
}

func normalizeHTTPValue(value any) any {
	switch value := value.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer
		}
		if number, err := value.Float64(); err == nil {
			return number
		}
		return string(value)
	case map[string]any, []any:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	default:
		return value
	}
}

func boundedText(content []byte) string {
	if len(content) > 12000 {
		return string(content[:12000]) + "\n… (truncated)"
	}
	return string(content)
}
