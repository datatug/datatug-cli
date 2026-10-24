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
