# Try DataTug over SSH

The SSH demo opens DataTug's interactive terminal workspace with a sample project. You can browse SQLite and inGitDB catalogs, run queries and explore results with no local DataTug installation or GitHub sign-in.

```bash
ssh -p 2222 178.104.41.143
```

When DNS is configured, use `ssh -p 2222 demo.datatug.app`. On your first connection, compare the ED25519 fingerprint SSH shows with the fingerprint published by the operator before accepting the key.

## Keep your work

Choose **Explore the demo project** to start with a temporary workspace. Your project files and local database changes disappear when the session ends unless you save them.

Choose **Save to GitHub** in the workspace to keep your work. DataTug displays a device code, which you approve in your browser using GitHub's OAuth device flow. The requested `public_repo` scope lets DataTug create or reuse your fork of the public demo repository and push there. It writes to `datatug-demo` and never force-pushes. If that branch has moved, DataTug offers to save on a new branch.

On a later visit, choose **Open my GitHub project** and sign in again to reopen your saved branch. Saves include committed project files, queries, and SQLite or inGitDB files inside the repository. External databases such as OpenVaultDB cloud stay outside the saved project. Changes made there may persist independently of the session.

The demo deletes the session workspace and forgets your GitHub token at disconnect. Saved commits stay in your fork. You can revoke the OAuth grant in GitHub Settings under Applications. Operators retain connection metadata such as the client IP and session duration in service logs.

## Run locally

With a CLI build that includes the SSH demo commands, start a loopback server using temporary directories you own.

```bash
demo_local_root=$(mktemp -d)
datatug demo ssh-server --isolation process --listen 127.0.0.1:2222 \
  --state-dir "$demo_local_root/state" --sessions-dir "$demo_local_root/sessions"
```

In a second terminal, run `ssh -p 2222 localhost`. This mode runs all sessions under your own user and is for local development. After stopping the server, remove the temporary directory with `rm -rf -- "$demo_local_root"`.

For the hosted deployment, see the [operator runbook](../deploy/ssh-demo/README.md) and [feature specification](../spec/features/ssh-demo/README.md).
