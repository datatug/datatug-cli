#!/usr/bin/env bash
# Tells whether datatug/homebrew-tap carries the cask of a published release.
#
# usage: verify-homebrew-cask.sh [--warn-only] <release-tag>
#
# Downloads the release's checksums file from $GITHUB_REPOSITORY, reads
# Casks/datatug.rb from the public tap, and hands both to check-homebrew-cask.sh.
# A read can fail for a moment, so the whole read-and-compare is retried. The
# final message says whether the tap could not be read or was read and is not
# the release's: those need different fixes.
#
# Publishing to the tap is manual ("Publish latest packages"), so the tap is
# behind the release unless someone has started that workflow since:
#   - in the release run (--warn-only) the answer is only a ::warning:: line and
#     the exit code is always 0: a stale tap or a check that cannot run never
#     fails or delays a release;
#   - at the end of "Publish latest packages" (no flag) every result except
#     "current" is an ::error:: line and a non-zero exit, so a manual publish
#     that did not reach the tap is a failed run.
#
# Environment: GITHUB_REPOSITORY (required), GH_TOKEN (for gh), and for tests
# GH (the gh command, default gh), ATTEMPTS (default 5), INTERVAL (seconds
# between attempts, default 30).
#
# Exit codes without --warn-only: 0 current, 1 not current or unreadable, 2 bad
# usage or environment, 3 the release's checksums file lacks an archive
# (reported at once, not retried). Every non-zero exit prints an ::error:: line
# that says why. With --warn-only the exit code is 0 for every outcome.
set -euo pipefail

warn_only=0
if [[ "${1:-}" == "--warn-only" ]]; then
  warn_only=1
  shift
fi

# finish <exit-status> <message>: print the single annotation and leave.
finish() {
  local status="$1" message="$2"
  if [[ $warn_only -eq 1 ]]; then
    echo "::warning::$message"
    exit 0
  fi
  echo "::error::$message"
  exit "$status"
}

if [[ $# -ne 1 ]]; then
  finish 2 "usage: verify-homebrew-cask.sh [--warn-only] <release-tag>; the Homebrew tap was not compared with the release."
fi

tag="$1"
repo="${GITHUB_REPOSITORY:-}"
if [[ -z "$repo" ]]; then
  finish 2 "GITHUB_REPOSITORY is not set, so the Homebrew tap was not compared with $tag."
fi
gh="${GH:-gh}"
attempts="${ATTEMPTS:-5}"
interval="${INTERVAL:-30}"
tap="datatug/homebrew-tap"
check="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-homebrew-cask.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The last thing learned about the tap: "stale" once a read compared and found
# it behind, kept through later failed reads so the final message does not
# turn a known-stale tap into an unknown one.
reason="read"
detail=""
for ((attempt = 1; attempt <= attempts; attempt++)); do
  # gh refuses to overwrite a file from an earlier attempt.
  rm -f "$work"/*
  if "$gh" release download "$tag" --repo "$repo" --pattern '*_checksums.txt' --dir "$work" &&
    "$gh" api -H 'Accept: application/vnd.github.raw' "repos/$tap/contents/Casks/datatug.rb" >"$work/datatug.rb"; then
    checksums="$(find "$work" -name '*_checksums.txt' | head -n 1)"
    status=0
    output="$(bash "$check" "$tag" "$work/datatug.rb" "$checksums" 2>&1)" || status=$?
    echo "$output" >&2
    if [[ $status -eq 0 ]]; then
      exit 0
    fi
    # The checker's last line is the one-sentence reason.
    detail="$(printf '%s\n' "$output" | tail -n 1)"
    if [[ $status -ne 1 ]]; then
      finish "$status" "the Homebrew tap was not compared with $tag (check exit $status): $detail"
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
  finish 1 "could not check whether the Homebrew tap is current: could not read datatug/homebrew-tap or the $tag checksums after $attempts attempts. Check Casks/datatug.rb by hand."
fi
if [[ $warn_only -eq 1 ]]; then
  finish 1 "the Homebrew tap is behind the release $tag ($detail). Homebrew installs get an older version until someone starts the workflow \"Publish latest packages\" (Actions tab of $repo, on main)."
fi
finish 1 "the Homebrew tap does not carry the release $tag after publishing ($detail). Homebrew installs would get an older version: check datatug/homebrew-tap Casks/datatug.rb."
