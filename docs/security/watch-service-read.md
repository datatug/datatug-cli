# Service Watch transport foundation

`watch-service-read/1` is a closed additive contract for the existing authenticated
runner/bundle channel. This commit provides shared CLI codecs and intent helpers;
it does not install protected Service execution or complete Task 7.

`watch.Claim.Deadline` copies the stored Run deadline at claim creation, GetClaim
and WithClaim. `ExecutionCeiling` checks the original deadline/current lease and
narrows it by supplied authenticated ceilings. A protected caller must pin the
result once for the fenced attempt and use it for context/output bounds. The
helper authenticates neither the claim nor credential/enrollment bounds. A missing
deadline fails closed for the new helpers; legacy worker behavior is unchanged.

`watchdaemon.ServiceReadRequest` has two closed variants. Catalog bootstrap carries
only contract/phase and receives classification, never a resolution. Resolve
carries a query ID, typed parameters and the existing Core ResultRequest. Its
response is the existing Core read-only exact Service resolution with no grants,
bounded by the original run ceiling and a maximum ten-second resolve lease.
Parsing a response is shape validation, never evidence of its origin or authority.

`BuildServiceReadIntent` consumes each peer's independently retained/admitted Claim,
verified immutable bundle, source descriptor and actual connection binding. It
uses saved structured TugQL and declared typed/defaulted parameters. Output bounds
are fixed at **100 rows and 262144 bytes**. Core's operation RequestFor digest remains
unchanged. A separate Watch hash binds the fixed 21-element Go JSON array defined
in service_read_intent.go: claim/run/fence/scope, Watch/Check/source/query pins,
exact query JSON hash, source/connection fingerprint, retained root inputs,
normalized operation parameters and the Core nonce/digest. Typed numeric strings,
canonical set order and nil-versus-empty retained root inputs keep their existing
Go JSON semantics. Invalid UTF-8 is refused before it can be replaced during
encoding. These helpers bind intent; they do not attest worker execution.

Query presence and typed/defaulted parameter normalization do not validate a
Check step's bindings or derive its actual parameters from retained root inputs
and nested Check occurrences. The future server integration must independently
derive and compare the operation parameters for the accepted Check occurrence,
using its retained inputs and the existing engine's mapping rules. It must never
pass request.Parameters directly to this helper as admitted intent.

The connection source ID is a hash of the existing descriptor shape, not an access
connection ID or grant. No Service identity, caller principal, role, private policy
generation, credential or native-proof assertion is accepted in the request.

Still required in separate CLI/Backend/Cloud integration: authenticated enrollment
and same-transaction current claim/source/protection/standing-policy admission;
server-selected Service identity; catalog/enrollment connection-variant validation;
independent actual native fingerprint/resource verification; a per-claim/query
runner context instead of process-global session changes; fresh resolve for each
operation/output and original credential/run ceilings; raw/write refusal before
dispatch and honest hasMore/incomplete Check evidence. Core owns query admission
and the policy compiler. No new Core export, database field, credential or endpoint
is introduced here. The released external-daemon journey and native/provider
qualification remain outstanding.
