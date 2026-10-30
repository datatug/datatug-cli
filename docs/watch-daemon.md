# DataTug Watch daemon

The DataTug daemon has independent foreground Coordinator and Runner roles:

```sh
datatug daemon --coordinate --watch-config /absolute/private/coordinator.json
datatug daemon --run --watch-config /absolute/private/runner.json
datatug daemon --coordinate --run --watch-config /absolute/private/combined.json
```

Combined mode uses the same authenticated HTTP registration, claim, renewal and
completion protocol as a remote runner. Runners require no inbound port. The
existing `daemon start`, `status`, `stop` and `url` commands retain their existing
local UI-helper behavior. Watch readiness is `GET /health`; authenticated Watch
availability is `GET /v1/watch/status`.

## Existing code reused

| Responsibility | Existing implementation reused |
| --- | --- |
| Check definitions, nested validators and true/false/unknown evaluation | Core `pkg/detective/catalog.go`, `evaluate.go`, `engine.go` |
| Local Check admission, query revision pinning, durable run audit | CLI `pkg/detective/action.go`, `query_revisions.go`, new `watch.go` guard |
| Saved TugQL, SQL, HTTP and optional OVDB execution | CLI `pkg/server/endpoints/detective.go`, `exec_run_query.go`, `pkg/secureread`, `pkg/api/resolver.go` |
| Project manifest loading and durable saves | Core `pkg/datatug/project.go`, `project_file_json.go`, `pkg/storage/filestore/store_loader.go`, `store_project_saver.go` |
| Coordinator transactions, state and outbox | CLI `pkg/watch`; DALGO SQLite transactions; `docs/architecture/watch-coordinator-storage.md` records the transactional gate and adapter decision, no OVDB write API |
| SMS transport | Shared `sneat-go-core/twiliosms` kernel |
| Runner-owned Telegram bot | `pkg/watchdaemon/telegram_runner.go`; outbound Bot API, existing webhook rejected, provider lease and offset in DALGO |
| CLI lifecycle and foreground role dispatch | `apps/datatugapp/commands/cmd_daemon.go`, `cmd_daemon_watch.go` |
| Typed Context Bag input binding | Core `pkg/detective/evaluate.go` and CLI `pkg/server/endpoints/detective.go`; Watch passes typed saved inputs |
| UI management and history | Apps project `/watch` and `/watch/:id` routes; existing Check details provide creation entry |

`datatug-go-models` is not the domain authority; current project and Check models
belong to Core. The hosted backend already has separate authenticated Detective
and project services. This self-hosted adapter does not claim hosted Firebase,
Paymentus or account-plan integration.

## Supported identity and source boundaries

The coordinator authenticates deployment-configured user and runner identities
with distinct bearer tokens. Each token fixes exactly one namespace, project,
environment and identity. Browser request bodies cannot select an actor, account
tier, recipient address, phone or runner capabilities. Personal definitions remain
in the coordinator's project-scoped DALGO repository, outside shared Git files.
Subscribers independently opt in; shared editors cannot subscribe other people.

The current CLI Detective project-asset grant is available only in explicit local
owner mode. A runner must name its local principal and environment. If project
policies require a secured asset grant, Watch startup fails closed. The existing
secured query path remains in use; no Watch bypass grants local-owner access.
This limitation must be resolved with the shared project-asset permission model
before claiming general hosted/private-policy support.

Source credentials stay in the runner's environment and its existing project
source descriptors. Coordinator storage configuration belongs to the deployment.
The coordinator never receives source connection URLs, query rows or credentials.
The runner pins the Check version, transitive catalog and saved query revisions
before dispatch. Dependency changes require an explicit Watch baseline reset;
changing the root Check version requires a definition edit. Infrastructure errors
remain unknown and cannot trigger a business false transition.

## Deployment configuration

The project manifest contains only the Coordinator locator:

```json
{"coordination":{"endpoint":"https://coord.example.com","namespace":"northwind"}}
```

The deployment file names environment variables rather than embedding secrets.
Keep it private and outside the project repository. An example combined local
deployment is:

