package api

import (
	"context"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// AuthorizeProjectQueryWrite decides whether this `datatug serve` process
// may perform operation (Insert, Set, Update or Delete) on the saved query
// queryID - its canonical, folder-qualified ID - of project projectID. It
// is the one project-write gate every query write path shares:
// queries/capture and the legacy queries/create_query, update_query and
// delete_query routes. It returns nil, or a *accesspolicies.WriteDeniedError
// that wraps secureread.ErrAccessDenied (access.ErrAccessDenied).
//
// Deny by default, in this order:
//
//  1. An agent with no configured serve session refuses.
//  2. Without --allow-writes (Capabilities.AllowWrites) every project write
//     is refused, whoever the principal is - api-contract.md "Security and
//     errors": "Project writes must be refused unless the server has an
//     explicit write capability". The endpoints' requireWriteCapability
//     route gate already refuses; this repeats it so no caller of this
//     package can skip it.
//  3. Otherwise accesspolicies.AuthorizeWrite decides for the session's
//     fixed principal under its loaded policies: the --no-policies local
//     owner may write; a secured session needs every loaded policy to grant
//     the write, unconditionally, to its principal - so a read-only
//     principal is refused.
//
// Nothing here writes, and no Git commit or hosted team grant is implied.
func AuthorizeProjectQueryWrite(ctx context.Context, projectID, queryID string, operation access.Operations) error {
	secureMu.RLock()
	configured := securityContextID != ""
	caps := capabilities
	session := secureSession
	secureMu.RUnlock()

	resource := accesspolicies.ProjectQueryResource(projectID, queryID)
	if !configured {
		return &accesspolicies.WriteDeniedError{Operation: operation, Resource: resource.String(),
			Reason: "this agent has no configured serve session"}
	}
	if !caps.AllowWrites {
		return &accesspolicies.WriteDeniedError{Operation: operation, Resource: resource.String(),
			Reason: "this agent was started without --allow-writes; project writes are refused"}
	}
	return accesspolicies.AuthorizeWrite(ctx, accesspolicies.WriteOptions{
		Principal:    session.Principal,
		Policies:     session.Policies,
		Unrestricted: session.Unrestricted,
	}, operation, resource)
}
