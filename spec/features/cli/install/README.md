---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Install

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/install?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/install?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/install?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/install?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug install` is built entirely on the fleet-wide
[CLI Install Command Library](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md)
(`github.com/strongo/cli-helpers/cliinstall`, Implementing), configured from
datatug's own compiled-in catalog entry
(cli-install#req:host-identity-from-catalog). Listing, details, the
destination policy, the confirmation gate, checksum verification, atomic
placement, `--dry-run` and machine-readable JSON output are all specified
once in that library, not restated here; this Feature specifies only
datatug's own configuration and exit-code contract.

`datatug install` lists the fleet CLIs relevant to a DataTug user —
`ingitdb`, `ovdb` and `specscore`, in that order
(cli-install#req:relevance-matrix) — each with its installed status, a
one-line description and why it helps someone using DataTug. `datatug
install <name>...` shows fuller details and installs them the same way
`datatug` itself was installed: by `brew install --cask` on a Homebrew host
whose target publishes a cask for the host OS, otherwise a verified direct
release download placed beside a manual `datatug` install or in the
per-user bin directory.

datatug had no `install` command before this Feature.

## Problem

DataTug reads inGitDB databases directly and connects to OpenVaultDB
servers as `openvaultdb` catalogs; its own specifications are SpecScore
artifacts. Nothing inside the `datatug` binary told a user those sibling
tools exist, let alone offered a safe, consistent way to install them.
Building that from scratch would repeat the same catalog, status-probe,
destination-policy and download/verify/place machinery the shared
`cliinstall` library and its `cobracmd` Cobra adapter already provide for
every other fleet CLI.

## Behavior

### Command surface

#### REQ: command-name

The CLI MUST expose the command as `datatug install`, built from
`github.com/strongo/cli-helpers/cliinstall/cobracmd`. It has no alias. The
command inherits the library's flag surface — `--all`, `--yes`/`-y`,
`--dry-run`, `--dir`, `--format text|json` — none of which is re-specified
here.

### Catalog configuration

#### REQ: catalog-configured-identity

`datatug install` MUST identify the host by `cliinstall.ByID("datatug")`
only (cli-install#req:host-identity-from-catalog), the same catalog entry
`datatug self-update` builds its `selfupdate.Config` from
([self-update#req:catalog-configured-identity](../self-update/README.md#req-catalog-configured-identity)),
so a target's `install datatug` and `datatug self-update` resolve releases
identically. `cliinstall.ByID("datatug")` being absent from the compiled
catalog is a programming error caught by this package's own tests
(`cobracmd.New` panics), never a runtime state a user sees.

Per the fleet [relevance matrix](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-relevance-matrix),
`datatug`'s relevant targets, in listing order, are `ingitdb`, `ovdb` and
`specscore`.

### Exit codes

#### REQ: exit-codes

`datatug install` MUST map every failure onto the parent [CLI](../README.md)
spec's shared exit-code contract, matching the contract `datatug
self-update` already established
([self-update#req:exit-codes](../self-update/README.md#req-exit-codes)),
through the SAME shared `failureExitCode` function
(`apps/datatugapp/commands/cmd_exit_codes.go`): an invalid `--format`/
`--all` usage and `KindUnknownTarget` map to `2` (invalid arguments);
`KindNoInstallDir` and `KindDestinationExists` map to `4` (connection/I/O
failure); every kind shared with self-update (ambiguous detection,
release-lookup, download, checksum, permission, non-interactive refusal, a
managed-command failure) maps identically to how self-update maps it. Each
of the three new kinds is mapped in its own explicit switch case inside
`failureExitCode`
(cli-install#req:host-owned-exit-codes: "MUST map the three new kinds
explicitly... MUST NOT let them fall into a self-update default branch").
`KindUnknownTarget`'s underlying error already names the unknown target and
lists every valid catalog id (cli-install#req:unknown-target-refused) — that
listing is the "usage message" this command reports for it; no separate
usage-error branch is needed.

| Exit code | Meaning |
|---|---|
| `0` | Success: a target installed, already installed, redirected (print-only Homebrew), a declined confirmation, or a dry run |
| `1` | Generic catch-all: ambiguous detection, checksum mismatch, or a failed `brew` command |
| `2` | Invalid arguments: an unknown target name, or an invalid `--format`/`--all` usage |
| `4` | Connection/I/O failure: a missing per-user bin directory, an already-occupied destination, or a network/download failure |

This is datatug's own choice among the exit-code contracts the library
supports (cli-install#req:host-owned-exit-codes), aligned with the parent
[CLI](../README.md) spec's own standard codes — the same choice `datatug
self-update` made, through the same shared function, so the two commands
can never disagree.

## Implementation

Source files implementing this feature (annotated with
`// specscore: feature/cli/install`):

- [`apps/datatugapp/commands/cmd_install.go`](../../../../apps/datatugapp/commands/cmd_install.go) —
  the `installErrors` exit-code mapper and the `cobracmd.New` wiring against
  `HostID: "datatug"`.
- [`apps/datatugapp/commands/cmd_upgrade.go`](../../../../apps/datatugapp/commands/cmd_upgrade.go) —
  the `upgradeErrors` mapper (embeds `installErrors`, adds
  `UpgradesAvailable`) and the `cobracmd.NewUpgrade` wiring against the same
  `HostConfig` `self-update` builds.
- [`apps/datatugapp/commands/cmd_exit_codes.go`](../../../../apps/datatugapp/commands/cmd_exit_codes.go) —
  `failureExitCode`, the one shared mapping install, upgrade and self-update
  all route through.