```json
{
  "namespace": "northwind",
  "listen": "127.0.0.1:8990",
  "storage": {"dalgoConnectionEnv": "DATATUG_COORDINATOR_DB_URL"},
  "allowedOrigins": ["https://datatug.app", "http://localhost:4200"],
  "projectUIURLs": {
    "northwind": "https://datatug.app/store/your-agent/project/northwind"
  },
  "credentials": [
    {"kind":"user","id":"alice","accountId":"alice","tier":"free",
     "namespace":"northwind","project":"northwind","environment":"training",
     "sharedEditor":true,"email":"alice@example.test","tokenEnv":"DATATUG_WATCH_ALICE_TOKEN"},
    {"kind":"runner","id":"runner-a","namespace":"northwind",
     "project":"northwind","environment":"training","tokenEnv":"DATATUG_WATCH_RUNNER_TOKEN",
     "telegramBotId":"123456789"}
  ],
  "runner": {
    "tokenEnv":"DATATUG_WATCH_RUNNER_TOKEN",
    "coordinatorEndpoint":"http://127.0.0.1:8990",
    "projectDir":"/absolute/private/northwind-working-copy",
    "environment":"training","principal":"local-watch-owner",
    "evidenceDir":"/absolute/private/watch-evidence",
    "allowOpaqueSQL":false,
    "telegram":{"botID":"123456789","secretEnv":"DATATUG_OWN_BOT_SECRET"}
  },
  "notifications": {
    "emailSink":"/absolute/private/watch-mail",
    "smsSink":"/absolute/private/watch-sms"
  },
  "limits": {
    "emailPerDay":50,"emailPerMinute":5,
    "smsPerDay":10,"smsPerMinute":2,
    "telegramPerDay":100,"telegramPerMinute":5,
    "watchesPerUser":100,"minIntervalSeconds":5
  }
}
```

For that local example set the manifest endpoint to `http://127.0.0.1:8990`.
The storage variable must contain an absolute local SQLite path or
`sqlite:///absolute/path`; the directory must already exist. Tokens must contain
at least 32 characters. Allocate distinct tokens for every user and runner.
Coordinator-only files omit runner settings; runner-only files need no coordinator
storage or user credentials. A runner loads its endpoint from the manifest and
requires it to match deployment `runner.coordinatorEndpoint` before sending its
token. An explicit `--coordinator` is an intentional operator trust override;
`--namespace` still must match the manifest. A project edit alone cannot redirect
a deployment credential.

Non-loopback Coordinator listeners require `tlsCertFile` and `tlsKeyFile`.
Configure exact browser origins. The browser holds a user token in memory only;
disconnecting or reloading requires reconnecting. Never put a token in a URL,
project manifest or browser local storage. `projectUIURLs` maps each authorized
project to its actual project page URL, from which notification Watch links are
built. A `publicUIURL` fallback applies only to a single-project deployment.

Run the foreground command under an existing supervisor for unattended operation.
Provide environment variables through that supervisor's secret mechanism, send
SIGTERM for graceful shutdown, and retain JSON stderr logs. No launchd/systemd job
is installed automatically. The process that starts a test daemon owns its PID
and must stop it and wait for exit. The Coordinator closes its HTTP listener and
cancels runner queries and outbox workers on shutdown.

## Channels and limits

In-app inbox entries are durable and private to their subscriber. Email is
available to free accounts when the operator configures a verified recipient and
provider. SMS additionally requires a paid account, a verified E.164 number and a
configured provider. Self-hosted account tiers are operator grants; they are not
proof of a DataTug Cloud subscription. The coordinator rechecks paid authority
when claiming delivery. Configuration changes take effect after restart. Hosted
adaptation must use the backend's existing active-account/plan authority, including
paid-until checks, before advertising paid SMS.

The defaults shown above are coordinator-enforced per-account/channel quotas.
Limits are configurable within bounded deployment caps and exposed by `identity`.
Reservations are durable and retries do not consume another logical delivery quota.
Missing entitlement or provider fails closed. Delivery status and retries are
separate from evaluation outcomes.

For development, each sink is a directory containing complete JSON messages keyed
by a stable delivery ID hash. Publication uses an fsynced temporary file and an
atomic non-replacing link. Interrupted temporary files do not poison the inbox;
retries validate an existing message and do not duplicate it. Do not use symlink
sink directories or message files. SMS sink recipient addresses use `@sms.test` to distinguish test messages.

