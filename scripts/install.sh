#!/bin/sh
# DataTug CLI installer
#
# Usage:
#   curl -fsSL https://datatug.io/install/get-cli | sh
#
# Environment variables:
#   DATATUG_VERSION      Version tag to install (default: latest)
#   DATATUG_INSTALL_DIR  Install location (default: /usr/local/bin or ~/.local/bin)

set -eu

REPO="datatug/datatug-cli"
BIN_NAME="datatug"
# Multi-component releases: when releases are published to a different repo
# (e.g. synchestra-io/synchestra-releases) and/or tags are prefixed
# (e.g. "cli-v0.x.y" alongside "servers-v0.x.y"), set these.
RELEASES_REPO=""
RELEASE_TAG_PREFIX=""

# Derive defaults
: "${RELEASES_REPO:=$REPO}"

log()  { printf '%s\n' "$*"; }
err()  { printf 'error: %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }

# --- Detect OS -------------------------------------------------------------
OS="$(uname -s)"
case "$OS" in
  Linux*)               OS="linux" ;;
  Darwin*)              OS="darwin" ;;
  MINGW*|MSYS*|CYGWIN*) OS="windows" ;;
  *) die "unsupported OS: $OS" ;;
esac

# --- Detect architecture ---------------------------------------------------
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) die "unsupported architecture: $ARCH" ;;
esac

if [ "$OS" = "windows" ] && [ "$ARCH" = "arm64" ]; then
  die "windows/arm64 is not released; please build from source"
fi

# --- Resolve version -------------------------------------------------------
VERSION="${DATATUG_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  if [ -n "$RELEASE_TAG_PREFIX" ]; then
    # Multi-component releases repo: /releases/latest doesn't know which
    # prefix we care about — list recent releases and pick the newest with
    # our prefix.
    VERSION="$(
      curl -fsSL "https://api.github.com/repos/${RELEASES_REPO}/releases?per_page=50" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
        | grep "^${RELEASE_TAG_PREFIX}" \
        | head -n1
    )"
  else
    VERSION="$(
      curl -fsSL "https://api.github.com/repos/${RELEASES_REPO}/releases/latest" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
        | head -n1
    )"
  fi
  [ -n "$VERSION" ] || die "failed to resolve latest release tag from GitHub"
fi

# goreleaser archives are named with the version without the leading "v"
# (and without the optional release-tag prefix).
VER_NO_V="${VERSION#${RELEASE_TAG_PREFIX}}"
VER_NO_V="${VER_NO_V#v}"

EXT="tar.gz"
[ "$OS" = "windows" ] && EXT="zip"

ARCHIVE="${BIN_NAME}_${VER_NO_V}_${OS}_${ARCH}.${EXT}"
BASE_URL="https://github.com/${RELEASES_REPO}/releases/download/${VERSION}"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUMS_URL="${BASE_URL}/${BIN_NAME}_${VER_NO_V}_checksums.txt"

# --- Resolve install directory --------------------------------------------
if [ -n "${DATATUG_INSTALL_DIR:-}" ]; then
  INSTALL_DIR="$DATATUG_INSTALL_DIR"
elif [ "$(id -u 2>/dev/null || echo 1)" = "0" ]; then
  INSTALL_DIR="/usr/local/bin"
elif [ -w "/usr/local/bin" ] 2>/dev/null; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="$HOME/.local/bin"
fi

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"

# --- Download, verify, install --------------------------------------------
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t "$BIN_NAME")"
trap 'rm -rf "$TMP"' EXIT INT TERM

log "${BIN_NAME} ${VERSION} (${OS}/${ARCH})"
log "  archive: ${ARCHIVE_URL}"

curl -fsSL "$ARCHIVE_URL" -o "$TMP/$ARCHIVE" \
  || die "download failed: $ARCHIVE_URL"

