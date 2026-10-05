#!/usr/bin/env bash
# Fails when datatug/homebrew-tap does not carry the cask of a published release.
#
# usage: verify-homebrew-cask.sh <release-tag>
#
# Downloads the release's checksums file from $GITHUB_REPOSITORY, reads
# Casks/datatug.rb from the public tap, and hands both to check-homebrew-cask.sh.
# The tap is pushed during the release, so a read can still race it; the whole
# read-and-compare is retried. The final error says whether the tap could not be
# read or was read and is not the release's: those need different fixes.
#
# Environment: GITHUB_REPOSITORY (required), GH_TOKEN (for gh), and for tests
# GH (the gh command, default gh), ATTEMPTS (default 5), INTERVAL (seconds
# between attempts, default 30).
#
# Exit codes: 0 current, 1 not current or unreadable, 2 bad usage.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <release-tag>" >&2
  exit 2
fi

tag="$1"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
gh="${GH:-gh}"
attempts="${ATTEMPTS:-5}"
interval="${INTERVAL:-30}"
tap="datatug/homebrew-tap"
check="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-homebrew-cask.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

reason="read"
for ((attempt = 1; attempt <= attempts; attempt++)); do
  # gh refuses to overwrite a file from an earlier attempt.
  rm -f "$work"/*
  reason="read"
  if "$gh" release download "$tag" --repo "$repo" --pattern '*_checksums.txt' --dir "$work" &&
    "$gh" api -H 'Accept: application/vnd.github.raw' "repos/$tap/contents/Casks/datatug.rb" >"$work/datatug.rb"; then
    checksums="$(find "$work" -name '*_checksums.txt' | head -n 1)"
    status=0
    bash "$check" "$tag" "$work/datatug.rb" "$checksums" || status=$?
    if [[ $status -eq 0 ]]; then
      exit 0
    fi
    if [[ $status -ne 1 ]]; then
      exit "$status"
    fi
    reason="stale"
    echo "attempt $attempt of $attempts: the tap's cask is not the release's yet" >&2
  else
    echo "attempt $attempt of $attempts: could not read the release checksums or the tap" >&2
  fi
  if [[ $attempt -lt $attempts ]]; then
    sleep "$interval"
  fi
done

if [[ "$reason" == "read" ]]; then
  echo "::error::could not read datatug/homebrew-tap or the $tag checksums after $attempts attempts. The tap may or may not be current: check Casks/datatug.rb by hand."
else
  echo "::error::datatug/homebrew-tap Casks/datatug.rb is not at $tag after $attempts attempts. Homebrew installs would get an older version."
fi
exit 1