- [`main.go`](../../../../main.go) — registers both commands where the root
  is built (`getCommand`), alongside `self-update`.

The shared behavior lives upstream, not in this repository:
`github.com/strongo/cli-helpers` `cliinstall/`, `cliinstall/cliui/`,
`cliinstall/cobracmd/` (the library and its Cobra adapter), and
`cliinstall/catalog_datatug.go` (datatug's own catalog entry, shared with
`self-update`).

### Upgrading

#### REQ: upgrade-command

The CLI MUST also expose `datatug upgrade [name...] [--all] [--check]
[--yes] [--dry-run] [--format text|json]`, built from
`cliinstall/cobracmd.NewUpgrade` against the same `HostID: "datatug"` and
the exact same `HostConfig` `datatug self-update` builds
(`datatugSelfUpdateConfig`, [`cmd_self_update.go`](../../../../apps/datatugapp/commands/cmd_self_update.go));
datatug's self-update has no after-update hook, so `upgrade` passes none
either. `datatug self-update` MUST therefore be `datatug upgrade datatug`
by construction, not by convention
([cli-install#req:self-update-equals-upgrade-self](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-self-update-equals-upgrade-self)).
`upgrade --all` means every *installed* catalog id plus datatug itself, not
the relevance matrix `install` lists — a target that is merely relevant but
not installed has nothing to upgrade. `upgrade` gets no `update` alias
([cli-install#req:update-alias-policy](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-update-alias-policy)).

`datatug upgrade` MUST use the same `upgradeErrors` exit-code mapper as
`install` (it embeds `installErrors` unchanged, so every failure kind
routes through the same `failureExitCode` and maps identically) with one
addition: `UpgradesAvailable` returns `nil`, mirroring `self-update`'s own
`UpdateAvailable`
([self-update#req:exit-codes](../self-update/README.md#req-exit-codes)) —
datatug reserves no distinct exit code for "an upgrade is available" for
either command, so `datatug self-update --check` and
`datatug upgrade --check`/`datatug upgrade datatug --check` exit `0` for
the same verdicts.

| Exit code | Meaning |
|---|---|
| `0` | Success, including a report showing an available upgrade |
| `1` | Generic catch-all: ambiguous detection, checksum mismatch, or a failed `brew` command |
| `2` | Invalid arguments: an unknown upgrade target, or an invalid `--format`/`--all` usage |
| `4` | Connection/I/O failure: a release-lookup or download failure |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [self-update](../self-update/README.md) | Shares the same `cliinstall.ByID("datatug")` catalog entry and the same `failureExitCode` exit-code mapping (parent CLI spec's `2`/`3`/`4`/`1` contract); a target's `install datatug` reads datatug's identity through [version](../version/README.md) `--json`. |
| [version](../version/README.md) | Other fleet CLIs' `install datatug` identifies an installed `datatug` build through `datatug version --json`, per [cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#ac-datatug-installs-ovdb-and-ovdb-sees-datatug). |
| [CLI](../README.md) (parent) | `install` is an ordinary command for telemetry purposes — only `version --json` is exempt from the parent's `REQ: telemetry-events`. |

## Acceptance Criteria

### AC: canonical-name-and-relevant-targets

**Requirements:** cli/install#req:command-name, cli/install#req:catalog-configured-identity

**Given** an installed `datatug` binary with none of `ingitdb`, `ovdb` or
`specscore` installed
**When** the user runs `datatug install`
**Then** it lists exactly `ingitdb`, `ovdb` and `specscore`, in that order,
each marked not installed, with its one-line description and its relevance
to a DataTug user — the shared behavior of
[cli-install#req:list-relevant](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#req-list-relevant),
offline and read-only
([cli-install#ac:listing-is-offline-and-read-only](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#ac-listing-is-offline-and-read-only)).

### AC: exit-code-contract

**Requirements:** cli/install#req:exit-codes

**Given** an installed `datatug` binary
**When** the user runs `datatug install nosuchcli`
**Then** the command exits `2` (invalid arguments), and the printed error
names `nosuchcli` and lists every valid catalog id, before any
confirmation, network request or write —
[cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#ac-hosts-keep-their-exit-codes-and-cutover-completes)'s
`install nosuchcli` case, applied to datatug's own exit code.

### AC: upgrade-exit-code-contract

**Requirements:** cli/install#req:upgrade-command

**Given** an installed `datatug` binary
**When** the user runs `datatug upgrade nosuchcli`
**Then** the command exits `2` (invalid arguments) before any release
lookup, the same way `datatug install nosuchcli` does; and when the user runs
`datatug self-update --check` and `datatug upgrade datatug --check` against
the same release, both exit `0` for the same verdict — up to date, an
update available, or undetermined — because both reach the exact same
`selfupdate.Config.Check` call.

The remaining behavior — status probing and its bounded, concurrent,
offline probe; the destination policy and denylist; Homebrew cask
execution; checksum verification and no-replace placement; the batch
confirmation gate and `--dry-run`; upgrade's own target selection, release
lookups and per-target policy; and machine-readable JSON output — is
specified and tested once in the
[CLI Install Command Library](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md)'s
own Acceptance Criteria, which this command inherits by construction rather
than re-proving.

## Open Questions

- Resolved: `datatug install`, `upgrade` and `self-update` now all route
  through the shared `failureExitCode` (`cmd_exit_codes.go`), which follows
  the parent [CLI](../README.md) feature's own `2`/`3`/`4`/`1` exit-code
  contract — `KindUnknownTarget` maps to `2` and
  `KindNoInstallDir`/`KindDestinationExists` map to `4` — rather than
  folding every failure into `1`.

---
*This document follows the https://specscore.md/feature-specification*
