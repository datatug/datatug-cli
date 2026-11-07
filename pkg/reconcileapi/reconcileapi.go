// Package reconcileapi is the wire contract of the agent's
// POST /datatug/reconcile endpoint, shared by the server and the CLI.
package reconcileapi

import (
	"errors"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/reconcile"
)

// Path is the endpoint path below the agent's /datatug prefix.
const Path = "/reconcile"

// Request runs one dataset reconciliation. A profile supplies the query, the
// instances and the rules; explicit fields override it.
type Request struct {
	SecurityContextID string                      `json:"securityContextId"`
	StoreID           string                      `json:"storeId,omitempty"`
	Project           string                      `json:"project"`
	QueryID           string                      `json:"queryId,omitempty"`
	ProfileID         string                      `json:"profileId,omitempty"`
	Instances         *reconcile.InstanceSelector `json:"instances,omitempty"`
	// Rules overrides the profile's rule field by field: a zero field keeps
	// the profile's (or the confirmed classification's) value.
	Rules *reconcile.Rules `json:"rules,omitempty"`
}

// Validate checks the request's shape before anything is read.
func (r Request) Validate() error {
	if r.SecurityContextID == "" {
		return errors.New("securityContextId is required")
	}
	if r.Project == "" {
		return errors.New("project is required")
	}
	if r.ProfileID == "" && r.QueryID == "" {
		return errors.New("queryId or profileId is required")
	}
	if r.ProfileID == "" && r.Instances == nil {
		return errors.New("instances are required without a profile")
	}
	return nil
}

// Response is the engine result plus one execution receipt per instance that
// was executed. Instances refused or failed before a receipt have none.
type Response struct {
	Result   reconcile.Result                          `json:"result"`
	Receipts map[string]apicontract.CompareSideReceipt `json:"receipts"`
}

// Overlay returns base with every non-zero field of override applied. The
// field policy maps are merged, override winning.
func Overlay(base reconcile.Rules, override *reconcile.Rules) reconcile.Rules {
	out := base
	out.Key = append([]string(nil), base.Key...)
	if len(base.Fields) > 0 {
		out.Fields = make(map[string]reconcile.FieldPolicy, len(base.Fields))
		for k, v := range base.Fields {
			out.Fields[k] = v
		}
	}
	if override == nil {
		return out
	}
	if len(override.Key) > 0 {
		out.Key = append([]string(nil), override.Key...)
	}
	if override.Mode != "" {
		out.Mode = override.Mode
		if override.Mode == reconcile.ModeConsensus {
			out.Baseline = ""
		}
	}
	if override.Baseline != "" {
		out.Baseline = override.Baseline
		if override.Mode == "" {
			out.Mode = reconcile.ModeBaseline
		}
	}
	if override.Presence != "" {
		out.Presence = override.Presence
	}
	if override.DefaultField != "" {
		out.DefaultField = override.DefaultField
	}
	if len(override.Fields) > 0 {
		if out.Fields == nil {
			out.Fields = make(map[string]reconcile.FieldPolicy, len(override.Fields))
		}
		for k, v := range override.Fields {
			out.Fields[k] = v
		}
	}
	// A request may tighten disclosure, never loosen it: a groups-only rule
	// stays groups-only.
	if override.Disclosure != "" && base.Disclosure != reconcile.DiscloseGroupsOnly {
		out.Disclosure = override.Disclosure
	}
	if override.Limit != 0 {
		out.Limit = override.Limit
	}
	return out
}

// OverlaySelector returns base narrowed or replaced by the non-zero fields
// of override.
func OverlaySelector(base reconcile.InstanceSelector, override *reconcile.InstanceSelector) reconcile.InstanceSelector {
	if override == nil {
		return base
	}
	out := base
	if override.Family != "" {
		out.Family = override.Family
	}
	if override.Kind != "" {
		out.Kind = override.Kind
	}
	if len(override.Tenants) > 0 {
		out.Tenants = append([]string(nil), override.Tenants...)
	}
	if len(override.Environments) > 0 {
		out.Environments = append([]string(nil), override.Environments...)
	}
	return out
}
