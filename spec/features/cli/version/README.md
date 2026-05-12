# Feature: Version

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli%2Fversion) — graph, discussions, approvals

**Status:** Planned

## Summary

`datatug version` and `datatug --version` (with its `-v` short alias) report the CLI's build identity. The subcommand prints the version, commit, and build date on a single line for humans and bug reports. The flag prints only the bare semver so scripts, installers, and CI gates can consume it without parsing. Today the CLI does NOT wire up either surface; this spec pins the target contract.

## Synopsis

```
datatug version
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
| `datatug --version` / `-v` | Scripts, installers, CI | `<version>` |

#### REQ: subcommand-output

`datatug version` MUST print a single line of the form `datatug <version> (<commit>) <date>`. `<version>` is the bare semver (see [REQ: no-v-prefix](#req-no-v-prefix)). `<commit>` is the full git commit SHA the binary was built from. `<date>` is the build timestamp in RFC 3339 / ISO 8601 form. The line MUST end with a single trailing newline.

#### REQ: flag-output

`datatug --version` MUST print only the bare semver on a single line, terminated by a newline. The output MUST NOT include the program name, the commit, the build date, or any other decoration. A caller MUST be able to consume the output with `$(datatug --version)` and receive exactly the version string.

#### REQ: short-flag

`-v` MUST be accepted as a short alias for `--version` and MUST produce identical output.

#### REQ: no-v-prefix

The `<version>` field MUST NOT carry a leading `v`. `0.11.0` is correct; `v0.11.0` is not. This holds on both surfaces. The `v` prefix MAY remain on git tags and release filenames; those are outside the scope of CLI output.

### Build-time value injection

#### REQ: ldflag-injection

The three values MUST be injected via Go linker flags against package-level `var` symbols (location TBD by the implementer; conventionally `apps/datatugapp` or `apps/global`):

```
-X <pkg>.version=<semver>
-X <pkg>.commit=<full-sha>
-X <pkg>.date=<rfc3339>
```

A release build MUST supply all three.

#### REQ: default-placeholders

When the binary is built without `-ldflags`, the three fields MUST fall back to literal placeholders: `version="dev"`, `commit="none"`, `date="unknown"`. The CLI MUST NOT error on missing version information.

A `dev` binary therefore prints:

- `datatug --version` → `dev`
- `datatug version` → `datatug dev (none) unknown`

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
| Telemetry (parent [REQ: telemetry-events](../README.md#req-telemetry-events)) | The injected version SHOULD be included in PostHog events for support correlation; the spec for that wiring lives in the telemetry feature (not yet written). |

## Acceptance Criteria

### AC: surfaces-agree

**Requirements:** version#req:subcommand-output, version#req:flag-output, version#req:short-flag

`datatug version`, `datatug --version`, and `datatug -v` all report the same version string (with different surrounding context). The flag surfaces print only the bare semver; the subcommand adds commit and date in parentheses.

### AC: scripting-friendly-flag

**Requirements:** version#req:flag-output, version#req:no-v-prefix

`$(datatug --version)` yields a single bare semver with no prefix, no program name, no commit, no trailing whitespace beyond a single newline.

### AC: dev-build-works

**Requirements:** version#req:default-placeholders

A `datatug` binary built without `-ldflags` exits `0` and prints `dev` for the flag surface and `datatug dev (none) unknown` for the subcommand surface.

## Outstanding Questions

- Which Go package owns the injected `version`/`commit`/`date` vars? Candidates: `apps/global`, `apps/datatugapp`, or a new `apps/datatugapp/buildinfo`.
- Should the build pipeline (`.github/workflows/...`) be updated in the same change that lands the version command, so release builds actually inject the values?
- Should `datatug version` gain a `--json` mode for machine consumption, parallel to [specscore-cli's open question on the same](https://github.com/synchestra-io/specscore-cli/blob/main/spec/features/cli/version/README.md#outstanding-questions)?

---
*This document follows the https://specscore.md/feature-specification*
