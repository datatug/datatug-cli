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
| `--include` / `--exclude` | Copy some tables. A foreign key into a table that is not copied is not created, and a warning says so. |
| `--from-schema` / `--to-schema` | PostgreSQL schema to read (default `public`) / to write (default `public`, created when missing). |
| `--fail-on-warning` | Exit 3 when anything could not be carried exactly (the copy is still made; combine with `--dry-run` for a gate). |

`--where`, `--limit` and column rules are not available for these copies (they copy whole tables);
`--overwrite reload` is refused (exit 2) until it has a faithful implementation.
A target SQLite file is created when it is missing (its folder too), unless its path looks like a
URL or holds a query string.

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
| `type-widened` | A column whose values do not fit its declared type (a `DATETIME` holding `2016-07-04`, a `NUMERIC(5,2)` holding `1.255`, a column of mixed storage classes). It is carried in a wider type (usually `text`), the declared type is recorded on the column, and the values are unchanged. |
| `type-approximated` | A PostgreSQL type SQLite cannot hold (arrays, enums, ranges, ...): carried as its text form. |
| `default-not-carried` | A default expression the target cannot take (`(1+2)`, `nextval(..)`). |
| `identity-not-carried` | An identity column that is not a single integer primary key (SQLite cannot auto-number it). |
| `generated-column` | The expression of a generated column; its values are copied as an ordinary column. |
| `collation-not-carried`, `storage-option-not-carried` | Collations; `WITHOUT ROWID`, `STRICT`, partitioning. |
| `comment-not-carried` | PostgreSQL column comments (SQLite has none). |
| `fk-not-validated` | The source holds rows that violate a foreign key: it is created `NOT VALID` (enforced from now on). |
| `fk-violations-in-source` | A SQLite target holds rows that violate a foreign key (copied as they are). |
| `fk-not-carried` | A foreign key into a table that is not part of the copy, into another schema, or that the target refused. |
| `object-not-carried` | Virtual tables, foreign tables, row level security policies, exclusion constraints. |

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
| `REAL`, `FLOAT`, `DOUBLE` / `FLOAT4` | `double precision` / `real` | integers and reals |
| `NUMERIC(p,s)`, `DECIMAL(p,s)`, `DECIMAL_TEXT(p,s)` | `numeric(p,s)` | every value fits p, s exactly (text values have exactly s decimals) |
| `NUMERIC`, `DECIMAL`, `MONEY` | `numeric` | numbers or numeric text |
| `BOOLEAN` | `boolean` | values 0 / 1 |
| `DATE` | `date` | text `YYYY-MM-DD` |
| `DATETIME`, `TIMESTAMP` | `timestamp` | text `YYYY-MM-DD HH:MM:SS[.ffffff]` without trailing fractional zeros |
| `TIMESTAMPTZ` / `TIME` | `timestamptz` / `time` | text in PostgreSQL's output form |
| `CHAR(n)`, `VARCHAR(n)`, `NVARCHAR(n)` | `varchar(n)` | no value longer than n |
| `TEXT`, `CLOB`, `STRING` | `text` | text without NUL |
| `BLOB` | `bytea` | blobs |
| `JSON_TEXT`, `JSONB_TEXT`, `UUID_TEXT` | `json`, `jsonb`, `uuid` | valid values |
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
as exact decimal text, blobs as bytes. A SQLite -> SQLite copy keeps the storage class of every
value. The copy ends with a row count check per table; a mismatch rolls everything back.

## Order, atomicity, overwrite

1. Plan: read the source catalog, pre-scan the values, translate, validate names (PostgreSQL: at most
   63 bytes) - nothing has been written yet, so a refusal here changes nothing.
2. One transaction on the target (both engines have transactional DDL): conflict check or drop
   (`--overwrite recreate`), `CREATE TABLE` (columns, primary key, unique constraints), load all
   rows, set auto-increment counters, then indexes, check constraints and foreign keys. Keys and
   indexes follow the data so the load is fast and independent of table order.
3. Indexes, checks and foreign keys the target refuses are warnings, not failures; each is tried in
   a savepoint. A foreign key that existing rows violate is created `NOT VALID`.
4. Commit. Any failure rolls back: the target is as it was (a SQLite file created by the copy that
   did not complete is empty).

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

`status` is `ok`, `ok-with-warnings`, `dry-run` or `failed` (then `error` is set). Exit codes: 0 done
(warnings allowed), 1 failed (nothing changed), 2 usage, 3 warnings with `--fail-on-warning`, 4 a
side could not be opened.

## Documented differences after a round trip

* A SQLite primary key column that was declared without `NOT NULL` comes back `NOT NULL` (PostgreSQL requires it; a column that holds NULL refuses the copy before anything is written).
* `serial` becomes an identity column; `integer` / `smallint` identity and key columns come back as
  `bigint` (SQLite integers are 64-bit); `char(n)` comes back as `varchar(n)` (SQLite does not pad).
* A column of mixed storage classes (an integer and a text in one untyped column) comes back as text.
* A `CURRENT_TIMESTAMP` default of a PostgreSQL `text` column becomes SQLite's UTC text form.
* Everything listed in the warnings of the first leg.
