package endpoints

import (
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apicontract_local"
)

// Bounds api-contract.md "Bounded lookups and HTTP" fixes: default/maximum
// result limit, the related-lookup cap, the count budget and the execution
// timeout default/maximum.
const (
	defaultResultLimit = 100
	maxResultLimit     = 500
	maxRelatedTargets  = 50
	countBudget        = 2 * time.Second
	defaultExecTimeout = 10 * time.Second
	maxExecTimeout     = 30 * time.Second
)

// validateScope checks the appendix's Scope invariants (api-contract.md
// "Scope and identity"): project/environment nonempty, and
// securityContextId present and current (STALE_CONTEXT otherwise — "the
// server validates it before execution and responds STALE_CONTEXT after
// principal/policy-session changes"). It does NOT check that project is a
// project this process actually serves — callers do that themselves via
// api.ProjectDir, since the right error there is NOT_FOUND, not
// INVALID_REQUEST.
func validateScope(s apicontract_local.Scope) error {
	if s.Project == "" {
		return apicontract_local.NewMissingParameter("project")
	}
	if s.Environment == "" {
		return apicontract_local.NewMissingParameter("environment")
	}
	if s.SecurityContextID == "" {
		return apicontract_local.NewMissingParameter("securityContextId")
	}
	if !api.ValidateSecurityContext(s.SecurityContextID) {
		return apicontract_local.NewStaleContext("securityContextId does not match the agent's current session; call agent-info again")
	}
	return nil
}

// boundLimit clamps a caller-supplied limit to
// [1, maxResultLimit], defaulting to defaultResultLimit when requested is
// 0 (unset) or negative.
func boundLimit(requested int) int {
	if requested <= 0 {
		return defaultResultLimit
	}
	if requested > maxResultLimit {
		return maxResultLimit
	}
	return requested
}

// boundRelatedLimit clamps a caller-supplied related-lookup limit to
// [1, maxRelatedTargets], defaulting to maxRelatedTargets when requested is
// 0 (unset) or negative — related discovery has no separate "default"
// bound in the appendix, only the cap ("returns at most 50 targets").
func boundRelatedLimit(requested int) int {
	if requested <= 0 || requested > maxRelatedTargets {
		return maxRelatedTargets
	}
	return requested
}
