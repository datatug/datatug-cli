# Task3 cumulative protocol/registry review r1

Personally verified exact staged diff SHA256334eb129e9a8950d63dc8f037651e68f009313bb22639376e182d124326a7bcc and pkg/mcpserver source hashes:
apps.go74c3fe033aa7abcb2927133a00548fd4c2006bb59aa1799b3f00552ef2aadb17
apps_registry.go5f0dd18c87c8e0afdaa165a65326fc939f5077b663e3b3c86876586320c6a5a1
apps_registry_test.go97426981313667e3b96b130683440b58719c940fddc4347a215a201cc239652e
server.gof28f7c4d08112ed46f5cbbf22e9e734b51585ae1f2f7282a0f96cc380f76a7f6
tools.goedfae9841d9cf3810f76a8d083f410e586f49cedf137d2d80110e9a674ffe0b9

Major finding1 (open): apps_registry.go235-239 and293-298 check operation cancellation before calling retain, but retain141-221 accepts no context and marshals result/parameters/envelope and waits on registry mutex before inserting. Request-only cancellation/deadline during that work can publish and retain a fresh handle after cancellation. Closed registry/revoked parent checks cover teardown/revocation, but request cancellation leaves registry open. This violates bounds no-late-canceled-publication requirement. Pass operation context into retain and check ctx.Err under registry lock immediately before state mutation/publication. Add deterministic initial and rerun tests holding retain commit mutex, canceling request after authority validation, releasing lock and asserting safe unavailable/no retained child. Root and author notified; no source edits by reviewer.

No other findings in completed readthrough. Session isolation, random32-byte handle, full unredacted definition+authority digest, pre/postexecution authority verification, scalar typed binding, exact result wire DTO, app-only listing/direct plain rejection, generic error replacement (including SDK schema errors), aggregate/envelope/count/expiry bounds and teardown cancellation were inspected. Existing source validations remain authoritative.

Personally executed authorized WB-governed go test ./pkg/mcpserver -run TestApps -count=1; passed0.616s. Inspected actual author logs: fullMCP race exit0, vet exit0; real child test passed for authenticated legacy saved query/CustomerId changes and clean process. This is not Apps browser acceptance; no renderer/host/release success claimed. Existing tests cover close/revoke cancellation but do not cover identified request-only finalcommit window. Parent root sole writer/landing owner.

VERDICT: blockers=0 majors=1 minors=0 land=no
