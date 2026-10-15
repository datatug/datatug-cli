package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Link kinds, as printed by `daemon url`.
const (
	KindTableMetadata = "table-metadata"
	KindTableRecords  = "table-records"
	KindRecord        = "record"

	// UISupport values: pending means the web UI has no route for the kind yet.
	UISupportAvailable = "available"
	UISupportPending   = "pending"

	// DefaultUIBase is the hosted web UI; DATATUG_UI_BASE replaces it.
	DefaultUIBase = "https://datatug.app"
	UIBaseEnv     = "DATATUG_UI_BASE"

	DefaultLimit = 50
	MaxLimit     = 1000
)

// uiSupport is flipped to available per kind once datatug-apps ships the route
// (spec/features/cli/daemon, Follow-up).
var uiSupport = map[string]string{
	KindTableMetadata: UISupportAvailable,
	KindTableRecords:  UISupportPending,
	KindRecord:        UISupportPending,
}

// UsageError is a bad `daemon url` input (exit code 2).
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

// UIBase is the web UI address of the agent on port.
func UIBase(port int) string {
	base := strings.TrimRight(os.Getenv(UIBaseEnv), "/")
	if base == "" {
		base = DefaultUIBase
	}
	return fmt.Sprintf("%s/store/http-%s:%d", base, Host, port)
}

// LinkRequest names one card's target.
type LinkRequest struct {
	Project, Env, DB, Table, Schema string
	View                            bool
	Records                         bool
	Limit                           int
	Record                          string // primary key as a JSON object
}

// Link is the deep link of one card.
type Link struct {
	URL       string `json:"url"`
	Kind      string `json:"kind"`
	Running   bool   `json:"running"`
	UISupport string `json:"uiSupport"`
}

// BuildLink builds the deep link of contract deep-link-contract for the agent
// reported by st. It never reaches the network.
func BuildLink(st Status, req LinkRequest) (Link, error) {
	if req.Records && req.Record != "" {
		return Link{}, UsageError{"--records and --record are mutually exclusive"}
	}
	for _, f := range [][2]string{{"--project", req.Project}, {"--env", req.Env}, {"--db", req.DB}, {"--table", req.Table}} {
		if f[1] == "" {
			return Link{}, UsageError{f[0] + " is required"}
		}
	}
	for _, v := range []string{req.Project, req.Env, req.DB, req.Table, req.Schema} {
		if strings.Contains(v, "://") || strings.Contains(v, "@") {
			return Link{}, UsageError{"identifiers must be ids and names, never connection URLs"}
		}
	}
	kind := "table"
	if req.View {
		kind = "view"
	}
	u := fmt.Sprintf("%s/project/%s/env/%s/db/%s/%s/%s", st.UIBase,
		url.PathEscape(req.Project), url.PathEscape(req.Env), url.PathEscape(req.DB), kind, url.PathEscape(req.Table))
	query := url.Values{}
	out := Link{Kind: KindTableMetadata, Running: st.Running}
	switch {
	case req.Records:
		limit := req.Limit
		if limit == 0 {
			limit = DefaultLimit
		}
		if limit < 1 || limit > MaxLimit {
			return Link{}, UsageError{fmt.Sprintf("--limit must be between 1 and %d", MaxLimit)}
		}
		u += "/records"
		query.Set("limit", fmt.Sprint(limit))
		out.Kind = KindTableRecords
	case req.Record != "":
		pk, err := canonicalPK(req.Record)
		if err != nil {
			return Link{}, err
		}
		u += "/record"
		query.Set("pk", pk)
		out.Kind = KindRecord
	}
	if req.Schema != "" {
		query.Set("schema", req.Schema)
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	out.URL, out.UISupport = u, uiSupport[out.Kind]
	return out, nil
}

// canonicalPK checks that v is a non-empty JSON object and re-encodes it with
// sorted keys so one record always has one link.
func canonicalPK(v string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(v)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || len(obj) == 0 || dec.More() {
		return "", UsageError{"--record must be a non-empty JSON object of primary-key column to value"}
	}
	out, _ := json.Marshal(obj) // a decoded map always encodes
	return string(out), nil
}
