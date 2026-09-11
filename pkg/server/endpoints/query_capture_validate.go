package endpoints

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// Bounds on a captured query. Names become file and directory names in a
// git-tracked tree; the rest bounds what one capture can put into it.
const (
	maxCaptureNameLength    = 128
	maxCaptureTitleLength   = 200
	maxCapturePurposeLength = 2000
	maxCaptureDTQLBytes     = 256 << 10
	maxCaptureParameters    = 32
)

// captureNamePattern is the allow-list for a query ID, each folder segment
// and a source ID: a letter or digit, then letters, digits, ".", "_" or
// "-". It admits every existing demo query, folder and source name and
// excludes, by construction, separators, "." and "..", hidden names, "~",
// ":" and "@", control and bidirectional characters and whitespace. The
// store applies its own portability rules (Windows device names) on top.
var captureNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// captureParameterTypes is the closed set of parameter types a capture may
// declare: every TypedValue type except null.
var captureParameterTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "decimal": true, "boolean": true, "date": true, "datetime": true,
}

// captureOrigins are the binding origins a capture may record. A default
// value is never captured, so "default" is not one of them.
var captureOrigins = map[string]bool{"selection": true, "context": true, "manual": true}

// validateCapturedQuery checks a captured query before any project data is
// read or written and returns the collection its DTQL reads. Every failure
// is 400 INVALID_REQUEST naming the "query.<field>" it concerns.
func validateCapturedQuery(q capturedQuery) (collection string, err error) {
	if err := validateCaptureLocation("query.id", q.ID); err != nil {
		return "", err
	}
	if q.FolderPath != "" {
		for _, segment := range strings.Split(q.FolderPath, "/") {
			if err := validateCaptureLocation("query.folderPath", segment); err != nil {
				return "", err
			}
		}
	}
	if err := validateCaptureText("query.title", q.Title, maxCaptureTitleLength, false); err != nil {
		return "", err
	}
	if err := validateCaptureText("query.purpose", q.Purpose, maxCapturePurposeLength, true); err != nil {
		return "", err
	}
	if err := validateCaptureName("query.source", q.Source); err != nil {
		return "", err
	}
	declared, err := validateCaptureParameters(q.Parameters)
	if err != nil {
		return "", err
	}
	if err := validateCaptureBindingOrigins(q.BindingOrigins, declared); err != nil {
		return "", err
	}
	return validateCaptureDTQL(q.DTQL, q.Parameters)
}

// validateCaptureLocation checks one folder segment or the query id: first
// the segment rules every query write path shares
// (querywrite.SegmentReason), then capture's stricter allow-list
// (validateCaptureName).
func validateCaptureLocation(field, value string) error {
	if value != "" {
		if reason, ok := querywrite.SegmentReason(value); !ok {
			return newInvalidRequest(field, fmt.Sprintf("segment %q %s", value, reason))
		}
	}
	return validateCaptureName(field, value)
}

// validateCaptureName checks one path-segment-shaped name against
// captureNamePattern and maxCaptureNameLength.
func validateCaptureName(field, value string) error {
	switch {
	case value == "":
		return newInvalidRequest(field, "is required and must not have an empty segment")
	case len(value) > maxCaptureNameLength:
		return newInvalidRequest(field, fmt.Sprintf("segment is longer than %d bytes", maxCaptureNameLength))
	case !captureNamePattern.MatchString(value) || strings.HasSuffix(value, "."):
		return newInvalidRequest(field, fmt.Sprintf("segment %q must start with a letter or digit, hold only letters, digits, '.', '_' or '-', and not end with '.'", value))
	}
	return nil
}

// validateCaptureText checks a required free-text field (captureTextReason).
func validateCaptureText(field, value string, maxRunes int, multiline bool) error {
	if reason := captureTextReason(value, true, maxRunes, multiline); reason != "" {
		return newInvalidRequest(field, reason)
	}
	return nil
}

// captureTextReason reports why value cannot be a free-text field, or ""
// when it can: present when required, at most maxRunes long, free of
// control characters (newlines and tabs allowed where multiline is) and of
// any embedded credential.
func captureTextReason(value string, required bool, maxRunes int, multiline bool) string {
	switch {
	case required && strings.TrimSpace(value) == "":
		return "is required"
	case utf8.RuneCountInString(value) > maxRunes:
		return fmt.Sprintf("is longer than %d characters", maxRunes)
	}
	for _, r := range value {
		if unicode.IsControl(r) && (!multiline || (r != '\n' && r != '\r' && r != '\t')) {
			return "must not contain control characters"
		}
	}
	if reason, found := captureCredentialReason(value); found {
		return reason
	}
	return ""
}

