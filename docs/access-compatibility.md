# Access boundary compatibility notices

The release carrying these notices starts a minimum two-week published warning
period. A source commit or a local test does not start that period. Release notes
must record the first published version and date before a later release removes
or gates either route.

`updateUrlConfig` still works during this period. It now prints a fixed notice on
stderr before validation or execution. It is legacy SQL execution outside unified
access grants; a reachable write credential remains its limit. Use `query run`
for read-only queries. This notice does not announce an available governed write
session or certify this legacy command as read-only.

`db copy` prints a fixed notice for remote database targets, including BigQuery
load-job recovery. It preserves existing copy and recovery behavior. Local file
targets and recognized loopback endpoints do not receive the notice. Ambiguous
non-file destinations receive the notice conservatively; that classification is
not an authorization decision or an endpoint validator.

Remote target writes have no access grant or deadline. They provide accident
prevention only, bounded by the credential the process can reach. A shell-capable
agent sharing that OS account can also use that credential. After the warning
period a future release will require an explicit
`--confirm-write-target=<database>` for remote destinations and refuse
Space-recorded targets. **That flag is not available in this release.** Local
files and loopback destinations keep their existing carveout.

The notices contain fixed text only. They never interpolate a connection URL,
database name, password, query, or command. Neither notice changes `--no-policies`,
the default read posture, explicit local file writes, or copy source selection.
