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
| `--decimals numeric\|text` | SQLite -> PostgreSQL: how a `DECIMAL_TEXT` column whose texts numeric spells differently is carried. `numeric` (default) writes numeric's spelling and says how many values changed; `text` keeps the text byte for byte. |
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
| `type-widened` | A column whose values do not fit its declared type (a `DATETIME` holding `2016-07-04`, a `NUMERIC(5,2)` holding `1.255`, a `FLOAT4` holding `16777217`, a `JSONB_TEXT` that jsonb would write differently, a `TIMESTAMPTZ` with offset `+02`, a `TEXT` holding a NUL byte, a column of mixed storage classes). It is carried in a type that holds every value (`text`, `double precision`, `json`, `bytea`, `numeric`), the declared type is recorded on the column, and the values are unchanged. A text with a NUL byte makes the whole column `bytea`, and a copy back to SQLite returns BLOBs. |
| `values-normalized` | (PostgreSQL -> SQLite) a numeric of more than 15 significant digits written into a column of numeric affinity is rounded by SQLite: counted. An integer numeric is bound as an integer and never passes through a double. |
| `values-normalized` | A `DECIMAL_TEXT` written in numeric's spelling (`.0000` -> `0.0000`, `-0.00` -> `0.00`, `1.5` -> `1.50` in `numeric(p,2)`): the count and the first example per column (`--decimals=text` keeps the text). A value the target cannot hold: PostgreSQL NaN becomes NULL in SQLite (a `numeric` NaN in a numeric-affinity column stays its text) and a negative zero becomes 0 (counted per column); a `char(n)` is copied with PostgreSQL's blank padding. |
| `default-translated` | A text default of a `DECIMAL_TEXT` column becomes the numeric default (`'0.00'` -> `0.00`; the same number, written back as text); when numeric would store another number (`'1.5'` into scale 2, `'+1.5'`, `'1e2'`) the warning says which one new rows get. A `CURRENT_TIMESTAMP` / `CURRENT_DATE` / `CURRENT_TIME` default of a text column is written with `to_char(... AT TIME ZONE 'UTC')` in the same text format. |
| `type-approximated` | A PostgreSQL type SQLite cannot hold (arrays, enums, ranges, ...): carried as its text form. |
| `default-not-carried` | A default expression the target cannot take (`(1+2)`, `nextval(..)`). |
| `identity-not-carried` | An identity column that is not a single integer primary key (SQLite cannot auto-number it); a sequence whose start or step is not 1 (the next value is carried, the step is not); a reload into a column that has no single sequence to move, whose sequence would have to go beyond its maximum (`CYCLE` at `MAXVALUE`, a `bigint` at the top), or that counts down: the counter is not moved. |
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
| `NUMERIC(p,s)`, `DECIMAL(p,s)`, `DECIMAL_TEXT(p,s)` | `numeric(p,s)` | every value fits p, s (integer digits fit, no more than s decimals). A `DECIMAL_TEXT` text that numeric spells differently (`.0000`, `-0.00`, `007.5`, `1.5` for `1.50`) is written in numeric's spelling and counted in a `values-normalized` warning; `--decimals=text` makes such a column `text` instead |
| `NUMERIC`, `DECIMAL`, `MONEY` | `numeric` | numbers or numeric text |
| `BOOLEAN` | `boolean` | values 0 / 1 |
| `DATE` | `date` | text `YYYY-MM-DD` |
| `DATETIME`, `TIMESTAMP` | `timestamp` | text `YYYY-MM-DD HH:MM:SS[.ffffff]` without trailing fractional zeros |
| `TIMESTAMPTZ` / `TIME` | `timestamptz` / `time` | text in PostgreSQL's output form and a time that exists (`23:59:60`, hour `24`, `2021-02-30` stay `text`); for `TIMESTAMPTZ` the offset is `+00` (any other offset would be rewritten to UTC: `text`) |
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
| `numeric(p,s)` / `numeric` | `DECIMAL_TEXT(p,s)` / `DECIMAL_TEXT` (numeric's canonical text, the convention of the demo databases) |
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
nothing changes. On PostgreSQL the foreign keys among the reloaded tables are dropped before the rows
are emptied and added again after the load, so the order of the tables, a cycle of keys and a key
that was created `NOT VALID` cannot stop a reload; a key the loaded rows violate (a filter left a
parent out, the source has orphans) is created `NOT VALID` again and reported, a key they satisfy is
validated. Each of those keys comes back with the definition the server had given it (match type, the
`SET NULL (columns)` list, deferral, enforcement, comment): the reload reads `pg_get_constraintdef`
before it drops the key and replays that text; a key that cannot be added back for any reason other
than the loaded rows fails the reload, which is rolled back. A key to a table that is *not* reloaded
(`--include child`) is not touched and stays enforced: a source row without its parent is then a
failure of the reload (23503, nothing changes), not a `NOT VALID` key. The counters of auto-numbered
columns are set with `setval` last in the transaction (a rollback does not undo a `setval`, so nothing
that can fail comes after it), on the sequence the column's `DEFAULT nextval('s')` takes values from (a serial
column's own, or another one the default was changed to), or the one an identity column owns. A sequence is
only ever moved *forward*, by one statement that reads it (its last value, plus its step once it has been
used; its start, which is its last value, while it has not) and decides: a sequence already past the rows (also
one that is set but not called yet) is not touched at all; one behind them is set to the id after the highest
copied one, in the same statement that found it behind. A sequence that several tables share is therefore not
moved back by the reload of one of them. The reload locks only the tables it reloads: another session that
writes to a table sharing the sequence during the reload can still lose a value in the instant of that one
statement (a repeated stress run of 20 seconds over 1,600 reloads saw none), so a shared sequence is best
reloaded while it is quiet. A sequence that cannot be set to the value (it would be beyond its maximum, as a
`CYCLE` sequence at its `MAXVALUE` or a `bigint` at the top) or that counts down (a negative step) is left
alone and an `identity-not-carried` warning says so; the copy does not fail. A target column with no
sequence, or with more than one, is an `identity-not-carried` warning too (its counter is not moved), never
silent. Reading the sequence needs the `SELECT` privilege on it, as `setval` needs `UPDATE`: a role that has
neither fails the reload (rolled back) with the server's message. A source table the target lacks is created.
Views and
triggers of a SQLite source are replaced by the source's.

Reload takes `ACCESS EXCLUSIVE` locks (`TRUNCATE`, the dropped keys) and waits as long as another
transaction holds a lock on those tables: it has no timeout of its own. A `REPEATABLE READ` snapshot
that another session opened before the reload sees the reloaded tables as empty afterwards (`TRUNCATE`
is not MVCC-safe). A column that an earlier copy created as text because of `--decimals=text` is
refused by a reload that is run without the flag (its values are text, numeric would not hold them as
they are).

## Values that are not carried exactly, and why

* A `DECIMAL(p,s)` column whose *default* does not fit `numeric(p,s)` (`1.005` into scale 2, `12345`
  into `(3,1)`) is carried as plain `numeric`, so that new rows get the number SQLite inserts; the
  warning says it is the default. A text default of a `DECIMAL_TEXT` that numeric pads (`'1.5'` into
  scale 2) says what new rows get (`1.50`), and a PostgreSQL `numeric(10,3) DEFAULT 1.5` copied to
  SQLite says that PostgreSQL stores `1.500` where SQLite inserts the text `1.5`.
* A date, timestamp or timestamptz in year `0000` is carried as text: PostgreSQL has no year 0.
* `+-Infinity` of a REAL in a DECIMAL column makes that column text (PostgreSQL 14+ `numeric` could
  hold them; this copy does not rely on it); a PostgreSQL `numeric` `NaN` that comes back into a SQLite
  column of numeric affinity (a `DECIMAL` an earlier copy wrote) stays its text `NaN`, exactly, and is
  counted per column in a `values-normalized` warning; a `numeric` column that comes back as text keeps
  `NaN` / `Infinity` as they are, and there is nothing to say.
* `--decimals=text` applies to SQLite to PostgreSQL copies only; any other copy refuses it (exit 2).
* The values-normalized count of a `DECIMAL_TEXT` is the count for the type the column gets:
  `numeric(p,s)` pads to the scale; a column some value of which does not fit it is a plain `numeric`,
  which does not.
* Catalog respellings that mean the same thing (`bpchar` and `char`, `int4` and `integer`, the way
  an engine prints a default or a check expression) are not warned: `db copy` compares what a column
  holds and the structure, not the text of the DDL.
* `Verify` compares checks by count, and does not compare comments or the position of a sequence:
  each engine writes its own names and counters. It compares the deferral, match type and validity of a
  foreign key, not PostgreSQL 18's NOT ENFORCED.

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

Equivalent catalog spellings that are not warned one by one (the meaning is the same): a regex `CHECK` written `\-` comes back `-`; a cast `::integer` comes back `::bigint`; `NULLS LAST` on a descending index comes back plain `DESC`; a PostgreSQL unique *index* comes back as a UNIQUE constraint; `serial` / `integer` identity comes back as a `bigint` identity; `GENERATED ALWAYS` identity comes back `BY DEFAULT`; the numeric default of a text column in SQLite (`DEFAULT 0.00` inserts `'0.0'`) is written as the text `'0.00'` and is warned.

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

## SQL script destination: `sqlscript://` (MySQL)

`datatug db copy --from sqlite://<file> --to 'sqlscript:///path/out.sql?dialect=mysql'` does not copy into a
database: it writes a **SQL script** that creates the same database in an engine. The script is made from the
SQLite file alone (no server is opened), it is the same bytes on every run of one datatug version, and everything
that could not be carried is a warning, as for any other `db copy` (`--json`, `--dry-run`, `--fail-on-warning`).

```sh
datatug db copy --from sqlite://./chinook.sqlite --to 'sqlscript:///tmp/chinook.mysql.sql?dialect=mysql&license=MIT'
mysql --default-character-set=utf8mb4 --skip-commands --skip-system-command --database chinook < /tmp/chinook.mysql.sql
```

The destination is a path in the URL (`sqlscript:///abs/path.sql`, or `sqlscript:rel/path.sql`) and its query:

| Parameter | Meaning |
|---|---|
| `dialect` | Required. `mysql` is implemented; `mssql`, `oracle`, `postgres` and `sqlite` are planned and refused with the list of the implemented ones. |
| `name` | Dataset name in the header and in the completion marker (default: the SQLite file name without its extension). At most 255 characters. |
| `edition` | A label for the header, such as `DemoDB contract edition, identical to the SQLite/CSV/JSON`. At most 255 characters. |
| `license`, `attribution` | Licence and attribution lines of the header (a demo database's `manifest.json` `source.license`). |

The query is parsed strictly (`net/url.ParseQuery`): a value is never dropped silently. A `;` or a stray `&` inside a value
(`attribution=Data by X; converted by Y`) is an error (exit 2), because a parser that ignores what it cannot read would
lose the attribution a licence requires from the header: write `%3B` for `;` and `%26` for `&`. A parameter given twice is
refused (exit 2), `dialect` included. A `name` or `edition` that is too long, not valid UTF-8 or that holds a NUL is refused
up front (exit 2): the marker row is the last statement of the script, and one that cannot hold it would fail after every row
was loaded. The datatug version in the marker is cut to the marker's 64 characters (the header comment has it whole).

Flags: `--include`, `--exclude`, `--dry-run`, `--json`, `--fail-on-warning` work as for any copy (a key into a table that
is not written is not created, with a warning). The source must be `sqlite://`. A script file is never replaced silently:
an existing file is refused (exit 1) unless `--overwrite=recreate` is given (`reload` does not apply); `--from` and
`--to` naming one file is exit 2, and a table named by `--include` or `--exclude` that the source lacks is exit 2. The script is
written to a temporary file beside the destination and put in place only when it is complete, so a failure leaves
nothing. A new file has the mode of any new file under your umask (0644 with the usual 022); a file replaced with
`--overwrite=recreate` keeps its mode. It is put in place with a hard link, which fails when the name is taken; on a file
system without hard links (FAT, exFAT, some network shares) the destination is created exclusively (`O_EXCL`) and the script
is copied into it, and a failed copy removes the file it made, so nothing that was there is ever replaced. `--where`, `--limit`, `--filter-config`, `--from-schema`, `--to-schema`, `--decimals`, `--parallel-streams`
and `--progress` are refused with exit 2: a script holds every row of the tables it writes.

### What the script is

* UTF-8, no byte order mark, LF line ends. **No literal holds a raw CR or LF** and no value can start a line of the
  script: every line starts with a token the generator wrote (`--`, `SET`, `CREATE`, `INSERT`, `(`, `START`, `COMMIT`, `ALTER`,
  two spaces inside `CREATE TABLE`). A text with a line break, any other control character, a Unicode line or
  paragraph separator or a backslash is written as `_utf8mb4 X'..'` (a backslash because the mysql client reads
  `\` as an escape inside every quoted string, whatever the server's SQL mode), a blob as `X'..'`, floats as exponent
  literals (`1.5e+00`). So a value such as `"\nGO"`, `"\n\! cmd"` or `"\nsource x"` cannot become a client command.
  The header recommends `--skip-commands` (mysql 8.4.6 and later) and `--skip-system-command` on top of that, and says that a
  blob is written in hex (twice its size), so one value over about 8 MB needs `--max-allowed-packet=1G`: the client's
  default is 16 MB.
* A header of comments: the label, dataset, datatug version and the SHA-256 of the SQLite file, licence and attribution,
  the minimum MySQL version (8.0.16, where CHECK constraints are enforced; verified on MySQL 8.4 LTS), how to load it
  safely, the tables, and every warning.
* `SET NAMES utf8mb4` and a session `sql_mode` of `STRICT_ALL_TABLES,NO_AUTO_VALUE_ON_ZERO,NO_ENGINE_SUBSTITUTION,NO_ZERO_DATE,NO_ZERO_IN_DATE`
  (`NO_AUTO_VALUE_ON_ZERO` because SQLite allows an id of 0 and MySQL would number it otherwise).
* **The first statement creates the marker table `_demodb_import`** and fails when it exists, so a second load into the
  same database stops at once; **the last statement inserts its row** (dataset, label, source SHA-256, version, tables,
  rows): a row means the load completed. MySQL commits DDL, so a failed load is not rolled back: drop the database (or the
  tables) and load again. `DROP TABLE _demodb_import` removes the marker.
* `CREATE TABLE` (columns, primary key, unique keys, `ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, no collation named), the rows
  of each table in one transaction per batch of at most 1000 rows or about 1 MiB, in a **total order** (primary key and
  then the rowid, so a table without a key has an order too), the counters of the auto-numbered columns, **then**
  indexes, check constraints and foreign keys (keys follow the data; a foreign key validates every row). A table that is
  not part of the script has no foreign key into it.

### Types (the declared type wins when every value fits it)

| SQLite | MySQL | Notes |
|---|---|---|
| `INTEGER`, `INT`... / `INT2` / `INT4` | `BIGINT` / `SMALLINT` / `INT` | wider when a value does not fit (`type-widened`); an `INTEGER PRIMARY KEY` is `AUTO_INCREMENT`, its counter is moved past the highest id and `sqlite_sequence` |
| `REAL`, `FLOAT`, `DOUBLE` | `DOUBLE` | an infinite value makes the column `TEXT` (MySQL has none) |
| a REAL in a column carried as `TEXT` or bytes | the text **SQLite itself** makes of it | `CAST(x AS TEXT)`: `1.0`, `1.0e+21`, `Inf`, `-Inf`, the digits of the SQLite version (`0.3` or `0.30000000000000004`); never Go's `1`, `1e+21`, `+Inf`. The exporter reads such a column as `CASE WHEN typeof(c) = 'real' THEN CAST(c AS TEXT) ELSE c END`, so the spelling is the engine's, not this program's. The length of a bounded type (a key column) counts that text and the decimal text of an INTEGER |
| `NUMERIC(p,s)`, `DECIMAL(p,s)`, `DECIMAL_TEXT(p,s)` | `DECIMAL(p,s)` | when every value fits; otherwise (and for a bare `NUMERIC`/`DECIMAL`/`MONEY`) `DECIMAL(P,S)` computed from the values; beyond 65 digits or 30 decimals `TEXT`. A text that DECIMAL spells differently (`.0000` becomes `0.0000`) is counted in `values-normalized`, and so are the REAL values of a DECIMAL column (SQLite keeps a binary float, and an INTEGER 5 and a REAL 5.0 apart; DECIMAL holds exact decimals: Chinook's money, Northwind's and Pubs' prices, Sakila's `payment.amount`). The values are numerically the same; the warning says the storage differs |
| `BOOLEAN` | `TINYINT(1)` | |
| `DATE` / `DATETIME`, `TIMESTAMP` / `TIME` | `DATE` / `DATETIME(f)` / `TIME(f)` | `f` is the longest fraction; a value that is not in MySQL's form, before year 1000 or a date in a `DATETIME` column becomes `TEXT` |
| `TIMESTAMPTZ`, `JSON_TEXT`, `UUID_TEXT` | `TEXT` | MySQL has no such type; `type-widened` |
| `CHAR(n)`, `VARCHAR(n)`, `NVARCHAR(n)` | `VARCHAR(n)` | n up to 16383 |
| `TEXT` | `TEXT`, `MEDIUMTEXT`, `LONGTEXT` | by the longest value in bytes |
| `BLOB` | `BLOB`, `MEDIUMBLOB`, `LONGBLOB` | |
| any text in a **key**, any blob in a key | `VARCHAR(n)`, `VARBINARY(n)` | `key-type-bounded`: MySQL cannot index an unbounded type. 255 characters when the key fits InnoDB's 3,072 bytes, the length of the longest value otherwise |
| text with a NUL byte, text that is not valid UTF-8, a column of text and blobs | `BLOB` (bytes) | `type-widened`; **policy**: a utf8mb4 column cannot hold such a text, so the whole column is bytes and the values are exact |
| no declared type, unknown types | from the values | |

All the columns of a foreign key equivalence class (the child columns and the parent columns they reference,
transitively) get **one type before anything is written**: the widest integer, the longest text, the largest
precision and fraction, bytes if any member is bytes, text if the families differ (`type-widened` for each member that changed).
A row that would exceed 65,535 bytes makes the widest unkeyed `VARCHAR` columns `TEXT` (`row-size-bounded`). A key that
cannot fit 3,072 bytes even at the length of its longest values is refused for a primary key, a unique key and a
foreign key, and dropped for a plain index (`index-not-carried`).

### Names

Every table, column and index name is kept **byte for byte** as one backtick-quoted identifier (`Order Details`,
`Production.Product`, a backtick doubled, reserved words, Unicode, up to 64 characters). A name MySQL cannot store is the
only reason to change one, and every change is a counted `name-renamed` (`index-renamed` for an index) warning: an empty
name, a name that ends in a space, a name longer than 64 characters, a name with a control or line-break character (a
script line must never carry one), a name with a character outside the Basic Multilingual Plane (MySQL **silently stores it
as `?`**, so two such names would meet), and a column that differs from another of its table **by case** (`Ä` and `ä`; SQLite
already refuses `a` and `A`). MySQL 8.4 compares the names of columns and indexes without case but **with every accent**:
`e`, `é`, `ünï` and `uni` are four columns and are all kept (the journey creates them on a real server), and `ß` and `ẞ` are
two. MySQL's case table is older than Go's: it was read off a real 8.4.11 server (670 characters, `mysql_fold.go`), so a
pair Go would fold and MySQL does not (`ß`/`ẞ`) is not renamed. The new name is the old one with each unusable character replaced by `_`,
cut to fit, and ended by `_` and 8 hex digits of the SHA-256 of the original, so it is the same on every run. Two table names that differ only by case load on a
server with case-sensitive table names (`lower_case_table_names=0`, the Linux default) and fail on macOS and Windows
defaults: a `name-case-collision` warning says so. An index name that is `PRIMARY` or taken by another key of the table is
renamed. A primary key's own name is not carried (MySQL calls it `PRIMARY`); the name of a foreign key, unique key or check
is carried when MySQL can use it (unique in the schema for a foreign key or check, at most 64 characters) and is
`constraint-name-not-carried` otherwise.

### Keys, defaults, checks

* A foreign key needs a primary or unique key of its parent (MySQL 8.4 `restrict_fk_on_non_standard_key`); otherwise it is
  `fk-not-carried`. A key whose rows the source itself violates is not created (`fk-violations-in-source`), because
  MySQL would refuse it. `SET DEFAULT`, `SET NULL` on a `NOT NULL` column, and `DEFERRABLE` are carried as the nearest
  thing MySQL has and say so (`fk-action-not-carried`). A partial index is `index-not-carried`.
* Defaults: numbers, quoted texts (in the `DEFAULT (..)` form for `TEXT`/`BLOB` columns, which MySQL needs), booleans.
  `CURRENT_TIMESTAMP`/`CURRENT_DATE`/`CURRENT_TIME` become `UTC_TIMESTAMP()`-based defaults in the column's own form: a
  `DATETIME` gets `UTC_TIMESTAMP()`, a **text** column gets `DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d %H:%i:%s')` (SQLite's
  text format, in UTC; `default-translated`). Anything else is `default-not-carried`.
* Checks: `GLOB` becomes `REGEXP_LIKE(col, '^..$', 'cn')` (case-sensitive, as GLOB is), a comparison or `IN` list of a text
  column gets `COLLATE utf8mb4_bin` (SQLite compares exactly; MySQL's default collation ignores case and accents),
  `length()` is `CHAR_LENGTH()`, `[x]` names are backtick-quoted. An expression the translator does not understand, or MySQL
  does not allow (a column of an `AUTO_INCREMENT`, or of a foreign key with a referential action), is `check-not-carried`; a comparison
  that stays case-insensitive is `collation-not-carried`.
* Not carried at all: views and triggers across engines (`view-not-carried`, `trigger-not-carried`), expression indexes,
  collations, `WITHOUT ROWID`/`STRICT`.

### What a person must know about MySQL's defaults

Text comparison in MySQL is case- and accent-insensitive by default. A key that differs only by case or accents in
SQLite is a duplicate there and **stops the load** (the script does not hide it). The demo databases have no such key.
The export looks for them: for every primary key, unique key and unique index with a text column it reads the rows once and
warns, `key-values-collide` (a counted kind, with the number of rows and the first pair), when two rows have keys that are
equal after folding case, accents and `ß`, `æ`, `œ` (the key values with `NULL` never collide). It is a conservative scan of what
a person meets (`Smith` and `SMITH`, `José` and `Jose`), not MySQL's collation: ignorable characters, compatibility forms and
Han variants are not folded, a decimal spelled two ways or an INTEGER and its text in one column are not looked for, so a
key it passes can still collide; the header says so.
`lower_case_table_names` (see above) and MariaDB are not tested here.

### How it is proved

`scripts/check-journey-mysql.sh` (the non-required workflow `.github/workflows/journey-mysql.yml`, path-filtered) starts
the digest-pinned `mysql:8.4` image on a private port; for the edge-case fixture
(`pkg/dbcopy/faithful/testdata/mysql_edge.sql`: hostile names and values, every type, composite, self-referencing and
text keys), for a fuzz fixture of 4,000 rows of hostile text, bytes and names, and for the six demo databases pinned in
`scripts/journey-datasets.txt` it exports twice (the two scripts must be the same bytes), loads the script with the
`mysql` client in the container (`--skip-commands`, stop at the first error), checks that a second load stops at the marker
table, and runs the cross-engine verifier. The verifier (`internal/crossverify`, an internal package, deliberately not a
command: its long-term home is an extension of `datatug compare` once compare's known defects are fixed) reads the
MySQL catalog and compares it with the plan the exporter makes for the same SQLite file: tables, columns (type,
nullability, auto-increment, **default by value**), primary key, foreign keys (columns, parent, actions), unique keys and indexes
(**the indexes MySQL creates for a foreign key by itself are modelled and left out**: a plain index on the key's columns named
as MySQL names it, the constraint's name or the first column's), check constraints (**their expressions**), the completion
marker, that no name was renamed without a warning, row counts, and **every value** read through the Go MySQL driver in
its binary protocol (int64, the 64 bits of a float, a DECIMAL as an exact rational, bytes as bytes). The canonical form of
each value in each family is written at the top of `internal/crossverify/canon.go`: a REAL of a `NUMERIC(10,2)` column
is read as its shortest decimal (`FormatFloat(x, 'f', -1, 64)`), an INTEGER or REAL in a `NUMERIC` column compares as the
same number, a date or time only as text in the column's own format with the fraction's trailing zeros removed.
A **default** is compared by what it means: SQLite evaluates the default expression itself (`SELECT <expr>`), the value is put
in the canonical form of the column's family, and MySQL's `COLUMN_DEFAULT` is parsed into the same form (a `CURRENT_*` default
must read the clock in UTC in the column's own form). A **check** is compared with what SQLite's expression requires of the
clause in `information_schema.CHECK_CONSTRAINTS`, never with the plan's own translation (a translator that loses a `COLLATE` would
lose it from the plan and from the script alike): every comparison, `IN` or `BETWEEN` of a text column is `COLLATE utf8mb4_bin`,
every `GLOB` is a `REGEXP_LIKE`, and every string literal of the expression is still in the clause. A REAL in a text column is
compared with `CAST(x AS TEXT)` read from SQLite, not with a spelling chosen by the verifier or the writer.
Unit tests (100% statement coverage of all new code) include golden scripts, a determinism test, a property test that no
literal holds a raw control character and every literal reads back as its text, and an emulated MySQL for the verifier.

### Design notes

* **Offline literals and DALGO.** The 2026-10-04 resolution of `dalgo-dialect-aware-sql-emission` ("no caller value may ever be
  sent as SQL text") is about `StructuredQuery` values sent to a server. An offline script written from a pinned file, never
  run against a server by DataTug, is outside it; the literal writer is `mysqlDialect.Literal`, the one place a value
  becomes SQL text, and it is fuzzed and round-tripped through a real MySQL.
* **Why not DALGO.** The reason is not `dalgo2mysql`'s identifier check (trivially relaxed): the DALgo `dbschema` model is too
  coarse for a faithful script (`ConstraintDef` has a name and a type only, `ForeignKeyDef` no actions, a single `Time`
  type, no declared type text).
* **Duplication debt.** Identifier quoting and reserved-word knowledge now exist twice: here (`mysqlDialect.QuoteIdent`,
  `StorableName`) and in `dalgo2sql`'s `typed_dialect.go`. Sharing them is a follow-up once the script dialects settle.
* **Done.** `datatug db export` (inGitDB) is now a shortcut over `db copy --to ingitdb://` (see below).

## Local backup to a folder: `db export` and `--to ingitdb://`

`datatug db export` is a shortcut for `datatug db copy --from URL --to ingitdb://FOLDER`: an ergonomic local backup as a
native inGitDB project (one records file per collection, the schema, keys and indexes as descriptive metadata). It is the
same engine as `copy`, so the same filters, report and exit codes apply.

```bash
# the folder is ./shop.ingitdb, named after the SQLite file (a PostgreSQL URL gives the database name)
datatug db export --from sqlite://$PWD/shop.db

# a plain path, or an ingitdb:// URL; both are the same
datatug db export --from sqlite://$PWD/shop.db --to ~/backups/shop-2026-10-16
datatug db copy   --from sqlite://$PWD/shop.db --to ingitdb://$HOME/backups/shop-2026-10-16

# format of every records file: json (default), jsonl, ingr, csv, yaml
datatug db export --from sqlite://$PWD/shop.db --to shop-csv --records-format csv
datatug db copy   --from sqlite://$PWD/shop.db --to 'ingitdb://shop-csv?records-format=csv'

# a part of it, a plan, a machine-readable report
datatug db export --from sqlite://$PWD/shop.db --include orders,customers --where orders:created:>=:2026-01-01 --dry-run --json
```

* **A new folder, published whole.** The destination must not exist. The project is built in a hidden staging folder beside
  it and renamed into place after every collection succeeded; a failure leaves neither. `--dry-run` reads and checks every
  row in a temporary folder, prints the counts of a real run and writes nothing. For `copy`, an `ingitdb://` folder that
  exists is copied into as before; a folder that does not exist is this native export.
* **Default folder.** Without `--to`, `export` writes `./<name>.ingitdb` in the working folder, where the name comes from
  `--from` only: the SQLite file name without its extension, or the PostgreSQL database name (never a host, a user, a
  password or a query). Characters other than letters, digits, `.`, `_` and `-` become `_`; the name is at most 100
  characters. A source with no name (an in-memory SQLite, a PostgreSQL URL without a database) is exit 2: pass `--to`.
* **Filters and report.** `--include`, `--exclude`, `--where`, `--limit` and `--filter-config` are those of `copy`;
  `--json`, `--dry-run` and `--fail-on-warning` print and exit as for the other destinations (`--json` report: the same
  fields; `command` is `db export` or `db copy`). A foreign key to a table `--include`/`--exclude` leaves out is not carried
  and is a `foreign-key-dropped` warning; a key to a table narrowed by `--where`/`--limit` is a `filtered-parent`
  warning; `--fail-on-warning` exits 3 after the project is published. A `--where` or `--limit` on a table that is not
  exported is exit 1.
* **Flags that do not apply** (`--overwrite`, `--to-schema`, `--decimals`, `--parallel-streams`, `--progress`,
  `--recover-job-ref`) are exit 2 with the reason; `?records-format=` on a folder that exists is exit 2.
* **Differences between the two commands.** `export` refuses a folder that exists (exit 1), takes a plain path and the
  default folder, and exits 1 on a source with nothing to write; `copy` into a missing folder prints "source has no tables;
  nothing to copy" and exits 0, like every other copy of an empty source.
* **Compatibility.** `datatug db export --from URL --to ingitdb://DIR [--records-format F]` writes the same bytes, prints
  the same `db export: exported N collections and M rows to ...` line and exits with the same code as before; the test
  `TestExportOldInvocationIsByteIdentical` holds the digests of the earlier binary's output.
* **Not here: cloud or remote backup.** `export` writes a local folder. A backup to a bucket, another server or on a
  schedule belongs to the planned `backup` command family, or to `db copy --to <destination>` where `copy` reaches one
  (PostgreSQL, BigQuery, a SQLite file).
