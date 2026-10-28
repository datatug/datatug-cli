# Native archive preparation

Both GoReleaser archive definitions retain `README.md` and these six own-source inputs at their original paths:

- `LICENSE`
- `LICENSING.md`
- `LICENSES/Apache-2.0.txt`
- `LICENSES/COMMERCIAL-TERMS.md`
- `THIRD_PARTY_NOTICES.md`
- `pkg/mcpserver/app/build-manifest.json`

These are scope, terms, attribution and provenance records. They do not make the default composite executable Apache-only or grant additional redistribution rights. The existing commercial terms remain **DRAFT — PENDING QUALIFIED LEGAL REVIEW**; dependency licensing and final artifact review remain separate publication gates. The six inputs are unchanged by this preparation.

The native CI intermediate payload contains a target binary and its integrity/commit/tag/version manifest. It is not the final `tar.gz` or `zip`. Final GoReleaser packaging adds the inputs above. The Homebrew cask still references its verified release archive; its binary installation directive does not assert installation or display of every retained text. Neither GoReleaser's ignored cask `license` field nor an invented composite license expression supplies legal clearance.

`TestNativeArchiveOwnSourceInputs` checks both explicit lists and nonempty regular source files. `TestNativeArchiveGoReleaserFixture`, enabled with `DATATUG_GORELEASER_FIXTURE` pointing to the trusted GoReleaser executable, uses real GoReleaser to create both archive formats in private temporary directories. It copies the actual archive definitions into an isolated configuration, replaces compilation with inert fixture bytes, and excludes publishing, hooks, signing and notarization. It verifies exact retained bytes/paths in both formats. Six separate omissions each cause the real archive pipeline to refuse packaging. The fixture targets Linux amd64 (`tar.gz`) and Windows amd64 (`zip`); these target labels do not prove native execution. The binaries are never executed. This is packaging-fixture evidence, not a real native release, signing, Gatekeeper, cask installation or manual acceptance result.

Initial local preparation used GoReleaser 2.18.0. The final archive fixture and source checks also passed with the CI-pinned GoReleaser 2.19.0, downloaded privately and checked against its official checksum; no signature or release identity claim follows. Final native binary/source/platform bindings, full dependency-notice retention, signing/notarization where required, legal review and owner-controlled publication are separate gates. No release, tag, tap, registry or live service is created by these tests.

Configuration references: [GoReleaser archive paths](https://goreleaser.com/customization/package/archives/), [GoReleaser casks](https://goreleaser.com/customization/publish/homebrew_casks/), [pinned 2.19.0 cask schema](https://github.com/goreleaser/goreleaser/blob/v2.19.0/pkg/config/config.go), and [Homebrew Cask Cookbook](https://docs.brew.sh/Cask-Cookbook).
