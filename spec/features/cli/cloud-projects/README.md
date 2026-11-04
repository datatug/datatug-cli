---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Open DataTug Cloud projects in the CLI

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/cloud-projects?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/cloud-projects?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/cloud-projects?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/cloud-projects?op=request-change) |

**Status:** Approved
**Date:** 2026-11-04
**Owner:** Alexander Trakhimenok
**Source Ideas:** —
**Supersedes:** —

## Summary

`datatug ui --deep-link https://datatug.app/space/<space>/store/firestore/project/<project>` opens a DataTug Cloud project in the terminal UI, on the same project and page as the datatug.app page the command was copied from. The CLI signs the person in with the existing `datatug auth login` device flow. It reads the project only through the DataTug backend's `/v0/datatug/*` endpoints, the same ones datatug.app calls, and never talks to Firestore. This replaces the "DataTug Cloud projects can't be opened in the terminal yet" notice that `--deep-link` shows for cloud projects today.

## Problem

Founder decision, 2026-11-04: every datatug.app project page gets an "Open in terminal" component. Its copy-paste commands install the CLI and run `datatug ui --deep-link <datatug.app address>`. The deep link already opens GitHub and local-agent projects. Cloud projects are stored in Firestore and usually owned by a Space, and they are where teams keep shared work. Today the copied command for one of them only shows a "can't be opened yet" notice.

Two things are missing:

1. **Who is asking.** A GitHub project can be read anonymously or with a GitHub token. A cloud project can only be read by a signed-in Sneat account that the project's Space admits.
2. **How to read it.** datatug.app reads Space projects through backend endpoints that check access on every read (`SharedProjectSummaryService` and `ProjectQueryApiService` in `datatug/datatug-apps`). The terminal UI's project screens read through a `datatug.ProjectStore`, and the only store the CLI has reads files on disk.

The address side is already solved. The deep-link parser (`pkg/deeplink`, being added by the `run-cli` task) already turns both cloud address shapes into a `KindCloud` link.

## What already exists (reused, not rebuilt)

| Piece | Where | Used for |
|---|---|---|
| Parsing a datatug.app address into `deeplink.Link{Kind: KindCloud, SpaceID, ProjectID, Page}` | `pkg/deeplink` (`run-cli` task) | Address handling: this feature writes no parser |
| `runUIDeepLink`, which parses the address before the TUI starts | `apps/datatugapp/commands/cmd_ui.go` (`run-cli` task) | Where sign-in and the first read go |
| `deepLinkResolvers[deeplink.KindCloud]`, which shows `cloudNotice` today | `apps/datatugapp/datatugui/dtproject/deep_link.go` (`run-cli` task) | The seam this feature fills |
| Device-flow sign-in: `datatug auth login`, `status`, `logout`, `whoami` | `pkg/auth/device` (client `datatug-cli`, scopes `openid profile datatug:projects:read datatug:projects:write`) | Signing in, which yields a Firebase ID token that refreshes |
| Credential storage: the OS keyring, or a 0600 file with `--insecure-storage` | The `strongo/deviceauth` store behind `pkg/auth/device` | Where the login lives |
| `device.SavedTokenSource`, which supplies a bearer and refreshes it a minute before expiry | `pkg/auth/device/token_source.go`, already used by `chat --model cloud` | The bearer on every read |
| Cloud API base `https://api.sneat.cloud/v0/` (`defaultAIAPIURLs`), host failover in `pkg/cloudapi`, redirects disabled | `apps/datatugapp/commands/chat_cloud.go` | The base URL and transport |
| Backend admission of CLI tokens on GET reads: product `datatug`, audiences `datatug-cli` and `datatug-web`, scope `datatug:projects:read` | `datatug/datatug-cloud` `internal/accesspolicy/accesspolicy.go`, enforced by `sneat-go-host` `firebasehost.ValidateOAuthAccess` | Why a CLI token is accepted |
| Space project reads, each taking `storage=firestore&spaceID=<space>&project=<project>` | `datatug/backend` `api4datatug/routes.go`: `projects/project_summary`, `queries/all_queries`, `projects/connection_catalog`, `environments/state`, plus `entities/all_entities` with `environment=<env>` | Reading the project |
| Project screens over a `datatug.ProjectStore` | `apps/datatugapp/datatugui/dtproject` (`loadProjectData`, `projectPage`) | Showing the project: only the store is new |

