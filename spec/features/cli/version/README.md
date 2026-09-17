---
format: https://specscore.md/feature-specification
status: Planned
---

# Feature: Version

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/version?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/version?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/version?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/version?op=request-change) |

**Status:** Planned
**Source Ideas:** —

## Summary

`datatug version` and `datatug --version` (with its `-v` short alias) report the CLI's build identity. The subcommand prints the version, commit, and build date on a single line for humans and bug reports. The flag prints only the bare semver so scripts, installers, and CI gates can consume it without parsing. `datatug version --json` prints one JSON object for machine consumption — the fleet-wide probe flag [cli-install](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md) uses to identify installed CLIs, including datatug itself when another fleet CLI's `install` lists or verifies it. Both surfaces are wired today via `github.com/strongo/buildinfo` (`buildinfo.Get("datatug")`) and `buildinfo/fangcmd.Wire`, which also supplies `--json`.

## Synopsis

```
datatug version
datatug version --json
datatug --version
datatug -v
```

## Problem

Users, install scripts, and support workflows need a reliable way to identify which `datatug` binary is running. Without two pinned surfaces:

- Humans get a terse, context-free flag output, OR
- Scripts have to parse a multi-field line.

Pinning both keeps humans and scripts from stepping on each other. The convention here mirrors [specscore version](https://github.com/synchestra-io/specscore-cli/blob/main/spec/features/cli/version/README.md), `go version`, `gh --version`, and `kubectl version --client --short`.

## Behavior

### Two output surfaces

| Surface | Audience | Output shape |
|---|---|---|
| `datatug version` | Humans, bug reports, support | `datatug <version> (<commit>) <date>` |
| `datatug version --json` | Other fleet CLIs' `install`/status probes, scripts, agents | one JSON object, `buildinfo.VersionJSON` |
| `datatug --version` / `-v` | Scripts, installers, CI | `<version>` |

#### REQ: subcommand-output

`datatug version` MUST print a single line of the form `datatug <version> (<commit>) <date>`. `<version>` is the bare semver (see [REQ: no-v-prefix](#req-no-v-prefix)). `<commit>` is the full git commit SHA the binary was built from. `<date>` is the build timestamp in RFC 3339 / ISO 8601 form. The line MUST end with a single trailing newline.

#### REQ: json-output

`datatug version --json` MUST print exactly one JSON object to stdout and nothing else, and exit `0` — the fleet-wide `version --json` contract
([cli-install#req:version-json-contract](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-version-json-contract)).
`--json` is the fleet's probe flag, independent of any other output-format
flag a command might offer; `datatug version` has none today. The object is
exactly `buildinfo.VersionJSON` (`name`, `version`, `commit`, `date`,
`date_source`), written by `buildinfo/cobracmd.VersionCommand`, which
`buildinfo/fangcmd.Wire` reuses — the same code path as the plain-text
`version` subcommand, so the two surfaces cannot disagree. `name` MUST equal
the catalog id `"datatug"`.

#### REQ: json-output-side-effect-free

`datatug version --json` MUST NOT perform network I/O, write or create
files, run update checks, or enqueue telemetry — including the
`DataTug CLI started`/`DataTug CLI exited` events every other invocation
enqueues (parent [REQ: telemetry-events](../README.md#req-telemetry-events)) —
so that probing an installed `datatug` build (by another fleet CLI's
`install`, a script, or an agent) is safe to repeat
([cli-install#req:version-json-side-effect-free](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-version-json-side-effect-free)).
`main.go` resolves this exemption through cobra's own command/flag
resolution (matching `version` with `--json` set) before deciding whether to
enqueue telemetry, so the check can never disagree with what actually runs.

#### REQ: flag-output

`datatug --version` MUST print only the bare semver on a single line, terminated by a newline. The output MUST NOT include the program name, the commit, the build date, or any other decoration. A caller MUST be able to consume the output with `$(datatug --version)` and receive exactly the version string.

#### REQ: short-flag

`-v` MUST be accepted as a short alias for `--version` and MUST produce identical output.

#### REQ: no-v-prefix

The `<version>` field MUST NOT carry a leading `v`. `0.11.0` is correct; `v0.11.0` is not. This holds on both surfaces. The `v` prefix MAY remain on git tags and release filenames; those are outside the scope of CLI output.

### Build-time value injection

#### REQ: ldflag-injection

The three values MUST be injected via Go linker flags against
`github.com/strongo/buildinfo`'s package-level `var` symbols — the fleet-shared
package, not a datatug-local one — exactly as `.goreleaser.yaml` already does:

```
-X github.com/strongo/buildinfo.version={{.Version}}
-X github.com/strongo/buildinfo.commit={{.FullCommit}}
-X github.com/strongo/buildinfo.date={{.Date}}
```

A release build MUST supply all three. `main.go` reads them via
`buildinfo.Get("datatug")` and wires both output surfaces (and `--json`)
from that one resolved `buildinfo.Info` through `buildinfo/fangcmd.Wire`.

#### REQ: default-placeholders

When the binary is built without `-ldflags` (and `runtime/debug.BuildInfo` cannot resolve a field either — see [REQ: runtime-debug-fallback](#req-runtime-debug-fallback)), the three fields MUST fall back to literal placeholders: `version="dev"`, `commit="unknown"`, `date="unknown"` — `buildinfo.Info.Long()`'s own fallback shape. The CLI MUST NOT error on missing version information.

A `dev` binary therefore prints:

- `datatug --version` → `dev`
- `datatug version` → `datatug dev (unknown) unknown`
- `datatug version --json` → `{"name":"datatug","version":"dev","commit":"","date":"","date_source":""}`

#### REQ: runtime-debug-fallback

If a future implementation chooses to read embedded `runtime/debug.BuildInfo` data when `-ldflags` are not supplied, the fallback MUST NOT report a `(devel)`-style Go-default string on `datatug --version`. The output MUST be either an injected semver or the literal `dev`.

## Parameters

None. Neither the subcommand nor the flag accepts arguments.

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Success (always) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| Telemetry (parent [REQ: telemetry-events](../README.md#req-telemetry-events)) | The injected version SHOULD be included in PostHog events for support correlation; the spec for that wiring lives in the telemetry feature (not yet written). `version --json` is the one command exempt from telemetry entirely — see [REQ: json-output-side-effect-free](#req-json-output-side-effect-free). |
| [self-update](../self-update/README.md) | `self-update`'s running version is the same `buildinfo.Get("datatug").Version` this feature's surfaces report. Another fleet CLI's `install datatug` reads datatug's identity through `version --json`, per [cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#ac-datatug-installs-ovdb-and-ovdb-sees-datatug). |

## Acceptance Criteria

### AC: surfaces-agree

**Requirements:** version#req:subcommand-output, version#req:flag-output, version#req:short-flag

`datatug version`, `datatug --version`, and `datatug -v` all report the same version string (with different surrounding context). The flag surfaces print only the bare semver; the subcommand adds commit and date in parentheses.

### AC: json-probe-is-uniform-and-quiet

**Requirements:** version#req:json-output, version#req:json-output-side-effect-free

**Given** a released `datatug` build and telemetry configured with no network access
**When** `datatug version --json` runs
**Then** stdout is exactly one `buildinfo.VersionJSON` object whose `name` is `"datatug"`, the process exits `0`, and neither the `DataTug CLI started` nor `DataTug CLI exited` PostHog event is enqueued.

### AC: scripting-friendly-flag

**Requirements:** version#req:flag-output, version#req:no-v-prefix

`$(datatug --version)` yields a single bare semver with no prefix, no program name, no commit, no trailing whitespace beyond a single newline.

### AC: dev-build-works

**Requirements:** version#req:default-placeholders

A `datatug` binary built without `-ldflags` exits `0` and prints `dev` for the flag surface and `datatug dev (unknown) unknown` for the subcommand surface.

## Open Questions

Resolved by this amendment: the injected vars live in `github.com/strongo/buildinfo` (not a datatug-local package), `.goreleaser.yaml` already injects them, and `--json` is now specified (see [REQ: json-output](#req-json-output)) rather than open.

---
*This document follows the https://specscore.md/feature-specification*
