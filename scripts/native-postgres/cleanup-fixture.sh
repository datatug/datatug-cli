#!/usr/bin/env bash
# Always-run fallback after native Go cleanup, including partial container create.
set -euo pipefail
if [ "${GITHUB_ACTIONS:-}" != true ] || [ "${RUNNER_OS:-}" != Linux ]; then
  echo 'native pooler cleanup requires the owned Linux Actions runner' >&2
  exit 1
fi
image="${DATATUG_NATIVE_POOLER_IMAGE:-}"
runtime="${DATATUG_NATIVE_POOLER_RUNTIME:-}"
if [ -n "$runtime" ]; then
  [[ "$runtime" =~ ^dt-native-pooler-[0-9a-f]{24}$ ]]
  [[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]]
  id="$(docker container ls --all --no-trunc --filter "name=^/${runtime}$" --format '{{.ID}}')"
  if [ -n "$id" ]; then
    [[ "$id" =~ ^[0-9a-f]{64}$ ]]
    test "$(docker inspect --format '{{index .Config.Labels "dev.datatug.pooler-runtime"}}' "$id")" = "$runtime"
    test "$(docker inspect --format '{{.Name}}' "$id")" = "/$runtime"
    test "$(docker inspect --format '{{.Image}}' "$id")" = "$image"
    docker rm --force "$id" >/dev/null
    remaining="$(docker container ls --all --no-trunc --filter "id=$id" --format '{{.ID}}')"
    test -z "$remaining"
  fi
fi
python3 -B "$(dirname "$0")/cleanup_private.py"
if [ -n "$image" ]; then
  [[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]]
  owner="${DATATUG_NATIVE_POOLER_BUILD:?}"
  [[ "$owner" =~ ^dt-native-[0-9]+-[0-9]+-(17|18)$ ]]
  test "$(docker image inspect --format '{{index .Config.Labels "dev.datatug.pooler-build"}}' "$image")" = "$owner"
  test "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image")" = f03e9bf263d7ccd5d1d152a61811bf3258dd6b87
  docker image rm "$image" >/dev/null
  remaining="$(docker image ls --quiet --no-trunc --filter "label=dev.datatug.pooler-build=$owner")"
  test -z "$remaining"
fi
