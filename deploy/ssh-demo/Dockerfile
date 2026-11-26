# syntax=docker/dockerfile:1

# renovate: pin digest
FROM golang:1.27.3-trixie AS source

WORKDIR /src
COPY . .

# BuildKit only builds this stage when the integration test requests it.
FROM source AS slot-link-probe-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -tags sshdemoprobe -trimpath -buildvcs=false \
    -o /out/ssh-slot-link-probe ./deploy/ssh-demo/slot-link-probe

FROM scratch AS slot-link-probe
COPY --from=slot-link-probe-build /out/ssh-slot-link-probe /

FROM source AS build
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE
ENV CGO_ENABLED=1
# All dependencies are public modules: the build needs no credential.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/strongo/buildinfo.version=${VERSION} -X github.com/strongo/buildinfo.commit=${REVISION} -X github.com/strongo/buildinfo.date=${BUILD_DATE}" \
    -o /out/datatug .

FROM debian:trixie-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="DataTug SSH demo" \
      org.opencontainers.image.description="DataTug terminal workspace and SSH edge" \
      org.opencontainers.image.source="https://github.com/datatug/datatug-cli" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

# DuckDB's static archives link against the shared C++ runtime with -lstdc++.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 datatug \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin datatug \
    && mkdir -p /var/lib/datatug-demo/state /var/lib/datatug-demo/sessions \
    && chmod 0711 /var/lib/datatug-demo/sessions \
    && install -d -o 10001 -g 0 -m 0750 /run/datatug-demo/proxy \
    && find / -xdev -perm /6000 -type f -exec chmod a-s {} +
COPY --from=build /out/datatug /usr/local/bin/datatug

ENV DATATUG_TELEMETRY=0 DO_NOT_TRACK=1
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/datatug"]
HEALTHCHECK --interval=5s --timeout=6s --start-period=90s --retries=5 \
    CMD ["/usr/local/bin/datatug", "demo", "ssh-health", "--addr", "127.0.0.1:2222"]
