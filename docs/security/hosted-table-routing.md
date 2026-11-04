# Optional hosted table routing

A source-bound CLI session can register `hostedTableRead` in its existing private
`.datatug/security.json`. It is absent by default. The closed private version 2 object
contains `version`, `spaceID`, `projectId`, `connectionId`, `catalogSHA256`,
`fileSHA256`, `database` and `hostedEngineIdentity`. IDs refer to the operator-recorded hosted Space,
project and saved connection, not local project/environment/catalog aliases.
Both SHA witnesses are lowercase SHA-256. The catalog witness records registration
provenance; the CLI does not read or certify the current hosted catalogue.

The trusted operator records the actual hosted source identity in the exact form
`sqlite-file-route-v1:<64 lowercase hex characters>`. The CLI independently checks
its local file against `fileSHA256`, then emits that recorded hosted identity.
This operator association is not cryptographic cross-machine content proof, and
the CLI cannot derive or verify the hosted path association offline. Present
private version 1 registrations are unsupported and fail closed; they never
become absent or trigger native fallback. The public marker remains version 1.

After the existing source/session checks, `catalog-tables` may return:

```json
{"hostedTableRead":{"version":1,"status":"available","spaceID":"S1","projectId":"P1","connectionId":"C1","fingerprint":{"engine":"sqlite","database":"controlled","engineIdentity":"sqlite-file-route-v1:<recorded-hosted-route-sha256>"}}}
```

The CLI hashes its already fixed source with a 64 MiB limit, cancellation and
file/path identity checks. A registered source whose content cannot be verified
returns only:

```json
{"hostedTableRead":{"version":1,"status":"unavailable","reasonCode":"source-binding-unavailable"}}
```

Malformed registration refuses session construction. Base source/session failure
still denies the request. Unregistered sessions omit the marker and retain the
legacy response. A present unavailable, unsupported or malformed marker must
never trigger native row fallback. No filesystem path or credential is returned.

Available is routing metadata, not permission, eligibility or live hosted
catalogue proof. The consumer must compare every fingerprint field against fresh
hosted protection and the table lease, preserving its current authority and lease
checks. The hosted service independently verifies the saved catalogue and source.
Use the existing nonempty table `schema` verbatim; do not infer `main` when absent.
This supports the bounded immutable SQLite source profile, not arbitrary engines
or protection against a hostile filesystem owner restoring all observed facts.
