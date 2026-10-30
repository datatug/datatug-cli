CREATE DATABASE explore;
CREATE DATABASE forbidden;
CREATE TABLE forbidden.secret (value String) ENGINE=Memory;
INSERT INTO forbidden.secret VALUES ('synthetic only');

CREATE TABLE explore.events (
    id UInt64,
    amount Decimal(38, 9),
    items Array(UInt64),
    label Nullable(String),
    at DateTime64(9, 'UTC'),
    attrs Map(String, UInt64),
    INDEX label_skip label TYPE set(100) GRANULARITY 1
) ENGINE=MergeTree ORDER BY id;
INSERT INTO explore.events VALUES
(18446744073709551615, 12345678901234567890.123456789, [18446744073709551615], NULL, '2026-01-02 03:04:05.123456789', {'maximum':18446744073709551615}),
(18446744073709551615, 12345678901234567890.123456789, [18446744073709551615], NULL, '2026-01-02 03:04:05.123456789', {'maximum':18446744073709551615}),
(7, 1.000000001, [], 'safe', '2026-01-02 03:04:05.123456789', {});

CREATE USER fixture_reader IDENTIFIED WITH plaintext_password BY 'synthetic-reader';
GRANT SELECT ON explore.* TO fixture_reader;
GRANT SELECT ON system.databases TO fixture_reader;
GRANT SELECT ON system.tables TO fixture_reader;
GRANT SELECT ON system.columns TO fixture_reader;
GRANT SELECT ON system.data_skipping_indices TO fixture_reader;
CREATE SETTINGS PROFILE fixture_reader_profile SETTINGS
    readonly=1 CONST,
    max_execution_time=30 MIN 0.001 MAX 65 CHANGEABLE_IN_READONLY,
    max_result_rows=1001 MIN 1 MAX 100001 CHANGEABLE_IN_READONLY,
    max_result_bytes=8388608 MIN 1 MAX 67108864 CHANGEABLE_IN_READONLY,
    result_overflow_mode='throw' CONST,
    max_rows_to_read=100000 CONST,
    max_bytes_to_read=67108864 CONST,
    read_overflow_mode='throw' CONST,
    timeout_overflow_mode='throw' CONST,
    max_memory_usage=536870912 CONST,
    max_threads=2 CONST
TO fixture_reader;
