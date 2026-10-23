---
format: https://specscore.md/features-index-specification
---

# MCP Apps delivery evidence

This is the durable evidence index for the [private MCP Apps saved query explorer](../README.md). Evidence was collected on 2026-10-23 and updated on 2026-10-24. CLI delivery and public macOS arm64 acceptance passed. Website publication and direct live acceptance now passed as recorded below. Website full post-merge CI remains pending in the captured observation. This evidence-only addition does not change feature, task or plan lifecycle status.

## Released identity

The implementation repository is `datatug/datatug-cli`; PR467 landed at `89d260fdfe00f3b0345adae451258443d2cd00a7`. The public distribution repository is `datatug/datatug`, tag [v0.112.0](https://github.com/datatug/datatug/releases/tag/v0.112.0). The downloaded `datatug_0.112.0_darwin_arm64.tar.gz` SHA256 is `bf688e927e1c9171d3ca9f59b7372a319384ce1af0fc9cc925ab10100d0b6b02`; extracted CLI SHA256 is `875af63bef87a6c3ce38f2651c0a41a5b2e26a82da3f966ba71892d1f42eb400`. Its version/build identity names that exact CLI commit, CGO is disabled, and native code-signature/notarization assessment passed. The public hub's target_commitish alone was not used as source evidence.

The maintained contributor harness source is `1d28aed057ee4a5000c1d5d9ad3ec835304baf45`; browser demo fixture is `08cc4d367e33cca82269bff54dc8a35b242e9937`. The separate plain MCP smoke used fixture `5d22712c2ea6a288d294576c4b76a6bd82b2e536`. Embedded HTML SHA256 is `8a9b9d77b710e2c4430436c5784627083c07a1bc1f83dd7b37b6ee0821f541ab`. The [54-file source manifest](source-reviewed-54-files.json), [source CI](source-ci-1d28aed0.json), [exact main checks](main-89d260fd-checks.json) and [complete release acceptance](release-v0.112.0-complete-acceptance.json) bind these stages separately.

## Actual acceptance and limits

The downloaded public binary passed all17 maintained official App/AppBridge browser scenarios on local macOS arm64 in headless Chromium, plus real plain MCP child acceptance. Browser receipt records real saved SQLite SQL counts14 then17, default5→7→cleared5, empty result, five views, sorting/paging/record copy, mixed typed values, native positive/denied clipboard,320px single toolbar, dark/light/keyboard validation, history bound, safe previous rows, timeout/teardown cancellation before barrier release, source/origin and CSP denial, no Node in CLI child PATH, no private projection/console/stderr leakage, clean child exit and zero active calls. Node is contributor host/test tooling; the supplied CLI was never rebuilt by this harness.

Exact decimal/integer sort/copy precision uses a separate core-valid renderer fixture. It does not establish a SQLite arbitrary-precision decimal storage guarantee. The upstream precision reviews and exact vendor identity remain separately auditable. This is a developer-preview maintained reference host acceptance, not a claim that ordinary stdio client configuration renders Apps, that ChatGPT accepts direct stdio, that official basic-host npmstart supports stdio, or that any named customer desktop host/package manager/other platform was tested. No tunnel or persistent remote runtime is part of this scope. The existing installed CLI was not upgraded by this acceptance run.

The [integrated source receipts](integration-29b72f3-receipts.json) preserve actual race results with100% statements in queryparams, mcpserver and endpoints, and a fresh CGO-disabled integrated CLI's17-scenario browser pass. They also record the byte-identical relevant Corev0.77→v0.78 contracts. These historical source results remain distinct from the later public binary acceptance.

## Executed contributor recipe

[contributor-acceptance-recipe.sh](contributor-acceptance-recipe.sh) is an exact byte copy of the executed literal contributor recipe, SHA256 `a4f91f9ba88a7098acdd7ff765ea42fcab145af1d048200f54c4e88117537315`. Required environment inputs are accepted source revision, demo revision, absolute source checkout, absolute downloaded binary, and absolute demo checkout. It installs test dependencies and invokes the maintained test:browser entrypoint; it does not rebuild the supplied binary. Source access/private Go modules, Go, Git, Node/npm and pinned Playwright Chromium are contributor prerequisites.

The first invocation safely refused an existing canonical demo checkout at a different revision. That checkout was preserved; the successful retry used a clean WB-isolated pinned fixture. The complete acceptance record retains this negative receipt and the actual successful recipe/plain-MCP log hashes. No raw rows, cells, handles, credentials or execution logs are copied into this evidence folder. Boolean/count/hash receipts and original review text are retained.

## Historical reviews and portable records

Original reviews are copied verbatim, including earlier failed/pending verdicts. Later passing reviews supersede findings only within their stated scope; historical land=no or pending text is not rewritten. Final design/spec/backend/UI/Task5/head/lint/CI-repair/precision/public-release reviews are present alongside the earlier rounds. Earlier website design/copy/recipe reviews remain preparation evidence; the separate landing/deployment/live receipts below establish the later publication observation. [provenance.json](provenance.json) maps every copied file to original machine provenance, byte count and SHA256. Essential verdicts and acceptance receipts are local relative links below, so reading them does not require the original temporary files.

| Evidence | SHA256 |
|---|---|
| [integration-4e30fd76-ci.json](integration-4e30fd76-ci.json) | `41739ade87718ea7c113c058c538807c2735edfc7779dc45e800b9fe06c9286b` |
| [review-design-r1.md](review-design-r1.md) | `0498eb57d34a844703c4079ea85f3523d87d44b24d03a9fbd84e8f719149a9f3` |
| [review-design-r2.md](review-design-r2.md) | `16800d50f3f866c3a1c00d4178f186e74a62dff0ca185de9cecda50041ef06e4` |
| [review-spec-r1.md](review-spec-r1.md) | `4e12d254a81a4fb190abad2b3a7cb72404387146046be82a0870a0260dd8886c` |
| [review-spec-r2.md](review-spec-r2.md) | `22b6f333b52545ec1771e7a152ae076d58e53d96867eff068258d300fc2bef78` |
| [review-spec-r3.md](review-spec-r3.md) | `d32025364bd4f2f50e0d724bac677efcc98312074367e349b41d1edacb8d677d` |
| [review-spec-r4.md](review-spec-r4.md) | `9a26c6580e0b4c27b08dbba76f2713cfc363bfbeebd93fe7489c13f65595f3bc` |
| [review-task2-r1.md](review-task2-r1.md) | `c99f4a34d41bc0bc8edd6b69b8da11abdf6017a01f382df7b888f338de702e3a` |
| [review-task3-r1.md](review-task3-r1.md) | `a9968ba9323ba035306e6cb4d43f886c41752e5bfb25ea472c608e67b5844f8e` |
| [review-task3-r2.md](review-task3-r2.md) | `3b221d0973a4850ef0ec3c3f4675ded7859a43b22966e37cd6d729dde93cdc1b` |
| [review-task4-r1.md](review-task4-r1.md) | `6430bdb50c8312d5e5cafd9ffd85ddf5ffe15897af9331dece48b9aef740fafa` |
| [review-task4-r2.md](review-task4-r2.md) | `6a9d1cacbdf1ac3ee3fbd6ae603d35054f1a28f0524019868d6e4d5c09972109` |
| [review-task5-design-r1.md](review-task5-design-r1.md) | `0130ee1e5983eb8acac5719997e6e8e8d676e36e789a800ebee8dd3549d6a6a3` |
| [review-default-design-r1.md](review-default-design-r1.md) | `5ca67af37d4331a6649f8fe565175b77a4e4f7208b228744cf1f80aeeb2bc7c2` |
| [review-default-design-r2.md](review-default-design-r2.md) | `b1883df6176d6830d3bd2e90ce1ef847d3af6f68a1e7592220711732341e71cd` |
| [review-task5-source-r1.md](review-task5-source-r1.md) | `055d2f3748530a177096d906829ac62dd84ea9c535db08697f62c907b5a46ee2` |
| [review-task5-source-r2.md](review-task5-source-r2.md) | `4aa31f2c1d155c8f565c717728f323ab4920fd2b58974512f5398d24ef3625a0` |
| [review-lint-repair-r1.md](review-lint-repair-r1.md) | `575dbad99ea6ad05e11a3c4860eca7e203545248f7eb3f69b08dbaacad4d6411` |
| [review-ci-repair-r1.md](review-ci-repair-r1.md) | `b895fabdb6ce2a6da5a14584e3d3404e9799d1204758d54047518e16ec9475ff` |
| [review-precision-r1.md](review-precision-r1.md) | `a8eb9e37d1a64c004aea705416c7f87c2ea620032684b1246cb55070b81a561e` |
| [review-precision-r2.md](review-precision-r2.md) | `87ba499a5317e466c3a3bff1f4f845b2165319c6378eae179eac57e24febf236` |
| [review-public-release-r1.md](review-public-release-r1.md) | `c421749f2fce9be2382157f0298686d81fb09961d86969252d424b1699c8b14f` |
| [review-public-release-r2.md](review-public-release-r2.md) | `a41f0440943ec1f66d77487113f2290e215d5bcf6ab15e7fb1663fc36002d9cb` |
| [review-website-design-r1.md](review-website-design-r1.md) | `2aae4a3e0792d6248a9f6bf9799da92d5940e31d4f2e0e950de4e5fc3ecf636b` |
| [review-website-design-r2.md](review-website-design-r2.md) | `7088b2ad4b6af097284c0210f439dcd21a8b938cd9c9f82cb3ef10c2d2762b2f` |
| [review-web-recipe-r2.md](review-web-recipe-r2.md) | `892fdf8b3e08c9ea6882f2035fba489e8f5b3458670863143d49f5c57298d417` |
| [review-web-copy-r4.md](review-web-copy-r4.md) | `e28dd7c1b79cf4120e8c10ee8cc2b8ce7fcd4c6b4e54b629a0abc7c965e8815b` |
| [review-backend-head-80991c18.md](review-backend-head-80991c18.md) | `c4e2fb6fd0fb25cf8a7a6435e00f27ebf06a870f03283101bcfa42cc36dcfce6` |
| [review-ui-head-15c202cf.md](review-ui-head-15c202cf.md) | `88127e2dc48b3854af8247a3345a361c17622a07755b917800f83c7ecd0fba3f` |
| [review-task5-head-2757b033.md](review-task5-head-2757b033.md) | `0e1877bd6e2ba1329669a555e3daa3ed28b1fa37e85739ba92ae0009331a98f4` |
| [review-task5-head-29b72f3.md](review-task5-head-29b72f3.md) | `5f5fe9ba8955c6a343782fea94aca1aa805f097fa8f4846eec0153fb0a61e519` |
| [review-task5-head-1d28aed0.md](review-task5-head-1d28aed0.md) | `a00db7d3f27fd5903cfdba77b1f8866827432d3ace68b2d778229d4f12756f2b` |
| [source-reviewed-54-files.json](source-reviewed-54-files.json) | `de28c833767030f5849f03cc31a646c255d5d29b0b792010f3622d9ebc067c8e` |
| [source-backend-manifest.json](source-backend-manifest.json) | `fc160cb423bcc256e1956463974f7e65ce8a4cde57670b4c6b57193dce3c3410` |
| [source-ui-r2-manifest.json](source-ui-r2-manifest.json) | `500995f95d533c1cdd9f66d05eccaa4908d36fb889f7b6746fb78de3ff4d4b01` |
| [source-task5-final-manifest.json](source-task5-final-manifest.json) | `0cfab602698050e6f53f8a37e1737dbe208ccce60cbc3c82e2fe5e8c2a80eb3b` |
| [integration-29b72f3-receipts.json](integration-29b72f3-receipts.json) | `ece9f9481dd80523f43a2a947d109231f7b1afca503e44ca39c50285daec36da` |
| [source-ci-1d28aed0.json](source-ci-1d28aed0.json) | `7b07f42bc89ca547337e5e84a56e76582022e76b913de6317460fd1c5b0ae77d` |
| [main-89d260fd-checks.json](main-89d260fd-checks.json) | `5c20ef730e1778c1cdc927c81117caed95dd1f42ea1ba41818d49ef080ea8001` |
| [release-v0.112.0-complete-acceptance.json](release-v0.112.0-complete-acceptance.json) | `60067197686c56dba95f7f69d61d015bc01e2429f2591ffd3348b58a98b64f90` |
| [release-v0.112.0-browser-receipt.json](release-v0.112.0-browser-receipt.json) | `4261d67ee5e9fd92410090ea665815a04a739ae2f9869adac5dea6d28d8660ad` |
| [contributor-acceptance-recipe.sh](contributor-acceptance-recipe.sh) | `a4f91f9ba88a7098acdd7ff765ea42fcab145af1d048200f54c4e88117537315` |

## Website publication and observed CI boundary

Website repository `datatug/websites` PR139 landed reviewed candidate `14690083ad0d100d315ce8d0761ff7a5eb483563` as merge `f5fc8b1b7ea5a1b97c6f0b889a92c183e8809bc5`. [WB landing receipt](website-pr139-land.json), [25-file candidate manifest](website-final-r5-manifest.json), [final review](review-website-final-r5.md) and [exact-head review](review-website-head-14690083.md) bind that publication. Candidate CI run40036858478 concluded success; WB reported stable green candidate checks before landing.

The parent's [actual deployment and live acceptance receipt](website-live-acceptance.json), observed2026-10-23T23:26:23.356903+00:00, records all three Cloudflare deployment workflows succeeding on the merge and all11 direct live page checks passing with status200. It captures localized accepted CLI/harness/fixture identities and recipe on datatug.io, Apps developer-preview claims on datatug.io/datatug.ai/datatug.app, and UI Bubbles remaining planned. Existing Pro checkout remains open; configuration was unchanged. This is deployed-page acceptance, not a named customer desktop-host acceptance claim.

**Website full post-merge CI run40039403748 was still in_progress**, with no conclusion in that receipt. Its deployment jobs and candidate CI successes do not imply that this separate full run passed. The parent is awaiting the final check before final cleanup. Retained worktrees are for required evidence/lifecycle publication and final checks.

CLI main subsequently advanced to `4e30fd76afdc9c51bb14242e0ab17b804dd212b9`; the [incoming CLI review](review-cli-incoming-4e30fd76.md) verifies all54 reviewed file hashes unchanged. [Go CI for that exact head](integration-4e30fd76-ci.json) passed; its automatic release and supplemental v0.113.0 acceptance subsequently passed as recorded below. Public v0.112.0 remains the literal contributor recipe acceptance identity. The [initial evidence review](review-delivery-evidence-r1.md) preserves its earlier43-file scope and then-pending website status unchanged.

Additional portable website records:

| Evidence | SHA256 |
|---|---|
| [website-pr139-land.json](website-pr139-land.json) | `b996ae6369be3a08bac8c9791fbc5815cdb030ad347ba41d51b96c53ea0bc56f` |
| [website-final-r5-manifest.json](website-final-r5-manifest.json) | `2954aecca923983d69f788904514407a95234b3f3683e0d16ab60f15a770fadc` |
| [review-website-final-r5.md](review-website-final-r5.md) | `15e3db28306a0557e3bc75f539d213018cdc9a5c10cdbddb657a037411d03f1b` |
| [review-website-head-14690083.md](review-website-head-14690083.md) | `95e6338e03ab4a4dc60091eb6948e03ac07d4370a19e0f9e1c692f3d61f823a4` |
| [review-cli-incoming-4e30fd76.md](review-cli-incoming-4e30fd76.md) | `8d2ee2efe955acc85c78d17c13487c176f3317f9034494bb3875c37e85a24f45` |
| [review-delivery-evidence-r1.md](review-delivery-evidence-r1.md) | `f04b906882aa4ae4e420bd2ed11bdec95ba4d54fe523141d78ebdcd048347be3` |
| [website-live-acceptance.json](website-live-acceptance.json) | `819b4fc4a4df1ae6e21d2e09dc8f415d88a3776c1c8613ec2978b8095848b210` |
| [review-website-live-r1.md](review-website-live-r1.md) | `0423166183c1c3e1655a4fd8a77b853601634a6115f24213b48fc1f79df905ea` |

## Newer CLI integration CI

The retained task checkout advanced to already-landed main `4e30fd76afdc9c51bb14242e0ab17b804dd212b9` after independent integration review confirmed all54 historical reviewed file hashes unchanged. [Exact-main Go CI](integration-4e30fd76-ci.json) passed at that commit. Its later automatic release is separate from the documented v0.112.0 literal recipe acceptance; actual supplemental v0.113.0 acceptance follows below.

## Supplemental current CLI release acceptance

Public [v0.113.0](https://github.com/datatug/datatug/releases/tag/v0.113.0) binds through downloaded binary build info and private CLI tag to source `4e30fd76afdc9c51bb14242e0ab17b804dd212b9`. Exact Go CI and automatic release passed. Native archive checksum, strict signature and Notarized Developer ID assessment passed. The downloaded binary passed all17 maintained official-host browser scenarios and real plain MCP acceptance, with fixture `08cc4d367e33cca82269bff54dc8a35b242e9937` and harness `2bd815ece0a2bc37c99de10862b06c877bc37b80`. Embedded HTML remains unchanged.

This supplemental run used the maintained browser command; the literal documented recipe was executed against v0.112.0. Neither the installed CLI nor package-manager publication was changed or claimed. Independent review found zero open findings. The batch4 second review records the evidence checkpoint review separately.

| Evidence | SHA256 |
|---|---|
| [release-v0.113.0-complete-acceptance.json](release-v0.113.0-complete-acceptance.json) | `56488205fcbd87cb0210e5bc3e469bbc8a03536a6633d48b63b44edde1ee0286` |
| [release-v0.113.0-browser-receipt.json](release-v0.113.0-browser-receipt.json) | `4411489f8b95cededa11738a4812e4b0def127d7668ce3214aa7342d0a16dcb1` |
| [review-public-integration-v0.113.0.md](review-public-integration-v0.113.0.md) | `81f65f5f0dcaacc972455a3c0e802256c0a7f61dbeac0ce8bf80c0fc205c353c` |
| [review-batch4-r2.md](review-batch4-r2.md) | `bd2a92f5e0991947c5fd1f40e42c6a61ecf4bd5cb0c5d684dbac7a55afa6940f` |

## Open Questions

Final website full post-merge CI remains with the parent owner. Exact CLI4e Go CI, automatic release and supplemental v0.113.0 acceptance passed. No task/feature/plan transition or cleanup completion is asserted by this evidence update. The [independent website live-evidence review](review-website-live-r1.md) personally verified the three deploys and11 live page receipts, preserved the pending post-merge CI distinction, and reported zero findings.

---
*This document follows the https://specscore.md/features-index-specification*
