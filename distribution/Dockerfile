FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

ARG TARGETARCH
ARG DATATUG_SOURCE_REVISION=unknown
ARG DATATUG_SOURCE_STATE=unknown
ARG DATATUG_SOURCE_STATUS_DIGEST=unknown
LABEL org.opencontainers.image.title="DataTug execution plane" \
    org.opencontainers.image.source="https://github.com/datatug/datatug-cli" \
    org.opencontainers.image.revision="${DATATUG_SOURCE_REVISION}" \
    io.datatug.source-state="${DATATUG_SOURCE_STATE}" \
    io.datatug.source-status-digest="${DATATUG_SOURCE_STATUS_DIGEST}" \
    io.datatug.license-inventory="/usr/share/licenses/datatug/PROJECT-LICENSE-INVENTORY.json"

ENV HOME=/var/lib/datatug \
    DATATUG_TELEMETRY=0 \
    DATATUG_STATE_DIR=/var/lib/datatug
WORKDIR /workspace
USER 65532:65532

COPY --chown=65532:65532 --chmod=0555 bin/${TARGETARCH}/datatug /usr/local/bin/datatug
COPY --chown=65532:65532 --chmod=0555 bin/${TARGETARCH}/datatug-entrypoint /usr/local/bin/datatug-entrypoint
COPY --chown=65532:65532 rootfs/var/lib/datatug/ /var/lib/datatug/
COPY --chown=65532:65532 licenses/ /usr/share/licenses/datatug/

ENTRYPOINT ["/usr/local/bin/datatug-entrypoint"]
CMD ["--help"]
