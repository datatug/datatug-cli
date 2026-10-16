package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
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
// (spec/features/cli/daemon, Follow-up). All three kinds are pending: the UI
// opens none of them with the shape below yet.
var uiSupport = map[string]string{
	KindTableMetadata: UISupportPending,
	KindTableRecords:  UISupportPending,
	KindRecord:        UISupportPending,
}

const (
	// MaxLinkLength bounds a link: the mod SDK draws at most this many characters.
	MaxLinkLength = 2048
	// maxKeyValueLength bounds one primary-key string value.
	maxKeyValueLength = 256
	// maxSafeInteger is 2^53 - 1: the largest integer a JavaScript number holds exactly.
	maxSafeInteger = 1<<53 - 1
)

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
	// Code is the one-time code of the agent (agentaccess.MintCode), put first in the fragment of
	// every link; "" when the agent has none to give.
	Code string
	// Metadata reads the scanned relations named Table (nil when the project holds none): the link is
	// checked against them, and carries a warning when there is nothing to check against. It runs
	// last, after every check of the request itself; a nil function is no metadata.
	Metadata func() *Metadata
}

// ContractVersion is the shape version of the `daemon url` JSON: bumped on any incompatible change.
const ContractVersion = 1

// Link is the deep link of one card.
type Link struct {
	Contract  int    `json:"contract"`
	URL       string `json:"url"`
	Kind      string `json:"kind"`
	Running   bool   `json:"running"`
	UISupport string `json:"uiSupport"`
	// Warnings is always present ([] when empty) so the shape is stable.
	Warnings []string `json:"warnings"`
}

// BuildLink builds the deep link of contract deep-link-contract for the agent
// reported by st: identity in the path, view parameters (limit, schema) in the
// query, the record key in the fragment so it is never sent to the web host.
// It never reaches the network.
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
	for _, v := range []string{req.Project, req.Env, req.DB, req.Table} {
		if v == "." || v == ".." {
			return Link{}, UsageError{"identifiers must not be . or .."}
		}
	}
	kind := "table"
	if req.View {
		kind = "view"
	}
	u := fmt.Sprintf("%s/project/%s/env/%s/db/%s/%s/%s", st.UIBase,
		encodeComponent(req.Project), encodeComponent(req.Env), encodeComponent(req.DB), kind, encodeComponent(req.Table))
	schema := "schema=" + encodeComponent(req.Schema)
	var fragment []string
	out := Link{Contract: ContractVersion, Kind: KindTableMetadata, Running: st.Running, Warnings: []string{}}
	var keys []string
	switch {
	case req.Records:
		limit := req.Limit
		if limit == 0 {
			limit = DefaultLimit
		}
		if limit < 1 || limit > MaxLimit {
			return Link{}, UsageError{fmt.Sprintf("--limit must be between 1 and %d", MaxLimit)}
		}
		u += fmt.Sprintf("/records?limit=%d&%s", limit, schema)
		out.Kind = KindTableRecords
	case req.Record != "":
		pk, err := canonicalPK(req.Record)
		if err != nil {
			return Link{}, err
		}
		u += "/record?" + schema
		fragment = append(fragment, "pk="+encodeComponent(pk))
		keys = keyNames(pk)
		out.Kind = KindRecord
	default:
		u += "?" + schema
	}
	if req.Code != "" {
		fragment = append([]string{"code=" + encodeComponent(req.Code)}, fragment...)
	}
	if len(fragment) > 0 {
		u += "#" + strings.Join(fragment, "&")
	}
	if len(u) > MaxLinkLength {
		return Link{}, UsageError{fmt.Sprintf("the link would be longer than %d characters", MaxLinkLength)}
	}
	var meta *Metadata
	if req.Metadata != nil {
		meta = req.Metadata()
	}
	if meta == nil {
		out.Warnings = append(out.Warnings, WarningNotScanned)
	} else {
		w, err := meta.check(req, keys)
		if err != nil {
			return Link{}, err
		}
		out.Warnings = append(out.Warnings, w...)
	}
	out.URL, out.UISupport = u, uiSupport[out.Kind]
	return out, nil
}

// encodeComponent is JavaScript's encodeURIComponent: every byte but letters,
// digits and - _ . ! ~ * ' ( ) is percent-encoded (a space is %20, never +).
func encodeComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// maxNumberLiteral bounds a key number's text, so an exponent cannot make an enormous integer.
const maxNumberLiteral = 40

// canonicalNumber is one spelling per number: an integer inside the exactly representable
// JavaScript range is its plain digits (1E3 and 1.0 are 1000 and 1, -0 is 0), an integer outside it
// is a string of its exact digits (so the UI never rounds it to a neighbour), any other number is
// its shortest float form (decimal reports that case: the record lookup takes no such key, see
// canonicalPK). A literal that is not finite is not a key.
func canonicalNumber(literal string) (v any, ok, decimal bool) {
	if len(literal) > maxNumberLiteral {
		return nil, false, false
	}
	if i := strings.IndexAny(literal, "eE"); i >= 0 && len(strings.TrimLeft(literal[i+1:], "+-0")) > 3 {
		return nil, false, false // an exponent beyond 999 is no key
	}
	r, _ := new(big.Rat).SetString(literal) // a valid json.Number with an exponent below 1000 always parses
	if r.IsInt() {
		n := r.Num()
		if new(big.Int).Abs(n).Cmp(big.NewInt(maxSafeInteger)) > 0 {
			return n.String(), true, false
		}
		return json.Number(n.String()), true, false
	}
	f, _ := r.Float64()
	if f == 0 || math.IsInf(f, 0) { // not representable
		return nil, false, false
	}
	return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), true, true
}

// unsupportedKeyKind is the refusal of a key value kind that POST /datatug/records/lookup cannot
// take (it binds strings and exact integers only: a float is never compared for equality and the
// structured SQLite read binds no boolean), so a link is never made for a record the lookup would
// refuse. It names the kind, never the value.
func unsupportedKeyKind(kind string) string {
	return "--record: a key value of kind " + kind + " is not supported; a primary key is looked up by strings and integers only"
}

// canonicalPK checks that v is a non-empty JSON object of scalar key values
// and re-encodes it with sorted keys so one record always has one link (see canonicalNumber).
func canonicalPK(v string) (string, error) {
	bad := UsageError{"--record must be a non-empty JSON object of primary-key column to value"}
	dec := json.NewDecoder(bytes.NewReader([]byte(v)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || len(obj) == 0 {
		return "", bad
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", bad
	}
	for k, val := range obj {
		switch x := val.(type) {
		case json.Number:
			n, ok, decimal := canonicalNumber(x.String())
			if !ok {
				return "", bad
			}
			if decimal {
				return "", UsageError{unsupportedKeyKind("decimal number")}
			}
			obj[k] = n
		case string:
			if utf8.RuneCountInString(x) > maxKeyValueLength {
				return "", UsageError{fmt.Sprintf("a key value must be at most %d characters", maxKeyValueLength)}
			}
		case bool:
			return "", UsageError{unsupportedKeyKind("boolean")}
		default: // null, object, array
			return "", bad
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(obj) // a decoded map always encodes
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
