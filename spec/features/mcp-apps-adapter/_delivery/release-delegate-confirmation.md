# DataTug release delegate confirmation

Result: RELEASE_OK v0.113.0. Legacy argument `minor` interpreted as confirmation only.

Confirmed source: 4e30fd76afdc9c51bb14242e0ab17b804dd212b9. Governed WB read-only remote queries verified origin git@github.com:datatug/datatug-cli.git and both remote main and v0.113.0 equal this exact SHA. Local worktree HEAD 01adea1557e2b0fc5445c835fda5908bd8dd53e6 descends from that release; intervening file names are exclusively spec/ and .specscore/ bookkeeping. Parent supplies independent reviewed-bookkeeping acceptance; no functional release gap found.

Fresh authoritative GitHub queries via wb run verified:
- Go CI https://github.com/datatug/datatug-cli/actions/runs/40038670317 completed success at the exact source SHA.
- Release https://github.com/datatug/datatug-cli/actions/runs/40038670940 completed success at the exact source SHA.
- Signed macOS artifact and promotion job succeeded, including Verify signature, notarization, and execution. Darwin smoke test's signing/notarization step succeeded. Public-hub publishing and tag finalization jobs succeeded.
- https://github.com/datatug/datatug/releases/tag/v0.113.0 is published, non-draft, non-prerelease; the hub repository visibility is public. The archive and checksum assets are present.

Reused exact downloaded artifacts under /private/tmp/datatug-mcp-apps-release-v0.113.0 per delegate contract. Fresh SHA256 verification: darwin/arm64 archive 09b318a71aeb6244e979b30e56f799e0b02fd1d1a86f150976a147fe1f551e1f matches checksum file. Binary 28fff92c9d54a5aa43bc05b42695657774ebdcb4579b34055b49a534e1ff8e00 matches acceptance receipt and the archive member. Fresh native version command returned datatug 0.113.0 (4e30fd76afdc9c51bb14242e0ab17b804dd212b9) 2026-10-23T23:27:48Z. Existing buildinfo records strongo/buildinfo version, commit and date ldflags and CGO_ENABLED=0.

Fresh strict codesign verification and spctl -a -t install -vv succeeded outside the restricted sandbox: valid on disk, designated requirement satisfied, accepted, source=Notarized Developer ID, origin=Developer ID Application: Alexander Trakhimenok (NFKJ9BRHE7). Initial sandbox signing inspection produced subsystem/signature errors; identical commands through approved native host execution succeeded, establishing the actual platform result.

Reused existing read-only update-discovery receipt: installed 0.102.2, latest 0.113.0, update available. This proves discovery; no update was installed. Existing complete-acceptance and native-acceptance receipts bind it to this public release. Runtime browser receipt, browser log and plain MCP log all retain exact SHA256 hashes from complete-acceptance.json; checked freshly. All 17 browser scenarios and plain MCP acceptance are retained there; broad tests were not rerun.

Source release workflow uses strongo/cicd v1.22.0, requires exact-commit Go CI, disallows pre-1.0 major bumps, and requires notarized macOS. Goreleaser verifies go mod tidy -diff and uses CGO_ENABLED=0 and strongo/buildinfo ldflags.

No build, deploy, tag creation, workflow dispatch, installation, configuration change, Feature lifecycle write, or ship.completed event was performed. Homebrew/Winget publication is not claimed. Restricted-network read queries initially failed DNS; approved read-only host queries then succeeded.
