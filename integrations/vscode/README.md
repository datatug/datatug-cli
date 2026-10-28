# DataTug Everywhere — VS Code preview

Browse saved queries and Check definitions in the native Explorer, choose an
environment explicitly, run the existing DataTug CLI, and open bounded JSON in
an untitled editor when requested. The full visual experience lives at
[DataTug.app](https://datatug.app). This extension supplies no webview, database
engine, background daemon or runtime dependency. No telemetry is collected by
the extension; CLI telemetry is disabled for its invocations.

This is an **unpublished implementation candidate**. It requires the candidate
CLI containing `checks run` documented in `docs/checks-run.md` in the parent
repository. A public CLI release with that contract, Stage 1 publication gates,
independent security review, remaining platform/provider acceptance, listing validation,
publisher ownership verification and explicit release approval remain pending.
The `datatug` publisher identifier is proposed metadata, not proof of account
access or an existing Marketplace listing. No pricing or paid-plan entitlement
is asserted.

## First run

1. Install a reviewed native CLI on the **extension execution host**, outside
   every workspace folder. Set **DataTug: Cli Path** in **User** settings to its
   absolute path. The extension ignores workspace settings and PATH discovery,
   rejects script wrappers and binaries physically inside the workspace, and
   never downloads or updates a binary.
2. Open and trust a filesystem workspace. Run **DataTug: Select Project** and
   select a directory inside that workspace containing `datatug-project.json`.
   Choose an environment, even if only one exists.
3. Expand **DataTug → Saved queries / Checks** or use the matching command.
   Choose the credential profile for this invocation, or explicitly use existing
   host configuration. Grant the separate modal local-owner consent. Workspace
   trust does not grant that execution authority.
4. See progress and cancel if necessary. A Check's pass, fail, warning, unknown,
   access-denied or infrastructure status remains distinct. Choose **Open JSON
   results** to view output. Query results may contain customer data. The editor
   can be saved or backed up by VS Code; follow your normal data-handling policy.

**DataTug: Refresh Project** reloads the bounded metadata listing. Selecting a
different project clears the environment selection. Workspace folder changes
clear the project, invalidate every pending execution and cancel active work.
A remaining trusted folder does not preserve a removed project selection; select
the project and environment again. Only one execution runs at a time.
There are no automatic retries. Changes to project files are trusted local
inputs; execution uses current CLI files rather than a snapshot.

## Credentials and connection profiles

Use **DataTug: Store Connection Profile Secret** to store a named credential in
VS Code `SecretStorage`. Only the profile UUID, display name and environment
variable name enter extension global state, without Settings Sync registration.
Values never enter settings, arguments, output channels or notifications.
**DataTug: Delete Connection Profile Secret** removes the value and profile.
Rotate an expired credential by deleting the old profile and storing a new one.

The verified existing CLI path resolves saved SQL/TugQL catalog connections
through connection descriptors and `env:NAME`; PostgreSQL descriptors name
`dsnEnv`. A profile can provide a whole connection URL via an uppercase variable
ending `_URL` (for example `SHOP_PG_URL`), matching that **existing descriptor**.
The extension does not generate or edit source configuration. It also permits
`PGPASSWORD` for the PostgreSQL driver's default credential mechanism; an
explicit password in a full source URL can take precedence. Driver authentication
is delegated to the installed CLI. This preview does not add device login,
cloud token refresh, arbitrary environment injection, principal/role selection,
multi-secret profiles, input parameters or HTTP secret headers. Existing host
credential configuration remains available to the child process. Profiles are
selected for each invocation, not automatically trusted from workspace content.

`SecretStorage` uses VS Code's credential service. Its backing store and remote
transport belong to VS Code, not this extension. Verify them on the actual host
before using production credentials. The secret is passed to the CLI environment
on the selected local/remote extension host; users with authority to inspect that
process can inspect its environment. Never share that host with untrusted users.

## Limits and supported contracts

- A native Node extension host is required (`extensionKind: workspace`); browser
  only VS Code and virtual workspaces are unsupported.
- Listings use the existing `queries/**/*.query.json`, `checks/**/*.json` and
  `environments/*/environment-summary.json` layouts. Metadata files are limited
  to 256 KiB, listing depth to eight folders and entries to 3,000. Symbolic links
  in these listings are refused. Preview identifiers use letters, numbers,
  underscores and hyphens, with `/` for query folders. Other identifiers need a
  future compatibility extension.
- Queries invoke `query run --project PATH --query ID --env ENV --format json
  --quiet`. The CLI retains its existing policy enforcement. The modal consent
  explicitly covers its no-policy local-owner fallback. This preview does not
  supply a named policy principal; projects requiring one must use a supported
  existing client until that interface is added.
- Checks invoke `checks run --project PATH --env ENV --check ID --local-owner
  --evidence-dir PRIVATE --timeout 30s --warnings fail --format json`. Only
  definitions without input declarations are supported. Secured Check profiles
  remain refused by the CLI; policies are never deleted or bypassed. Bare
  interactive `checks` is never invoked.
- The process uses fixed arguments with `shell: false`, ignored stdin, at most
  30 seconds and 1 MiB of combined stdout/stderr. An explicitly opted-in summary
  run allows one additional second for the CLI to flush its bounded JSON after
  its own 30-second execution/publication deadline. Output above the limit is
  discarded and execution terminated. Cancellation sends termination then a
  bounded force kill. Results are JSON text, without executable links or HTML.
  Check output is allowlisted and excludes raw evidence, SQL, definitions, IDs,
  source values and errors. Query rows appear only after an explicit editor-open
  action. Raw errors and stderr are withheld; check credentials, connectivity,
  access policies and project configuration on the execution host when a run
  fails. No OutputChannel is created.
- Private Check evidence is retained in new mode-0700 directories below VS Code
  `globalStorageUri`, physically outside all workspace roots. It is never placed
  in source control or automatically uploaded. Preserve evidence after an
  uncertain execution; remove it privately when no longer needed. The extension
  deliberately does not delete that evidence automatically.

## Hosted handoff

Ordinary **DataTug: Run Check** stays local. Publishing requires the distinct
**DataTug: Run Check and Publish Minimal Summary** command for each invocation.
First use **DataTug: Bind Hosted Summary Space** to enter an explicit Space ID,
then **DataTug: Store Hosted Summary Token** to place a current hosted bearer
token in VS Code SecretStorage. The Space binding is nonsecret local extension
state; it grants no hosted authority. **DataTug: Delete Hosted Summary Token**
deletes the token on this execution host while retaining the Space binding.
Connection profiles and hosted tokens have separate storage keys and validators.
No token is stored in settings, the workspace, arguments, results or logs.

After local-owner consent, the summary command asks for separate one-run upload
consent naming the Space, actual manifest project ID and selected environment.
It sends only identifiers/version, execution status, outcome, severity, duration
and execution time, with seven-day retention. SQL, rows, input values, error text
and private evidence remain on the execution host. It invokes the tested CLI
flags `--publish-summary --summary-space SPACE --summary-token-env
DATATUG_SUMMARY_TOKEN`; the token enters only that child's named environment
variable. Inherited `DATATUG_SUMMARY_TOKEN` is removed from every invocation.
The extension exposes no API URL override and uses the CLI's fixed trusted
production default. It performs no refresh or automatic retry.

A denied, offline or incompatible optional publication preserves the local
Check status and JSON and shows a fixed bounded explanation. A successful
publication offers **Open summary in DataTug.app** as a separate explicit action.
Only the exact supported route scoped to the chosen Space, actual project ID
and selected environment is accepted, with a closed canonical fragment holding
version 1, environment and a canonical 32-byte summary ID. Other URLs, fragment
fields and scopes are refused. Hosted sign-in, Space access and the current
hosted grant remain independent in DataTug.app; the local token or link grants
no authority to the reader.

**DataTug: Open Hosted Home** retains its accurate local-context fallback and
opens only `https://datatug.app` after explaining the limitation. It does not
upload or claim to open a project/query/Check/result. The new summary adapter's
actual VS Code-to-hosted success/denial journey and hosted permission-isolation
acceptance remain release gates; unit and CLI process tests are separate evidence.

## Host compatibility and evidence

| Host | Execution / credentials | Evidence |
| --- | --- | --- |
| macOS local | CLI and workspace on Mac; SecretStorage API | Historical earlier-candidate Node/real CLI SQLite tests and VS Code Insiders 1.142.0 no-project/native synthetic query/Check journey are recorded, including three separate owner consents and basic JSON. This is not current summary/password/SecretStorage acceptance; those manual/provider gates remain deferred |
| Linux local | Native CLI on Linux; SecretStorage API; backing-store/transport acceptance remains separate | On 2026-10-26, the [b5 Everywhere run](https://github.com/datatug/datatug-cli/actions/runs/40404519557) and [da0 run](https://github.com/datatug/datatug-cli/actions/runs/40407664356) passed all nine jobs. Exact CLI79 [Linux no-project activation](https://github.com/datatug/datatug-cli/actions/runs/40412007550/job/121884799905) and [candidate package](https://github.com/datatug/datatug-cli/actions/runs/40412007550/job/121884800146) jobs also passed. Retained da0 artifact 12692155065 records official VS Code 1.143.1/Linux x64 activation, 12 registered commands, empty tree/native view and profile cleanup, without trust bypass or CLI execution. That artifact retains its da0 source scope; unchanged runtime/workflow bytes do not turn it into a newly inspected79 artifact. Interactive trusted-workspace, password/consent, SecretStorage and hosted/provider acceptance remain deferred |
| Windows local | Native `.exe`; host credential service | Pending, including force termination and private evidence ACL/mode compatibility |
| Remote SSH | Workspace extension + CLI on remote host; secret explicitly transmitted to remote child environment | Pending real-host transport, secret-store and cancellation tests |
| Dev container | Extension and CLI inside container; private persistent storage outside workspace | Pending container lifecycle/storage tests |
| Codespaces | Extension and CLI on Codespace host, not browser | Pending actual Codespaces activation, trust and secret-store tests |
| Browser-only/virtual workspace | No supported native execution host | Unsupported |

The actual macOS host smoke uses the installed editor's official
`--extensionDevelopmentPath` / `--extensionTestsPath` mechanism with isolated
temporary user-data and extension directories. It opens no workspace and does
not disable Workspace Trust. The owned host exits and its profile is removed;
the normal user profile is untouched. Its receipt is `dist/host-smoke.json`.
This no-project acceptance is distinct from fake-API unit tests. A second actual
host acceptance uses a copied synthetic project and a disposable companion
driver extension, without `--extensionTestsPath`: VS Code intentionally refuses
modal consent dialogs in test mode. CUA explicitly trusted only that temporary
project, selected its environment, granted three separate local-owner consents,
and opened the real saved-query and Check pass/fail JSON. A driver focus
transition cancelled one QuickPick after the query; both Checks were completed
through genuine native tree actions, with their JSON read through native
accessibility. The receipt records that manual continuation. The owned host
exited 0; the copied project was unchanged, and its two mode-0700 private
evidence directories and temporary profile were removed. Genuine screenshots
and the bounded receipt are in `dist/trusted-project-acceptance/`. This establishes
the macOS synthetic journey, not remote-host or real-provider acceptance.
Credential-provider, auth expiry and offline negative tests currently
cover simulated responses/process failures; real-provider acceptance is pending.

The dated [Backstage continuation record](https://github.com/datatug/backstage/blob/51c4e04bc6445d917ca9722cc6dfaab55a1c27b9/spec/features/datatug-everywhere/_evidence/continuation-preparation-2026-10-26.json) binds b5/da0/79 source, run and artifact scopes. See the 2026-10-27 addendum in `TEST-EVIDENCE.md` for the full refs, historical test counts and unchanged-byte limits. No editor, provider or hosted acceptance was rerun for this documentation correction.

## Build, test, package and inspect

No runtime dependencies or compilation are required. Node 20+ is needed for the
development scripts. The lockfile pins the packaging tool and its dependencies.

```sh
npm ci --ignore-scripts
npm run build
npm test
DATATUG_TEST_CLI=/absolute/path/to/reviewed/candidate/datatug npm test
npm run test:host
npm run package
npm run audit:vsix
```

`dist/datatug-everywhere-0.1.0.vsix` is an unpublished candidate; install only
after review. Packaging uses the pinned `@vscode/vsce` and `--no-dependencies`.
`test:host` defaults to the installed macOS VS Code Insiders; set
`DATATUG_VSCODE_TEST_EXECUTABLE` to an explicit installed executable on another
host. It downloads no editor. The archive audit requires the `unzip` utility.
The strict archive audit accepts only extension JS, manifest, README, security,
changelog and licence. It rejects unexpected payloads and restricted UI asset
references; no CLI binaries/source, hosted-app output, development dependencies
or customer metadata belong in the VSIX. Review the package hash with each
candidate. Rollback is to uninstall this preview and reinstall the previously
reviewed VSIX, retaining private evidence only as needed. The CLI is independently
installed and versioned.

Genuine macOS screenshots now cover restricted/explicit trust, native project
and query/Check tree, environment choice, three owner consents and query/Check
pass/fail JSON. Before submission validate listing assets and capture remaining
cancellation/provider/platform evidence. Marketplace account validation is
pending; do not substitute mock screenshots for host acceptance. Publication requires
the initiative's separate Stage 1, licensing, security and owner approval gates.

## Support and privacy

Report reproducible issues through [the public DataTug community issue tracker](https://github.com/datatug/datatug/issues).
Do not attach credentials, source URLs, private results, project files or Check
evidence. For a security report follow the repository's private GitHub security
reporting channel where available; avoid public disclosure of customer data.
The extension collects no install/activation/query/Check analytics. Marketplace
install counts and provider analytics are not available to this unpublished
candidate. The available local outcome/progress feedback is user-facing only.

Implementation references: [VS Code manifest](https://code.visualstudio.com/api/references/extension-manifest),
[Workspace Trust](https://code.visualstudio.com/api/extension-guides/workspace-trust),
[packaging](https://code.visualstudio.com/api/working-with-extensions/publishing-extension).
