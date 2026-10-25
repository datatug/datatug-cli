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

The process is bounded to 30 seconds and 1 MiB combined output. Cancellation
terminates it then force-kills after 1.5 seconds; an unconfirmed shutdown is
reported after another second. No retries follow uncertain execution. Check
evidence remains private outside the workspace in extension storage with mode
0700. Query JSON is shown as native text only after user request; VS Code may
save/backup editor text. It is never rendered as HTML, Markdown or a webview.
Raw CLI stderr/errors never appear in user-facing diagnostics.

Only the constant hosted home URL can be opened, after explaining context is
unavailable. There is no upload, opaque reference exchange, token in a URL,
tenant assertion or invented hosted-result route. Reauthorised context links
and real remote/provider testing are independent release gates.

No runtime dependency is bundled. The archive allowlist and restricted-asset
scan are mandatory for each VSIX. The actual macOS no-project extension-host
activation smoke passes with isolated profile/extension directories and no
Workspace Trust override; its owned host/profile are closed/removed. Independent
security review, trusted-project UI execution, remote/platform credential and
provider tests, and publication approval remain pending.