## Behavior

### Opening a cloud link

#### REQ: open-before-tui

For a `KindCloud` link, `runUIDeepLink` MUST do three steps before `runApp` takes the screen: sign in if needed (REQ: sign-in-on-demand), read `projects/project_summary`, and map a failed read to an exit code (REQ: error-mapping). Only after a successful summary does it start the TUI. The cloud resolver in `deepLinkResolvers` then pushes the project page from the summary that was already loaded, together with a store for the remaining reads. Errors that happen after the TUI is up appear in place, and the TUI stays open.

#### REQ: replaces-coming-soon

The cloud resolver MUST take the place of `deepLinkNotice(cloudNotice)` for Space links. GitHub, agent, browser and other-store links MUST resolve exactly as before.

#### REQ: identifiers-checked

Before any network call, the cloud path MUST check `SpaceID` and `ProjectID` against `^[A-Za-z0-9_-]{1,128}$`, the rule of datatug.app's `isSharedProjectIdentifier` and the backend's `ValidateSharedProjectIdentifier`. A link that fails the check exits `2` with one line naming the bad part. Every other rule about the address (hosts, loopback development origins, trailing slash, query string, encoded `/` in a page) is the deep-link parser's and is not repeated here.

#### REQ: cloud-page

`Link.Page` uses datatug.app's page names. When the TUI has a screen for the page, it MUST open on that screen. In this version those pages are the project home (no page, or `overview`), `queries`, and `query/<id>`. A `query` page with more than one segment after `query` (`query/reports/top`) names query `top` in folder `reports`. The encoded form `query/reports%2Ftop` exits `2` from the deep-link parser until that parser accepts it, which is not this feature's change. For any other page the TUI MUST open on the project home with the deep link's existing `noPageScreenNotice`. A page never makes the command fail.

### Sign-in and credentials

#### REQ: sign-in-on-demand

When no login is saved, `runUIDeepLink` MUST start the same device flow as `datatug auth login`, in the same terminal. It prints the verification URL and code, opens a browser when one is available, waits for approval, and then continues to open the project. The person copies one command from datatug.app and ends up in the project. Ctrl+C while waiting exits `1` and saves nothing. With `--no-sign-in`, a missing login instead exits `5` with `Sign in with: datatug auth login`, for scripts.

#### REQ: credential-storage

The login MUST be stored only where `datatug auth login` stores it: the OS keyring by default, or the 0600 plaintext file when `--insecure-storage` is given. `datatug ui` MUST accept `--insecure-storage` and `--auth-host` and pass them through. This feature MUST NOT add a second credential store or cache a bearer anywhere else. It MUST NOT put a token in a URL, argument, log line, telemetry event or error message, and MUST NOT borrow a Sneat web, Google or GitHub credential. The issuer is whichever one `pkg/auth/device` names, and `DATATUG_AUTH_HOST` keeps working for testing.

#### REQ: token-refresh

Every request MUST take its bearer from `device.SavedTokenSource`, so a TUI left open for hours keeps reading. When a refresh is refused, or the backend answers `401`, the TUI MUST show one line, `Your DataTug sign-in expired. Run: datatug auth login`, keep what is on screen, and send no further request for that project.

### Reading the project

#### REQ: reads-through-backend-only

Every read of a cloud project MUST be an authenticated `GET` to a `/v0/datatug/*` endpoint of the DataTug backend, with `Authorization: Bearer <Firebase ID token>`. The CLI MUST NOT link or call a Firestore client SDK, the Firestore REST API, or the Firebase Realtime Database for cloud projects. The backend checks Space membership and project access on every read. The CLI never decides access itself. This follows `rule:writes-go-through-sneat-go-endpoints` (`sneat-co/backstage`).

