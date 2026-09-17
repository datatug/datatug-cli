---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Self-Update

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/self-update?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/self-update?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/self-update?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/self-update?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug self-update` is built entirely on the fleet-wide
[Self-Update Library](https://github.com/strongo/cli-helpers/blob/main/spec/features/self-update/README.md)
(`github.com/strongo/cli-helpers/selfupdate`, Stable), configured from
datatug's own compiled-in catalog entry in
[`cliinstall`](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md)
(cli-install#req:host-identity-from-catalog). Detection (managed vs. manual
vs. ambiguous), the confirmation gate, checksum verification, atomic
replace, version pinning, `--dry-run`, and Homebrew cask execution are all
specified once in that library, not restated here; this Feature specifies
only datatug's own configuration and exit-code contract.

datatug had no self-update command before this Feature.

## Problem

Every CLI that ships binaries eventually needs to update itself: resolve the
latest release, verify its checksum, and swap the binary in place without
fighting a package manager that already owns the install. datatug ships
release archives and a Homebrew cask (`datatug/tap/datatug`) but had no way
to apply an update itself — a user had to notice a new release, download it
by hand, and re-place it next to the running binary, or run `brew upgrade`
themselves without the CLI ever confirming the swap actually landed.

Building this from scratch would repeat the same install-method detection,
download-verify-swap machinery, and confirmation/refusal rules every other
fleet CLI's self-update already carries — exactly the class of duplication
the shared Self-Update Library and its compiled-in catalog exist to close,
by making datatug's release identity (repository, asset naming, the
Homebrew cask) one typed value shared with every other fleet CLI that needs
to know it (in particular, any other catalog CLI's `install datatug`).

## Behavior

### Command surface

#### REQ: command-name

The CLI MUST expose the command as `datatug self-update`, built from
`github.com/strongo/cli-helpers/selfupdate/cobracmd`. It has no `update`
alias: an alias was planned before this Feature shipped but was dropped
before release
([cli-install#req:update-alias-policy](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-update-alias-policy):
"datatug's planned `update` alias, never released, MUST NOT ship"). The
command inherits the library's flag surface — `--check`,
`--yes`/`-y`, `--version`, `--allow-downgrade`, `--dry-run` — none of which
is re-specified here. `--format` is not registered: datatug's self-update
does not (yet) offer machine-readable output.

### Catalog configuration

#### REQ: catalog-configured-identity

datatug's `self-update` MUST build its `selfupdate.Config` from its own
`cliinstall.ByID("datatug")` catalog entry
(cli-install#req:host-identity-from-catalog,
cli-install#req:catalog-identity-single-source), never from hand-written
identity values, so its self-update and every other fleet CLI's
`install datatug` resolve releases identically. The entry declares:

- Repository `datatug/datatug-cli`, no tag prefix.
- The default GoReleaser asset and checksums naming (no override), matching
  `.goreleaser.yaml`'s `archives[].name_template`
  (`{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}`) and
  `checksum.name_template`
  (`{{ .ProjectName }}_{{ .Version }}_checksums.txt`) exactly — no pipeline
  change was needed to add this command.
- `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and
  `windows/amd64` as the supported platforms, matching the union of
  `.goreleaser.yaml`'s `datatug-unix` and `datatug-windows` build ids.
- One manager, Homebrew, configured **executable** via
  `selfupdate.HomebrewCask("datatug")` — unlike a redirect-only manager, the
  library actually runs `brew update && brew upgrade --yes --cask -- datatug`
  as structured argv after confirmation, then verifies the swap by probing
  the installed binary. Cask token `datatug/tap/datatug`, matching
  `.goreleaser.yaml`'s `homebrew_casks[0]`.

A consumer test (`TestDatatugCatalogEntry_MatchesGoReleaserConfig`) asserts
the catalog entry's archive/checksum naming, release repository, platform
matrix, and cask token match `.goreleaser.yaml`, so drift fails this
repository's own CI rather than surfacing as a broken download or a stale
cask token for a user.

### Exit codes

#### REQ: exit-codes

`datatug self-update` MUST use exit code `1` for every operational failure
— ambiguous detection, release-lookup, download, checksum, permission,
non-interactive refusal without `--yes`, a managed-command (`brew`)
failure, or an invalid usage — via the same `commands.Exit`/`ExitCoder`
mechanism every other datatug command uses to signal a specific process
exit code. `--check` MUST exit `0` whether the binary is up to date, an
update is available, or the running version is undetermined: datatug
reserves no distinct exit code for "update available" — the printed
`--check` verdict line is the only signal, and `UpdateAvailable` returns
`nil` rather than a failure.

