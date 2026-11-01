# CI-only transaction-pooler fixture

Task `das-2`, N-PG-11 with N-PG-10 reuse/recovery controls. This fixture adds no
production registration, native guarantee or driver-tier promotion. Native
execution is restricted to the owned Linux PostgreSQL 17/18 Actions jobs.

PgBouncer source is pinned to `f03e9bf263d7ccd5d1d152a61811bf3258dd6b87`
(upstream `pgbouncer_1_26_1`, annotated tag
`c4a9e336a75bb7a05bec8661a724704170efe251`). The build verifies both the fetched
commit and binary version 1.26.1. Source and build helpers come from that tree.
See [upstream installation](https://www.pgbouncer.org/install.html) and
[configuration](https://www.pgbouncer.org/config.html).

The Debian base and BuildKit images are digest-pinned. Apt package versions are
not frozen, so this is not a claim of bit-reproducible artifacts. A unique
BuildKit builder is CPU/memory bounded before bootstrap; its PID cap is applied
and verified after bootstrap, before any image build. The default builder is
never selected or removed. The loaded fixture image is identified by immutable
ID and an exact per-job build label, then removed in the always step.

The runtime is non-root, read-only, 128 MiB/0.5 CPU/64 PIDs, without added
capabilities, persistent restart or Docker-socket access. It is configured to
reach the existing owned PostgreSQL service on the CI host and listens on a generated
loopback port. Config/auth files are private to the test and group-readable only
by the fixed container GID. Logs are disabled and process/native errors are
reported using fixed stage labels. No customer database or saved credentials
participate.

`ignore_startup_parameters` deliberately discards the client's read-only
startup default. PgBouncer 1.26 still tracks PostgreSQL's reported
`default_transaction_read_only` changes and restores each client's default,
independently of `server_reset_query_always=0`; see the pinned
[release notes](https://github.com/pgbouncer/pgbouncer/blob/f03e9bf263d7ccd5d1d152a61811bf3258dd6b87/NEWS.md#L23)
and [variable restoration](https://github.com/pgbouncer/pgbouncer/blob/f03e9bf263d7ccd5d1d152a61811bf3258dd6b87/src/objects.c#L962).
The test requires the same backend across client restoration, explicit ON/OFF
changes, and an observed OFF default before returning the connection. The next
guarded transaction must be ON and refuse writes natively. These adversarial
settings must not be copied into production. The owned test role can write
outside a read-only transaction; it is not labelled a read-only credential.

Cleanup validates exact name, ID, task label and immutable image before removing
the owned container, verifies listener absence, then removes private files. A
workflow always step recovers the same exact identity after an abnormal test
exit and removes the owned image. No broad Docker cleanup or caller-supplied
database process is terminated. Database-backend replacement validates the
randomized database/user/application plus PID/start time in the terminating SQL
statement. The existing helper owns disposable database/role teardown.

Authoring and safe local compile/fake checks do not establish native execution.
The existing required-test checker rejects missing or skipped pooler acceptance
in either PostgreSQL matrix job. Full Task 2, attestation and other native-matrix
gates remain separate.
