package secureread

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

// CanReadWholeCollection is a no-row-read policy preflight for a prospective
// JOIN target. A JOIN must not turn a target's row predicate or field mask
// into an unprotected read through its parent relation. The actual JOIN still
// goes through RunDTQL and its normal secure-read authorization.
func (e *Executor) CanReadWholeCollection(ctx context.Context, collection string) error {
	if e == nil || collection == "" {
		return fmt.Errorf("JOIN target is unavailable")
	}
	if e.session.Unrestricted {
		return nil
	}
	if len(e.session.Policies) == 0 {
		return fmt.Errorf("JOIN target access is not configured")
	}
	if e.session.Principal != nil {
		ctx = access.WithPrincipal(ctx, *e.session.Principal)
	}
	query := dal.From(dal.NewRootCollectionRef(collection, "")).NewQuery().Limit(1).SelectColumns()
	resource := access.CollectionResourceFor(nil, collection)
	request := access.Request{Operation: access.Query, Resources: []access.Resource{resource}, Query: query}
	for _, loaded := range e.session.Policies {
		decision := loaded.Policy.Decide(ctx, request)
		if !decision.Allowed {
			return fmt.Errorf("JOIN target is not readable under the current policy")
		}
		for _, residual := range decision.Residuals {
			if residual != nil {
				return fmt.Errorf("JOIN target has a row policy that cannot be applied safely")
			}
		}
		for _, residual := range decision.Writes {
			if residual != nil {
				return fmt.Errorf("JOIN target has a field policy that cannot be applied safely")
			}
		}
	}
	return nil
}
