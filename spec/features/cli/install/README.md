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

`datatug install` MUST use exit code `1` for every operational failure,
matching the simple contract `datatug self-update` already established
([self-update#req:exit-codes](../self-update/README.md#req-exit-codes)):
ambiguous detection, release-lookup, download, checksum, permission,
non-interactive refusal, a managed-command (`brew`) failure, an invalid
`--format`/`--all` usage, and every install-only failure kind
`cli-helpers/cliinstall` adds — `KindUnknownTarget`, `KindNoInstallDir`,
`KindDestinationExists` — all map to `1` via the same `commands.Exit`/
`ExitCoder` mechanism every other datatug command uses. Each of the three
new kinds is mapped in its own explicit switch case
(cli-install#req:host-owned-exit-codes: "MUST map the three new kinds
explicitly... MUST NOT let them fall into a self-update default branch"),
even though datatug's own code for all of them is the same `1`.
`KindUnknownTarget`'s underlying error already names the unknown target and
lists every valid catalog id (cli-install#req:unknown-target-refused) — that
listing is the "usage message" this command reports for it; no separate
usage-error branch is needed.

| Exit code | Meaning |
|---|---|
| `0` | Success: a target installed, already installed, redirected (print-only Homebrew), a declined confirmation, or a dry run |
| `1` | Every operational failure: an unknown target name, a missing per-user bin directory, an already-occupied destination, ambiguous detection, network/download failure, checksum mismatch, permission denied, non-interactive without `--yes`, a failed `brew` command, or an invalid `--format`/`--all` usage |

This is datatug's own choice among the exit-code contracts the library
supports (cli-install#req:host-owned-exit-codes) — the same choice
`datatug self-update` made, for consistency across the two commands rather
than because the library requires it.

## Implementation

Source files implementing this feature (annotated with
`// specscore: feature/cli/install`):

- [`apps/datatugapp/commands/cmd_install.go`](../../../../apps/datatugapp/commands/cmd_install.go) —
  the `installErrors` exit-code mapper and the `cobracmd.New` wiring against
  `HostID: "datatug"`.
- [`main.go`](../../../../main.go) — registers the command where the root is
  built (`getCommand`), alongside `self-update`.

The shared behavior lives upstream, not in this repository:
`github.com/strongo/cli-helpers` `cliinstall/`, `cliinstall/cliui/`,
`cliinstall/cobracmd/` (the library and its Cobra adapter), and
`cliinstall/catalog_datatug.go` (datatug's own catalog entry, shared with
`self-update`).

`upgrade` (cli-install's fleet-wide update verb, `cliinstall/cobracmd`'s
future `upgrade` command) is a separate, later change — not wired by this
Feature.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [self-update](../self-update/README.md) | Shares the same `cliinstall.ByID("datatug")` catalog entry and the same simple "every failure exits 1" exit-code convention; a target's `install datatug` reads datatug's identity through [version](../version/README.md) `--json`. |
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
**Then** the command exits `1`, and the printed error names `nosuchcli` and
lists every valid catalog id, before any confirmation, network request or
write —
[cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md#ac-hosts-keep-their-exit-codes-and-cutover-completes)'s
`install nosuchcli` case, applied to datatug's own exit code.

The remaining behavior — status probing and its bounded, concurrent,
offline probe; the destination policy and denylist; Homebrew cask
execution; checksum verification and no-replace placement; the batch
confirmation gate and `--dry-run`; and machine-readable JSON output — is
specified and tested once in the
[CLI Install Command Library](https://github.com/strongo/cli-helpers/blob/main/spec/features/cli-install/README.md)'s
own Acceptance Criteria, which this command inherits by construction rather
than re-proving.

## Open Questions

- Should `datatug install` gain the fleet-wide `upgrade` command
  (cli-install's `upgrade` amendment) in the same way `self-update` will
  become `upgrade <self>`? Tracked as a separate, later change.
- Should datatug's own exit-code contract eventually reserve a dedicated
  invalid-arguments code for `KindUnknownTarget` and an invalid-state code
  for `KindNoInstallDir`/`KindDestinationExists`, matching the parent
  [CLI](../README.md) feature's wider `2`/`4` exit-code contract, instead of
  folding every install failure into `1`? Left as `1` for now, for
  consistency with `datatug self-update`'s own existing, simpler contract.

---
*This document follows the https://specscore.md/feature-specification*
