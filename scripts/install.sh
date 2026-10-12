#!/bin/sh

# Install an official DataTug CLI release without a language toolchain.
# DATATUG_INSTALL_DIR may override the default per-user destination.
# DATATUG_VERSION may pin a release tag such as v0.32.0.
set -eu

repo="https://github.com/datatug/datatug-cli"
install_dir=${DATATUG_INSTALL_DIR:-"$HOME/.local/bin"}
version=${DATATUG_VERSION:-}
destination_tmp=

fail() {
  printf 'datatug install: %s\n' "$1" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$version" ]; then
  latest_url=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$repo/releases/latest") ||
    fail "could not resolve the latest release"
  version=${latest_url##*/}
fi

case "$version" in
  v[0-9]*) ;;
  *) fail "release version must be a tag such as v0.32.0" ;;
esac

asset_version=${version#v}
archive="datatug_${asset_version}_${os}_${arch}.tar.gz"
checksums="datatug_${asset_version}_checksums.txt"
release_url="$repo/releases/download/$version"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/datatug-install.XXXXXX") ||
  fail "could not create a temporary directory"

cleanup() {
  rm -rf "$work_dir"
  if [ -n "$destination_tmp" ]; then
    rm -f "$destination_tmp"
  fi
}
trap cleanup EXIT HUP INT TERM

curl -fsSLo "$work_dir/$checksums" "$release_url/$checksums" ||
  fail "could not download checksums for $version"
curl -fsSLo "$work_dir/$archive" "$release_url/$archive" ||
  fail "could not download $archive"

expected=$(awk -v file="$archive" '$2 == file || $2 == "*" file { print $1; exit }' "$work_dir/$checksums")
[ -n "$expected" ] || fail "$archive is missing from $checksums"

if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$work_dir/$archive" | awk '{ print $1 }')
elif command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work_dir/$archive" | awk '{ print $1 }')
else
  fail "shasum or sha256sum is required"
fi

[ "$actual" = "$expected" ] || fail "SHA-256 checksum verification failed"

tar -xzf "$work_dir/$archive" -C "$work_dir" || fail "could not extract $archive"
[ -f "$work_dir/datatug" ] || fail "release archive does not contain datatug"

mkdir -p "$install_dir" || fail "could not create $install_dir"
destination_tmp="$install_dir/.datatug.install.$$"
cp "$work_dir/datatug" "$destination_tmp" || fail "could not copy datatug to $install_dir"
chmod 755 "$destination_tmp" || fail "could not make datatug executable"
mv "$destination_tmp" "$install_dir/datatug" || fail "could not install datatug in $install_dir"
destination_tmp=

printf 'Installed datatug %s at %s/datatug\n' "$version" "$install_dir"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) printf 'Add %s to PATH for this shell: export PATH="%s:$PATH"\n' "$install_dir" "$install_dir" ;;
esac
