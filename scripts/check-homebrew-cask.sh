#!/usr/bin/env bash
# Fails when the Homebrew cask in the tap is not the cask of the given release.
#
# usage: check-homebrew-cask.sh <release-tag> <cask-file> [<checksums-file>]
#
# <cask-file> is a copy of Casks/datatug.rb read from datatug/homebrew-tap.
# <checksums-file> is the release's datatug_<version>_checksums.txt; when given,
# the cask must also carry the SHA-256 of each macOS and Linux archive in it.
#
# Exit codes: 0 current, 1 the cask is not the release's, 2 bad usage.
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <release-tag> <cask-file> [<checksums-file>]" >&2
  exit 2
fi

tag="$1"
cask="$2"
checksums="${3:-}"

if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "expected a stable vX.Y.Z release tag, got '$tag'" >&2
  exit 2
fi
if [[ ! -f "$cask" ]]; then
  echo "cask file '$cask' does not exist" >&2
  exit 2
fi
if [[ -n "$checksums" && ! -f "$checksums" ]]; then
  echo "checksums file '$checksums' does not exist" >&2
  exit 2
fi

want="${tag#v}"
got="$(sed -n 's/^[[:space:]]*version "\([^"]*\)".*/\1/p' "$cask" | head -n 1)"
if [[ -z "$got" ]]; then
  echo "the tap's cask has no version line" >&2
  exit 1
fi
if [[ "$got" != "$want" ]]; then
  echo "the tap's cask is at version $got, but the release is $want" >&2
  exit 1
fi

if [[ -n "$checksums" ]]; then
  for platform in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
    asset="datatug_${want}_${platform}.tar.gz"
    hash="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$checksums")"
    if [[ ! "$hash" =~ ^[0-9a-f]{64}$ ]]; then
      echo "the release's checksums file has no checksum for $asset ($platform)" >&2
      exit 1
    fi
    if ! grep -q "sha256 \"$hash\"" "$cask"; then
      echo "the tap's cask does not carry the checksum of $asset ($platform)" >&2
      exit 1
    fi
  done
fi

echo "the tap's cask is current: version $want"
