# DataTug CLI Docker image

The `datatug-cli` image packages the existing CLI as one execution-only image.
It supports one-shot Checks and queries and the existing foreground daemon
Coordinator/Runner roles. It does not contain the DataTug web application,
Apps bundles, a new execution engine, Go source, module caches, demo projects,
or credentials. The full visual experience remains at `https://datatug.app`.

The image defaults to `datatug --help`. It runs as UID/GID `65532:65532`, uses
a read-only root filesystem in both supplied Compose examples, disables
telemetry, and writes persistent private state only under `/var/lib/datatug`.
The image itself declares no health check because a one-shot process and an
outbound Runner have no readiness listener. The hybrid Compose file adds a
Coordinator-only check of loopback `GET /health` with a three-second deadline.
That probe rejects redirects and never sends a token.

## Build a source-free context

From the repository root, create a new temporary output directory outside the
checkout. The helper compiles only static Linux `amd64` and `arm64` executables
with CGO disabled, inventories actual Go modules from `go list -deps -json`,
and copies the pinned Dockerfile, license/notice files and private state
directory skeletons. The generated context contains no Go source, module
cache, project fixture, environment file, secret or local project config.

```sh
context=$(mktemp -d /tmp/datatug-docker-context.XXXXXX)
rmdir "$context"
python3 distribution/build_context.py --output "$context"
```

The generated `CONTEXT-INVENTORY.md` lists every included file and its digest;
`licenses/DEPENDENCY-LICENSE-INVENTORY.md` records each module and included
notice. The helper requires `github.com/datatug/datatug-core v0.79.0` and
retains its MIT license notice. If a dependency has no recognized top-level
license/notice file, the helper prints it and records a **publication gate**;
development builds can still be inspected. A release build must use
`--fail-on-missing-licenses` and resolve every missing notice with the
maintainer before it is eligible for publication.

Build one local architecture using the pinned base image and the generated
context. Select `arm64` on Apple Silicon and `amd64` on Intel hosts; this only
builds a local image and does not publish it.

```sh
docker build --platform linux/arm64 -t datatug-cli:local "$context"
```

The build helper pins Go `go1.27.3`, disables workspace overrides and ambient Go flags, and requires a read-only module graph. It includes the Go toolchain and vendored standard-library license notices alongside actual module notices.

The Dockerfile pins
`gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99`.
The final image contains only the CLI and PID 1 wrapper binaries, license and
notice files, and empty private state directories. Inspect the generated
context and image before using them as release evidence; a successful local
build does not verify vulnerabilities, SBOM/provenance or publication.

## Run the synthetic local Check

Copy `distribution/.env.example` to `distribution/.env`, set `DATATUG_IMAGE`
to the local image tag, and explicitly set `DATATUG_LOCAL_OWNER=true`. The
Compose file also requires those variables to exist. The explicit
`--local-owner` CLI option authorizes local-owner Check execution against the
trusted, read-only synthetic fixture. A principal name by itself is not
consent.

```sh
cd distribution
cp .env.example .env
docker compose --env-file .env -f compose.local.yaml run --rm checks-demo
```

The fixture is mounted read-only; a named volume holds private Check evidence
at `/var/lib/datatug/evidence`. The JSON output is a bounded summary and does
not include evidence rows or a hosted result URL. Keep the named evidence
volume private. Remove the test project and volume when its evidence is no
longer needed:

```sh
docker compose --env-file .env -f compose.local.yaml down -v
```

The sample demonstrates the `pass` Check. Other fixture IDs and their expected
outcomes are documented in [checks-run.md](checks-run.md). `checks run` does
not start a listener. It needs no connection to `datatug.app` to execute a
local Check; the output's hosted home link is not a result page or evidence
synchronization claim.

## Optional hybrid Coordinator and outbound Runner

