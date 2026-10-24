# ADR: DataTug Watch coordinator persistence

Status: accepted for the single-coordinator implementation, 2026-10-23.

## Decision and ownership

A DataTug daemon Coordinator uses a deployment-configured absolute local SQLite
file through `dalgo2sqlite`. The shared project manifest contains only the
coordinator endpoint and namespace. It contains no storage credentials, runner
credentials, provider tokens, email addresses, telephone numbers, Telegram chat
IDs, ephemeral registrations or leases. OVDB remains optional for source reads.

The `watch.DefinitionRepository` is the project's durable definition repository.
For this implementation it is backed by the authenticated coordinator's
`dt_watch_definitions` table. These definitions belong to a project, environment
and owner; they are not Git files. Personal definitions must not be exported to
shared Git. Shared definitions can be read by verified project members and edited
by their owner or a verified shared editor. Publishing a personal Watch requires
that shared-authoring grant. Subscriptions and addresses belong to each consenting
user and live in a separate table. A coordinator backup must preserve definitions,
subscriptions and account connections along with the runtime tables. Runtime
retention never removes those durable project records.

## Verified DALGO contract

The CLI's resolved modules are DALGO v0.95.3, dalgo2sql v0.30.0, dalgo2sqlite
v0.4.7 and modernc.org/sqlite v1.61.0. `pkg/watch/store_test.go` exercises the
actual CLI module graph; there are no local module replacements for the store.

Reconnaissance fetched these upstream heads: DALGO
`d8ee60a8be438d34bf0716642649e0060070fcec`, dalgo2sql
`463e32835a626f8c20d8ab142f1991ed1afa5585`, dalgo2sqlite
`e7826a8c1c959f9ccaf1dfa697cd04116aba9a77` and dalgo2postgres
`7962cfc936ab40a8bf4536284b367cf44a574a37`. The SQL adapter's
`updater.go:updateSingle` ignores DALGO preconditions, writes by primary key and
accepts zero affected rows. `database.go:RunReadwriteTransaction` does not map
DALGO's requested isolation level or attempts into SQL transaction options.
Neither API is used as an atomic compare-and-swap guarantee here. PostgreSQL's
database capabilities therefore do not establish the same consumer contract.

`NewSQLiteStore` owns the SQLite driver options. It accepts an absolute local file
path or `sqlite:///absolute/path`, rejects arbitrary DSNs, and supplies
`_txlock=immediate`, a 50 ms busy timeout, DELETE journal mode and FULL synchronous
mode. The modernc driver applies those settings to every opened connection, so
DALGO's `RunReadwriteTransaction` takes the SQLite writer reservation before
calling the worker. Every decision reads current state and writes within that
same transaction using DALGO `Get`, `Exists`, `Update`, `Insert` and `Delete`.
Schema bootstrap uses the SQLite DALGO adapter's DDL API. No application runtime
mutation bypasses DALGO. Startup retries the complete open-and-schema operation
on SQLite BUSY/LOCKED codes, closing an opened database after every failed schema
attempt. `IfNotExists` keeps the retried bootstrap idempotent. All startup attempts
share one ten-second deadline, checked before each attempt. The pinned adapter's
initial Ping uses a background context; its configured 50 ms busy timeout leaves
at most that in-flight driver wait beyond the deadline. Validation, permissions,
malformed databases and other non-lock errors fail immediately. Runtime contention
retries the entire read/write transaction under its own ten-second bound and
caller cancellation. External
queries and provider calls run outside those transactions.

The database must reside on a local filesystem with working SQLite file locks
and durable writes. Network filesystems, read-uncommitted shared caches, concurrent
schema operators and externally changed journal settings are outside this
contract. This is one Coordinator deployment per namespace. Competing database
handles and OS processes have been tested for claims, but scheduler HA, leader
coordination, failover, distributed clocks and multi-host shared storage have not
been implemented or claimed.

## Scheduling, leases and result acceptance

`Reconcile` generates due jobs separately from `ClaimDue`. Execution IDs include
namespace, project, environment, Watch ID, definition version and scheduled time.
One active job per Watch coalesces missed periods after downtime; retries preserve
that execution ID. An eligible runner must register with verified project,
environment and capability grants. Registration does not grant source access.
The Coordinator may have no monitored-database credentials.

Claims increment a durable fence. Lease times use the Coordinator's clock, not a
runner timestamp. Renewals are bounded by the attempt's execution deadline.
Expired leases can be reclaimed with a higher fence; expired or superseded
workers cannot renew or complete, including before another runner reclaims.
Error retries have bounded attempts and exponential delay. Pausing, deleting or
editing a definition cancels active/pending jobs and invalidates old acceptance.
An exact completion replay is idempotent; a different payload for the same accepted
claim is a conflict. Result acceptance, observation changes, one-shot completion,
audit and generated inbox/outbox records commit or roll back together.

