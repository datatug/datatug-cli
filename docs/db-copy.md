# `datatug db copy` between SQLite and PostgreSQL

`datatug db copy --from <url> --to <url>` copies a database. When both sides are SQLite or
PostgreSQL (`sqlite://`, `postgres://`) the copy keeps what the two engines can both say and
**reports everything it could not carry**; it never drops a feature silently. SQLite -> PostgreSQL
-> SQLite and PostgreSQL -> SQLite -> PostgreSQL give back the database you started with, except
for the differences listed under "Documented differences".

```sh
export DATATUG_PREVIEW_POSTGRES=1          # PostgreSQL is a preview
datatug db copy --from sqlite://./chinook.sqlite --to 'postgres://user@127.0.0.1/chinook'
datatug db copy --from 'postgres://user@127.0.0.1/chinook' --to sqlite://./chinook-copy.sqlite
datatug db copy --from sqlite://./a.sqlite --to sqlite://./b.sqlite   # exact: the stored CREATE statements
```

| Flag | Meaning |
|---|---|
| `--dry-run` | Print the plan (DDL, row counts, every warning) and change nothing. The target is not opened and a SQLite target file is not created. |
| `--json` | Print the summary as JSON on stdout (see below). |
| `--overwrite recreate` | Drop the target tables named like source tables, then copy. Without it, a target that already holds one of them is refused (exit 1) and left as it was. |
| `--overwrite reload` | Keep the target's tables and their schema, empty them and load the source rows again, in one transaction (see "Reload"). |
| `--include` / `--exclude` | Copy some tables. A foreign key into a table that is not copied is not created, and a warning says so. |
| `--where table:field:op:value`, `--limit table:N` | Copy some rows (operators `=`, `<`, `<=`, `>`, `>=`, `in` with a comma list). They are applied in the source `SELECT`; see "Filters". |
| `--progress` | Print `copying <table> (est. N rows)…` and `copied <table>: N rows in <duration>` on stderr. |
| `--parallel-streams` | Accepted; these copies write one table after the other in one transaction, so a value above 1 prints a warning and uses 1. |
| `--from-schema` / `--to-schema` | PostgreSQL schema to read (default `public`) / to write (default `public`, created when missing). |
| `--fail-on-warning` | Exit 3 when anything could not be carried exactly (the copy is still made; combine with `--dry-run` for a gate). |

Column rules (`--columns`) do not exist yet for any copy. A target SQLite file is created when it is
missing (its folder too; both are removed again if the copy fails), unless its path looks like a URL
or holds a query string. A PostgreSQL target is a preview: it needs `DATATUG_PREVIEW_POSTGRES=1`,
a dry run included.

## What is carried

Tables; columns in order; declared types (see the tables below); nullability; defaults (literals and
`CURRENT_TIMESTAMP` / `CURRENT_DATE` / `CURRENT_TIME`); auto-increment (`INTEGER PRIMARY KEY
[AUTOINCREMENT]` <-> identity column, with the counter position); primary keys including composite
ones, with their constraint names; foreign keys including composite ones, with `ON DELETE` /
`ON UPDATE` actions, names (PostgreSQL), and `DEFERRABLE INITIALLY DEFERRED`; unique constraints;
plain, descending, unique and partial indexes; check constraints (SQLite `GLOB` <-> PostgreSQL `~`,
`IN (..)` <-> `= ANY (ARRAY[..])`); every value exactly (see "Values").

## What is not carried (and is reported)

Every item is a warning with a `kind`, the object it is about and a message.