`distribution/compose.hybrid.yaml` demonstrates the existing daemon roles. It
binds no host port: the Runner calls Coordinator DNS on Compose's private
network and does not expose an inbound listener. For a remote deployment,
configure a Coordinator reachable through an authenticated outbound connection
and a trusted project manifest endpoint; do not expose a customer database to
the public internet.

Create the private files and directories referenced by Compose:

- `distribution/private/coordinator.json`: Coordinator config using the
  existing `docs/watch-daemon.md` schema.
- `distribution/private/runner.json`: Runner config using that same schema.
- `distribution/private/project/`: a trusted project checkout, including its
  manifest. Mounts are read-only; source config and any credentials it uses
  must be private and available through the runner environment.
- `distribution/.env`: private token environment values. Use distinct random
  values of at least 32 characters for the user and Runner tokens. Do not put
  token values in either JSON config or a project manifest.

Provision a certificate with SANs for the Coordinator DNS name and IP `127.0.0.1` (the verified in-container health URL), its private key, and a deployment CA bundle at `private/tls/coordinator.crt`, `private/tls/coordinator.key` and `private/tls/ca.crt`. The private key is mounted only into the Coordinator; the Runner receives only the CA bundle. Keep the host private directory owner-only and provision readable mounts for runtime UID 65532. Use production-trusted certificates or an explicitly managed deployment CA; do not disable TLS verification. The default sample previously suggested non-loopback HTTP, which the daemon correctly refuses.

The Coordinator config's essential shape is:

```json
{
  "namespace": "northwind",
  "listen": "0.0.0.0:8990",
  "tlsCertFile": "/run/tls/coordinator.crt",
  "tlsKeyFile": "/run/tls/coordinator.key",
  "storage": {"dalgoConnectionEnv": "DATATUG_COORDINATOR_DB_URL"},
  "credentials": [
    {"kind":"user","id":"operator","accountId":"operator","tier":"free",
     "namespace":"northwind","project":"northwind","environment":"training",
     "sharedEditor":true,"email":"operator@example.test","tokenEnv":"DATATUG_WATCH_ALICE_TOKEN"},
    {"kind":"runner","id":"runner-a","namespace":"northwind",
     "project":"northwind","environment":"training","tokenEnv":"DATATUG_WATCH_RUNNER_TOKEN"}
  ]
}
```

The Runner config's essential shape is:

```json
{
  "namespace": "northwind",
  "runner": {
    "tokenEnv": "DATATUG_WATCH_RUNNER_TOKEN",
    "coordinatorEndpoint": "https://coordinator:8990",
    "projectDir": "/project",
    "environment": "training",
    "principal": "operator-local-owner",
    "evidenceDir": "/var/lib/datatug/evidence",
    "allowOpaqueSQL": false,
    "privateExecutionEvidence": true
  }
}
```

Use the exact project, namespace, environment, tokens and source configuration
for the deployment. The project manifest coordinator locator must match the
Runner config endpoint. The source config and token values remain local to the
Runner. The Coordinator stores only its own Watch state; it does not receive
source URLs, credentials or query rows. Enable `privateExecutionEvidence` for a read-only project: immutable query execution metadata and snapshots then live in separate protected paths under the private evidence volume. Legacy Watch deployments retain their original routing unless they opt in.

The supplied Compose example gives
each role a separate persistent private volume, read-only root filesystem,
dropped capabilities, no-new-privileges, bounded CPU/memory/process counts and
a bounded `/tmp` tmpfs. Logs use Docker's rotating JSON-file driver and must be
handled under the operator's retention policy.

The entrypoint refuses any `daemon --run` or `daemon --run=true` invocation
unless `DATATUG_LOCAL_OWNER` is exactly `true`. A configured `principal` alone
does not pass that gate. Coordinator-only mode does not require local-owner
consent. The `healthcheck` entrypoint command is accepted only when
`DATATUG_DAEMON_ROLE=coordinator` and `DATATUG_HEALTH_URL` is a verified HTTPS loopback IP URL
with an explicit port and `/health` path. Runner mode has no readiness probe or
listener. Do not combine Coordinator and Runner in one container when using
the role-specific health probe.