#### REQ: api-base-url

The base URL MUST be the cloud API host list that `chat --model cloud` uses (`defaultAIAPIURLs`, today `https://api.sneat.cloud/v0/`), with the same `pkg/cloudapi` failover and redirects disabled. Overriding it for development uses a new `--api-url` flag on `datatug ui`. The flag is validated by the existing `validateAIAPIURL` rule: `https`, or `http` only to loopback, with a `/v0/` path. The bearer only ever goes to this base URL and never to the host of the pasted link.

#### REQ: cloud-project-store

The CLI MUST add a read-only cloud store for the `dtproject` screens. It embeds a base that answers every `datatug.ProjectStore` method with `ErrNotAvailableForCloudProject` ("not available for DataTug Cloud projects in the terminal"). It implements only the reads the v1 screens call, and builds the project with `datatug.NewProjectWithStore(id, store)` so `Project.GetDBs` and `Project.GetEnvironments` reach it:

| Read | Endpoint (`storage=firestore&spaceID=…&project=…`) | Conversion |
|---|---|---|
| `LoadProjectFile`, `LoadProject` (id, title, description) | `GET projects/project_summary` | The summary's id, title and description |
| `LoadQueries(folderPath)`, `LoadQuery` | `GET queries/all_queries` | Queries filtered by id from the list, with no per-query request |
| `LoadProjDbDrivers` (behind `Project.GetDBs`) | `GET projects/connection_catalog` | One `ProjDbDriver` per catalog connection, with the item's `ID` set to the connection id (the id REQ: run-queries-locally matches on) and its title to the connection's title. A catalog that comes without an `ETag` is the backend's public fallback, sent when the project has no catalog of its own, and MUST be shown as an empty list |
| `LoadEnvironments` (behind `Project.GetEnvironments`) | `GET environments/state` | One `Environment` per `EnvironmentBrief` (id, title) |
| `LoadEntities`, `LoadEntity` | `GET entities/all_entities&environment=dev` | Entities filtered by id from the list. `dev` is the environment datatug.app reads entities in |

A failing list MUST show in its own panel, as `loadProjectData` already does for a project on disk. Answers are held in memory for the TUI session only. The CLI MUST NOT write a cloud project's definition, queries or rows to disk, and MUST NOT register it as a folder.

#### REQ: recent-cloud-projects

After a successful open, the CLI MUST record the project in its recent projects by datatug.app address and title only (`dtconfig.ProjectRef{ID, Url, Title}`, with no `Path`). Opening it from the projects screen runs the same cloud open path, so the backend checks access again. `datatug projects` MUST label these entries `cloud`.

#### REQ: read-only-in-v1

In this version, every save, rename, delete or create action on a cloud project MUST be disabled with the hint `Edit this project on datatug.app`. When a later version adds edits, they MUST go to the backend's existing write endpoints, which already take `storage=firestore` (`queries/save_query`, `entities/save_entity`), and never to Firestore.

#### REQ: run-queries-locally

Running a cloud project's query MUST use the CLI's existing secure executor against a connection on this machine. The connection is matched to the catalog by connection id. When no local connection has that id, the query panel MUST say `No connection to <database> on this machine. Add one with: datatug db add` and run nothing.

### Personal cloud projects

#### REQ: personal-project-notice

A personal cloud project link (`/store/firestore/project/<id>`, no Space) MUST exit `1` with `Personal DataTug Cloud projects can't be opened in the terminal yet. Move the project to a Space to open it here.` It MUST make no request and MUST NOT fall back to Firestore. The backend's shared reads require `spaceID` (`sharedProjectQueryScope`), and datatug.app reads personal projects straight from Firestore in the browser, which the CLI may not do. Opening personal projects is listed under Not Doing.

### Errors

#### REQ: error-mapping

Failures before the TUI starts MUST exit with the CLI's exit codes ([cli](../README.md)) and print one line that names no token and no internal path:

