# Layered ACL publication (task 24)

This PR integrates the completed ACL chain with current DataTug contracts.
The old separate `/datatug/ovdb/*` proxy, settings registry, fragment session,
and app routes are superseded. Current fixed `secureread.Session`, project
source IDs, `exec/run_query` envelopes and `store/http-<host>:<port>` routes
remain authoritative. No policy viewer/editor is included.

## Connection

Register an ordinary environment catalog with `driver: "openvaultdb"` and
`path: "connections/crm.json"`. Its existing catalog ID/DbModel is the source
ID; no second target registry is created. The CLI-owned JSON descriptor is:

```json
{"baseUrl":"https://vault.example","databaseId":"crm","tokenEnv":"CRM_OVDB_TOKEN","principalId":"alice"}
```

Set `CRM_OVDB_TOKEN_BASE_URL=https://vault.example` and
`CRM_OVDB_TOKEN_PRINCIPAL_ID=alice` in the daemon environment as well: a project file cannot redirect this credential to another endpoint.
The daemon environment supplies a scoped OVDB credential provisioned for the
same internal user as `datatug serve --as alice`. OVDB independently validates
that credential and resolves memberships. The descriptor records the trusted
operator's binding; it does not authenticate or change the token's subject.
Do not bind an owner credential to a less-privileged user's session. Requests
cannot provide a token, URL, principal, roles, groups, or descriptor. A missing
or mismatched fixed local principal fails closed, even with `--no-policies`.

Use an existing saved DTQL query or the existing ad-hoc query request with
that source ID. The local policies rewrite/restrict the query through DALgo
before the source sends it to OVDB's `/v1/databases/{id}/dtql`; OVDB and InGitDB
then enforce their own policies. Native execution and generic transactions
are refused by this source. There is no fallback to unprotected execution.

Structured owner denials retain their DTQL result in `details.authorization`
of the existing `ACCESS_DENIED` error envelope, using the same sibling-details
extension already used for snapshot diagnostics. The owner projects diagnostic
visibility; the client does not gain policy administration. The matching app
PR validates the result and shows blocker codes/layers in the current query
page. Core's `error` schema is unchanged.

## Retained work and landing

The recovered client includes bounded Query, Explain, Update and Evidence
methods and their existing validation tests. Their former standalone UI/proxy
is not restored: connecting writes and Explain to the lead-owned current
write/session APIs requires that coordinated contract, and must not be mistaken
for a fully integrated browser write/Explain flow. The new integration proves
real remote DTQL reads against SQLite and InGitDB; OVDB's own suite separately
proves protected writes. This distinction remains a task-24 acceptance item.

Provider review PRs: dal-go/dalgo#159, dal-go/dalgo2sql#181,
ingitdb/dalgo2ingitdb#9, openvaultdb/openvaultdb-go#18. Dependencies are immutable
reachable review commits, without local replaces. Converge to observed release
tags after the lead approves and the providers land. The lead owns DataTug
landing; task 24 stays in progress until the whole wave and final E2E land.


## Verification, 2026-09-11

- `go test ./pkg/openvaultdb ./pkg/secureread ./pkg/server/endpoints`: pass.
- CGO-enabled `go test ./pkg/dbcopy ./pkg/server/endpoints`: pass, including existing SQL injection/binding cases.
- Existing HTTP `exec/run_query` tests resolve an OpenVaultDB catalog and preserve an owner denial in `details.authorization`.
- `golangci-lint run`: zero issues.
- Full CGO-enabled `go test ./...`: all packages pass except commands' existing `TestQueryRunSaved_SQL` and `TestQueryRunSaved_PolicyRefusal`. Both lack the operator-level opaque SQL capability required by current main. Baseline comparison is recorded in the PR; do not weaken that gate to make fixtures pass.
- Dependency integration preserves native numeric lookup values. The reviewed SQLite compiler intentionally avoids affinity coercion and uses binary comparison; the consumer's SQL placeholder mock is updated accordingly while retaining its bound-argument assertion.

The original task-21 branch is retained as `recovery/acl-task21-original` before replacing its publication branch. The replacement worktree is `.worktrees/acl24-datatug-publication`; canonical clones stay clean.