| Exit code | Meaning |
|---|---|
| `0` | Success: self-replace completed, the Homebrew cask command ran successfully, or already up to date; also `--check` reporting any verdict, including an available update |
| `1` | Every operational failure: ambiguous detection, network/download failure, missing OS/arch asset, checksum mismatch, permission denied, non-interactive without `--yes`, unknown `--version` tag, a refused downgrade, or a failed `brew` command |

This is datatug's own choice among the exit-code contracts the library
supports (cli-install#req:host-owned-exit-codes) — there being no
pre-existing self-update command, there was no prior contract to preserve.

cliinstall's three install-only failure kinds (`KindUnknownTarget`,
`KindNoInstallDir`, `KindDestinationExists`) cannot reach this command:
datatug declares no `install` command yet. They will get their own explicit
mapping when `install` is added.

## Implementation

Source files implementing this feature (annotated with
`// specscore: feature/cli/self-update`):

- [`apps/datatugapp/commands/cmd_self_update.go`](../../../../apps/datatugapp/commands/cmd_self_update.go) —
  the catalog lookup, the `selfUpdateErrors` exit-code mapper, and the
  `cobracmd.New` wiring.
- [`main.go`](../../../../main.go) — registers the command where the root
  is built (`getCommand`), against the running build's own version
  (`buildinfo.Get("datatug").Version`).

The shared behavior lives upstream, not in this repository:
`github.com/strongo/cli-helpers` `selfupdate/`, `selfupdate/cliui/`,
`selfupdate/cobracmd/` (the library and its Cobra adapter), and
`cliinstall/catalog_datatug.go` (datatug's own catalog entry).

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [version](../version/README.md) | `self-update`'s running version is datatug's own build identity (`buildinfo.Get("datatug").Version`), the same value `version --json` reports. |
| [CLI](../README.md) (parent) | `self-update` is exempt from the parent's `REQ: telemetry-events` only for `version --json`, per that spec's amendment; `self-update` itself still emits the ordinary CLI started/exited events. |

## Acceptance Criteria

### AC: canonical-name-no-alias

**Requirements:** cli/self-update#req:command-name

**Given** an installed `datatug` binary
**When** the user runs `datatug self-update --check`
**Then** it runs the self-update check, and `datatug update` is not a
recognized command (no `update` alias ships).

### AC: catalog-identity-matches-releases

**Requirements:** cli/self-update#req:catalog-configured-identity

**Given** the compiled-in `cliinstall.ByID("datatug")` catalog entry and this repository's `.goreleaser.yaml`
**When** `TestDatatugCatalogEntry_MatchesGoReleaserConfig` runs offline
**Then** the entry's checksum naming, archive naming, release repository, supported platform matrix, and Homebrew cask token equal what `.goreleaser.yaml` actually publishes, and the Homebrew manager is executable (not redirect-only).

### AC: check-exit-code-contract

**Requirements:** cli/self-update#req:exit-codes

**Given** three scenarios — up to date, update available, and a release-lookup error
**When** the user runs `datatug self-update --check` in each
**Then** the exit codes are `0`, `0`, and `1` respectively: only the release-lookup failure is a non-nil error, mapped to exit `1`.

The remaining behavior — install-method detection and its safe-ambiguous
default, the confirmation gate and non-interactive refusal, checksum
verification and atomic replace, version pinning and the downgrade guard,
`--dry-run`, and the executable Homebrew cask command sequence — is
specified and tested once in the
[Self-Update Library](https://github.com/strongo/cli-helpers/blob/main/spec/features/self-update/README.md)'s
own Acceptance Criteria, which this command inherits by construction rather
than re-proving.

## Open Questions

- Should `datatug self-update` gain `--format json` (`JSONFormat: true` on
  `cobracmd.CommandOptions`), matching the fleet's other machine-readable
  surfaces, or does datatug have no near-term scripted-self-update use
  case that would justify it?
- Resolved: datatug gained the shared `install` command
  (cli-install#req:fleet-cutover) — see [install](../install/README.md),
  which maps the three install-only failure kinds explicitly.

---
*This document follows the https://specscore.md/feature-specification*