For SMTP configure `notifications.smtp` with `address`, `from`, optional
`usernameEnv`/`passwordEnv`. TLS is required; explicit
`allowLoopbackPlaintext:true` is available only for a numeric loopback development
receiver. SMTP acceptance is known after DATA succeeds; QUIT is best effort.
An ambiguous DATA response or provider acceptance followed by a crash can still
produce at-least-once external delivery. Stable Message-ID aids provider diagnosis;
it does not promise exactly-once SMTP delivery.

For SMS set `notifications.twilio:true` and the shared `TWILIO_*` environment
variables documented by `sneat-go-core/twiliosms`. Tests use a fake transport and
never spend money. Twilio transport delivery is also at least once when an
acceptance acknowledgement is lost.

Telegram uses the operator's own dedicated bot, configured only on the runner.
Set `runner.telegram.botID` and `secretEnv`; the latter contains the secret part
following the colon in the BotFather token. Alternatively `tokenEnv` contains the
whole token and must match `botID`. Configure exactly one form. The Coordinator's
runner credential must separately authorize that `telegramBotId`. Neither the
bot secret nor its token is sent to the Coordinator, browser or project manifest.
Runners verify `getMe` identity and username, then reject an existing webhook with
instructions to use a dedicated bot. They never remove or replace a webhook.
A runner without Telegram configuration can continue ordinary Check evaluation.

The subscriber chooses Connect Telegram and opens a short-lived, one-use link to
the verified bot username. A private `/start watch-<nonce>` proves the matching
sender and chat; browser requests cannot choose recipients. The nonce and durable
link bind the authenticated subscriber, project, environment and bot ID. One bot
belongs to one scope. Multiple authorized runners can cooperate, but only the
current fenced provider-poll lease owner calls `getUpdates` and sends deliveries.
Only processed updates advance the durable offset; renewal returns the current
offset, resetting it after seven days without a processed update to accommodate
Telegram's update-ID reset. Polls request at most 5 updates and 5 seconds; all
provider calls are bounded and canceled when the lease or runner shuts down.

The Coordinator owns durable Telegram quotas, subscriptions, outbox retries and
both poll/delivery fences. Its notification worker never sends Telegram. The
runner claims scoped deliveries, sends only to the linked bot/chat, and acknowledges
the exact delivery fence. Explicit blocked/deactivated/chat-not-found failures mark only that recipient's
Telegram connection as requiring reconnection. Other subscribers and channels
continue independently. Provider/authentication failures keep links intact;
validated Telegram429 `retry_after` creates a durable bot-wide cooldown and
bounded delivery retry without charging the quota again. A crash after provider acceptance and before durable
acknowledgement can produce at-least-once external Telegram delivery. No global
DataTugBot, hosted gateway, forwarding callback, inbound runner listener or bot
secret on the Coordinator is required. Tests use a local fake Bot API and never
create a bot or message a real account.

Outbound templates contain generic notification text and an operator-configured
Watch and Check deep links. They exclude titles, parameter values, rows, raw errors and source
URLs. Provider errors are reduced to safe status codes.

## API and troubleshooting

All API calls use `/v1/watch/` and an in-memory Bearer credential. `identity`,
`definitions`, `definition?id=`, `subscriptions?watchId=`, `runs?watchId=`,
`deliveries?watchId=`, `status` and `inbox` are GETs. POSTs are `definition`,
`lifecycle`, `delete`, `subscription`, `reset`, `retry`, `read` and Telegram
connect/disconnect. Definition/lifecycle/delete/reset changes carry an operation
ID and expected version. Runner POSTs are register, claim, heartbeat and complete;
user credentials cannot enter these routes. Telegram runners additionally use
`runner/telegram/poll`, `renew`, `advance`, `defer`, `link`, `claim` and `complete`; fixed
credential scope and authorized bot ID apply to every call. Error responses contain stable codes
without storage errors or credentials.

Create a Watch paused, subscribe, then enable it. This ensures an immediately true
one-shot condition has recipients before the first observation. A definition is
project-owned; subscriber preferences and read state are separate.

