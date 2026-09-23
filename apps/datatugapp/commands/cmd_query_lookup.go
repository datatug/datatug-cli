package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

var lookupIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func lookupBase(federation *datatug.QueryFederation) (*url.URL, error) {
	base, err := url.Parse(federation.OVDBBaseURL)
	if err != nil || base == nil {
		return nil, fmt.Errorf("lookup OVDB base URL must use HTTPS or local HTTP without credentials or query parameters")
	}
	localHTTP := base.Scheme == "http" && (base.Hostname() == "localhost" || base.Hostname() == "127.0.0.1" || base.Hostname() == "::1")
	if base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && !localHTTP) {
		return nil, fmt.Errorf("lookup OVDB base URL must use HTTPS or local HTTP without credentials or query parameters")
	}
	return base, nil
}

func lookupValue(base *url.URL, client *http.Client, lookup datatug.QueryHTTPLookup) (dal.LookupValue, error) {
	if !lookupIdentifier.MatchString(lookup.Database) || !lookupIdentifier.MatchString(lookup.Collection) {
		return nil, fmt.Errorf("lookup database and collection must be simple identifiers")
	}
	return func(ctx context.Context, row record.Record) (map[string]any, error) {
		data, ok := row.Data().(map[string]any)
		if !ok {
			return nil, fmt.Errorf("lookup source row has non-object data")
		}
		value := data[lookup.FromColumn]
		var id string
		switch typed := value.(type) {
		case string:
			id = typed
		case int:
			id = strconv.Itoa(typed)
		case int64:
			id = strconv.FormatInt(typed, 10)
		case float64:
			if typed != float64(int64(typed)) {
				return nil, fmt.Errorf("lookup column %s requires a string or integer", lookup.FromColumn)
			}
			id = strconv.FormatInt(int64(typed), 10)
		default:
			return nil, fmt.Errorf("lookup column %s requires a string or integer", lookup.FromColumn)
		}
		endpoint := strings.TrimSuffix(base.String(), "/") + "/v1/databases/" + lookup.Database + "/records/" + lookup.Collection + "/" + url.PathEscape(id)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("OVDB lookup %s/%s failed (%d)", lookup.Database, lookup.Collection, response.StatusCode)
		}
		var payload struct {
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
			return nil, err
		}
		if payload.Data == nil {
			return nil, fmt.Errorf("OVDB lookup returned no JSON data")
		}
		fields := make(map[string]any, len(lookup.Fields))
		for _, field := range lookup.Fields {
			fields[field.Target] = payload.Data[field.Source]
		}
		return fields, nil
	}, nil
}

func newLookupClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func appendLookupColumns(columns []string, lookup datatug.QueryHTTPLookup) []string {
	for _, field := range lookup.Fields {
		found := false
		for _, name := range columns {
			if name == field.Target {
				found = true
				break
			}
		}
		if !found {
			columns = append(columns, field.Target)
		}
	}
	return columns
}

func applySavedQueryLookups(ctx context.Context, result secureread.Result, federation *datatug.QueryFederation, progress io.Writer, quiet bool) (secureread.Result, error) {
	base, err := lookupBase(federation)
	if err != nil {
		return result, err
	}
	client := newLookupClient()
	rows := make([]record.Record, len(result.Rows))
	for i, row := range result.Rows {
		rows[i] = record.NewRecordWithData(record.NewKeyWithID("result", i), row.Data)
	}
	for _, lookup := range federation.Lookups {
		value, err := lookupValue(base, client, lookup)
		if err != nil {
			return result, err
		}
		rows, err = dal.ExecuteRecordLookups(ctx, rows, value, dal.LookupOptions{Concurrency: lookup.Concurrency, OnProgress: lookupProgress(progress, quiet)})
		if err != nil {
			return result, err
		}
		result.Columns = appendLookupColumns(result.Columns, lookup)
	}
	for i, row := range rows {
		result.Rows[i].Data = row.Data().(map[string]any)
	}
	return result, nil
}

func lookupProgress(progress io.Writer, quiet bool) func(dal.LookupProgress) {
	return func(item dal.LookupProgress) {
		if !quiet && (item.RequestsCompleted == item.RowsLoaded || item.RequestsCompleted%25 == 0) {
			_, _ = fmt.Fprintf(progress, "lookup: %d rows loaded, %d completed, %d in flight, %d pending\n", item.RowsLoaded, item.RequestsCompleted, item.RequestsInFlight, item.RequestsPending)
		}
	}
}

func streamSavedQueryLookups(ctx context.Context, source dal.RecordsReader, federation *datatug.QueryFederation, progress io.Writer, quiet bool) (dal.RecordsReader, error) {
	if federation == nil || len(federation.Lookups) == 0 {
		return source, nil
	}
	base, err := lookupBase(federation)
	if err != nil {
		return nil, err
	}
	client := newLookupClient()
	current := source
	for _, lookup := range federation.Lookups {
		value, err := lookupValue(base, client, lookup)
		if err != nil {
			return nil, err
		}
		current, err = dal.ExecuteRecordLookupStream(ctx, current, value, dal.LookupOptions{Concurrency: lookup.Concurrency, OnProgress: lookupProgress(progress, quiet)})
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}
