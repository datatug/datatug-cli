#!/usr/bin/env bash
# CI only. Reuses the existing disposable PostgreSQL17/18 journey runner.
set -euo pipefail
if [ "${GITHUB_ACTIONS:-}" != true ] || [ "${RUNNER_OS:-}" != Linux ]; then
  echo 'native pooler fixture build requires the owned Linux Actions runner' >&2
  exit 1
fi
case "${DATATUG_TEST_POSTGRES_MAJOR:-}" in 17|18) ;; *) exit 1 ;; esac
case "${GITHUB_RUN_ID:?}-${GITHUB_RUN_ATTEMPT:?}" in *[!0-9-]*) exit 1 ;; esac
owner="dt-native-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${DATATUG_TEST_POSTGRES_MAJOR}"
image="datatug-native-pooler:${owner}"
builder="$owner"
buildkit='moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea'
# A preexisting name is not ours. Never select or mutate the default builder.
builders="$(docker buildx ls --format '{{.Name}}')"
if printf '%s\n' "$builders" | grep -Fxq "$builder"; then
  echo 'native pooler builder name already exists' >&2
  exit 1
fi
existing_images="$(docker image ls --quiet --no-trunc "$image")"
test -z "$existing_images"
created=0
complete=0
cleanup() {
  local status=$?
  trap - EXIT
  if [ "$created" = 1 ]; then
    local containers ownership=1
    containers="$(docker container ls --all --no-trunc --filter "name=^/buildx_buildkit_${builder}0$" --format '{{.ID}}')" || ownership=0
    if [ -n "$containers" ]; then
      [[ "$containers" =~ ^[0-9a-f]{64}$ ]] || ownership=0
      test "$(docker inspect --format '{{.Config.Image}}' "$containers")" = "$buildkit" || ownership=0
      docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$containers" | grep -Fxq "DATATUG_POOLER_BUILD=$owner" || ownership=0
    fi
    if [ "$ownership" = 1 ]; then
      docker buildx rm --force "$builder" || status=1
      containers="$(docker container ls --all --quiet --filter "name=^/buildx_buildkit_${builder}0$")" || status=1
      test -z "$containers" || status=1
    else
      echo 'native pooler builder ownership unverified' >&2
      status=1
    fi
  fi
  if [ "$complete" != 1 ]; then
    local id
    id="$(docker image ls --quiet --no-trunc "$image")" || status=1
    if [ -n "$id" ]; then
      if [ "$(docker image inspect --format '{{index .Config.Labels "dev.datatug.pooler-build"}}' "$id")" = "$owner" ]; then
        docker image rm "$id" >/dev/null || status=1
      else
        echo 'native pooler partial image ownership unverified' >&2
        status=1
      fi
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
# Mark before create so a partial successful create is included in cleanup.
# The unique name was absent; the driver receives no external credentials.
created=1
docker buildx create --name "$builder" --driver docker-container \
  --driver-opt "image=$buildkit" \
  --driver-opt "env.DATATUG_POOLER_BUILD=$owner" \
  --driver-opt memory=768m,memory-swap=768m,cpu-period=100000,cpu-quota=100000,restart-policy=no
docker buildx inspect --bootstrap "$builder"
container="buildx_buildkit_${builder}0"
test "$(docker inspect --format '{{.Config.Image}}' "$container")" = "$buildkit"
docker update --pids-limit 128 "$container" >/dev/null
test "$(docker inspect --format '{{.HostConfig.PidsLimit}}' "$container")" = 128
test "$(docker inspect --format '{{.HostConfig.Memory}}' "$container")" = 805306368
test "$(docker inspect --format '{{.HostConfig.MemorySwap}}' "$container")" = 805306368
test "$(docker inspect --format '{{.HostConfig.CpuQuota}}' "$container")" = 100000
docker buildx build --builder "$builder" --platform linux/amd64 --load \
  --label "dev.datatug.pooler-build=$owner" --tag "$image" \
  --file scripts/native-postgres/pgbouncer.Dockerfile scripts/native-postgres
test "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image")" = f03e9bf263d7ccd5d1d152a61811bf3258dd6b87
image_id="$(docker image inspect --format '{{.Id}}' "$image")"
[[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]]
printf 'DATATUG_NATIVE_POOLER_IMAGE=%s\nDATATUG_NATIVE_POOLER_BUILD=%s\n' "$image_id" "$owner" >> "${GITHUB_ENV:?}"
complete=1
