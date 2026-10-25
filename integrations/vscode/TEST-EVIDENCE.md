# VS Code candidate evidence — 2026-10-25

Scope: reversible Task 8 implementation in `integrations/vscode` only. This is
an unpublished candidate, awaiting the orchestrator's independent Astra family
review and CI integration. No commit, push, tag, merge, Marketplace account
change, hosted data upload or release was performed by this lane.

## Local receipts

Host: founder Mac, native Codex Sol 6.1 High. Governed Node runtime:
`wb run -- node --version` → `v22.23.1`. Build syntax/manifest verification:
`wb run -- npm run build` → exit 0.

Clean locked offline install:
`wb run -- npm ci --ignore-scripts --offline --no-audit --no-fund --cache /private/tmp/datatug-vscode-npm-cache-01a17328`
→ exit 0, followed by successful build, all tests, package and archive audit.
The development-only tree emits upstream deprecation notices for `glob`,
`whatwg-encoding` and `prebuild-install`; the advisory audit below is zero and
none of these development packages ships in the VSIX.

`DATATUG_TEST_CLI=/private/tmp/datatug-everywhere-checks-cli wb run -- npm test`
→ exit 0, **21 tests passed, 0 skipped**. This includes real process spawning,
stdout/stderr limits, timeout, cancellation with forced termination, physical
project/binary symlink boundaries, explicit global executable selection, native
binary checks, private evidence permissions, fixed argv and credential-env
restrictions, malicious result text, schema allowlisting, safe fallback URLs,
fake-VS-Code activation/trust/owner-consent/SecretStorage/native-document tests,
and an actual candidate CLI saved-query + Check pass/fail/source-error sequence
against the synthetic SQLite `testdata/checks-demo`.

Tested CLI binary SHA-256:
`6bc2e7fdbe7bb38e6080c7347720193f2d3a656f8f05a2a56983634f6574d933`.
The orchestrator owns source-revision mapping and release of that binary. Tests
create isolated temporary evidence directories and remove their own fixtures;
the production extension retains private evidence.

`npm audit --json --cache /private/tmp/datatug-vscode-npm-cache-01a17328`
→ exit 0, **zero vulnerabilities** across the locked development tool tree.
Runtime dependencies: zero. The pinned packaging tool is `@vscode/vsce 3.7.1`.
Packaging-only overrides pin compatible `@azure/identity 4.13.0` to avoid the
affected newer authentication dependency, and CommonJS `uuid 11.1.1` beneath
`@azure/msal-node` to resolve the older UUID buffer-bounds advisory. Actual
packaging succeeds with these overrides; Marketplace login/publish paths are
not used or certified by this candidate.

`wb run -- npm run package` → exit 0, **9-file VSIX**, approximately 21 KiB.
`wb run -- npm run audit:vsix` → exit 0:

```json
{"files":9,"runtimeDependencies":0,"restrictedAssets":0,"sha256":"3e22b87f20743337b644f8c2f4f0842f8e0d6cd50feb522f9bc286b1e5755f0e"}
```

Candidate output: `dist/datatug-everywhere-0.1.0.vsix` (ignored local build
artifact). The strict allowlist includes only manifest/XML, two extension JS
files, README, security, changelog and Apache licence. It excludes CLI
source/binaries, hosted-app outputs, dependencies, tests, tool scripts, this
evidence note and workspace/customer files. Repackaging may change the ZIP hash;
audit each emitted candidate.

## Actual VS Code extension-host smoke

`wb run -- npm run test:host` → exit 0 using installed
`/Applications/Visual Studio Code - Insiders.app/Contents/MacOS/Code - Insiders`.
The official `--extensionDevelopmentPath` / `--extensionTestsPath` mechanism
loads the extension and its real VS Code API; no editor download or new SDK
dependency is needed. Actual host receipt: VS Code `1.142.0-insider`, app
`Visual Studio Code - Insiders`, host Node `v24.21.0`, Darwin arm64, extension
`datatug.datatug-everywhere`, active, eight registered commands, empty tree and
successful native `datatug.projects.focus` command. It opened no workspace and
executed no CLI. Workspace Trust was enabled (the empty-window workspace is
trusted by VS Code); no authority/trust override or test bypass was used.

The editor ran with unique temporary `--user-data-dir` and `--extensions-dir`,
exited successfully through the test-host mechanism and removed its owned
temporary profile. The normal user profile and installed extensions were never
used or changed. The receipt records `hostExitCode: 0`,
`isolatedProfileRemoved: true`, and `defaultProfileUsed: false`.
`dist/host-smoke.json` retains the bounded receipt. An initial
sandboxed Electron launch aborted before activation; rerunning through the
approved app-launch escalation completed the same isolated smoke successfully.
The aborted launch's owned temporary profile was also removed.
This is actual-host activation evidence, not a fake API assertion. It does not
prove trusted-project UI execution, credentials or remote acceptance.