Start and stop the example only after reviewing the configs and local-owner
consent boundary:

```sh
cd distribution
cp .env.example .env
mkdir -m 700 private
# Place the private configs and trusted project described above under private/.
docker compose --env-file .env -f compose.hybrid.yaml up -d
docker compose --env-file .env -f compose.hybrid.yaml ps
docker compose --env-file .env -f compose.hybrid.yaml logs --tail=100 coordinator runner
docker compose --env-file .env -f compose.hybrid.yaml down
```

No public listener is published by this file. A customer-managed external
Coordinator, proxy, firewall or cloud network may still make private services
reachable; configure and audit those boundaries separately. Verify the
Coordinator's `/health` and authenticated Watch status independently, and
confirm that the Runner has registered and can reach its exact namespace,
project and environment. A healthy Coordinator alone does not prove the Runner
or a Check is healthy.

## Operations and limits

- **Offline:** one-shot local Checks can run without `datatug.app`; hosted
  pages, cloud Coordinator communication, remote source APIs and online image
  pulls need their respective network paths. Do not treat a home-page link as
  synchronized evidence.
- **Private networking:** Compose creates an isolated project network and
  publishes no ports. For remote use, permit only required outbound Runner to
  Coordinator/API traffic. Do not add a public database listener. Set explicit
  proxy and `NO_PROXY` values in the Runner environment where required.
- **Certificates:** the distroless image contains system CA roots. Private CA
  trust must be mounted read-only and configured using a supported Go/system
  trust path. No custom CA bundle is baked into the image. Do not disable TLS
  verification to make connectivity work.
- **Credential rotation:** Watch credentials come from environment variables
  named in deployment config. Rotate through the deployment's secret manager,
  update the Coordinator and Runner together as required, then restart the
  relevant containers. Never put tokens in command-line arguments, URLs,
  project manifests, image layers or logs.
- **State and backups:** Coordinator SQLite state is under
  `/var/lib/datatug/coordinator`; Runner evidence is under
  `/var/lib/datatug/evidence`. Stop or quiesce the relevant container before
  taking an application-consistent backup of its volume. Encrypt backups and
  restrict access. Restore into a private volume owned by UID 65532 and verify
  the database before resuming operation.
- **Upgrade:** use immutable image tags or digests, keep a backup of private
  state and configs, stop the old image, then start the selected new image.
  Retain the old digest for rollback. Database schema compatibility and
  rollback must be checked for each CLI version; this image has no automatic
  downgrade procedure.
- **Teardown:** stop Compose roles first, preserve or securely delete their
  private volumes according to retention requirements, then remove the local
  image and temporary generated context if no longer needed. `down -v` deletes
  the named data volumes; use it only when their contents are disposable.
- **Current limits:** local Checks require a trusted local-filesystem project,
  explicit environment, and Checks without input declarations. Access-policy
  profiles are rejected because secured Check asset grants are not yet
  supported. The runner still requires local-owner admission. This package
  does not claim hosted Check result references, SaaS result synchronization,
  high availability, automatic source credential injection or marketplace
  availability.

## Verification evidence

The package implementation has focused wrapper tests for argument pass-through,
the explicit Runner owner gate, Coordinator-only health checks, loopback URL
validation, three-second timeout behavior and redirect rejection. The project
release workflow is responsible for multi-architecture builds, SBOM,
provenance, vulnerability scanning and final-payload licensing checks. Record
the exact source revision, helper output, image digest, test commands and exit
codes with any candidate build. Do not describe a local image build as a public
pull, release or live customer acceptance.

The template defaults owner consent to false. Copying it does not authorise execution; set true only after reviewing the project. Compose passes the Runner only its scoped Runner token; the Coordinator receives its user and Runner verifier tokens. Do not replace these allowlists with a shared env_file containing all deployment secrets.