Checks execute at least once. An ambiguous response or partition can cause a
retry to execute a read again. No exactly-once query execution is claimed. A
result is externally actionable only after its transaction commits. Provider
requests can also repeat after an ambiguous acknowledgement; delivery IDs are
stable and providers/sinks must use them as idempotency keys when supported.

## Watch observations and revisions

Observations distinguish never, true, false and unknown. Check `pass` maps to
true and `fail` to false. Infrastructure errors and unknown results preserve the
last known boolean and never create a business failure transition. Changes and
recovery compare against that last known value, including across an intervening
error. Initial observation is explicitly `baseline` or `notify`.

The first known result pins the semantic source fingerprint. Claims return that
fingerprint to the runner. A later mismatch becomes unknown with `SOURCE_CHANGED`
and cannot create a business notification. A semantic edit (Check/version/input
binding change) resets the baseline and pin. A cosmetic title edit preserves the
baseline and existing paused/completed lifecycle. Editing always versions the
definition and cancels in-flight results; resuming is an explicit lifecycle
operation. A reviewed source change is acknowledged through the explicit
`ResetBaseline` operation; a cosmetic save cannot silently acknowledge it.
Environments are separate definition scopes. There is no implicit
cross-environment mutation.

One-shot Watches complete atomically with their first qualifying durable event,
including when there are no subscribed delivery channels. Delivery preferences do
not cause repeated occurrences from the same condition. Cooldown
suppresses event storms; unchanged conditions are quiet. Continuous Watches can
notify failure, change and optional recovery. Subscriber preferences do not
change the Watch's execution state.

## Channels, consent and limits

Canonical channel identifiers are `in_app`, `email`, `sms` and `telegram`.
Email is free. SMS requires a paid entitlement from authenticated deployment
identity and a verified telephone number. Telegram is free and requires a
connected account on the operator's own Telegram bot. The bot ID is a fixed
runner credential grant; its secret stays on the outbound provider runner. The
runner verifies its public identity with Telegram before registration. One bot
can serve one project/environment scope in this coordinator, and one active bot
can serve that scope. User status exposes its public ID and username only while
an eligible provider runner remains registered and online.

Clients cannot select another user or invent a recipient: email and telephone
recipients are copied from verified identity. Telegram connects using a random,
expiring nonce bound transactionally to the verified user, scope and registered
bot; only its hash is stored. A fenced provider poller proves the private chat ID
by processing that bot's `/start` update. An exact nonce/chat replay is idempotent;
a different chat cannot reuse it. Unlinking revokes outstanding nonces and pending
Telegram deliveries. Replacing the configured bot cancels its pending deliveries
and requires connection and subscription consent for the replacement bot.

Provider polling has a separate durable DALGO lease and fence per bot. A second
process using even the same runner identity cannot claim an active poll lease.
Renewals return the authoritative offset. The runner advances it only after an
update is accepted or deliberately classified as irrelevant; a crash before the
advance replays the update safely. After seven days without a processed update,
renewal atomically resets the offset to zero for Telegram's randomized update-ID
restart behavior. Renewing an idle poll does not count as processing an update.
Telegram delivery claims require the same live scoped bot grant and poll fence,
in addition to the delivery fence. Coordinator email/SMS workers skip Telegram.
No inbound bot webhook or shared DataTug-owned bot is used by this flow.

A verified reconnect increments the account link's durable generation, including
when it reconnects the same chat. Delivery claims retain that generation. An
explicit Telegram recipient-unavailable response terminally fails the affected
delivery and invalidates only that user's matching bot/chat/generation connection;
it cancels that connection's other pending Telegram deliveries. A delayed failure
cannot revoke a newer reconnect. Other subscribers, channels and the Watch's
evaluation remain independent. The affected user sees a reconnect requirement;
reconnecting does not replay terminal deliveries or change subscription consent.

Telegram flood-wait responses retain the provider's bounded retry delay in the
delivery and bot poll record. The same transaction fences the current poll lease.
No runner can reclaim that bot before the durable cooldown expires. Poll-only
rate limits use the same cooldown, so runner failover cannot bypass Telegram's
bot-wide limit.