| Symptom | Check |
| --- | --- |
| Coordinator unavailable | `/health` must return 200 only when DALGO storage is reachable; check TLS, configured endpoint and storage file permissions |
| Runner unavailable/delayed | `status` shows eligible registrations; verify fixed project/environment/capabilities and outbound connection |
| Registration denied | Runner and user tokens are distinct, environment-scoped and deployment-owned |
| Source unavailable | Existing project environment catalog, local source credentials and read-only access must work; errors remain unknown |
| Source changed | Review the Check and saved query revisions, edit root version if needed, then explicitly reset the baseline |
| Secured project assets refused | Current local Detective asset admission requires local owner mode; no secured-grant bypass is provided |
| Email/SMS/Telegram failed | Inspect own `deliveries`; provider/recipient/quota failure is distinct from Check outcome; explicitly retry after fixing the cause |
| Telegram connect unavailable | An authorized bot runner must be online; verify bot ID/secret and use a dedicated bot without an existing webhook |

The supported deployment is one Coordinator per namespace with local SQLite.
It does not claim HA. Read-only Check execution may occur at least once after a
crash. DALGO fencing prevents an expired worker from replacing an accepted newer
result; transactional outbox keys deduplicate logical notification creation.

## Build and integration dependencies

The CLI pins published `datatug-core v0.79.0` (Coordination manifest support,
release target `d3579d7c6a1d2280f3fd9b3a7a618857fd3070fc`). No local module
replacement or `datatug-go-models` change is required. Watch adds no general OVDB
write API. The storage ADR documents why transactional DALGO is required.
SMTP uses Go's existing TLS-capable SMTP client; SMS reuses Sneat's Twilio kernel.
The Apps UI uses the existing project navigation and typed Check inputs; its
Coordinator URL/namespace come from Core's project model. Hosted Firebase identity
and hosted paid-account billing remain separate adapters, not self-host claims.

```sh
wb run -- go build -o /absolute/private/datatug ./main.go
```

## Verification

Focused transport tests use real HTTP, DALGO SQLite handles and a local SMTP
receiver, plus fake Twilio/Telegram provider transports. They cover competing
claims, stale results, per-user scope, cancellation, durable inbox/sinks/events, readiness,
manifest credential pinning, Telegram nonce replay and daemon compatibility.

```sh
wb run -- go test ./pkg/watchdaemon ./pkg/detective ./apps/datatugapp/commands -run 'TestWatch|TestDaemon' -count=1
wb run -- go test -race ./pkg/watch ./pkg/watchdaemon -count=1
```

The complete Northwind process/browser journey is a separate integration receipt;
passing transport tests alone does not establish that demo acceptance.

## Shared cloud runner

Hosted projects use the trusted DataTug API's default coordinator. Their endpoint
is the API base followed by `/v0/datatug/watch/<space>/<project>/<environment>`;
the enrollment screen exposes this exact scope and endpoint. Hosted projects do
not persist an arbitrary coordinator override. Firebase user tokens are never
sent to a manifest URL. Self-hosted project coordination remains configured by
its existing manifest descriptor.

The identity response separates configured transport availability in
`providers.email` and `providers.sms` from the current user's permitted `channels`.
SMS still requires a paid account and verified phone number when its provider is
configured. Optional `coordination` metadata identifies the trusted project
coordinator; it never selects a Firebase token destination or grants permission.

An enrolled runner keeps its one-time credential in the operator's secret store
or process environment. The project manifest contains no credential. A cloud
runner configuration has this shape (replace every placeholder):

```json
{
  "namespace": "<spaceID>",
  "runner": {
    "coordinatorEndpoint": "https://<trusted-api>/v0/datatug/watch/<spaceID>/<projectID>/<environment>",
    "tokenEnv": "DATATUG_WATCH_RUNNER_TOKEN",
    "projectDir": "/private/operator/source-project",
    "environment": "<environment>",
    "principal": "operator-local-owner",
    "evidenceDir": "/private/operator/watch-evidence",
    "allowOpaqueSQL": true,
    "cloud": {
      "spaceID": "<spaceID>",
      "projectID": "<projectID>",
      "sourceBindings": {"<admitted-source-ID>": "<local-SQLite-catalog-alias>"}
    }
  }
}
```