# Verify checksum if we can fetch the manifest and have a sha256 tool.
if curl -fsSL "$CHECKSUMS_URL" -o "$TMP/checksums.txt" 2>/dev/null; then
  EXPECTED="$(awk -v f="$ARCHIVE" '$2==f {print $1}' "$TMP/checksums.txt")"
  if [ -n "$EXPECTED" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      ACTUAL="$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
      ACTUAL="$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')"
    else
      ACTUAL=""
      log "  checksum: skipped (no sha256sum or shasum available)"
    fi
    if [ -n "$ACTUAL" ]; then
      [ "$ACTUAL" = "$EXPECTED" ] \
        || die "checksum mismatch for $ARCHIVE (expected $EXPECTED, got $ACTUAL)"
      log "  checksum: OK"
    fi
  fi
else
  log "  checksum: skipped (manifest not available)"
fi

log "  extracting..."
if [ "$EXT" = "tar.gz" ]; then
  tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
else
  command -v unzip >/dev/null 2>&1 || die "unzip is required to install on windows"
  (cd "$TMP" && unzip -q "$ARCHIVE")
fi

SRC="$TMP/$BIN_NAME"
DST="$INSTALL_DIR/$BIN_NAME"
if [ "$OS" = "windows" ]; then
  SRC="${SRC}.exe"
  DST="${DST}.exe"
fi

[ -f "$SRC" ] || die "binary not found in archive: $SRC"

cp "$SRC" "$DST"
chmod +x "$DST"

log "installed ${BIN_NAME} ${VERSION} to ${DST}"

# --- PATH advisory --------------------------------------------------------
IN_PATH=0
case ":$PATH:" in
  *":$INSTALL_DIR:"*) IN_PATH=1 ;;
  *)
    log ""
    log "note: ${INSTALL_DIR} is not in your PATH. Add it with:"
    log "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac

# --- Shadow check ----------------------------------------------------------
# If another ${BIN_NAME} appears earlier on PATH (commonly a stale
# `go install` build in $GOPATH/bin), it shadows the binary we just
# installed. Rename it to .backup so the released version takes effect.
if [ "$IN_PATH" = "1" ]; then
  SHADOW="$(command -v "$BIN_NAME" 2>/dev/null || true)"
  if [ -n "$SHADOW" ] && [ "$SHADOW" != "$DST" ]; then
    BACKUP="${SHADOW}.backup"
    # If a prior .backup already exists (e.g. user reinstalled via `go install`
    # after a previous curl-install), suffix with a timestamp so we never
    # clobber the earlier preserved binary.
    if [ -e "$BACKUP" ]; then
      BACKUP="${SHADOW}.backup.$(date +%Y%m%d%H%M%S)"
    fi
    log ""
    log "note: another ${BIN_NAME} is earlier on PATH at ${SHADOW}"
    log "  it would shadow the version just installed at ${DST}"
    if mv "$SHADOW" "$BACKUP" 2>/dev/null; then
      log "  renamed to ${BACKUP}"
    else
      log "  unable to rename (insufficient permissions); remove or rename it manually:"
      log "    mv ${SHADOW} ${BACKUP}"
    fi
  else
    # PATH resolution is fine, but a behind-PATH ${BIN_NAME} elsewhere (e.g.
    # a stale `go install` binary in $GOPATH/bin that appears after
    # $INSTALL_DIR in PATH) may have been hashed by the caller's interactive
    # shell from a previous invocation. We can't clear the parent shell's
    # hash table from this subshell — print the fix instead.
    OTHER=""
    IFS_SAVE="$IFS"
    IFS=":"
    for d in $PATH; do
      [ -n "$d" ] || continue
      [ "$d/$BIN_NAME" = "$DST" ] && continue
      if [ -x "$d/$BIN_NAME" ]; then
        OTHER="$d/$BIN_NAME"
        break
      fi
    done
    IFS="$IFS_SAVE"
    if [ -n "$OTHER" ]; then
      log ""
      log "note: another ${BIN_NAME} is on PATH at ${OTHER} (behind ${DST})"
      log "  if your shell still reports an old version, clear its command cache:"
      log "    hash -r"
    fi
  fi
fi
