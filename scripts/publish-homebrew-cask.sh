#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <release-tag> <checksums-file> <cask-path>" >&2
  exit 2
fi

tag="$1"
checksums="$2"
cask="$3"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "expected a stable vX.Y.Z release tag" >&2
  exit 2
fi
version="${tag#v}"

checksum() {
  local asset="$1" hash
  hash="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$checksums")"
  if [[ ! "$hash" =~ ^[0-9a-f]{64}$ ]]; then
    echo "missing or invalid checksum for $asset" >&2
    exit 1
  fi
  printf '%s' "$hash"
}

darwin_arm64="$(checksum "datatug_${version}_darwin_arm64.tar.gz")"
darwin_amd64="$(checksum "datatug_${version}_darwin_amd64.tar.gz")"
linux_arm64="$(checksum "datatug_${version}_linux_arm64.tar.gz")"
linux_amd64="$(checksum "datatug_${version}_linux_amd64.tar.gz")"

cat > "$cask" <<EOF
# Generated from verified DataTug release artifacts. DO NOT EDIT.
cask "datatug" do
  version "$version"

  on_macos do
    on_arm do
      sha256 "$darwin_arm64"
      url "https://github.com/datatug/datatug-cli/releases/download/v#{version}/datatug_#{version}_darwin_arm64.tar.gz"
    end
    on_intel do
      sha256 "$darwin_amd64"
      url "https://github.com/datatug/datatug-cli/releases/download/v#{version}/datatug_#{version}_darwin_amd64.tar.gz"
    end
  end
  on_linux do
    on_arm do
      sha256 "$linux_arm64"
      url "https://github.com/datatug/datatug-cli/releases/download/v#{version}/datatug_#{version}_linux_arm64.tar.gz"
    end
    on_intel do
      sha256 "$linux_amd64"
      url "https://github.com/datatug/datatug-cli/releases/download/v#{version}/datatug_#{version}_linux_amd64.tar.gz"
    end
  end

  name "datatug"
  desc "DataTug – Context-aware data viewer & collaborative query manager for effortless exploration of related data — CLI + Web UI"
  homepage "https://github.com/datatug/datatug-cli"

  livecheck do
    skip "Published manually from verified release artifacts."
  end

  binary "datatug"
end
EOF
