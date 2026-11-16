package accesspolicies

import (
	"context"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

// Explicit policy-free reads supply only a database baseline. They still run
// through DALgo's shared evaluator with all context and underlying DB policies.
// This is never installed on the context, so it cannot satisfy RequireContextPolicy.
func secureReadSession(session dal.ReadSession, options Options) dal.ReadSession {
	policies := Policies(options.Policies)
	if len(policies) == 0 && options.Unrestricted {
		policies = []access.Policy{policyFreeQueryBaseline{}}
	}
	return access.SecureReadSession(session, policies...)
}

// The legacy profile already permits every query resource. A constant baseline
// preserves collection-group/qualified/opaque identity without walking resources
// or deciding any of the mandatory policies that DALgo composes beneath it.
type policyFreeQueryBaseline struct{}

func (policyFreeQueryBaseline) Name() string         { return "explicit-policy-free-query-baseline" }
func (policyFreeQueryBaseline) InspectionPure() bool { return true }
func (p policyFreeQueryBaseline) Decide(_ context.Context, r access.Request) access.Decision {
	return access.Decision{Allowed: r.Operation == access.Query, Operation: r.Operation, Policy: p.Name()}
}
func (policyFreeQueryBaseline) Authorize(_ context.Context, r access.Request) error {
	if r.Operation != access.Query {
		return access.ErrAccessDenied
	}
	return nil
}