## Actual trusted-project native UI acceptance — follow-up

Command:
`DATATUG_TEST_CLI=/private/tmp/datatug-everywhere-candidate-final-01a17328 wb run -- node scripts/host-journey.js`
→ final owned host **exit 0**. VS Code Insiders `1.142.0-insider`, host Node
`v24.21.0`, Darwin arm64. The CLI SHA-256 was
`2cf6b53549f1a27431dbcc83b2a80fc83d76f55b2bd8212d816234c1366bceef`.

The launcher copied only the synthetic `testdata/checks-demo` into an owned
temporary folder, wrote the CLI path only to the isolated profile's User
settings, used separate temporary user-data/extensions directories, labelled
its ownership, and imposed a 600-second hard timeout. No normal settings or
extensions were changed. CUA discovery initially opened an empty ordinary
Insiders instance; that owned empty discovery instance was closed through its
native Quit menu before CUA bound to the isolated acceptance host. No user's
open project/editor was used for acceptance.

The first attempt used the official extension-test mechanism, which reached
trust/project/environment selection but refused the modal owner dialog.
Installed VS Code source confirms `DialogService.skipDialogs()` rejects modal
prompts under `--extensionTestsPath`. That attempt was closed and cleaned up;
it is not counted as query/Check acceptance. The final attempt used a disposable
companion driver extension with the actual VS Code API and no test-mode flag.
It did not replace UI APIs, consent responses, trust checks or CLI execution.

CUA performed the actual native actions: explicitly trust only the synthetic
workspace; select its folder and `local` environment; browse saved queries and
Check definitions; select existing host configuration (SQLite needed no
credentials); grant separate modal owner consent for each of three executions;
and explicitly open each native untitled JSON result. The saved query returned
one synthetic row. Check `pass` displayed `status: pass`, `exitCode: 0`; Check
`fail` displayed `status: fail`, `exitCode: 1`. Both Check documents excluded
evidence rows. These are real-host results, not fake API assertions.

After the query, closing its editor caused a driver focus transition to cancel
the next QuickPick. The driver stopped its automated assertions at that point.
Both Checks were then run through actual CUA native tree actions; their native
JSON was read through accessibility, and the bounded receipt explicitly records
`driverFocusTransitionCancelled` and `checksCompletedByManualNativeTreeActions`.
The helper now leaves result tabs open to avoid that transition; a fully
unattended end-to-end driver run is not claimed.

`dist/trusted-project-acceptance/receipt.json` records the real observed journey,
three separate owner-consent actions, unchanged synthetic project digest, two
private evidence directories with mode 0700, host exit 0 and removal of the
owned temporary profile, project and evidence. The companion was only in that
temporary extensions directory. No credentials, hosted upload or Marketplace
publication occurred. Twelve genuine PNG captures are retained locally there;
the final run's useful listing captures are:

- `05-final-run-explicit-trust.png`, `06-final-native-tree.png`
- `07-query-explicit-owner-consent.png`, `08-saved-query-native-json.png`
- `09-pass-check-owner-consent.png`, `10-pass-check-native-json.png`
- `11-fail-check-owner-consent.png`, `12-fail-check-native-json.png`

These ignored local artifacts and test drivers are excluded by the strict VSIX
allowlist. Runtime `src/` was not modified in this acceptance follow-up.
After the acceptance documentation/helper changes, build passed; all 21
unit/process/fake-API/real-CLI integration tests passed with the current candidate
CLI (zero skipped); packaging and the nine-file archive audit passed again. The
VSIX hash above is that rebuilt candidate.

## Evidence limits and open gates

- Actual no-project smoke and the macOS synthetic trusted-project native UI
  journey/screenshots are verified above. Cancellation in the actual UI,
  provider credentials and the broader platform matrix remain pending; fake
  API/process tests do not close those real-host/provider gates.
- macOS real CLI/process tests do not establish Linux/Windows/SSH/dev-container/
  Codespaces acceptance. Windows evidence permission/ACL and cancellation
  behavior require actual testing. README has the matrix.
- Auth-expiry/offline tests use bounded simulated process responses; real
  PostgreSQL/cloud/provider credentials and remote SecretStorage transport were
  not exercised. Existing CLI descriptor/env credential mechanisms were verified
  in source, including `dsnEnv`, `env:NAME`, and pgx `PGPASSWORD` handling.