Run `datatug daemon --run --watch-config /private/operator/watch.json` with
`DATATUG_WATCH_RUNNER_TOKEN` supplied through the private environment. The token
already identifies the enrollment; there is no separate runner-ID setting.
`principal` names the local operator, not a Firebase user impersonation.

The supported native mode requires an explicitly configured local owner project
with an admitted SQLite catalog. Secured local Check-asset grants remain
unsupported and fail closed. `allowOpaqueSQL` is an explicit operator grant for
the existing readonly SQL executor; it does not promise row/column policy
filtering. A native source binding names the local catalog alias. An OVDB binding
must equal the exact server-admitted origin. Embedded cloud demo URIs have no
local runner mapping and are rejected.

For the bounded shipping source, the coordinator supplies the exact immutable
SQLite image admitted with the claim. The runner verifies its size, SHA and
bundle digest, validates the image in an owned private temporary directory, and
uses the existing readonly query and Core Check engine. It never falls back to a
local Check, query, or mutable database after a bundle/artifact failure. Local
aliases, filesystem paths and unrelated extra bindings do not change a Watch's
semantic baseline.

Source semantics retain Check/query/validator versions, publication binding,
schema, fixture provenance and shipping generation. Same-generation business
correction/replay changes the evaluation artifact without resetting the Watch;
a generation reset or source-definition change requires explicit review/reset.
Missing artifacts produce unknown/retry. Personal Watches are evaluated only by
owner-issued runner grants; shared Watches can use other currently authorized
source grants. Another project manager can revoke a runner, but cannot reissue
its owner's credential.

The initial cloud fixture executed three independent owner-enrolled OS runners,
actual Firebase token verification/current native ownership, real Firestore,
the existing governed Northwind order 11045 carrier-assignment data and a local
SMTP sink. Its fixture authored a Check/query over that existing data; this is
not a physical-shipment signal. Production cloud activation and broader
restart/crash/browser acceptance have separate receipts; a focused execution
receipt is not a hosted deployment claim. External SMTP/Telegram delivery remains
at-least-once after ambiguous provider acceptance despite stable message IDs.

## Work integration event API and upgrade

`GET /v1/watch/events?watchId=<id>&limit=100` returns
`{"events":[...],"nextCursor":"<event-id-or-empty>"}` newest first. Follow with
`before=<nextCursor>` for another page; limit must be 1–1000 for SQLite or 1–100 for the scoped Firestore provider. Ties have stable
ID ordering. `GET /v1/watch/event?id=<id>` returns `{"event":{...}}`. Both require
the existing user bearer credential and enforce its exact namespace, project,
environment and current Watch visibility. Runners cannot read these routes.
Paused/completed history remains readable; deleted Watch history fails closed.
A personal event snapshot remains owner-only after its Watch becomes shared.

An event has `id`, `namespace`, `project`, `environment`, `watchId`, `runId`,
`definitionVersion`, `title`, `kind` (`trigger` or `recovery`), `previous`
(`true`, `false`, `never`), `outcome` (`pass` or `fail`), `occurredAt`, `checkId`,
`checkVersion`, `sourceFingerprint`, optional `checkRunRef`, optional typed
`inputs`, `owner` and `visibility`. Events are evidence, not Work items.
New notifications reference `eventId`; acknowledgement only marks the current
subscriber's read state. It creates neither events nor Work.

Upgrade adds `dt_watch_events` and its history index automatically. Existing
Watch IDs and delivery receipts are preserved; old notifications have no invented
source snapshot. Events outlive runtime-history pruning: include them in backup
and disk capacity planning. There is no automatic event purge in v1.
One-shot behavior deliberately changes: a qualifying durable event completes it
even without subscribers. Create paused and subscribe before enabling to receive
that first event. Reset/resume stays explicit; Work closure does not change
monitoring. Capture is independent of cooldown and recovery notification settings.
The Work backend's trusted runtime bridge authorizes hosted identity mappings;
the coordinator never infers them or accepts a request-supplied bridge endpoint.