| Kind | Not carried |
|---|---|
| `view-not-carried` | Views and materialized views across engines (they are written in each engine's SQL). SQLite -> SQLite copies carry them, and triggers and indexes, exactly as stored. |
| `trigger-not-carried` | Triggers and their functions. |
| `index-not-carried` | Indexes on expressions, non-btree indexes, indexes with `INCLUDE`, partial indexes whose predicate the target does not accept; an index the target refused. |
| `check-not-carried` | A check constraint the target does not accept (the regular expression `~ '^[A-Z]{3}$'` has no SQLite equivalent). |
| `type-widened` | A column whose values do not fit its declared type (a `DATETIME` holding `2016-07-04`, a `NUMERIC(5,2)` holding `1.255`, a `FLOAT4` holding `16777217`, a `JSONB_TEXT` that jsonb would write differently, a `TIMESTAMPTZ` with offset `+02`, a `DECIMAL_TEXT` holding `-0.00`, a `TEXT` holding a NUL byte, a column of mixed storage classes). It is carried in a type that holds every value (`text`, `double precision`, `json`, `bytea`, `numeric`), the declared type is recorded on the column, and the values are unchanged. A text with a NUL byte makes the whole column `bytea`, and a copy back to SQLite returns BLOBs. |
| `values-normalized` | A value the target cannot hold: PostgreSQL NaN becomes NULL in SQLite and a negative zero becomes 0 (counted per column); a `char(n)` is copied with PostgreSQL's blank padding. |
| `default-translated` | A `CURRENT_TIMESTAMP` / `CURRENT_DATE` / `CURRENT_TIME` default of a text column is written with `to_char(... AT TIME ZONE 'UTC')` in the same text format. |
| `type-approximated` | A PostgreSQL type SQLite cannot hold (arrays, enums, ranges, ...): carried as its text form. |
| `default-not-carried` | A default expression the target cannot take (`(1+2)`, `nextval(..)`). |
| `identity-not-carried` | An identity column that is not a single integer primary key (SQLite cannot auto-number it); a sequence whose start or step is not 1 (the next value is carried, the step is not). |
| `generated-column` | The expression of a generated column; its values are copied as an ordinary column. |
| `collation-not-carried`, `storage-option-not-carried` | Collations; `WITHOUT ROWID`, `STRICT`, partitioning. |
| `comment-not-carried` | PostgreSQL column comments (SQLite has none). |
| `fk-not-validated` | Rows violate a foreign key (the source holds orphans, or `--where` / `--limit` left a parent out): it is created `NOT VALID` (enforced from now on), and the warning keeps the server's message. |
| `fk-violations-in-source` | A SQLite target holds rows that violate a foreign key (copied as they are, or left without a parent by a filter). |
| `fk-not-carried` | A foreign key into a table that is not part of the copy, into another schema, or that the target refused. |
| `object-not-carried` | Virtual tables, foreign tables, row level security policies, exclusion constraints; the `_dalgo_time_columns` table an earlier `db copy` wrote (name it with `--include` to copy it). |

## Type mapping

SQLite -> PostgreSQL decides from the **declared type and the values**: every column is scanned
once before anything is written, and a column whose values do not fit the PostgreSQL type that its
declared type suggests is carried in a type that holds them (a `type-widened` warning). The declared
type is stored in a column comment (`datatug:{"src":"sqlite","type":"DATETIME",...}`) so the way
back restores it.

| SQLite declared type | PostgreSQL | Condition |
|---|---|---|
| `INTEGER`, `BIGINT`, `INT`, ... | `bigint` | all values integers |
| `INT2` / `INT4` | `smallint` / `integer` | values in range |
| `INTEGER PRIMARY KEY [AUTOINCREMENT]` | `bigint GENERATED BY DEFAULT AS IDENTITY` | counter set past the highest id and `sqlite_sequence` |
| `REAL`, `FLOAT`, `DOUBLE` / `FLOAT4` | `double precision` / `real` | integers and reals; for `FLOAT4` every number is held by a 4-byte real (`1.1` is, `16777217` and `0.30000000000000004` are not: `double precision`) |
| `NUMERIC(p,s)`, `DECIMAL(p,s)`, `DECIMAL_TEXT(p,s)` | `numeric(p,s)` | every value fits p, s exactly (text values have exactly s decimals) and numeric writes every text as it is (`-0.00`, `.5` and `007.5` are text for a `DECIMAL_TEXT`: the column is `text`) |
| `NUMERIC`, `DECIMAL`, `MONEY` | `numeric` | numbers or numeric text |
| `BOOLEAN` | `boolean` | values 0 / 1 |
| `DATE` | `date` | text `YYYY-MM-DD` |
| `DATETIME`, `TIMESTAMP` | `timestamp` | text `YYYY-MM-DD HH:MM:SS[.ffffff]` without trailing fractional zeros |
| `TIMESTAMPTZ` / `TIME` | `timestamptz` / `time` | text in PostgreSQL's output form; for `TIMESTAMPTZ` the offset is `+00` (any other offset would be rewritten to UTC: `text`) |
| `CHAR(n)`, `VARCHAR(n)`, `NVARCHAR(n)` | `varchar(n)` | no value longer than n |
| `TEXT`, `CLOB`, `STRING` | `text` | text without NUL |
| `BLOB` | `bytea` | blobs |
| `JSON_TEXT`, `JSONB_TEXT`, `UUID_TEXT` | `json`, `jsonb`, `uuid` | valid values; for `JSONB_TEXT` every text is in the spelling jsonb writes (`{"a": 1, "bb": [1, 2]}`: keys by length then bytes, one space after `,` and `:`, no exponent, no duplicate key), else the column is `json`, which keeps every byte |
| no declared type, other names | inferred from the values | text for mixed values |

PostgreSQL -> SQLite writes a declared type that SQLite stores faithfully and that maps back
exactly (a column that came from SQLite gets its recorded declared type back):

| PostgreSQL | SQLite declared type |
|---|---|
| `smallint`, `integer`, `bigint` | `INT2`, `INT4`, `INT8` |
| identity / `serial` single-column primary key | `INTEGER PRIMARY KEY AUTOINCREMENT` |
| `real`, `double precision` | `FLOAT4`, `FLOAT8` |
| `numeric(p,s)` / `numeric` | `DECIMAL_TEXT(p,s)` / `DECIMAL_TEXT` (exact text, the convention of the demo databases) |
| `boolean` | `BOOLEAN` (0 / 1) |
| `text`, `varchar(n)`, `char(n)` | `TEXT`, `VARCHAR(n)`, `CHAR(n)` |
| `bytea` | `BLOB` |
| `date`, `timestamp`, `timestamptz`, `time` | `DATE`, `TIMESTAMP`, `TIMESTAMPTZ`, `TIME` (text) |
| `json`, `jsonb`, `uuid` | `JSON_TEXT`, `JSONB_TEXT`, `UUID_TEXT` |
| anything else | `TEXT` (`type-approximated`) |

## Values

Values are copied without the driver's help: dates and timestamps travel as the text they are
(`'1968-12-08'` stays `1968-12-08`; it is never turned into a Go `time.Time` and re-formatted), numbers
as exact decimal text, blobs as bytes (an empty blob stays an empty blob, never NULL). A SQLite ->
SQLite copy keeps the storage class of every value. The source is read in one transaction (a
snapshot: PostgreSQL `REPEATABLE READ`; SQLite one read transaction, so in WAL mode a writer can
commit meanwhile and the copy still holds one moment, and in rollback-journal mode a writer waits
for the copy), and a PostgreSQL source is read with `DateStyle`, `IntervalStyle`, `extra_float_digits`
and the time zone pinned and no statement timeout, so what the server is set to print does not
change the copy. The copy ends with a check that the rows read are the rows the snapshot counted
and that the target holds the rows written; a mismatch rolls everything back.

## Order, atomicity, overwrite

1. Plan: read the source catalog, pre-scan the values, translate, validate names (PostgreSQL: at most
   63 bytes) - nothing has been written yet, so a refusal here changes nothing.
2. One transaction on the target (both engines have transactional DDL): conflict check or drop
   (`--overwrite recreate`), `CREATE TABLE` (columns, primary key, unique constraints), load all
   rows, set auto-increment counters, then indexes, check constraints and foreign keys. Keys and
   indexes follow the data so the load is fast and independent of table order.
3. Indexes, checks and foreign keys that fail because of the data or their spelling (SQLSTATE class
   22, 23, 42) are warnings with the server's own reason, not failures; each is tried in a
   savepoint. A foreign key that existing rows violate (23503) is created `NOT VALID`. A statement
   that fails for any other reason (a timeout, a lock, a lost connection) fails the copy.
4. Commit. Any failure rolls back: the target is as it was (a SQLite file created by the copy that
   did not complete is empty).

## Reload

`--overwrite reload` keeps the target tables (their columns, types, keys, indexes, extra columns) and
replaces their rows: PostgreSQL `TRUNCATE ... RESTART IDENTITY` (SQLite `DELETE`) and the load run in
the one transaction, so a failure leaves the old rows. Before anything is written each target table is
checked: the source columns must exist, the primary key must be the same, a column must be of a kind
the values fit, and an extra target column must be nullable or have a default; otherwise exit 1 and
nothing changes. PostgreSQL tables are loaded parents first, so the foreign keys of the target hold
(a cycle of keys needs `DEFERRABLE` keys). A source table the target lacks is created. Views and
triggers of a SQLite source are replaced by the source's.

## Filters

`--where Artist:ArtistId:<:10 --limit Album:5` copies the artists below 10 and 5 albums. Values are
coerced to the column's type (integer, number, `true`/`false`, `YYYY-MM-DD` or RFC 3339 for dates);
`in` takes a comma list (`--where Customer:Country:in:UK,FR`); several `--where` of a table are ANDed;
the limit is applied after the filter in primary-key order. The filter runs in the source `SELECT`,
not after it. A table that is filtered can leave a foreign key without its parent rows: on PostgreSQL
the key is created `NOT VALID`, on SQLite the orphans are counted, and the warning says the filter
is the reason.

## `--json`

```json
{
  "command": "db copy", "status": "ok-with-warnings", "dryRun": false,
  "from": {"engine": "sqlite", "display": "sqlite:///data/northwind.sqlite"},
  "to": {"engine": "postgres", "display": "postgres://127.0.0.1/nw", "schema": "public"},
  "tables": [{"name": "Orders", "rows": 830, "columns": 14, "primaryKey": ["OrderID"],
              "foreignKeys": 3, "indexes": 0, "uniqueConstraints": 0, "checkConstraints": 0}],
  "totals": {"tables": 13, "rows": 3310, "columns": 88, "primaryKeys": 13, "foreignKeys": 13,
             "indexes": 0, "uniqueConstraints": 0, "checkConstraints": 7},
  "warnings": [{"kind": "view-not-carried", "object": "Invoices", "message": "..."}],
  "plan": ["CREATE TABLE ..."],
  "exitCode": 0
}
```

`status` is `ok`, `ok-with-warnings`, `dry-run` or `failed` (then `error` is set). A failure that
comes before any copy (a bad flag, a side that cannot be opened) prints the same object with
`status: failed`, so a script can always read stdout. Exit codes: 0 done (warnings allowed), 1 failed
(nothing changed), 2 usage, 3 warnings with `--fail-on-warning`, 4 a side could not be opened.

## Documented differences after a round trip

* A SQLite primary key column that was declared without `NOT NULL` comes back `NOT NULL` (PostgreSQL requires it; a column that holds NULL refuses the copy before anything is written). An `INTEGER PRIMARY KEY` comes back `NOT NULL`, and a `DEFAULT NULL` comes back without its default (the same thing).
* A long index or constraint name (over 63 bytes) is shortened or left to PostgreSQL's own name, with a warning; a table or column name over 63 bytes refuses the copy.
* `serial` becomes an identity column; `integer` / `smallint` identity and key columns come back as
  `bigint` (SQLite integers are 64-bit); `char(n)` comes back as `varchar(n)` (SQLite does not pad).
* A column of mixed storage classes (an integer and a text in one untyped column) comes back as text.
* A `CURRENT_TIMESTAMP` default of a PostgreSQL `text` column becomes SQLite's UTC text form.
* Everything listed in the warnings of the first leg.

## Proving a copy

`faithful.Verify` (package `pkg/dbcopy/faithful`; it reads empty blobs and the default of a text column exactly, so neither hides) compares two databases of one engine: declared
types, nullability, defaults, keys, foreign keys, unique sets, indexes, check counts, row counts and
a digest of every row. The round-trip tests (`go test ./pkg/dbcopy/faithful -run TestRoundTrip`) run
SQLite -> PostgreSQL -> SQLite and PostgreSQL -> SQLite -> PostgreSQL on the demo databases when
`DATATUG_TEST_POSTGRES_URL` (a local server), `DATATUG_DEMO_SQLITE` (`name=path,...`) and
`DATATUG_PG_SAMPLES_DIR` are set; `datatug compare` over the copies is the second check, and
`pkg/dbcopy/faithful/testdata/chk.py` is an independent checker (it shares no code with the package:
it reads every column's storage class and value through python's `sqlite3` or `psycopg`, and compares
catalogs) that the journey test `TestPostgresExactValuesRoundTrip` runs when `python3` is available.