- The earlier candidate's constant home fallback remains available. The summary
  adapter now has a separate explicitly consented, scoped summary handoff, with
  unit and actual CLI-process evidence recorded below. Actual VS Code summary
  consent/SecretStorage-to-hosted success and denial, independent hosted sign-in
  and current-grant isolation remain acceptance gates. No public readiness or
  hosted-authority claim follows from the process tests.
- Screenshots, publisher entitlement, listing validation, independent review,
  Stage 1 gates, legal interpretation and explicit publication approval remain
  separate prerequisites. No publication claim is made.

## Minimal summary adapter follow-up — 2026-10-25

The separate `datatug.runCheckWithSummary` command now requires the existing
local-owner consent plus one-run minimal publication consent. Space binding and
hosted token storage/deletion are separate native commands. Ordinary Check runs
load no hosted SecretStorage token and add no upload flags; the child environment
removes an inherited `DATATUG_SUMMARY_TOKEN`. Summary runs use the fixed token
variable, never token argv, and expose no API URL setting or input. Their CLI
invocation retains the 30-second execution/publication deadline with one second
of supervisor flush allowance and no retries.

Current verification:

- `wb run -- npm run build` passed syntax and manifest/runtime boundary checks.
- `DATATUG_TEST_CLI=/private/tmp/datatug-everywhere-summary-01a17328/datatug
  wb run -- npm test` passed all 30 tests, zero skipped. These include actual
  built-CLI SQLite query and Check pass/fail/error processes, no-upload default,
  scrubbed inherited token and missing-auth opt-in, plus a test-owned loopback
  403 endpoint proving uploaded minimal field allowlists and unchanged local
  pass/fail/exit codes. The extension-generated args contain no API override;
  only the integration fixture appends the CLI's explicit loopback test option.
  The sandbox refused the first listener bind (`EPERM`); the focused and final
  suites passed under approved escalation. Every listener and temporary private
  evidence directory was closed/removed by test teardown.
- Native API tests cover separate Space/SecretStorage boundaries, token deletion,
  owner/trust/upload cancellation before spawn/auth, exact command registration,
  safe explicit opening only, and bounded denial diagnostics. Link tests reject
  wrong hosts, credentials, ports, paths/scopes, query strings, noncanonical
  base64url/32-byte IDs, unknown/extra fragment fields and Markdown-like input.
- `wb run -- npm run package` and `wb run -- npm run audit:vsix` passed. The
  unpublished 0.1.0 candidate has nine archive files, zero runtime dependencies,
  and zero restricted-asset matches. No dependency or lockfile change was made.
  SHA-256: `f19d6bbedafe6f8aaaadaa890e1828e8e50a26e51188a9356fe8c9cf68302c62`.
- Independent native Codex Sol (6.1) review r1 found one minor stale acceptance
  statement; fix r1 corrected the scope. Review r2 returned
  `VERDICT: blockers=0 majors=0 minors=0 land=yes`.

This does not establish the actual new VS Code summary UI/SecretStorage-to-hosted
journey. That gate still requires real per-run user consent, independent hosted
sign-in and current grant, a successful summary page, and hosted denial/isolation
acceptance using owner-controlled fixtures. Earlier macOS native query/Check
journey receipts remain historical evidence for their earlier candidate. No new
summary screenshot, hosted entitlement or public release is claimed here.

## Workspace invalidation fix — Astra review r4 follow-up, 2026-10-25

A workspace-folder change now increments an invalidation epoch before clearing
selection and cancelling registered children. Every execution captures that
epoch before its first asynchronous selection step. Pending selections and
refreshes cannot restore stale state after a folder change. Execution checks
the epoch after async consent, credential and setup steps, physically revalidates
the captured project after final setup, then checks epoch/trust inside the
progress callback immediately before calling `runCLI`, with no intervening
await. The fixed failure text contains no project/credential values.

Regression tests use controlled promise barriers and real temporary filesystem
containment validation. They remove or replace the selected root while a
separate trusted root remains during summary consent and hosted SecretStorage
retrieval, and assert no spawn or token delivery. Further tests cover pending
query/default Check consent even when the original root is re-added, delayed
progress callbacks for all three execution commands, the post-setup containment
check order, and cancellation of an already registered running child.

`wb run -- npm run build` passed. The full suite with the actual candidate CLI
passed 43 tests, zero skipped, including all earlier actual CLI integration
cases. `wb run -- npm run package` and `wb run -- npm run audit:vsix` passed:
nine files, zero runtime dependencies and zero restricted-asset matches.
The rebuilt unpublished 0.1.0 VSIX SHA-256 is
`f56c92dd3a44e63ef6a8d1c679520b0fa44373b3995a34b93342e7e74fe8737a`.
Independent Astra review r5 is owned by the parent lane and remains pending;
this fix does not close the actual summary UI/hosted acceptance gate above.
