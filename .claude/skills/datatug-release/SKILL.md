---
name: datatug-release
description: |
  Deploy delegate for specstudio:ship. Confirms the automatic datatug-cli
  main release using exact-commit CI and public artifact delivery receipts.
  Reports explicit success only after publication and native verification.
---

# datatug-release

`specstudio:ship` gates and records lifecycle changes. This delegate verifies the
public release covering the intended landed code. Root lands through WB;
`.github/workflows/release.yml` runs automatically on pushes to main. Do not
manually push main, create a version tag or dispatch `release.yml` here.

## Contract with ship

Print exactly one terminal result:

- `RELEASE_OK <tag>` only after every confirmation below succeeds.
- `RELEASE_FAILED <specific reason>` if proof is absent, pending or failed.

A tag, accepted workflow, green build or draft release alone is insufficient.
Failure leaves the Feature Implementing. This delegate does not edit lifecycle
status or emit `ship.completed`.

`confirm` means verification only. Existing empty/bump/version arguments,
including the configured `minor`, are legacy compatibility inputs: they never
request a tag or a new release. The automatic workflow owns version selection;
an explicit version must match the release being confirmed or fail. Do not
change repository configuration merely to trigger another release.

## Confirm the executed automatic release

1. Verify repository identity and intended remote target SHA through governed WB
   queries. Locate the public release whose tag covers that code; bind every
   receipt to the tag and full commit. For bookkeeping after a release, verify
   the tag is an ancestor and the intervening diff contains only reviewed
   bookkeeping under `spec/`, `.specscore/` or `.claude/`. Any other path needs
   separate release-classification verification; never assume `specscore.yaml`
   is bookkeeping. Confirmation creates no release and installs no update.
2. Require successful **Go CI for the exact release commit** and successful
   automatic release/promotion. `release.yml` delegates to strongo/cicd v1.21.2,
   requires `Go CI`, caps pre-1.0 major bumps and requests notarized macOS builds.
   Inspect authoritative run/check results; a queued/running job is not success.
3. Verify the release is public, download its platform archive and checksum file,
   and match SHA-256 before extracting. `.goreleaser.yaml` verifies
   `go mod tidy -diff` and builds with `CGO_ENABLED=0`. The release tag supplies
   the version; ldflags set `github.com/strongo/buildinfo.version`, `.commit` and
   `.date`, not `main.version`. Check embedded version/commit against the public
   tag and execute the downloaded native binary's version command.
4. For macOS, require strict codesign verification and the CLI-appropriate
   `spctl -a -t install` assessment accepting Notarized Developer ID, together
   with the workflow's signed-artifact gate. Record the actual platform and
   assessment; a non-app execute assessment is not a substitute.
5. Check installed CLI read-only `self-update --check` discovery against the
   public release. Record current/latest versions and actual verdict; an older
   installation reporting `update_available` proves discovery, not installation.
   Reuse existing exact-artifact receipts when their identity remains valid;
   do not repeat broad tests/builds for bookkeeping-only confirmation.
6. Retain the public release URL, exact CI/release runs, checksum, buildinfo,
   native execution/signing and update-discovery receipts, with any terminal
   limits. Only then print `RELEASE_OK <tag>`. Stop at the first missing/failed
   condition and print `RELEASE_FAILED <specific reason>` instead.

## Package distribution is separate

Automatic release skips Homebrew and Winget publication. The separate manual
`publish-packages.yml` downloads the public release, verifies checksums, publishes
its Homebrew cask and reads back the public tap. Its source contract and the
release's cask-install artifact smoke test do not prove the tap was updated for
that release. Claim manual distribution or local Homebrew installation only
with its own execution receipt. Do not dispatch that workflow implicitly.

## Executed reference

DataTug CLI v0.77.0 publicly covers `714a6c161745841075b8c009d61eac88d00167c4`:
[Go CI](https://github.com/datatug/datatug-cli/actions/runs/38456229628),
[automatic release](https://github.com/datatug/datatug-cli/actions/runs/38456230052),
[public assets](https://github.com/datatug/datatug-cli/releases/tag/v0.77.0).
Root verified downloaded darwin/arm64 checksum, native version/buildinfo,
strict signing/notarization and read-only update discovery. No update installation
or separate manual Homebrew publication is claimed by this reference.