| Failure | Exit | Line |
|---|---|---|
| Bad space or project id | `2` | which part is invalid |
| `404`, or the summary says the project is missing | `3` | `Project not found, or it was deleted.` |
| Network failure, or `5xx` after failover | `4` | `Can't reach DataTug Cloud: <reason>` |
| `401`, or no login with `--no-sign-in` | `5` | `Sign in with: datatug auth login` |
| `403` | `5` | `<email> has no access to this project. Ask a Space admin to invite you.` |

`5` is used on purpose for "not signed in" as well as for "denied", so a script has one code that means "this account can't read it". `<email>` comes from the saved login's identity (the same source as `datatug whoami`). If the email is unknown, the line starts with `This account`.

## Synopsis

```
datatug ui --deep-link https://datatug.app/space/<spaceID>/store/firestore/project/<projectID>[/<page>…]
      [--no-sign-in] [--insecure-storage] [--auth-host <url>] [--api-url <url>]
```

## Not Doing

- **Opening personal cloud projects.** That needs a new personal-project read in `datatug/backend`. It is a follow-up, specified when the backend read exists.
- **Hosted query runs.** Running a query through the backend's `queries/run_query` when there is no local connection is a separate decision: the route's audience admission and billing.
- **Boards and cards.** The TUI has no board screen, so `boards/all_boards` is not read.
- **Edits.** See REQ: read-only-in-v1.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [ui](../ui/README.md) | Parent surface. This feature adds the cloud branch of `--deep-link`. |
| `--deep-link` (in flight, `run-cli` task) | Owns address parsing, the `runUIDeepLink` entry and the resolver map. This feature adds the cloud open step and resolver, and depends on that work landing first. |
| `auth login` and `whoami` ([cli](../README.md)) | Sign-in and storage are reused unchanged. The unlanded `oauth-host-clients` branch moves the issuer from `auth.sneat.co` to `oauth.sneat.co`. This feature uses whichever issuer `pkg/auth/device` names. |
| [projects](../projects/README.md) | Recent cloud projects are listed with a `cloud` label and no folder. |

## Dependencies

- `run-cli` task in this repository: `pkg/deeplink`, `runUIDeepLink` and `deepLinkResolvers` must land first.
- Deployed routing: `https://api.sneat.cloud/v0/datatug/*` must reach `datatug/datatug-cloud`, whose access policy admits the `datatug-cli` audience. AC: live-smoke checks this. No backend code change is needed for Space projects.

## Acceptance Criteria

### AC: space-link-opens-project

**Requirements:** cloud-projects#req:open-before-tui, cloud-projects#req:replaces-coming-soon, cloud-projects#req:cloud-project-store

**Given** a saved login and a fake backend serving Space `acme` project `sales` with a summary, two queries, a catalog with connection `warehouse`, one environment and one entity
**When** the person runs `datatug ui --deep-link https://datatug.app/space/acme/store/firestore/project/sales`
**Then** the summary is read before the TUI starts, the TUI opens the `sales` project home with its title and lists, `cloudNotice` is not shown, and a GitHub link still opens as before.

### AC: page-opens-screen

**Requirements:** cloud-projects#req:cloud-page

**Given** the same project with query `top` in folder `reports`
**When** the person opens the link with page `/query/reports/top`, then with page `/board/b1`
**Then** the first opens that query's screen, and the second opens the project home with `noPageScreenNotice`. Both exit `0` on quit.

### AC: bad-id-before-network

**Requirements:** cloud-projects#req:identifiers-checked, cloud-projects#req:api-base-url

**Given** a saved login and a recording HTTP transport
**When** the person opens a Space link whose project id is 129 characters long, or whose space id contains `.`
**Then** each exits `2` with one line naming the bad part, and no request is recorded. For a valid link, every recorded request goes to the API base URL and none goes to the link's host.

### AC: sign-in-inline

**Requirements:** cloud-projects#req:sign-in-on-demand, cloud-projects#req:credential-storage

