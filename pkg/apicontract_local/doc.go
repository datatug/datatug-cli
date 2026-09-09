// Package apicontract_local is datatug-cli's OWN provisional copy of the
// shared JSON types core-investigation-loop's normative transport appendix
// (datatug/datatug spec/features/core-investigation-loop/api-contract.md)
// defines: Scope, SourceRef, TypedValue, Fact, Limitation, Binding, Result,
// Candidate, ExecutionRequest, the agent-info envelope and the structured
// error envelope.
//
// Plan task 12 (S64) assigns the real schema authority to a new
// github.com/datatug/datatug-core/pkg/apicontract package (lane S64a,
// concurrent with this one) that will also freeze the shared fixtures
// (pkg/apicontract/fixtures/*.json) both server and client consume. This
// package exists ONLY because that module tag did not exist yet when this
// stream needed to start writing server code against the contract — see the
// stream brief (s64-server-task12.md): "start by writing the server against
// the contract markdown with your own provisional types under
// pkg/apicontract_local ... and when the lead tells you the core tag exists
// (or v0.25.0 shows up on `git ls-remote --tags`), replace the local types
// with the module's and consume its fixtures."
//
// Every type below is named, field-tagged and shaped to be byte-identical
// to the appendix's own examples, so that swap is a mechanical
// find-and-replace of the import path (github.com/datatug/datatug-cli/pkg/
// apicontract_local -> github.com/datatug/datatug-core/pkg/apicontract) plus
// deleting this package, not a redesign. Fixtures under ./fixtures mirror
// the appendix's "Acceptance and migration" section: empty arrays, null/
// false/zero/large-integer values, ambiguous/missing inputs, omitted
// unauthorized metadata, HTTP failure and every error code.
package apicontract_local
