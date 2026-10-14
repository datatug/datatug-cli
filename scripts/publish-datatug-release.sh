#!/usr/bin/env bash
# Syncs or publishes a GitHub release and its assets from datatug/datatug-cli to datatug/datatug.
#
# usage: publish-datatug-release.sh [--dry-run] <release-tag> [source-repo] [target-repo]
#
# Arguments:
#   release-tag: e.g. v0.78.0
#   source-repo: default ${GITHUB_REPOSITORY:-datatug/datatug-cli}
#   target-repo: default datatug/datatug
#
# Environment variables:
#   GH_SOURCE_TOKEN: GitHub token for reading from source repo (default: $GH_TOKEN or inherited gh auth)
#   GH_TARGET_TOKEN: GitHub token for writing to target repo (default: $GH_TOKEN or inherited gh auth)
#   GH_TOKEN: fallback token for gh
set -euo pipefail

dry_run=0
if [[ "${1:-}" == "--dry-run" ]]; then
  dry_run=1
  shift
fi

if [[ $# -lt 1 || $# -gt 3 ]]; then
  echo "usage: $0 [--dry-run] <release-tag> [source-repo] [target-repo]" >&2
  exit 2
fi

tag="$1"
source_repo="${2:-${GITHUB_REPOSITORY:-datatug/datatug-cli}}"
target_repo="${3:-datatug/datatug}"

if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "expected a stable vX.Y.Z release tag, got '$tag'" >&2
  exit 2
fi

gh_source() {
  if [[ -n "${GH_SOURCE_TOKEN:-}" ]]; then
    GH_TOKEN="$GH_SOURCE_TOKEN" gh "$@"
  else
    gh "$@"
  fi
}

gh_target() {
  if [[ -n "${GH_TARGET_TOKEN:-}" ]]; then
    GH_TOKEN="$GH_TARGET_TOKEN" gh "$@"
  else
    gh "$@"
  fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Downloading release $tag assets from $source_repo..."
gh_source release download "$tag" --repo "$source_repo" --dir "$work"

version="${tag#v}"
checksums="$work/datatug_${version}_checksums.txt"
if [[ ! -f "$checksums" ]]; then
  echo "checksums file $checksums not found in release assets of $source_repo" >&2
  exit 1
fi

echo "Verifying downloaded assets against checksums..."
(cd "$work" && sha256sum -c "datatug_${version}_checksums.txt")

echo "Extracting release metadata for $tag..."
title="$(gh_source release view "$tag" --repo "$source_repo" --json name --jq .name)"
if [[ -z "$title" ]]; then
  title="$tag"
fi
gh_source release view "$tag" --repo "$source_repo" --json body --jq .body > "$work/release-notes.md"

if [[ $dry_run -eq 1 ]]; then
  echo "[dry-run] Would publish release $tag ('$title') to $target_repo with assets:"
  (cd "$work" && ls -la datatug_*)
  exit 0
fi

echo "Publishing release $tag to $target_repo..."
if gh_target release view "$tag" --repo "$target_repo" >/dev/null 2>&1; then
  echo "Release $tag already exists in $target_repo. Uploading / updating assets..."
  gh_target release upload "$tag" "$work"/datatug_* --repo "$target_repo" --clobber
else
  echo "Creating release $tag in $target_repo..."
  gh_target release create "$tag" "$work"/datatug_* \
    --repo "$target_repo" \
    --title "$title" \
    --notes-file "$work/release-notes.md"
fi

echo "Verifying published release in $target_repo..."
gh_target release view "$tag" --repo "$target_repo" --json tagName,name,assets
echo "Successfully published $tag to $target_repo."