**Given** no saved login and a fake device-flow server that approves the code
**When** the person opens a Space link
**Then** the verification URL and code print before the TUI starts, the login is saved to the keyring store (or to the 0600 file with `--insecure-storage`), and the project opens. With `--no-sign-in`, the same command exits `5` with `Sign in with: datatug auth login`.

### AC: no-firestore-client

**Requirements:** cloud-projects#req:reads-through-backend-only

**Given** the cloud store package and its tests run against a recording transport
**When** `go list -deps` of that package is inspected and the store's reads run
**Then** no `cloud.google.com/go/firestore` or `firebase.google.com/go` database package is a dependency, and every recorded request is a `GET` to `<base>datatug/…` with `Authorization: Bearer`.

### AC: expired-token-refreshes-or-asks

**Requirements:** cloud-projects#req:token-refresh

**Given** an open cloud project whose saved token expires in 30 seconds
**When** the person opens the queries list
**Then** the token refreshes before the request and the list loads. If the refresh is refused instead, the TUI shows `Your DataTug sign-in expired. Run: datatug auth login`, keeps the screen, and records no further request for that project.

### AC: failures-map-to-exit-codes

**Requirements:** cloud-projects#req:error-mapping

**Given** a fake backend whose summary read answers `403`, `404`, `401`, and then refuses connections
**When** the person opens a Space link against each
**Then** the command exits `5`, `3`, `5` and `4` with the lines in the table, the `403` line names the saved login's email, and no line contains the bearer.

### AC: store-conversions

**Requirements:** cloud-projects#req:cloud-project-store

**Given** fake answers for `environments/state`, a project catalog, the public fallback catalog, and `entities/all_entities`
**When** the store's `LoadEnvironments`, `LoadProjDbDrivers` and `LoadEntity` run, and then `SaveQuery`
**Then** the environments and DB drivers carry the briefs' ids and titles, the fallback catalog (no `ETag`) becomes an empty list, the project catalog's drivers carry the connection ids, the entity request carries `environment=dev`, and `SaveQuery` returns `ErrNotAvailableForCloudProject` with no request. This AC shares AC space-link-opens-project's fake backend fixture.

### AC: nothing-written-to-disk

**Requirements:** cloud-projects#req:cloud-project-store, cloud-projects#req:recent-cloud-projects

**Given** a temporary home and config folder
**When** a cloud project is opened and the TUI is closed
**Then** the only file changed is DataTug's settings or state file, with an entry holding the address and title and no `path`. No query text, row or definition file exists under the temporary folders, and `datatug projects` lists the entry labelled `cloud`.

### AC: read-only-actions

**Requirements:** cloud-projects#req:read-only-in-v1

**Given** an open cloud project
**When** the person tries to save, rename or delete a query, or create an entity
**Then** each action is disabled with the hint `Edit this project on datatug.app`, and no non-`GET` request is recorded.

### AC: queries-run-on-local-connection

**Requirements:** cloud-projects#req:run-queries-locally

**Given** a cloud project whose catalog names connections `warehouse` and `crm`, and a machine that has a local connection with id `warehouse` only
**When** the person runs a saved query on `warehouse`, then one on `crm`
**Then** the first runs through the secure executor and shows rows, and the second shows `No connection to crm on this machine…` and runs nothing.

### AC: personal-project-notice

**Requirements:** cloud-projects#req:personal-project-notice

**Given** a recording transport
**When** the person runs `datatug ui --deep-link https://datatug.app/store/firestore/project/p1`
**Then** the command exits `1` with the personal-project line, and no request is recorded.

### AC: live-smoke

**Requirements:** cloud-projects#req:api-base-url, cloud-projects#req:reads-through-backend-only

**Given** the released CLI, a real `datatug auth login`, and a Space project on datatug.app that the account can read
**When** the person runs the command copied from that project's "Open in terminal" component
**Then** the project home opens with the same title and query count that datatug.app shows. This is a manual release check recorded in the PR, not a CI test.

## Rehearse

No Rehearse stubs. Every AC except live-smoke is a Go command or store test with a fake device server and a recording transport, and live-smoke is a manual release check.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
