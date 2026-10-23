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
| Coordinator transactions, state and outbox | CLI `pkg/watch`; DALGO SQLite transactions, no OVDB write API |
| SMS transport | Shared `sneat-go-core/twiliosms` kernel |
| Existing hosted DataTugBot owner | Cloud bot host and Sneat bot start-command registry; no second webhook registration or polling owner |

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
     "project":"northwind","environment":"training","tokenEnv":"DATATUG_WATCH_RUNNER_TOKEN"}
  ],
  "runner": {
    "tokenEnv":"DATATUG_WATCH_RUNNER_TOKEN",
    "coordinatorEndpoint":"http://127.0.0.1:8990",
    "projectDir":"/absolute/private/northwind-working-copy",
    "environment":"training","principal":"local-watch-owner",
    "evidenceDir":"/absolute/private/watch-evidence",
    "allowOpaqueSQL":false
  },
  "notifications": {
    "emailSink":"/absolute/private/watch-mail",
    "smsSink":"/absolute/private/watch-sms",
    "telegramSink":"/absolute/private/watch-telegram",
    "telegramCallbackSecretEnv":"DATATUG_WATCH_BOT_BRIDGE_SECRET"
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
sink directories or message files. Sink recipient addresses for SMS and Telegram
use `@sms.test` and `@telegram.test` to distinguish test channels.

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

Telegram requires connecting the subscriber's account. `telegram/connect` returns
a short-lived, one-use `DataTugBot` start link. The existing bot owner forwards only
a private `/start watch-<nonce>` update to the operator-allowlisted Coordinator's
`/v1/watch/telegram/update`, authenticated with the configured bridge secret.
The browser cannot provide a chat ID. No Watch process changes the production
bot webhook or starts polling.

Self-hosted production delivery uses `telegramGatewayURL` and
`telegramGatewayTokenEnv`: an allowlisted existing bot-host gateway verifies the
opaque chat/route/expiry capability created during account linking. The global
DataTugBot token is not distributed to runners or self-hosted Coordinators.
`telegramTokenEnv` is reserved for a deployment already trusted to own that bot
identity; it is not a self-hosted onboarding requirement. The real bot forwarding
adapter must be deployed and tested before claiming production DataTugBot linking.

Outbound templates contain generic notification text and an operator-configured
Watch deep link. They exclude titles, parameter values, rows, raw errors and source
URLs. Provider errors are reduced to safe status codes.

## API and troubleshooting

All API calls use `/v1/watch/` and an in-memory Bearer credential. `identity`,
`definitions`, `definition?id=`, `subscriptions?watchId=`, `runs?watchId=`,
`deliveries?watchId=`, `status` and `inbox` are GETs. POSTs are `definition`,
`lifecycle`, `delete`, `subscription`, `reset`, `retry`, `read` and Telegram
connect/disconnect. Definition/lifecycle/delete/reset changes carry an operation
ID and expected version. Runner POSTs are register, claim, heartbeat and complete;
user credentials cannot enter these routes. Error responses contain stable codes
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
| Telegram connect unavailable | Existing bot forwarding bridge and provider must be configured; never re-register the production webhook |

The supported deployment is one Coordinator per namespace with local SQLite.
It does not claim HA. Read-only Check execution may occur at least once after a
crash. DALGO fencing prevents an expired worker from replacing an accepted newer
result; transactional outbox keys deduplicate logical notification creation.

## Verification

Focused transport tests use real HTTP, DALGO SQLite handles and a local SMTP
receiver, plus fake Twilio/Telegram provider transports. They cover competing
claims, stale results, per-user scope, cancellation, durable inbox/sinks, readiness,
manifest credential pinning, Telegram nonce replay and daemon compatibility.

```sh
wb run -- go test ./pkg/watchdaemon ./pkg/detective ./apps/datatugapp/commands -run 'TestWatch|TestDaemon' -count=1
wb run -- go test -race ./pkg/watch ./pkg/watchdaemon -count=1
```

The complete Northwind process/browser journey is a separate integration receipt;
passing transport tests alone does not establish that demo acceptance.
