# Public v0.112.0 native acceptance review r1 (runtime pending)

Personally bound actual archive datatug_0.112.0_darwin_arm64.tar.gz SHA256bf688e927e1c9171d3ca9f59b7372a319384ce1af0fc9cc925ab10100d0b6b02 to downloaded checksumfile and extracted binary SHA256875af63bef87a6c3ce38f2651c0a41a5b2e26a82da3f966ba71892d1f42eb400; independently read tar member bytes and verified same binaryhash. Public release JSON tagv0.112.0, notdraft/notprerelease. Native receipt/buildinfo version0.112.0 and vcs.revision89d260fdfe00f3b0345adae451258443d2cd00a7, CGO0 darwinarm64. Actual privateCLI v0.112.0 tag resolves that commit; git ancestry includes reviewed1d28aed0. Publichub target_commitish is not used as sourceidentity.

Inspected native checksum/signature/notarization receipts: codesign valid/satisfies requirement; spctl accepted NotarizedDeveloperID. Inspected recorded actual successful CI40034686825 and automaticrelease40034686763 sourcecommit89d260...; these are rootcollected authoritative snapshots, not independently live-requeried by reviewer. Packagepublication/installedCLIupdate not claimed; installed0.102.2 remains separate and discoveryreports0.112.0.

Literal shell recipe matches reviewed documented setup body and never rebuilds suppliedCLI. Current literalrecipe log ended safeexistingcheckout revision refusal/exit1; that is correct checkout protection, NOT browseracceptance. Root notified. Plainactualchild log passed14/17, JSON-only legacy stdout and cleanexit. Source/offline earlierbrowser receipts do not substitute for suppliedpublicbinary browser/recipe pass. Final17scenario publicbrowser receipt still missing/pending at time of review; no approval of it.

No native-source/artifact findings. Release acceptance remains INCOMPLETE until correct exactrevision isolatedfixture recipe rerun passes and actualartifact receipt is bound to above publicbinary, privateCLI source/harnessrevision and demo08cc... identity. No sourceedits/services/broadtests/publication or RELEASE_OK emitted. Root sole landing owner.

VERDICT: blockers=0 majors=0 minors=0 land=no
