package accesspolicies

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/record"
)

// ProjectsCollection and ProjectQueriesCollection name DataTug project files
// as DALgo policy resources: a saved query is the record
// /datatug_projects/{projectID}/queries/{queryID}, where queryID is the
// query's canonical, folder-qualified ID kept as one path segment. The
// "datatug_" prefix keeps project files apart from every data source's own
// collection names, so a grant on a data collection (path: /Customer) never
// grants a project write, while a catch-all grant (path: /**) - the
// demo-project-1 admin rule set - covers them.
const (
	ProjectsCollection       = "datatug_projects"
	ProjectQueriesCollection = "queries"
)

// ProjectQueryResource returns the policy resource for the saved query
// queryID of project projectID.
func ProjectQueryResource(projectID, queryID string) access.Resource {
	project := record.NewKeyWithID(ProjectsCollection, projectID)
	return access.RecordResourceForKey(record.NewKeyWithParentAndID(project, ProjectQueriesCollection, queryID))
}

// WriteOptions is the identity and policy set a project write is decided
// under - a `datatug serve` session's fixed principal and loaded policies.
type WriteOptions struct {
	// Principal is the caller; nil means no principal, which no grant binds.
	Principal *access.Principal
	// Policies are the loaded documents; every one must allow the write.
	Policies []Loaded
	// Unrestricted is the explicit --no-policies local-owner profile.
	Unrestricted bool
}

// WriteDeniedError reports a refused project write. It names the deciding
// policy, the operation, the resource and a reason, and never the policy
// file's location on the serving machine. It unwraps to
// access.ErrAccessDenied.
type WriteDeniedError struct {
	// Policy is the name of the policy that refused the write; empty when
	// the write was refused by default, with no policy deciding.
	Policy    string
	Operation access.Operations
	Resource  string
	Reason    string
}

func (e *WriteDeniedError) Error() string {
	if e.Policy == "" {
		return fmt.Sprintf("%v: %s on %s: %s", access.ErrAccessDenied, e.Operation, e.Resource, e.Reason)
	}
	return fmt.Sprintf("%v: policy %q does not allow %s on %s: %s", access.ErrAccessDenied, e.Policy, e.Operation, e.Resource, e.Reason)
}

// Unwrap makes errors.Is(err, access.ErrAccessDenied) true.
func (e *WriteDeniedError) Unwrap() error { return access.ErrAccessDenied }

// AuthorizeWrite decides whether o's principal may perform operation (one
// of Insert, Set, Update or Delete) on resource, returning nil when it may
// and a *WriteDeniedError when it may not. Deny by default:
//
//   - An Unrestricted session is the explicit --no-policies local-owner
//     profile and is allowed.
//   - Otherwise at least one policy must be loaded and every loaded policy
//     must allow the write, for the principal on the context - the same
//     intersection a secured session applies to reads. A principal no
//     grant binds, a read-only grant and a missing principal are all
//     denied.
//   - A grant that holds only under a row condition, a check or a field
//     allow-list is denied too: a project file has no rows or fields to
//     evaluate it against, so such a grant cannot be enforced here.
func AuthorizeWrite(ctx context.Context, o WriteOptions, operation access.Operations, resource access.Resource) error {
	switch operation {
	case access.Insert, access.Set, access.Update, access.Delete:
	default:
		return fmt.Errorf("accesspolicies: %s is not a single project-write operation", operation)
	}
	if o.Unrestricted {
		return nil
	}
	if len(o.Policies) == 0 {
		return &WriteDeniedError{Operation: operation, Resource: resource.String(),
			Reason: "no access policy is loaded, and project writes are denied by default"}
	}
	if o.Principal != nil {
		ctx = access.WithPrincipal(ctx, *o.Principal)
		if o.Principal.ID != nil {
			ctx = access.WithCurrentUser(ctx, o.Principal.ID)
		}
	}
	request := access.Request{Operation: operation, Resources: []access.Resource{resource}}
	for _, item := range o.Policies {
		decision := item.Policy.Decide(ctx, request)
		if !decision.Allowed {
			reason := decision.Explanation
			if reason == "" {
				reason = "no rule allows it"
			}
			return &WriteDeniedError{Policy: item.Policy.Name(), Operation: operation, Resource: resource.String(), Reason: reason}
		}
		if constrainedWrite(decision) {
			return &WriteDeniedError{Policy: item.Policy.Name(), Operation: operation, Resource: resource.String(),
				Reason: "the grant holds only under a row condition, check or field list, which a project file write cannot enforce"}
		}
	}
	return nil
}

// constrainedWrite reports whether an allow decision still carries a
// constraint the caller would have to enforce: a row residual, or a write
// residual with conditional alternatives or a terminal rule restricted by a
// where, a check or a field allow-list.
func constrainedWrite(decision access.Decision) bool {
	for _, residual := range decision.Residuals {
		if residual != nil {
			return true
		}
	}
	for _, write := range decision.Writes {
		if write == nil {
			continue
		}
		if len(write.Alternatives) > 0 || write.Terminal == nil {
			return true
		}
		if t := write.Terminal; t.Where != nil || t.Check != nil || len(t.Fields) > 0 {
			return true
		}
	}
	return false
}