// validateCaptureParameters checks the typed parameters and returns the
// declared IDs.
func validateCaptureParameters(params []capturedParameter) (map[string]bool, error) {
	const field = "query.parameters"
	if len(params) > maxCaptureParameters {
		return nil, newInvalidRequest(field, fmt.Sprintf("more than %d parameters", maxCaptureParameters))
	}
	declared := make(map[string]bool, len(params))
	for i, p := range params {
		invalid := func(format string, args ...any) error {
			return newInvalidRequest(field, fmt.Sprintf("index %d: ", i)+fmt.Sprintf(format, args...))
		}
		if !dal.ValidParamName(p.ID) {
			return nil, invalid("id %q is not a valid DTQL parameter name", p.ID)
		}
		if !captureParameterTypes[p.Type] {
			return nil, invalid("type %q must be one of string, number, integer, decimal, boolean, date or datetime", p.Type)
		}
		if declared[p.ID] {
			return nil, invalid("duplicate parameter %q", p.ID)
		}
		declared[p.ID] = true
		if reason := captureTextReason(p.Title, false, maxCaptureTitleLength, false); reason != "" {
			return nil, invalid("title %s", reason)
		}
		if p.Meta != nil {
			for _, part := range []struct{ name, value string }{{"meta.entity", p.Meta.Entity}, {"meta.field", p.Meta.Field}} {
				if reason := captureTextReason(part.value, true, maxCaptureNameLength, false); reason != "" {
					return nil, invalid("%s %s", part.name, reason)
				}
			}
		}
	}
	return declared, nil
}

// validateCaptureBindingOrigins checks each origin names a declared
// parameter, once, with a capture origin.
func validateCaptureBindingOrigins(origins []captureBindingOrigin, declared map[string]bool) error {
	const field = "query.bindingOrigins"
	bound := make(map[string]bool, len(origins))
	for i, b := range origins {
		switch {
		case !declared[b.ParameterID]:
			return newInvalidRequest(field, fmt.Sprintf("index %d: names parameter %q, which is not declared", i, b.ParameterID))
		case !captureOrigins[b.Origin]:
			return newInvalidRequest(field, fmt.Sprintf("index %d: origin %q must be selection, context or manual; default values are not captured", i, b.Origin))
		case bound[b.ParameterID]:
			return newInvalidRequest(field, fmt.Sprintf("index %d: duplicate entry for parameter %q", i, b.ParameterID))
		}
		bound[b.ParameterID] = true
	}
	return nil
}

// validateCaptureDTQL checks the query text: present, bounded, free of
// credentials, real DTQL reading one named collection, with parameters
// only where Run can bind them and exactly the declared ones. It returns
// that collection.
func validateCaptureDTQL(text string, params []capturedParameter) (string, error) {
	const field = "query.dtql"
	if strings.TrimSpace(text) == "" {
		return "", newInvalidRequest(field, "is required")
	}
	if len(text) > maxCaptureDTQLBytes {
		return "", newInvalidRequest(field, fmt.Sprintf("is longer than %d bytes", maxCaptureDTQLBytes))
	}
	if reason, found := captureCredentialReason(text); found {
		return "", newInvalidRequest(field, reason)
	}
	query, err := dtql.Deserialize([]byte(text))
	if err != nil {
		return "", newInvalidRequest(field, "does not parse as DTQL: "+err.Error())
	}
	collection := dtqlCollection(query)
	if collection == "" {
		return "", newInvalidRequest(field, "must read one named collection")
	}
	referenced, err := accesspolicies.QueryParameters(query)
	if err != nil {
		return "", newInvalidRequest(field, err.Error())
	}
	declared := make(map[string]bool, len(params))
	for _, p := range params {
		declared[p.ID] = true
	}
	used := make(map[string]bool, len(referenced))
	for _, name := range referenced {
		if !declared[name] {
			return "", newInvalidRequest("query.parameters", fmt.Sprintf("the dtql references parameter %q, which is not declared", name))
		}
		used[name] = true
	}
	for _, p := range params {
		if !used[p.ID] {
			return "", newInvalidRequest("query.parameters", fmt.Sprintf("parameter %q is declared but the dtql never uses it", p.ID))
		}
	}
	return collection, nil
}

// dtqlCollection returns the name of the collection query reads, or ""
// when it reads anything else.
func dtqlCollection(query dal.StructuredQuery) string {
	from := query.From()
	if from == nil {
		return ""
	}
	switch base := from.Base().(type) {
	case dal.CollectionRef:
		return base.Name()
	case *dal.CollectionRef:
		if base != nil {
			return base.Name()
		}
	}
	return ""
}

// checkCaptureSource checks source resolves in environment as a target a
// saved query can bind to: the captured query will carry
// Targets [{catalog: source}], so it must be one of the environment's
// catalog sources, through the same resolver saved-query execution uses
// (api.EligibleTargets). An unregistered source, a recordset or HTTP
// source, and an unknown environment are all 400 on query.source; the
// resolver's own error text (which can name server paths) is not echoed.
func checkCaptureSource(ctx context.Context, projectID, projectDir, environment, source string) error {
	projStore, err := api.ProjectStoreFor(projectID)
	if err != nil {
		return captureInternal(err)
	}
	target := &datatug.QueryDef{Type: datatug.QueryTypeDTQL, Targets: []datatug.QueryDefTarget{{Catalog: source}}}
	eligible, err := api.EligibleTargets(ctx, projStore, projectDir, environment, target)
	if err == nil {
		for _, s := range eligible {
			if s.ID == source {
				return nil
			}
		}
	}
	return newInvalidRequest("query.source", fmt.Sprintf("source %q is not a catalog source of environment %q that a saved query can target", source, environment))
}
