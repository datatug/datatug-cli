---
name: datatug-release
description: |
  Deploy delegate for specstudio:ship. Publishes a datatug-cli release by
  pushing main, cutting a semver git tag, and dispatching the release.yml
  GitHub workflow (goreleaser → GitHub Release + Homebrew). Performs the
  deploy mechanics ship is barred from doing, and reports a single explicit
  success/failure signal back to ship. Non-interactive; fail-loud; idempotent
  per commit. Invoked via `ship.delegate.skill: datatug-release`.
---

# datatug-release

The deploy delegate `specstudio:ship` dispatches to. Ship gates and records;
**this skill does the actual publishing**: push `main`, cut the version tag, and
trigger the release workflow. The git tag is the single source of truth for the
version — goreleaser derives `main.version` from it (`-X main.version={{.Version}}`),
so there is **no in-repo version file to edit**.

## Contract with ship

- **Invocation:** ship invokes this skill once and passes `ship.delegate.args`
  verbatim as the bump argument. Ship does not retry or sequence.
- **Explicit success signal (load-bearing):** this skill MUST end by printing
  **exactly one** of:
  - `RELEASE_OK <tag>` — success; ship may transition the Feature → `Stable`.
  - `RELEASE_FAILED <reason>` — failure; ship leaves the Feature `Implementing`.
  Anything else is treated by ship as failure. Never print `RELEASE_OK` unless
  every step below succeeded (or the commit was already released).
- **Idempotent per commit:** if `HEAD` is already tagged with a `vX.Y.Z` tag,
  the release for this commit already happened — succeed without re-tagging or
  re-dispatching. This is what makes the multi-Feature case safe: shipping a
  second Feature at the same commit just flips its status, it does not cut a
  second release.

## Input (`args`)

`args` selects the target version. Accepted forms:

- empty / `patch` → patch bump of the latest `v*` tag
- `minor` → minor bump
- `major` → major bump
- an explicit `vX.Y.Z` → use exactly that version (overrides bump logic)

## Procedure

Run from the repository root. Stop at the first failure and print
`RELEASE_FAILED <reason>` — do not continue, do not print `RELEASE_OK`.

### 1. Preconditions (fail-loud)

```bash
test "$(git rev-parse --abbrev-ref HEAD)" = "main"            # must be on main
test -z "$(git status --porcelain)"                           # clean working tree
gh auth status >/dev/null 2>&1                                 # gh authenticated
git remote get-url origin | grep -q 'datatug/datatug-cli'     # correct repo
```

If any check fails, print `RELEASE_FAILED <which precondition>` and stop.

### 2. Idempotency — already released this commit?

```bash
existing=$(git tag --points-at HEAD --list 'v*' \
  | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)
if [ -n "$existing" ]; then
  echo "RELEASE_OK $existing (HEAD already tagged — already released)"
  exit 0
fi
```

### 3. Resolve the target version from `args`

```bash
BUMP="$1"   # the value of ship.delegate.args
case "$BUMP" in
  v[0-9]*)                       # explicit version
    echo "$BUMP" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$' || { echo "RELEASE_FAILED bad explicit version '$BUMP'"; exit 1; }
    NEW="$BUMP" ;;
  *)                             # bump level
    latest=$(git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)
    latest=${latest:-v0.0.0}
    IFS=. read -r MA MI PA <<EOF
${latest#v}
EOF
    case "${BUMP:-patch}" in
      major) MA=$((MA+1)); MI=0; PA=0 ;;
      minor) MI=$((MI+1)); PA=0 ;;
      patch) PA=$((PA+1)) ;;
      *) echo "RELEASE_FAILED unknown bump '$BUMP'"; exit 1 ;;
    esac
    NEW="v$MA.$MI.$PA" ;;
esac
# Guard: target tag must not already exist anywhere
git rev-parse -q --verify "refs/tags/$NEW" >/dev/null && { echo "RELEASE_FAILED tag $NEW already exists but not on HEAD"; exit 1; }
git ls-remote --exit-code --tags origin "$NEW" >/dev/null 2>&1 && { echo "RELEASE_FAILED tag $NEW already exists on origin"; exit 1; }
```

### 4. Push, tag, dispatch

```bash
git push origin main                                   || { echo "RELEASE_FAILED push main"; exit 1; }
git tag -a "$NEW" -m "Release $NEW"                     || { echo "RELEASE_FAILED create tag"; exit 1; }
git push origin "$NEW"                                  || { echo "RELEASE_FAILED push tag"; exit 1; }
gh workflow run release.yml --field tag="$NEW"          || { echo "RELEASE_FAILED dispatch workflow"; exit 1; }
echo "RELEASE_OK $NEW"
```

`gh workflow run` returning 0 means GitHub accepted the dispatch for tag `$NEW`.
That is this skill's success boundary: the local responsibility — push the
commits, cut the tag, trigger the workflow — completed.

## Async caveat (read before relying on `Stable`)

The GitHub release runs **asynchronously** after dispatch; `RELEASE_OK` means
"release triggered," not "artifacts published." If goreleaser fails later, ship
will already have flipped the Feature to `Stable`. To make `Stable` mean
"actually published," replace the final line of step 4 with a blocking watch:

```bash
sleep 8
run=$(gh run list --workflow=release.yml --event=workflow_dispatch --limit=1 --json databaseId --jq '.[0].databaseId')
gh run watch "$run" --exit-status || { echo "RELEASE_FAILED release workflow run $run"; exit 1; }
echo "RELEASE_OK $NEW"
```

This is intentionally left as the non-blocking default for the MVP; opt into the
watch if you want `Stable` gated on the workflow's completion.

## Boundary

This skill owns deploy mechanics (push/tag/dispatch) — the things ship is barred
from doing. It does **not** gate, transition Feature status, or emit
`ship.completed`; those belong to ship and run only on this skill's
`RELEASE_OK`. It releases the whole `datatug` binary at one tag; it does not
coordinate per-Feature releases.
