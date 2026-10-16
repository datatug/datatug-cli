package daemon

import (
	"fmt"
	"strings"
)

// KindQueryResult is the link of a stored query result (`daemon url --result`).
const KindQueryResult = "query-result"

// ResultViews are the views of a result page, in the order the card shows them.
var ResultViews = []string{"table", "record", "columns", "sources", "related"}

// MaxResultRow bounds --row: a position among the rows a result keeps.
const MaxResultRow = 10000

// ResultLinkRequest names a stored result, the view to open and, optionally,
// the position of a row (never a key).
type ResultLinkRequest struct {
	ID   string
	View string
	Row  int
	// Exists is whether the result's meta.json is there; no link is built
	// for a result that is not.
	Exists bool
	// Code is the one-time code put first in the fragment; "" for none.
	Code string
}

// ResultLink is the `daemon url --result` answer: the link is absent when the
// result is not there.
type ResultLink struct {
	Contract     int    `json:"contract"`
	URL          string `json:"url,omitempty"`
	Kind         string `json:"kind"`
	Running      bool   `json:"running"`
	UISupport    string `json:"uiSupport"`
	ResultExists bool   `json:"resultExists"`
	// Warnings is always present ([] when empty), as on every daemon url link.
	Warnings []string `json:"warnings"`
}

// BuildResultLink builds <uiBase>/result/<id>?view=<view>[&row=<n>]#code=<code>.
// The link carries no project, database, table, column, value or query text:
// the page reads all of it from the agent. validID is the result id grammar.
// It never reaches the network.
func BuildResultLink(st Status, req ResultLinkRequest, validID func(string) bool) (ResultLink, error) {
	if !validID(req.ID) {
		return ResultLink{}, UsageError{"--result must be a result id: letters, digits, '_' and '-', at most 128"}
	}
	view := req.View
	if view == "" {
		view = ResultViews[0]
	}
	known := false
	for _, candidate := range ResultViews {
		known = known || candidate == view
	}
	if !known {
		return ResultLink{}, UsageError{"--view must be one of " + strings.Join(ResultViews, ", ")}
	}
	if req.Row < 0 || req.Row > MaxResultRow {
		return ResultLink{}, UsageError{fmt.Sprintf("--row must be between 1 and %d", MaxResultRow)}
	}
	out := ResultLink{Contract: ContractVersion, Warnings: []string{}, Kind: KindQueryResult, Running: st.Running, UISupport: uiSupport[KindQueryResult], ResultExists: req.Exists}
	if !req.Exists {
		return out, nil
	}
	u := st.UIBase + "/result/" + req.ID + "?view=" + view
	if req.Row > 0 {
		u += fmt.Sprintf("&row=%d", req.Row)
	}
	if req.Code != "" {
		u += "#code=" + encodeComponent(req.Code)
	}
	if len(u) > MaxLinkLength {
		return ResultLink{}, UsageError{fmt.Sprintf("the link would be longer than %d characters", MaxLinkLength)}
	}
	out.URL = u
	return out, nil
}
