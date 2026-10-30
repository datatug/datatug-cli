#!/bin/sh

# Install an official DataTug CLI release without a language toolchain.
# DATATUG_INSTALL_DIR may override the default per-user destination (~/.local/bin).
# DATATUG_VERSION may pin a release tag such as v0.78.2.
# DATATUG_MODIFY_PATH controls shell configuration (yes|no|prompt).
set -eu

repo="https://github.com/datatug/datatug"
install_dir=${DATATUG_INSTALL_DIR:-"$HOME/.local/bin"}
version=${DATATUG_VERSION:-}
modify_path=${DATATUG_MODIFY_PATH:-}
work_dir=""
destination_tmp=""

fail() {
  printf 'datatug install: %s\n' "$1" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"

# Detect OS
case "$(uname -s)" in
  Darwin)
    os=darwin
    display_os="macOS"
    ;;
  Linux)
    os=linux
    display_os="Linux"
    ;;
  *)
    fail "unsupported operating system: $(uname -s) (supported: macOS, Linux)"
    ;;
esac

# Detect architecture
case "$(uname -m)" in
  x86_64|amd64)
    arch=amd64
    ;;
  arm64|aarch64)
    arch=arm64
    ;;
  *)
    fail "unsupported architecture: $(uname -m) (supported: amd64, arm64)"
    ;;
esac

# Resolve version
if [ -z "$version" ]; then
  latest_url=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$repo/releases/latest") ||
    fail "could not resolve the latest release from $repo/releases/latest"
  version=${latest_url##*/}
fi

case "$version" in
  v[0-9]*) ;;
  *) fail "release version must be a tag such as v0.78.2 (got: $version)" ;;
esac

