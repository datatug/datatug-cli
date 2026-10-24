# Feature and Plan review r1

Status: Issues Found
Pinned personally verified SHA256: Feature03261599a5c41f59c6335edb65d002376f14c8b887637c4f2e1e109ff986d566; Plana35c1680168a080177aaa6582e91f35f8ad89cfd4d6cbaa385a1bc2866832aad. Paths: spec/features/mcp-apps-adapter/README.md and spec/plans/mcp-apps-adapter/README.md in WB CLI worktree. Source approved design618daeed6d667d20066f63acafcbad70236bd5db641c99e6c0431bd4f569d875; CLI428847f1f962e56475e72ffb8cd6e79cbed5289d; ai-plugin41a2c10252c490da4788edac075866c88bca0b43.

BA: Explorer requirements address stated private result problem; scope stays within adapter and upstream precision dependency.
Developer: Two privacy/session ambiguities could produce incorrect implementation and require correction below.
QA: Ten REQs have referenced observable scenarios and Rehearse omission rationale; privacy scenario currently promises an impossible property for model-supplied inputs and negotiation needs adversarial mutation coverage.

Blockers, following repository specify-reviewer taxonomy:

1. Feature31 and91-93, privacy requirement/scenario: no parameters may enter model context and no sentinel parameters may appear in transcripts, including initial execution. Initial model-visible datatug_run_query takes model-authored parameters, which necessarily are already model-visible. This absolute requirement contradicts retained initial invocation contract. Scope adapter nondisclosure to returned envelopes and UI-entered continuation parameter values; acknowledge model-supplied initial arguments are already visible. Test distinct initial arguments versus UI-only secret sentinels. Category: architecture/requirements contradiction and vague REQ. Disposition: notified author, pending.
2. Feature27/67 and negotiation AC83-85; Plan Task2: mode is called per-session but wrapper relies on SDK InitializeParams without explicitly snapshotting first real connection/session negotiation and refusing later request metadata mode changes. SDK1.8.0 server.go1942-2004 permits per-request sessionless protocol metadata and initializes session state from it. Require one immutable mode snapshot per existing stdio connection/session; later supplied capabilities/context cannot flip legacy/private in either direction, including tools/call/resource paths. Add adversarial per-request metadata case to negotiation AC and Task2 tests. No new transport support needed. Category: vague security REQ. Disposition: notified author, pending.

No additional majors/minors. Plan tasks cover all ten ACs, sequence upstream reviewed source before vendor, and preserve parent landing/release ownership. Plan cannot pass while source Feature requires changes; refresh source lifecycle only after independent Feature pass.

Limits: read-only artifact/source review. No implementation, executable test or host compatibility claims. No commits, settings changes, builds or daemons. Verified SDK behavior by source inspection only.

VERDICT: blockers=2 majors=0 minors=0 land=no
