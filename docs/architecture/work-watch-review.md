# Work + Watch event implementation review

2026-10-24, v1. Reviewer: integration orchestrator, independent of the CLI
implementation agent. Reviewed `events.go`, accepted-completion integration,
notification identities, storage/index migration and authenticated event routes.

The specification review required notification-independent occurrences, recovery
independent of cooldown/preference, no-subscriber one-shot completion, durable
retention and same-transaction visibility checks. The implementation addresses
these without replacing scheduling, leases, subscriptions or delivery providers.

Independent implementation review found a high-severity historical privacy gap:
checking only current definition visibility would expose old personal input
snapshots after promoting a Watch to shared. Fixed by also checking snapshot
owner/visibility inside the same transaction, filtering before the database page
limit and rejecting hidden pagination cursors. The new mixed-visibility paging
test verifies later shared events remain reachable while old personal snapshots
remain owner-only.

The orchestrator inspected the actual transaction/event code, native execution
receipt and process-cleanup receipt. The implementation lane executed normal
Watch/HTTP suites, final privacy-adjusted race tests and adjacent native daemon,
Detective and endpoint tests. The cross-service browser journey and exact
verification commands/results are recorded in the Backend and Apps integration
acceptance reports; this code review alone does not establish browser success.

No unresolved critical/high findings remain in this reviewed CLI scope. Event
retention intentionally grows with meaningful transitions and requires backup;
operator archival/purge tooling remains a follow-up. Physical execution can
repeat after lease loss; accepted completion and associated event/outbox writes
remain fenced and atomic. SQLite coordinator HA remains unsupported.

## Upstream Firestore provider reconciliation (2026-10-24)

Before landing, current main `3e4a9015` added a scoped transactional Firestore
provider. Source merge preserves its normalization, admission, buffered writes,
producer hooks, quotas and maintenance together with durable Work integration
Events. Event timestamps remain immutable; cloud metadata is added afterwards.
Both storage ADR sections are retained.

Independent review found the existing Event OR/tuple query did not fit the new
provider's narrow query contract. A scoped Event read now uses two disjoint ACL
lanes and a signed-sortable timestamp/physical-ID scalar cursor. Current source
admission and project authorization run in the same read snapshot; historical
personal inputs remain owner-only. Cloud pages cap at 100 and declare exact
production index shapes. SQLite retains its existing 1000-entry page limit.

Verified combined-source results: normal Watch, Watchdaemon and daemon suites
passed; all 44 Firestore top-level tests passed against the existing emulator
using an isolated demo project, including typed inputs, same-instant cursors,
private-history filtering, current source revocation/reset, retention and atomic
producer-hook rollback. No test was skipped. An independent reviewer separately
ran ten focused Event/privacy/fencing and provider-join tests and checked the
exact two index declarations. The required CI emulator script now explicitly
includes and requires both new Event cases; its separately owned emulator is not
needed for the local shared-emulator run. Production index deployment and cloud
runtime activation remain separate from this local conformance evidence.

The final combined-source race run against the isolated existing emulator also
passed (`go test -race ./pkg/watch -run '^TestFirestore(Store|Events)'`;
17.972 seconds package time, no skips). The shared accepted browser runtime was
not restarted or mutated for provider conformance. Its earlier pinned binary
receipt remains distinct from this newer merged source.