asset_version=${version#v}
archive="datatug_${asset_version}_${os}_${arch}.tar.gz"
checksums="datatug_${asset_version}_checksums.txt"
release_url="$repo/releases/download/$version"

printf 'DataTug CLI installer\n\n'
printf 'Platform: %s %s\n' "$display_os" "$arch"
printf 'Version:  %s\n\n' "$version"

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/datatug-install.XXXXXX") ||
  fail "could not create a temporary directory"
chmod 700 "$work_dir"

cleanup() {
  if [ -n "$work_dir" ] && [ -d "$work_dir" ]; then
    rm -rf "$work_dir"
  fi
  if [ -n "$destination_tmp" ] && [ -f "$destination_tmp" ]; then
    rm -f "$destination_tmp"
  fi
}
trap cleanup EXIT HUP INT TERM

printf 'Downloading...\n'
curl -fsSLo "$work_dir/$checksums" "$release_url/$checksums" ||
  fail "could not download checksums for $version"
curl -fsSLo "$work_dir/$archive" "$release_url/$archive" ||
  fail "could not download release archive $archive"

printf 'Verifying...\n'
expected=$(awk -v file="$archive" '$2 == file || $2 == "*" file { print $1; exit }' "$work_dir/$checksums")
[ -n "$expected" ] || fail "$archive is missing from $checksums"

if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$work_dir/$archive" | awk '{ print $1 }')
elif command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work_dir/$archive" | awk '{ print $1 }')
else
  fail "shasum or sha256sum is required for checksum verification"
fi

[ "$actual" = "$expected" ] || fail "SHA-256 checksum verification failed"

printf 'Installing...\n'
tar -xzf "$work_dir/$archive" -C "$work_dir" || fail "could not extract $archive"
[ -f "$work_dir/datatug" ] || fail "release archive does not contain datatug executable"

mkdir -p "$install_dir" || fail "could not create installation directory: $install_dir"
destination_tmp="$install_dir/.datatug.install.$$.$RANDOM"
cp "$work_dir/datatug" "$destination_tmp" || fail "could not copy datatug to $install_dir"
chmod 755 "$destination_tmp" || fail "could not make datatug executable"

# Validate that the binary is functional on this system
"$destination_tmp" --version >/dev/null 2>&1 ||
  fail "installed binary failed validation ($destination_tmp --version)"

mv "$destination_tmp" "$install_dir/datatug" || fail "could not install datatug in $install_dir"
destination_tmp=""

# Format display path for user
if [ "$install_dir" = "$HOME/.local/bin" ]; then
  display_dir="~/.local/bin"
elif case "$install_dir" in "$HOME"/*) true ;; *) false ;; esac; then
  display_dir="~${install_dir#"$HOME"}"
else
  display_dir="$install_dir"
fi

printf '\n✓ Installed DataTug %s to %s/datatug\n\n' "$version" "$display_dir"

# --- PATH handling ---

in_path() {
  target="$1"
  case ":$PATH:" in
    *":$target:"*) return 0 ;;
  esac
  target_trimmed="${target%/}"
  case ":$PATH:" in
    *":$target_trimmed:"*) return 0 ;;
  esac
  if case "$target" in "$HOME"/*) true ;; *) false ;; esac; then
    tilde_target="~${target#"$HOME"}"
    case ":$PATH:" in
      *":$tilde_target:"*) return 0 ;;
    esac
  fi
  return 1
}

if in_path "$install_dir"; then
  printf 'Open a new terminal and run:\n\n'
  printf '  datatug --version\n'
  exit 0
fi

# Detect user shell and appropriate profile file
user_shell=$(basename "${SHELL:-}")
profile=""
display_profile=""
export_cmd=""

case "$user_shell" in
  zsh)
    profile="$HOME/.zshrc"
    display_profile="~/.zshrc"
    ;;
  bash)
    case "$os" in
      darwin)
        if [ -f "$HOME/.bash_profile" ]; then
          profile="$HOME/.bash_profile"
          display_profile="~/.bash_profile"
        elif [ -f "$HOME/.bashrc" ]; then
          profile="$HOME/.bashrc"
          display_profile="~/.bashrc"
        elif [ -f "$HOME/.profile" ]; then
          profile="$HOME/.profile"
          display_profile="~/.profile"
        else
          profile="$HOME/.bash_profile"
          display_profile="~/.bash_profile"
        fi
        ;;
      *)
        if [ -f "$HOME/.bashrc" ]; then
          profile="$HOME/.bashrc"
          display_profile="~/.bashrc"
        elif [ -f "$HOME/.bash_profile" ]; then
          profile="$HOME/.bash_profile"
          display_profile="~/.bash_profile"
        elif [ -f "$HOME/.profile" ]; then
          profile="$HOME/.profile"
          display_profile="~/.profile"
        else
          profile="$HOME/.bashrc"
          display_profile="~/.bashrc"
        fi
        ;;
    esac
    ;;
  fish)
    profile="$HOME/.config/fish/config.fish"
    display_profile="~/.config/fish/config.fish"
    export_cmd="fish_add_path \"$install_dir\""
    ;;
  *)
    # Unsupported or unknown shell
    ;;
esac

if [ -z "$export_cmd" ]; then
  if case "$install_dir" in "$HOME"/*) true ;; *) false ;; esac; then
    export_cmd="export PATH=\"\$HOME${install_dir#"$HOME"}:\$PATH\""
  else
    export_cmd="export PATH=\"$install_dir:\$PATH\""
  fi
fi

# Check if profile already configures this directory
already_configured=0
if [ -n "$profile" ] && [ -f "$profile" ]; then
  if grep -Fq "$install_dir" "$profile" 2>/dev/null ||
     ( [ "$install_dir" = "$HOME/.local/bin" ] && ( grep -Fq ".local/bin" "$profile" 2>/dev/null || grep -Fq "\$HOME/.local/bin" "$profile" 2>/dev/null ) ); then
    already_configured=1
  fi
fi

if [ "$already_configured" -eq 1 ]; then
  printf '%s is not in your current PATH, but is already configured in %s.\n\n' "$display_dir" "$display_profile"
  printf 'Open a new terminal or run:\n\n'
  printf '  %s\n' "$export_cmd"
  printf '  datatug --version\n'
  exit 0
fi

# Determine if interactive prompt is possible
is_interactive=0
if [ -z "${CI:-}" ] && [ "${DATATUG_NONINTERACTIVE:-}" != "1" ] && [ "${DATATUG_NONINTERACTIVE:-}" != "true" ]; then
  if [ -r /dev/tty ] && [ -w /dev/tty ]; then
    is_interactive=1
  elif [ -t 0 ]; then
    is_interactive=1
  fi
fi

should_modify=0
case "$modify_path" in
  yes|true|1)
    should_modify=1
    ;;
  no|false|0|skip)
    should_modify=0
    ;;
  *)
    if [ "$is_interactive" -eq 1 ] && [ -n "$profile" ]; then
      printf '%s is not in your PATH.\n\n' "$display_dir"
      printf 'Add it to %s? [Y/n] ' "$display_profile"
      answer=""
      if [ -r /dev/tty ] && [ -w /dev/tty ]; then
        read -r answer </dev/tty || answer=""
      else
        read -r answer || answer=""
      fi
      case "$answer" in
        [yY][eE][sS]|[yY]|"")
          should_modify=1
          ;;
        *)
          should_modify=0
          ;;
      esac
    fi
    ;;
esac

if [ "$should_modify" -eq 1 ] && [ -n "$profile" ]; then
  profile_dir=$(dirname "$profile")
  mkdir -p "$profile_dir" || fail "could not create directory: $profile_dir"
  if [ -s "$profile" ]; then
    last_char=$(tail -c 1 "$profile" 2>/dev/null || true)
    if [ -n "$last_char" ]; then
      printf '\n' >> "$profile"
    fi
  fi
  printf '\n# DataTug CLI\n%s\n' "$export_cmd" >> "$profile"
  printf '\n✓ Updated %s\n\n' "$display_profile"
  printf 'Open a new terminal and run:\n\n'
  printf '  datatug --version\n'
else
  printf '%s is not in your PATH.\n\n' "$display_dir"
  if [ -n "$profile" ]; then
    printf 'To add it to your PATH, add this to %s:\n\n' "$display_profile"
  else
    printf 'To add it to your PATH, add this to your shell profile:\n\n'
  fi
  printf '  %s\n\n' "$export_cmd"
  printf 'Or run it in your current terminal session:\n\n'
  printf '  %s\n' "$export_cmd"
  printf '  datatug --version\n'
fi
