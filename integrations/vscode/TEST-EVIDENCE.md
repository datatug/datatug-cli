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
{"files":9,"runtimeDependencies":0,"restrictedAssets":0,"sha256":"ef87509b8f384e2bf28a77cba8f9c0c6a10bb8ae5fff9ffbeeeaca8807e3f8f3"}
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

## Evidence limits and open gates

- Actual no-project activation/native-view smoke is verified above. Trusted
  project/environment/owner-consent/query/Check UI acceptance, genuine
  screenshots and full end-user acceptance remain pending. Fake API tests cover
  those boundaries only at unit level.
- macOS real CLI/process tests do not establish Linux/Windows/SSH/dev-container/
  Codespaces acceptance. Windows evidence permission/ACL and cancellation
  behavior require actual testing. README has the matrix.
- Auth-expiry/offline tests use bounded simulated process responses; real
  PostgreSQL/cloud/provider credentials and remote SecretStorage transport were
  not exercised. Existing CLI descriptor/env credential mechanisms were verified
  in source, including `dsnEnv`, `env:NAME`, and pgx `PGPASSWORD` handling.
- The only hosted handoff is an explicitly explained constant home fallback.
  Task 17's authorised tenant/project/environment/result handoff, its permission
  negatives, and corresponding publication gate remain pending. No invented URL
  or assertion of hosted result synchronisation is present.
- Screenshots, publisher entitlement, listing validation, independent review,
  Stage 1 gates, legal interpretation and explicit publication approval remain
  separate prerequisites. No publication claim is made.
