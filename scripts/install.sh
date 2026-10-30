#!/bin/sh

# Install an official DataTug CLI release without a language toolchain.
#
#   curl -fsSL https://datatug.app/install/get-cli | sh
#
# DATATUG_INSTALL_DIR may override the default per-user destination (an absolute path).
# DATATUG_VERSION may pin a release tag such as v0.32.0.
# When the install directory is not on PATH, the installer appends one line that puts it there to
# your shell's startup file (~/.zshrc, ~/.bashrc or ~/.bash_profile on macOS, fish's config.fish,
# otherwise ~/.profile) and prints the file, the line number and the line. DATATUG_MODIFY_PATH=0
# skips that edit and prints the line to add yourself.
#
# The whole installer is one function that is called on the last line, so a download that
# is cut short can never run a partial script: shell parses the complete function first.
set -eu

fail() {
  printf 'datatug install: %s\n' "$1" >&2
  exit 1
}

# fetch <output-file> <url>: https only (also across redirects), TLS 1.2 or newer.
fetch() {
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL -o "$1" "$2"
}

# check_tag <tag>: a strict semantic-version release tag, such as v0.32.0 or v1.0.0-rc.1.
# Nothing else is accepted, so the value can never add a path segment or a query to a URL.
check_tag() {
  case "$1" in
    "" | *[!0-9A-Za-z.-]*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
}

# add_to_path <dir>: <dir> is not on PATH. Append one line that puts it there to the startup file of
# the user's shell and say exactly what changed, unless DATATUG_MODIFY_PATH=0. Never fails the install.
add_to_path() {
  dir=$1
  # shellcheck disable=SC2016 # $PATH is written to the startup file literally
  line='export PATH="'"$dir"':$PATH"'
  case "${SHELL:-}" in
    */zsh) rc="${ZDOTDIR:-${HOME:-}}/.zshrc" ;;
    */bash)
      if [ "$os" = darwin ]; then rc="${HOME:-}/.bash_profile"; else rc="${HOME:-}/.bashrc"; fi ;;
    */fish)
      rc="${XDG_CONFIG_HOME:-${HOME:-}/.config}/fish/config.fish"
      line='fish_add_path "'"$dir"'"' ;;
    *) rc="${HOME:-}/.profile" ;;
  esac

  if [ "${DATATUG_MODIFY_PATH:-1}" = 0 ]; then
    printf '%s is not on PATH. DATATUG_MODIFY_PATH=0, so no startup file was changed. Add this line to %s:\n  %s\n' "$dir" "$rc" "$line"
    return 0
  fi
  if [ -z "${HOME:-}" ]; then
    printf '%s is not on PATH. HOME is not set, so no startup file was changed. Add it to PATH yourself:\n  %s\n' "$dir" "$line"
    return 0
  fi
  case "$dir" in
    *[\"\$\`\\]* | *"
"*)
      printf '%s is not on PATH, and its name has a character that cannot be written safely to %s. Add it to PATH yourself.\n' "$dir" "$rc"
      return 0 ;;
  esac
  if [ -f "$rc" ] && grep -Fqx "$line" "$rc"; then
    printf '%s already has this line, so it was not changed:\n  %s\nOpen a new terminal, or run the line in this one.\n' "$rc" "$line"
    return 0
  fi
  if mkdir -p "${rc%/*}" 2>/dev/null && printf '\n# Added by the DataTug CLI installer (https://datatug.app/install/get-cli)\n%s\n' "$line" >>"$rc" 2>/dev/null; then
    printf 'Added %s to PATH: appended line %s to %s:\n  %s\nOpen a new terminal, or run that line in this one. DATATUG_MODIFY_PATH=0 skips this edit.\n' "$dir" "$(wc -l <"$rc" | tr -d ' ')" "$rc" "$line"
  else
    printf '%s is not on PATH, and %s could not be written. Add this line to it yourself:\n  %s\n' "$dir" "$rc" "$line"
  fi
}

main() {
  repo="https://github.com/datatug/datatug"
  version=${DATATUG_VERSION:-}
  destination_tmp=
  work_dir=

  if [ -n "${DATATUG_INSTALL_DIR:-}" ]; then
    install_dir=$DATATUG_INSTALL_DIR
  else
    [ -n "${HOME:-}" ] || fail "HOME is not set; set DATATUG_INSTALL_DIR to an absolute path"
    install_dir="$HOME/.local/bin"
  fi
  case "$install_dir" in
    /*) ;;
    *) fail "DATATUG_INSTALL_DIR must be an absolute path, not \"$install_dir\"" ;;
  esac

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
    latest_url=$(curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSIL -o /dev/null -w '%{url_effective}' "$repo/releases/latest") ||
      fail "could not resolve the latest release"
    version=${latest_url##*/}
  fi

  check_tag "$version" || fail "release version must be a tag such as v0.32.0"

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

  fetch "$work_dir/$checksums" "$release_url/$checksums" ||
    fail "could not download checksums for $version"
  fetch "$work_dir/$archive" "$release_url/$archive" ||
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
  # An unpredictable name in the destination directory (same filesystem, so the final mv is atomic).
  destination_tmp=$(mktemp "$install_dir/.datatug.install.XXXXXX") ||
    fail "could not create a temporary file in $install_dir"
  cp "$work_dir/datatug" "$destination_tmp" || fail "could not copy datatug to $install_dir"
  chmod 755 "$destination_tmp" || fail "could not make datatug executable"
  mv "$destination_tmp" "$install_dir/datatug" || fail "could not install datatug in $install_dir"
  destination_tmp=

  printf 'Installed datatug %s at %s/datatug\n' "$version" "$install_dir"
  case ":${PATH:-}:" in
    *":$install_dir:"* | *":$install_dir/:"*) ;;
    *) add_to_path "$install_dir" ;;
  esac
}

main "$@"
