#!/usr/bin/env bash
# check-hermetic-tests.sh proves `go test ./...` never reads or writes the
# real user's home, XDG config, or XDG cache directories.
#
# It runs the full suite with HOME, XDG_CONFIG_HOME and XDG_CACHE_HOME
# pointed at one more fresh, empty directory — on top of, not instead of,
# internal/hermetictest's own per-package redirection (see that package's
# doc comment for why both layers exist: this script is the backstop that
# catches a package that forgot to call it, or a new production code path
# that starts resolving a per-user directory this module's seams don't
# cover yet). If anything survives in that directory once the suite exits,
# something in this module — or a dependency such as
# github.com/ingitdb/dalgo2ingitdb, which offers no override for its
# os.UserCacheDir()-rooted lock directory — wrote outside its own temp
# directory, and this script fails, listing exactly what it found.
#
# Go toolchain telemetry and build/module caches are excluded: they are not
# ours to fix, and redirecting them would make the run itself unreliable
# (a cold module cache re-downloads the world on every CI run).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

hermetic_home="$(mktemp -d "${TMPDIR:-/tmp}/datatug-hermetic-ci-home.XXXXXX")"
cleanup() { chmod -R u+w "$hermetic_home" 2>/dev/null || true; rm -rf "$hermetic_home"; }
trap cleanup EXIT

# GOCACHE/GOMODCACHE/GOPATH are captured BEFORE HOME is overridden and
# passed through explicitly. Go computes each of them from $HOME when unset,
# so without this a redirected HOME makes the run re-download and rebuild
# the entire module cache under the fresh, empty hermetic_home — slow, and
# it leaves that cache (Go marks it read-only) right where this script
# would otherwise report it as a leak. They are Go toolchain state, not
# user data: keeping them pointed at the real, shared cache is what
# scripts/check-hermetic-tests.sh's own exclusion list below assumes.
real_gocache="$(go env GOCACHE)"
real_gomodcache="$(go env GOMODCACHE)"
real_gopath="$(go env GOPATH)"

echo "check-hermetic-tests: running 'go test ./...' with HOME=$hermetic_home" >&2

HOME="$hermetic_home" \
XDG_CONFIG_HOME="$hermetic_home/.config" \
XDG_CACHE_HOME="$hermetic_home/.cache" \
GOCACHE="$real_gocache" \
GOMODCACHE="$real_gomodcache" \
GOPATH="$real_gopath" \
	go test -count=1 ./...

# Go toolchain telemetry/build state under $HOME is not this module's to
# fix; ignore only these specific paths, nothing else. Telemetry lives under
# os.UserConfigDir()/go/telemetry, which this script's own XDG_CONFIG_HOME
# redirect (above) points at $hermetic_home/.config on Linux (CI) but
# os.UserConfigDir ignores XDG_CONFIG_HOME on Darwin, landing telemetry
# under $HOME/Library/Application Support/go instead (confirmed both ways:
# this script's local macOS run left nothing behind, while the "Hermetic
# tests" GitHub Actions job — ubuntu-latest — first failed listing exactly
# these paths under .config/go/telemetry).
excluded_prefixes=(
	"$hermetic_home/Library/Application Support/go"
	"$hermetic_home/Library/Caches/go-build"
	"$hermetic_home/go/pkg/mod"
	"$hermetic_home/.config/go"
)

leaked=()
while IFS= read -r -d '' path; do
	excluded=0
	for prefix in "${excluded_prefixes[@]}"; do
		case "$path" in
		"$prefix" | "$prefix"/*)
			excluded=1
			break
			;;
		esac
	done
	if [ "$excluded" -eq 0 ]; then
		leaked+=("$path")
	fi
done < <(find "$hermetic_home" -mindepth 1 ! -type d -print0)

if [ "${#leaked[@]}" -gt 0 ]; then
	echo "check-hermetic-tests: FAIL — go test ./... wrote outside its own temp directories:" >&2
	for path in "${leaked[@]}"; do
		echo "  $path" >&2
	done
	echo "check-hermetic-tests: route the production code path that created these through a seam (see internal/hermetictest and pkg/personalqueries's userHomeDir for the pattern), or add a TestMain that calls hermetictest.Main." >&2
	exit 1
fi

echo "check-hermetic-tests: PASS — nothing was left under $hermetic_home" >&2
