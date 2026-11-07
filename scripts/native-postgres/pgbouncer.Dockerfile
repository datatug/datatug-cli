# CI-only fixture: pinned upstream source, never a production image.
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
    autoconf automake build-essential ca-certificates git libevent-dev libssl-dev libtool pkg-config python3 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /source
RUN git init . \
    && git remote add origin https://github.com/pgbouncer/pgbouncer.git \
    && git fetch --depth 1 origin f03e9bf263d7ccd5d1d152a61811bf3258dd6b87 \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = f03e9bf263d7ccd5d1d152a61811bf3258dd6b87 \
    && ./autogen.sh \
    && ./configure --prefix=/usr/local --without-cares \
    && make -j2 pgbouncer \
    && ./pgbouncer --version | grep -Fx 'PgBouncer 1.26.1' \
    && strip pgbouncer

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
RUN apt-get update && apt-get install -y --no-install-recommends libevent-2.1-7t64 libssl3t64 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 pooler \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin pooler
COPY --from=build /source/pgbouncer /usr/local/bin/pgbouncer
LABEL org.opencontainers.image.source="https://github.com/pgbouncer/pgbouncer" \
      org.opencontainers.image.revision="f03e9bf263d7ccd5d1d152a61811bf3258dd6b87" \
      org.opencontainers.image.version="1.26.1" \
      dev.datatug.fixture="native-readonly-pooler"
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/pgbouncer"]
CMD ["/fixture/pgbouncer.ini"]
