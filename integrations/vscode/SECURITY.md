# Security boundary

Only trusted filesystem projects may be selected; trust is rechecked before
execution. A separate modal grants unrestricted local-owner consent for each
query/Check. The CLI retains policy/source admission; secured Check profiles
fail closed. The extension never removes policies, edits source descriptors,
starts listeners or supplies arbitrary workspace commands.

The executable is an explicit global machine setting, physically outside all
workspace roots; native executable magic is verified. No automatic discovery,
PATH resolution, shell, downloads or updates occur. JSON metadata is bounded
and project-contained, including physical symlink checks. Project files can
still direct the trusted CLI to data sources: inspect and trust them before
execution. Concurrent local file replacement by an attacker with write access
to the project or trusted binary is outside this trust model.

Credential values use VS Code SecretStorage. Explicitly selected profiles feed
only permitted credential variables in the CLI environment. Runtime injection
variables are refused; inherited loader controls are stripped. Secrets never
enter settings, process arguments, telemetry, notifications or extension logs.
The CLI inherits existing host authentication and configuration. Same-user
process inspection and VS Code's own credential transport/storage are host
security boundaries. Verify those boundaries for each remote environment.

The CLI execution/publication deadline is 30 seconds, with 1 MiB combined output.
An explicitly opted-in summary run allows one additional second for JSON flush. Cancellation
terminates it then force-kills after 1.5 seconds; an unconfirmed shutdown is
reported after another second. No retries follow uncertain execution. Check
evidence remains private outside the workspace in extension storage with mode
0700. Query JSON is shown as native text only after user request; VS Code may
save/backup editor text. It is never rendered as HTML, Markdown or a webview.
Raw CLI stderr/errors never appear in user-facing diagnostics.

The default query/Check commands never opt into hosted summary publication.
A separate command requires an explicit Space binding, a separately stored hosted
SecretStorage token, local-owner consent and one-run publication consent. It
passes only the fixed CLI publication flags and the named token environment
variable. The inherited hosted token is scrubbed on every child invocation;
the database credential validator cannot select this variable. There is no
extension API URL override. Hosted credentials never enter arguments or state.
The only uploaded fields are minimal identifiers/version, execution status,
outcome, severity, duration and execution time, retained for seven days. SQL,
rows, inputs, errors and private evidence remain excluded. Upload failure keeps
the local Check status and native JSON results, with allowlisted diagnostics.

The constant hosted home fallback remains available. A successful explicitly
requested summary may offer its exact scoped DataTug.app route only after the
closed canonical fragment, manifest project ID, explicit Space ID and selected
environment match. Arbitrary CLI/project URLs, extra fragment members, encoded
path substitutions, credentials and query strings are refused. External opening
requires the user to select its action. Hosted sign-in and current Space/grant
authorization remain independent. Actual summary host-to-hosted acceptance,
remote/provider tests and independent security review remain release gates.

No runtime dependency is bundled. The archive allowlist and restricted-asset
scan are mandatory for each VSIX. The actual macOS no-project extension-host
activation smoke passes with isolated profile/extension directories and no
Workspace Trust override; its owned host/profile are closed/removed. The earlier
synthetic trusted-project query/Check UI journey also passed. Actual summary
consent and hosted handoff in VS Code, remote/platform credentials, provider
tests, independent release security review and publication approval remain
pending. These gates are distinct from the adapter's independent code review.