Each accepted event has deterministic per-user/channel delivery IDs. In-app
notifications persist immediately in the same acceptance transaction and maintain
per-user read state. External delivery state is independent of evaluation success.
Quotas are durable namespace/account/channel counters, shared by all Watches and
projects using that account. Defaults are 50 email/account/UTC day and 5/minute,
10 SMS/account/UTC day and 2/minute, and 100 Telegram/account/UTC day and 5/minute.
Operators can configure limits. Reservation is atomic, once per delivery; provider
retries reuse it. Rate-limited events retain their payload and expose the next
window; in-app delivery remains independent. SMS entitlement and all subscription
revocations are rechecked before dispatch. A provider attempt already in flight
cannot be unsent by later revocation.

External delivery retries stop at the configured maximum (default three) and show
`failed`. The user's explicit retry uses the original idempotency key and quota
reservation. There is no automatic paid-message retry loop without a bound.

## Retention, limits and verification

There are separate queryable DALGO tables for definitions, observations, runs,
runner registrations, subscriptions, inbox, outbox, audit, idempotency operations,
account links, link requests, quotas, bot/scope registrations and bot poll leases/offsets. Payloads have a 128 KiB cap; there are at
most 1,000 definitions per namespace and, by default, 100 active definitions
per user. The default minimum interval is five seconds; deployments can raise
that minimum (up to one hour) and adjust the user count (up to 1,000). Terminal runtime history defaults to 1,000
records per table/namespace, configurable to 10,000. Active jobs and undelivered
outbox events are retained; a 10,000-event outbox cap applies backpressure rather
than silently dropping notifications. Definitions, tombstones, observations,
subscriptions and account links remain durable. Old offline registrations and
expired link requests are cleaned; quota counters remain for at least 48 hours.
Idempotency operation retention is bounded: a very old replay may receive a
version conflict after its receipt is pruned.

Executed verification from the CLI worktree:

```
wb run -- go test ./pkg/watch -count=1 -v
wb run -- go test -race ./pkg/watch -count=3
```

Tests cover independent DALGO handles and separate OS processes racing for one
job, lease expiry and hard deadlines, fencing, error retry, transaction rollback,
restart durability, exact/changed result replay, owner/shared/privacy boundaries,
wrong-environment claims, one-shot completion, failure/recovery/unknown semantics,
source changes, verified recipient consent, paid-only SMS, quotas shared across
projects, retry reservation reuse, Telegram nonce replay/unlink, multi-subscriber
recipient isolation, reconnect generation fencing, durable flood waits, and unsafe DSN
rejection. `beforeCommit` is an unexported test seam used to prove that notification
and completion cannot partially commit. A narrow unexported database interface
allows test wrappers to inject adapter failures while delegating successful calls
to the actual SQLite DALGO database; the production constructor remains fixed.
These storage tests do not claim that a
browser, remote runner, SMTP/SMS/Telegram provider or public deployment passed.


## Work integration events (v1)

Accepted known results record immutable `dt_watch_events` evidence in the same
DALGO transaction as the fenced run, observation and delivery outbox. Event IDs
are derived from scope, logical run and transition kind. The source snapshot
contains the Watch definition version, Check ID/version, source fingerprint,
runner-local Check receipt reference, owner/visibility and typed definition
inputs. It contains no runner credentials or query rows. Existing definition
validation and the 128 KiB DALGO record bound apply to snapshots.

Canonical evidence and recipient delivery are separate:

| Previous → accepted value | Canonical event |
| --- | --- |
| never → matching target, initial `notify` | trigger |
| never → any, initial `baseline` | none |
| unchanged known value | none |
| false → true | recovery, irrespective of delivery preference |
| true → false, matching target | trigger |
| rejected stale result, infrastructure error, unknown | none |

A reset clears the observation baseline, preserving earlier immutable events;
it does not fabricate a recovery. Notification trigger labels, subscription
consent, cooldown and recovery delivery preferences keep their existing meaning.
New runs, observations and notifications expose optional `eventId`/`lastEventId`.
Legacy notifications without event IDs remain readable and are not backfilled.

`GetEvent` and `ListEventPage` read current definition visibility and evidence
inside one transaction. Paused/completed history remains readable; deleted
Watch definitions and another owner's personal definitions cannot expose events.
An event captured while personal remains owner-only after its Watch becomes
shared; pagination filters snapshot visibility before applying page limits.
A consumer separately authorizes shared Work creation and requires explicit
consent to share personal source evidence. Runners cannot read these APIs.

Events are excluded from automatic runtime-history pruning so old notification
links and Work handoff retries retain source authority. Back up the SQLite file
with the deployment's backup procedure. Operator archival/deletion policy is a
follow-up; growth is proportional to meaningful transitions, not polling. Work
keeps its committed source snapshot after a deliberate future source archive.
History reads have a database limit (default 100, maximum 1000), an index over
scope/Watch/occurrence instant/ID and a cursor. Consumers follow `nextCursor`
until empty for complete reconciliation.
