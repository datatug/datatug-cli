# `datatug db copy` between SQLite and PostgreSQL

`datatug db copy --from <url> --to <url>` copies a database. When both sides are SQLite or
PostgreSQL (`sqlite://`, `postgres://`) the copy keeps what the two engines can both say and
**reports everything it could not carry**; it never drops a feature silently. SQLite -> PostgreSQL
-> SQLite and PostgreSQL -> SQLite -> PostgreSQL give back the database you started with, except
for the differences listed under "Documented differences".

```sh
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
or holds a query string.

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

## Sensitivity labels of a project source

A `--from` that is a project source (`datatug://<project>/<db>`) keeps the project's sensitivity labels.
A copy cannot mask, so `db copy` and `db export` refuse, with exit 5 and `FIELD_PROTECTED`, when a table
they would write has a column protected by a stored label (an owner label or a pending proposal), whoever
runs them. A column that only a read-time suggestion marks sensitive (an `Email` column nobody has labelled
yet) refuses an AI agent; anyone else copies it and gets one warning naming the suggested columns, as with
native SQL. Copy only the unlabelled tables with `--include` (or leave the labelled ones out with
`--exclude`); `--where` and `--limit` choose rows and do not help:

```sh
datatug db copy --from datatug://datatug-demo/chinook --to sqlite://./chinook.sqlite
# access denied: FIELD_PROTECTED: chinook.Customer.Email is protected (pii), and a copy cannot mask ...
datatug db copy --from datatug://datatug-demo/chinook --to sqlite://./music.sqlite --include Genre,MediaType
```

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

## SQL script destination: `sqlscript://` (MySQL, SQL Server and Oracle)

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
| `dialect` | Required. `mysql`, `mssql` (SQL Server, see [below](#sql-server-dialect-mssql)) and `oracle` (see [below](#oracle-dialect-oracle)) are implemented; `postgres` and `sqlite` are planned and refused with the list of the implemented ones. |
| `name` | Dataset name in the header and in the completion marker (default: the SQLite file name without its extension). At most 255 characters for `mysql` (the marker column); `mssql` takes any length. Neither takes invalid UTF-8 or a NUL. |
| `edition` | A label for the header, such as `DemoDB contract edition, identical to the SQLite/CSV/JSON`. The same limits as `name`. |
| `license`, `attribution` | Licence and attribution lines of the header (a demo database's `manifest.json` `source.license`). One line each, written whole: a value that would not fit a header line (see below) is refused. |
| `notice-file` | Path of a text file with the multi-line licence notice, written in the header after those lines (see [below](#licence-notice)). |

The query is parsed strictly (`net/url.ParseQuery`): a value is never dropped silently. A `;` or a stray `&` inside a value
(`attribution=Data by X; converted by Y`) is an error (exit 2), because a parser that ignores what it cannot read would
lose the attribution a licence requires from the header: write `%3B` for `;` and `%26` for `&`. A parameter given twice is
refused (exit 2), `dialect` and `notice-file` included. A `name` or `edition` that is too long, not valid UTF-8 or that holds a NUL is refused
up front (exit 2): the marker row is the last statement of the script, and one that cannot hold it would fail after every row
was loaded. The datatug version in the marker is cut to the marker's 64 characters (the header comment has it whole).

**A header line is never cut.** A header comment line holds at most 400 bytes (after control characters become spaces and runs
of blanks one space; the label counts: `Licence: `, `Attribution: `, `Dataset: `). A `license`, `attribution`, `edition` or `name` whose line would be longer is
refused up front (exit 2, in every dialect, before anything is written), because the exporter used to cut such a line silently
at 400 bytes and end it in `...`: a legally required attribution could be shortened with no warning. The message names the parameter and the size and
points at `notice-file`, which takes up to 16 KiB and is written whole (see below). Values whose line fits are written exactly as before; only inputs over 400 bytes behave
differently (they were cut, they are refused). The machine-made lines of the header (the warnings, the list of tables) can still be cut at 400 bytes; the full list is in `--json`.

#### Licence notice

A script travels without the website that carries its licence, so the header can hold the whole notice a licence asks for:
the copyright line, the permission notice and the disclaimer (MIT); the copyright line, the three conditions and the
disclaimer (BSD-3-Clause); the licence name and link, the copyright notice, the authors, the title, the source link and the
adaptation statement (CC BY-SA 3.0). Put the text in a file and name it:

```sh
datatug db copy --from sqlite://./sakila.sqlite --to 'sqlscript:///tmp/sakila.mysql.sql?dialect=mysql&license=BSD-3-Clause&notice-file=/abs/path/sakila-NOTICE.txt'
```

The path is percent-encoded like any value: write `%20` for a space and `%2B` for a `+` (a literal `+` in the query is read as
a space, so a path with a `+` in it would name another file or none); a relative path is the current folder's. The file is
opened first and the open handle is checked, so it must be a regular file (a FIFO, a device or a folder is refused at once, also
when the path is swapped between a check and the open). It is UTF-8 with no NUL, at most 16 KiB (a leading byte order mark is dropped) and has text in it: a file of
only blanks, byte order marks and control characters is empty. Anything else is a usage error (exit 2) before anything is written,
and the message never repeats the text. The same URL gives the same bytes on every run.

**Whatever text file the path names is copied into the script.** The notice is read from the path in the `--to` URL and written, line by line, into the header: any readable text file up to 16 KiB
(a configuration file, a key, a note) would end up in a script that is then shared. So give datatug a `--to` URL only from a source you trust: an untrusted URL
with a `notice-file` is a way to read a local text file, which is why the command never takes the path from anywhere else than the URL you typed.

Every line of the file becomes one comment line (`-- ` and the line), in every dialect. The text is verbatim except for what
could end the comment or start a script line: CR, LF, CR LF, VT, FF, U+001C to U+001F, U+0085, U+2028 and U+2029 all end a line;
other control characters, a byte order mark, bidirectional controls and invalid bytes become spaces; a tab becomes four spaces;
`*/` and `/*` get a space between the two characters; for `mssql` `$(` becomes `$ (`, for `oracle` `&` becomes `& ` (the header comes before `SET DEFINE OFF`; a comment line that ends in a hyphen joins nothing, measured on 23.26.3; a statement line that ends in one is joined with the next, see the wrapping rule). Trailing spaces and the blank lines at both
ends are dropped. So a notice line such as `GO`, `\! cmd`, `source x`, `HOST cmd`, `@file` or `/` stays a comment. The notice follows the
`license` and `attribution` lines, after a blank comment line.

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
* A header of comments: the label, dataset, datatug version and the SHA-256 of the SQLite file, licence and attribution
  (and the multi-line notice of `notice-file`), the minimum MySQL version (8.0.16, where CHECK constraints are enforced; verified on MySQL 8.4 LTS), how to load it
  safely, the tables, and every warning.
* `SET NAMES utf8mb4` and a session `sql_mode` of `STRICT_ALL_TABLES,NO_AUTO_VALUE_ON_ZERO,NO_ENGINE_SUBSTITUTION,NO_ZERO_DATE,NO_ZERO_IN_DATE`
  (`NO_AUTO_VALUE_ON_ZERO` because SQLite allows an id of 0 and MySQL would number it otherwise).
* **The first statement creates the marker table `_demodb_import`** and fails when it exists, so a second load into the
  same database stops at once; **the last statement inserts its row** (dataset, label, source SHA-256, version, tables,
  rows): a row means the load completed. MySQL commits DDL, so a failed load is not rolled back: drop the database (or the
  tables) and load again. `DROP TABLE _demodb_import` removes the marker.
  The marker name is compared like table names are, case-insensitively and ignoring trailing spaces: a SQLite table named
  `_DEMODB_IMPORT` (or `_demodb_import `) is refused (an earlier version compared the name exactly; SQL Server's default collation merges the case as well).
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
`constraint-name-not-carried` otherwise. MySQL names a check without a name `<table>_chk_<n>` and a foreign key without one `<table>_ibfk_<n>`, in the
same list as the names given: a given name of that shape (case aside, `n` a number without a leading zero) is not carried while a table of the script has a
check (a foreign key) without a name, because the load would fail with error 3822 (1826). The constraint is then written without a name and MySQL names it itself; a table whose names were dropped
this way has an unnamed constraint too, so the rule runs until nothing changes. Names of any other shape (`t_chk_x`, `t_chk_01`) and names of a table with no unnamed constraint of that kind are kept.

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

### SQL Server dialect (`mssql`)

`datatug db copy --from sqlite://<file> --to 'sqlscript:///path/out.sql?dialect=mssql'` writes a T-SQL script for **SQL Server 2016
and later** (verified on SQL Server 2022, Developer edition). The dialect key is `mssql`; the demo-db site names the same
format `sqlserver` (`<id>.sqlserver.sql` in a provider's `manifest.json` `exports`), so the site's `sqlserver` export is this
dialect's output.

```sh
datatug db copy --from sqlite://./chinook.sqlite --to 'sqlscript:///tmp/chinook.mssql.sql?dialect=mssql&license=MIT'
sqlcmd -S localhost -d chinook -X -x -b -f 65001 -i /tmp/chinook.mssql.sql
```

Load it with `-i <file>`, **not** with a redirect (`< file`): the script is UTF-8 **with a byte order mark** (what SQL Server
Management Studio expects), `sqlcmd -i` skips it, and a redirect hands it to the server as a syntax error. `-f 65001` reads
UTF-8, `-b` stops at the first error, `-X` turns off the commands that run programs (`!!`, `ED`) and `-x` turns off
`$(variable)` substitution. The script also loads without `-x` and `-X`, because no value holds `$(` and none starts a line.
Lines end in LF; there is no CR anywhere.

**No data byte can start a line**, so a line is never a `sqlcmd` command (`GO`, `:r`, `:!!`, `!!`, `EXIT`, `QUIT`, `RESET`, `ED`):
every line starts with a token the generator wrote (`--`, `SET`, `GO`, `IF`, `CREATE`, `INSERT`, `(`, `ALTER`, `DBCC`,
`COMMIT`, `BEGIN`, or two spaces inside `CREATE TABLE`) and no literal holds a raw CR, LF or other control character. A text
with a control character, a Unicode line or paragraph separator, the byte order mark or a non-character is written in pieces,
`CAST(N'a' AS NVARCHAR(MAX))+NCHAR(13)+NCHAR(10)+N'b'` (the first piece is `NVARCHAR(MAX)`: T-SQL silently stops a
concatenation of two plain `NVARCHAR` pieces at 4,000 characters); a text with more than 200 such characters (the T-SQL parser
fails on a few thousand terms), or with a NUL, as `CONVERT(NVARCHAR(MAX),0x<UTF-16LE hex>)`. The `(` of a `$(` is its own piece.
A blob is `0x..`, a float an exponent literal (`1.5e+00`), a date `'2024-01-01'` (the ISO form, which no `SET LANGUAGE` changes).

#### One transaction, and how it stays one

SQL Server has transactional DDL, so the whole script is **one transaction**: `SET XACT_ABORT ON`, `BEGIN TRANSACTION`, every
statement its own batch ended by a `GO` line the generator wrote (SQL Server refuses more than 1,000 rows in one
`INSERT ... VALUES`, so a table is loaded in batches of at most 1,000 rows or about 1 MiB), and `COMMIT TRANSACTION` last. A
failed statement rolls everything back, and the database is left as it was. The prelude sets `QUOTED_IDENTIFIER`, `ANSI_NULLS`,
`ANSI_PADDING`, `ANSI_WARNINGS`, `ARITHABORT`, `CONCAT_NULL_YIELDS_NULL` ON and `NUMERIC_ROUNDABORT` OFF (sqlcmd starts with
`QUOTED_IDENTIFIER` OFF, and filtered indexes need these).

A client that goes on after an error (`sqlcmd` without `-b`, a tool that sends each batch on its own) would run the rest of
the script **outside** the transaction and keep half the data, so **every batch after `BEGIN TRANSACTION` starts with the guard
`IF @@TRANCOUNT=0 SET NOEXEC ON`**: once the transaction is gone, every later batch only compiles. The last batches
check that every table holds exactly the rows the script wrote (`IF (SELECT COUNT_BIG(*) FROM [t]) <> n THROW ..`, which rolls
back too), fill the marker, `COMMIT`, and end with `SET NOEXEC OFF`.

**The first statement creates the marker table `_demodb_import`** and fails when it exists, so a second load stops there and
changes nothing; the last one inserts its row (dataset, label, source SHA-256, version, tables, rows): a row means the load
completed. `DROP TABLE [_demodb_import]` removes it. The script never says `USE` or `DROP DATABASE`: it creates into the
current database.

#### Types (the declared type wins when every value fits it)

| SQLite | SQL Server | Notes |
|---|---|---|
| `INTEGER`... / `INT2` / `INT4` | `BIGINT` / `SMALLINT` / `INT` | wider when a value does not fit (`type-widened`); an `INTEGER PRIMARY KEY` is `IDENTITY(1,1)` loaded under `SET IDENTITY_INSERT`, then `DBCC CHECKIDENT (.., RESEED, n)` puts the counter where SQLite's is (RESEED sets the last number used, except in a table with no row, where it sets the first to use) |
| `REAL`, `FLOAT`, `DOUBLE` | `FLOAT(53)` | an infinite value, or a value below 2.2250738585072014E-308 (SQL Server flushes it to zero), makes the column `NVARCHAR(MAX)` (`type-widened`) |
| `NUMERIC(p,s)`, `DECIMAL(p,s)`, `DECIMAL_TEXT(p,s)` | `DECIMAL(p,s)` | when every value fits; otherwise, and for a bare `NUMERIC`/`DECIMAL`/`MONEY`, computed from the values; beyond 38 digits `NVARCHAR(MAX)`. `values-normalized` counts the texts DECIMAL spells differently |
| `BOOLEAN` | `BIT` | |
| `DATE` / `DATETIME`, `TIMESTAMP` / `TIME` | `DATE` / `DATETIME2(f)` / `TIME(f)` | `f` is the longest fraction (never `DATETIME`, which rounds to 3.33 ms); a value that is not in the form of its type, or year 0000, makes the column text |
| `TIMESTAMPTZ`, `JSON_TEXT`, `UUID_TEXT` | `NVARCHAR(MAX)` | `type-widened` |
| `CHAR(n)`, `VARCHAR(n)`, `NVARCHAR(n)` | `NVARCHAR(n)` | n up to 4,000 UTF-16 code units (a character outside the Basic Multilingual Plane is two); longer is `NVARCHAR(MAX)` |
| `TEXT` | `NVARCHAR(MAX)` | |
| `BLOB` | `VARBINARY(MAX)` | |
| any text or blob in a **key** | `NVARCHAR(n)`, `VARBINARY(n)` | `key-type-bounded`: SQL Server cannot index `MAX`. 255 characters when the key fits, the longest value otherwise; the limits are 900 bytes for the clustered primary key and 1,700 for any other key (and 32 columns): a primary or unique key beyond that is refused, a plain index is dropped (`index-not-carried`) |
| text with a NUL byte, text that is not valid UTF-8, a column of text and blobs | `VARBINARY(MAX)` | `type-widened`; **policy**: an `NVARCHAR` cannot hold such a text, so the whole column is bytes and the values are exact |

All the columns of a foreign key equivalence class get one type first (SQL Server refuses a key between columns whose type or
length differ): the widest integer, the longest text, the most decimals; bytes if any member is bytes, text if the families
differ (`type-widened` for each member that changed).

#### Names

Every name is kept **byte for byte** as one bracket-quoted identifier (`[Order Details]`, `[Production.Product]`, `]` doubled,
reserved words, Unicode). A name SQL Server cannot store, or the script cannot carry safely, is the only reason to change one,
and every change is a counted `name-renamed` / `index-renamed` warning: an empty name or one of only spaces, a name longer than
128 UTF-16 code units, a name with a control or line-break character or the byte order mark, a name with `$(`, a table that
differs from an earlier one only by case or trailing spaces (SQL Server compares names under the database's collation:
case-insensitive by default, and a trailing space does not count), and a column that does. The new name is the old one with
each unusable character replaced by `_`, cut to fit and ended by `_` and 8 hex digits of its SHA-256. SQL Server keeps one list of
names for tables and constraints: a unique key or foreign key named like a table or like another constraint loses its name
(`constraint-name-not-carried`) and SQL Server names it.

#### Keys, defaults, checks

* **Unique keys over NULLs.** SQLite never treats two NULLs as equal; SQL Server's `UNIQUE` does. A unique key (or unique index)
  whose nullable columns hold more than one NULL is written as a unique index **filtered** to the rows without NULL
  (`unique-filtered`), which is exactly SQLite's rule but cannot be referenced by a foreign key (`fk-not-carried`); one whose
  columns hold at most one NULL stays a `UNIQUE` constraint, which differs from SQLite only for rows added later.
* **Foreign keys** are added after the data `WITH CHECK`, so they are trusted. T-SQL has no `RESTRICT`: it is `NO ACTION`
  (`fk-action-not-carried`, the same refusal for a key that is not deferred). `SET DEFAULT`, `SET NULL` on a `NOT NULL`
  column and `DEFERRABLE` are carried as the nearest thing and say so.
* **Cascade paths.** SQL Server refuses a foreign key that makes a cycle or a second cascade path from one table to another
  (error 1785), at the first key it cannot place. The script decides that statically and deterministically
  (`mssqlDialect.limitCascades`): the keys are taken in the order of the plan (tables as planned, keys as the table lists
  them); a key keeps both of its actions when the graph of cascades stays valid, otherwise its `ON UPDATE` action goes, then
  (keeping the update) its `ON DELETE` action, then both; the first that fits is kept; each lost action is a counted
  `fk-action-not-carried` with the reason. A `DELETE` follows `CASCADE` keys and a `SET NULL` key updates the child rows, from
  where the update goes on through the keys that cascade updates; an `UPDATE` follows the keys with an `ON UPDATE` action. A
  table may be reached at most once by one change, and never again. For Sakila, 8 of the 22 `ON UPDATE CASCADE` keys lose their
  action (`film.language_id`, `rental` x3, `staff` x2, `store` x2), and its 21 `ON DELETE RESTRICT` keys become `NO ACTION`. **The rule is measured, not derived from
  SQL Server's source**: a SET NULL key is also counted among the keys that update. It was checked on SQL Server 2022 against 400
  random foreign key graphs (every downgraded action, 1,245 of them, was tried back on the loaded database and refused with error
  1785 by the server, except 28, and no script failed to load); the 28 (2%) are in graphs with an `ON DELETE SET NULL` key, where the
  analysis keeps NO ACTION on an action SQL Server would have taken. The rule errs towards the downgrade, never towards a script that
  cannot load.
* **Defaults**: numbers, `N'text'` (the same literal rules as values), booleans; `CURRENT_TIMESTAMP`/`CURRENT_DATE`/`CURRENT_TIME`
  become `SYSUTCDATETIME()`-based defaults in the column's own form (`DATETIME2` gets `SYSUTCDATETIME()`, a text column gets
  `CONVERT(NVARCHAR(19), SYSUTCDATETIME(), 120)`, SQLite's text format in UTC; `default-translated`). Anything else is
  `default-not-carried`.
* **Checks** are added `WITH CHECK`. `GLOB` becomes `LIKE` (`*` `?` `[..]`; a literal `%` or `_` becomes `[%]`/`[_]`) on an operand
  with `COLLATE Latin1_General_100_BIN2`, and a comparison, `IN` or `BETWEEN` of a text column gets the same collation (SQLite
  compares exactly; the default collation ignores case). `UPPER`, `LOWER`, `RTRIM`, `LTRIM`, `ABS`, `COALESCE`, `NULLIF` and
  `REPLACE` carry over; anything else (`length`, `typeof`, `||`, a hexadecimal literal, a `LIKE` pattern with `[`) is
  `check-not-carried`; a comparison that stays case-insensitive is `collation-not-carried`.
* Not carried: views and triggers (`view-not-carried`, `trigger-not-carried`), partial indexes other than the filtered unique
  ones above, expression indexes, `WITHOUT ROWID`.

#### What a person must know about SQL Server's defaults

The script uses the database's **default collation** (`SQL_Latin1_General_CP1_CI_AS` on a default install), as users of the
engine expect. Text comparison is **case-insensitive**, and `=`, `UNIQUE` and a foreign key **ignore trailing spaces**, unlike
SQLite's exact comparison, so keys that differ only by case or by a trailing space in SQLite are duplicates and stop the load
(and roll it back); the demo databases have none. `LEN()` ignores trailing spaces too. Text is `NVARCHAR` (UTF-16).

#### How it is proved

`scripts/check-journey-mssql.sh` (the non-required, path-filtered workflow `.github/workflows/journey-mssql.yml`) starts the
digest-pinned `mcr.microsoft.com/mssql/server:2022-latest` image and, for the edge-case fixture
(`pkg/dbcopy/faithful/testdata/mssql_edge.sql`), a fuzz fixture of 4,000 rows of hostile text, bytes and names, a fixture of 80
random foreign key graphs, and the six demo databases pinned in `scripts/journey-datasets.txt`: exports twice (the same bytes),
loads the script with `sqlcmd -X -x -b -f 65001 -i` inside the container, checks that a second load stops at the marker table,
and runs the cross-engine verifier. Once per run it also proves **atomicity** (a script with a failing batch loaded with
`sqlcmd` *without* `-b` leaves no table behind, and the same script with its guard lines removed does), the **threat** (without `-x`, `sqlcmd`
replaces `$(NAME)` inside a literal with the environment variable `NAME`, which is why no value or name of the script ever holds `$(`)
and that the fuzz script loads without `-X` and `-x`. (The `sqlcmd` of `mssql-tools18` keeps a multi-line literal whole across a
`GO` line, which SQL Server Management Studio and plain batch splitters do not; the script does not rely on it, and the journey only reports
which it saw.) The verifier (`internal/crossverify`,
`Engine: crossverify.EngineMSSQL`) reads the `sys` catalog and compares it with the plan: tables, columns (`nvarchar(255)`,
`datetime2(3)`, ... spelled as the plan spells them, nullability, identity, whether there is a default), primary key, foreign keys (columns, parent,
actions, **trusted**), unique keys and indexes (a `PRIMARY KEY` or `UNIQUE` constraint's backing index is the key, not an index;
a filtered index must be filtered), check constraints (their number, and that they are trusted), the marker, renames, row counts, the **identity counter**, and
every value read through `github.com/microsoft/go-mssqldb` (int64, a bit as a bool, the 64 bits of a float, a decimal as an exact
rational, `time.Time` for a date or time, written with an explicit format and the column's own number of decimals).
The verifier does **not** compare the text of a default or of a `CHECK`, so the `COLLATE Latin1_General_100_BIN2` of a translated
`CHECK` is covered by the unit tests and the golden script, not by the real-engine journey.

SQL Server runs only on amd64: on an Apple-silicon Mac the container runs as `linux/amd64` under emulation (OrbStack uses Rosetta;
Docker Desktop needs Rosetta or QEMU enabled) and AdventureWorks takes minutes instead of seconds.

#### Licence of the test image

`ACCEPT_EULA=Y` accepts the end-user licence of the SQL Server image. The founder decided on 2026-10-16 (decision D10) that CI may
accept it **for automated testing only** (record: the founder answered "Yes, both" on 2026-10-16 to the question whether CI may accept the SQL Server Developer EULA with `ACCEPT_EULA=Y` and the Oracle Free Use Terms for test-only containers, with no redistribution of any image; the same decision is cited in `.github/workflows/journey-mssql.yml` and `scripts/check-journey-mssql.sh`): the Developer edition is licensed for development and testing, never production,
and the image is pulled and thrown away with the runner, **never redistributed**. The generated scripts contain no Microsoft
code.

#### Known limits

A unique constraint whose columns hold one NULL admits one NULL in later inserts, not many. A `CHECK` the translator cannot read is
not carried.

**Names that SQL Server's default collation merges.** At export time two tables, or two columns of one table, are treated as the
same name when they differ by case (`Abc` / `abc`) or by trailing spaces, and one of them is renamed with a `name-renamed`
warning. The default collation (`SQL_Latin1_General_CP1_CI_AS`, `Latin1_General_*_CI_AS`) merges more than that, and the
export does **not** catch it: `ß` and `SS` (`straße` / `STRASSE`), full-width and half-width letters (`ａbc` / `abc`), and the
other expansions and compatibility forms of the Unicode comparison rules. Two such table names fail the load with Msg 2714, two such column names with
Msg 2705, and so can two key values (a unique or primary key that is distinct in SQLite). The load runs in one transaction with `-b`, so
the failure rolls back and leaves nothing behind (see below); rename one of the names in SQLite and export again. A case-sensitive or
binary database collation does not merge any of these. Loading **without `-b`** continues after a batch fails: a failed data
or key batch is then skipped by the batch guard and rolled back at the end, but a statement that fails to compile
(a syntax error) is not part of any transaction the server can roll back, so always use `-b` as the header says. A database with
another collation (`COLLATE` at `CREATE DATABASE`) compares text in its own way. SQL Server 2014 and earlier are not supported
(`THROW`, 1,700-byte index keys). Azure SQL Database is the same engine but untested here.

### Oracle dialect (`oracle`)

`datatug db copy --from sqlite://<file> --to 'sqlscript:///path/out.sql?dialect=oracle'` writes a SQL*Plus script for **Oracle
Database 23ai and later**: the script uses multi-row `INSERT ... VALUES` and 128-byte quoted identifiers, and its first
block raises ORA-20001 on a database below version 23 or with `COMPATIBLE` below 23 (which also covers the 30-byte identifier
limit of 12.1 and earlier). It was **verified on Oracle AI Database 26ai Free 23.26.3** (`compatible` 23.6.0, `AL32UTF8`,
`MAX_STRING_SIZE=STANDARD`) with SQL*Plus 23.26.3, natively on arm64 and in CI on amd64. The dialect key is `oracle`.

```sh
datatug db copy --from sqlite://./chinook.sqlite --to 'sqlscript:///tmp/chinook.oracle.sql?dialect=oracle&license=MIT'
NLS_LANG=.AL32UTF8 sqlplus -L -S user/password@//host/service @/tmp/chinook.oracle.sql < /dev/null
```

Load it with **SQL*Plus** from a file (`@file`), as the header says, with `NLS_LANG=.AL32UTF8` (any other client character
set stops the load at the first statement with ORA-20003; the script is UTF-8 without a byte order mark). **SQLcl, SQL
Developer, Windows clients and a non-default NLS setup are untested.** The script targets the schema of the connected user
(it creates no user and no schema; that user needs `CREATE SESSION`, `CREATE TABLE`, `CREATE SEQUENCE` and a tablespace quota).

#### How the script protects the load

SQL*Plus continues after many errors and reads some data as commands, so the script avoids the hazards instead of relying on
the client:

* `SET DEFINE OFF` (an `&` or `&&` in data would be a prompt; the journey proves the control: without the line a load loses rows),
  `SET ESCAPE OFF`, `SET CMDSEP OFF`, `SET SQLTERMINATOR ';'`, `SET SQLPREFIX #` and `NLS_COMP = BINARY`/`NLS_SORT = BINARY` for the
  session. **No line of the script starts with a value** and **no literal holds a raw CR or LF**: a text with a line break or control character is written with `CHR()` pieces, and
  any non-ASCII text as `TO_CHAR(UNISTR('\XXXX'))` escapes, so a lone `/`, `@`, `@@`, `EXIT` or a `SET` line cannot occur in data.
* No line is over 2,300 characters (SQL*Plus refuses an overlong line and ignores it: SP2-0027, exit status 0, observed with a 6,000-character line; the
  exact limit of each client is untested). Every statement is wrapped at a safe place: after a comma, or before a keyword, a quoted name, a literal or a number, outside
  any quote, so a continuation line starts with a token the generator wrote. A wide `CHECK (x IN (...))`, a key over many long names or a composite foreign key is several lines. A
  statement that cannot be wrapped makes the export **fail** (the writer refuses any line over the limit; a comment line is continued on another comment line).
  **No line outside a `--` comment ends in a hyphen**: SQL*Plus drops a trailing hyphen (a trailing blank does not matter) and joins the next line (`SELECT 10 -` and `3` is `SELECT 10  3`, measured on 23.26.3; a trailing `+` or `\\` is no continuation), so a minus written apart from its number in a wrapped `CHECK` list would silently change its sign. The wrapper never cuts after a hyphen and the writer refuses such a line, so nothing is ever joined and the 4,999-character limit applies to each line as written.
  Long literals are wrapped too, and a literal over 4,000 bytes is a `TO_CLOB` group of at most 3,500 bytes. A `BLOB`
  over 2,000 bytes is written by a per-row PL/SQL block of `HEXTORAW` chunks.
* `WHENEVER SQLERROR EXIT FAILURE ROLLBACK` stops at the first error. Oracle DDL commits, so the script is **not transactional**:
  atomicity is by **guard**. The marker table `_demodb_import` is created first (a second load stops at it with ORA-00955), and the
  last block counts, for every table, its rows and, in the catalog, its keys (primary, unique, foreign: enabled and validated), its check and NOT NULL
  constraints and its indexes, and raises ORA-20000 before the marker row is inserted. A client that went on after an error (no `WHENEVER`) or skipped a line
  therefore never writes the marker row. The index count is the number of distinct column sets of the primary key and the unique constraints of a table (Oracle backs a `UNIQUE (b, a)` with the index of a `PRIMARY KEY (a, b)`, measured on 23.26.3: one index, not two), plus its plain and filtered indexes. The marker proves rows and the presence of the objects; it does not compare the text of a default or of a check. A failed load leaves the tables it created: drop the schema's objects and load again.
* A version check (ORA-20001), a database character set check (ORA-20002) and a client character set check (ORA-20003) run first.
  The only `/` lines are the ones the generator writes to end its own PL/SQL blocks.

#### Types

| SQLite | Oracle | Notes |
|---|---|---|
| `INTEGER`, `INT2`, `INT4`, `BOOLEAN` | `NUMBER(p,0)` | sized from the values; there is no `BOOLEAN` before 23ai, so a boolean is `NUMBER(1,0)` |
| `REAL`, `FLOAT` | `BINARY_DOUBLE` | infinities are carried; SQLite stores `-0.0` as `0` |
| `NUMERIC(p,s)`, `DECIMAL` | `NUMBER(p,s)` | a value that does not fit, or over 38 digits, widens the column (`type-widened`) |
| `DATE` | `DATE` | years 0001 to 9999, except 1582-10-05 to 1582-10-14, which Oracle's calendar skips (it stores them ten days later): a column that holds one is carried as text (`type-widened`) |
| `DATETIME`, `TIMESTAMP` | `TIMESTAMP(0)` to `TIMESTAMP(9)` | the fraction digits the values need (the same days of 1582 are carried as text) |
| `CHAR(n)`, `VARCHAR(n)`, `TEXT` | `VARCHAR2(n CHAR)` / `VARCHAR2(4000 BYTE)` / `CLOB` | char semantics for declared lengths; a text over 4,000 bytes is a `CLOB` |
| `BLOB` | `BLOB` (`RAW(n)` in a key) | `HEXTORAW` chunks |

A column with no Oracle equivalent (`TIME`, `TIMESTAMPTZ`, JSON, mixed or unknown types) is widened to text with a `type-widened`
warning. Identity columns (`AUTOINCREMENT`) are `GENERATED BY DEFAULT AS IDENTITY` with `START WITH` the next value. A key
text column with no declared length is `VARCHAR2(255 CHAR)` (`key-type-bounded`); an index key over 6,000 bytes or 32 columns is refused (primary or unique) or dropped (index).

#### Differences a person must know

* **The empty string is NULL.** Oracle stores `''` as `NULL`: every empty text is loaded as `NULL`, a `NOT NULL` column that
  holds one is relaxed, and the column gets a `values-normalized` warning. The verifier models this (an empty source text must be NULL in Oracle).
* **Trailing spaces and case** are kept (`VARCHAR2` comparison is exact in the default binary collation); `NLS_COMP`/`NLS_SORT` settings are untested.
* **Foreign key actions** are limited to `ON DELETE CASCADE`, `SET NULL` and `NO ACTION`; `ON UPDATE` and `RESTRICT` are not carried
  (`fk-action-not-carried`). `DEFERRABLE` is carried.
* **Names** are quoted, case-preserving and up to 128 bytes (UTF-8). A name Oracle cannot store (empty, over 128 bytes, a NUL or control character, a quote
  character, or one that differs from another only by what Oracle ignores) is renamed with a hash (`name-renamed`); a table or column named `ROWID` is renamed too. Two tables whose names differ only by case are two names in Oracle and are a `name-case-collision` note.
* **Views** are not carried. A `CHECK` the translator cannot read, or that uses `GLOB`/collations Oracle lacks, is not carried (`check-not-carried`); a default
  is translated when it is a literal or `CURRENT_TIMESTAMP` (to `SYS_EXTRACT_UTC(SYSTIMESTAMP)`), else `default-not-carried`.
* **Unique keys over NULLs.** Oracle's composite unique key admits many rows whose columns are all NULL but not two with the same non-NULL
  prefix and a NULL elsewhere, unlike SQLite; such a key is written as a function-based unique index (`CASE ... END`) and counted as `unique-filtered`.
* **Foreign key columns** of one equivalence class get one type (ORA-02267 otherwise).
* **Index limits.** A key row is at most 6,000 bytes and 32 columns. A unique key over nullable columns that holds several NULLs is a function-based index with one `CASE` per column, each repeating the
  test of every nullable column, and Oracle counts every reference towards the 32 (ORA-01793; measured: 16 columns with one test load, 6 columns with 6 tests do not), so a key with
  more than 32 references is not created and is reported as `index-not-carried` (its uniqueness over NULLs is then not enforced). A key that cannot be indexed at all (a CLOB, a BLOB, a text over 4,000 bytes) is refused for a primary or unique key.
* **Names.** A name that starts with `BIN$` (any case) is renamed: such a table belongs to the recycle bin and hides from the data dictionary views.

#### How it is proved

`scripts/check-journey-oracle.sh` (the non-required, path-filtered workflow `.github/workflows/journey-oracle.yml`) starts the
digest-pinned `gvenzl/oracle-free:23-slim` image and, for the edge-case fixture (`pkg/dbcopy/faithful/testdata/oracle_edge.sql`),
a fuzz fixture of 4,000 hostile rows, a fixture of 80 random foreign key graphs and the six demo databases pinned in
`scripts/journey-datasets.txt`: exports twice (the same bytes), loads with SQL*Plus inside the container, checks that a second
load stops at the marker table, and runs the cross-engine verifier (`Engine: crossverify.EngineOracle`) through the pure-Go driver
`github.com/sijms/go-ora/v3`. The verifier reads the `ALL_*` catalog (tables, columns, nullability, identity, keys, foreign keys with actions and
deferrability, unique and function-based indexes, the marker) and every value with typed reads (a `NUMBER` as exact text, a `BLOB` by its length and bytes). It compares **only whether a default exists and the number of `CHECK` constraints, not their text**, so a translated `CHECK` or `DEFAULT` is covered by the unit tests and the golden script, not by the real-engine journey.
Once per run it proves the controls: a client in another character set stops with no table, a failing statement stops the load,
a client that goes on after an error gets no marker row (and with the counts removed it does), `&`, a long line and a raw
newline plus `/` are real hazards, and the fuzz script needs its `SET DEFINE OFF`.

#### Licence of the test image

The database in the image is Oracle Database Free, used under the Oracle Free Use Terms and Conditions (development and
testing use). The founder decided on 2026-10-16 (decision D10, answer "Yes, both") that CI may run it **for automated testing only**; the
image is digest-pinned, pulled and thrown away with the runner, and **never redistributed**. The Apache-2.0 label of the gvenzl
image covers its build scripts only. The generated scripts contain no Oracle code.

#### Untested

Oracle before 23ai (refused by the script's version check, which was not run on an older server), SQLcl, SQL Developer, Windows, a non-default
`NLS_LANG`/`NLS_COMP`, small block sizes, `MAX_STRING_SIZE=EXTENDED`, and Autonomous Database.

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
  password or a query). Characters other than letters, digits, `.`, `_` and `-` become `_`; leading and trailing `.`, `_`
  and `-` are stripped; the name is at most 100 characters and 247 bytes (a file system takes 255 bytes of a name, `.ingitdb`
  included: 100 CJK characters are 300 bytes), cut on a character boundary. A name Windows reserves for a device (`CON`, `PRN`,
  `AUX`, `NUL`, `COM1`-`COM9`, `LPT1`-`LPT9`, in any case, also before a `.`) gets a `_` in front (`_CON.ingitdb`). For
  an `ingitdb://` source the name is that of the folder itself, also for `ingitdb://.` run inside the project (the absolute
  path is used). A source with no name (an in-memory SQLite, a PostgreSQL URL without a database) is exit 2: pass `--to`.
* **Interrupts.** SIGINT (Ctrl-C) and SIGTERM stop the export at the next row through its own return path: the staging folder is
  removed (for `--dry-run` too, which stages in the temporary folder), nothing is published, and the command exits 1 saying
  `interrupted (interrupt)` or `(terminated)`. A second signal while it winds down ends the process as usual.
* **The destination's folder** must exist, for `--dry-run` as for a real run (`--to nopar/x/y` is exit 1 either way).
* **`?` in a path.** `--to` is a plain folder path or an `ingitdb://` URL. A `?` in a plain path is exit 2 (it used to make a
  folder named with the query, write JSON and print another path): write `ingitdb://FOLDER?records-format=jsonl` (or `--records-format jsonl`).
  **Changed:** `ingitdb://DIR?records-format=jsonl` used to create a folder literally named `DIR?records-format=jsonl` with JSON records; it
  now creates `DIR` in jsonl. This is the one exception to "byte for byte as before" below.
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
* **Compatibility.** `datatug db export --from URL --to ingitdb://DIR [--records-format F]` (with no `?` in the URL) writes the same bytes, prints
  the same `db export: exported N collections and M rows to ...` line and exits with the same code as before; the test
  `TestExportOldInvocationIsByteIdentical` holds the digests of the earlier binary's output.
* **Not here: cloud or remote backup.** `export` writes a local folder. A backup to a bucket, another server or on a
  schedule belongs to the planned `backup` command family, or to `db copy --to <destination>` where `copy` reaches one
  (PostgreSQL, BigQuery, a SQLite file).

## Copying from a schemaless source: `--schema=destination`

`--schema` says where the collections to copy, and their shapes, come from.
`source` (the default, and what every copy above does) reads them from the
source's own schema. Firestore has none, so a Firestore `--from` without
`--schema=destination` fails, with a hint naming `--schema=destination`.

`datatug db copy --schema=destination` copies a chosen part of a Firestore
database into an inGitDB database, **read-only**, laid out exactly as the inGitDB
definitions of a *mirror* (the destination's definitions) say. It is the way to
back up Firestore, or a slice of it, into files. (It replaces `datatug db
mirror`, which was this command under another name and is removed.)

### What it needs

1. **A mirror**: an inGitDB database whose collection definitions follow the
   Firestore collection paths. The documents of
   `teams/{team}/ext/{module}/events/{event}` are the collection `events` defined
   under `ext` under `teams` (its id is `teams/ext/events`). **The definition
   decides how records are stored**, and the mirror writes what it declares (see
   [Storage forms](#storage-forms)); a definition in a form the mirror does not
   write is reported as `unsupported` and not copied.
2. **A mirror policy** in the DataTug project (one YAML file; `format:
   datatug-mirror-policy/v1`). It says what may leave Firestore:

   ```yaml
   format: datatug-mirror-policy/v1
   definitions: ../firestore          # the mirror (relative to this file)
   output: ../data/prod-backup        # optional; default data/<policy name> in the project
   copy:                              # collections that are copied
     - teams
     - teams/ext
     - teams/ext/events
     - ext/customers
   exclude:                           # collections that are never read
     - ext/secrets
   paths:                             # optional: the only Firestore paths a collection is copied from
     teams/ext: [teams/*/ext/calendar]
     teams/ext/events: [teams/*/ext/calendar/events]
   mask:                              # fields replaced before a record is written
     ext/customers: [email, name, contacts]
   ```

   **A defined collection the policy does not name is not copied**, and every
   run lists it as `undeclared`, so a collection added to the mirror later is
   never copied by accident. Every name must be a defined collection, and a
   collection cannot be both copied and excluded. Unknown keys in the policy
   are an error.

   **`paths`** pins a copied collection to Firestore paths. A pattern is a
   collection path (`teams/*/ext/calendar/events`) or a document path
   (`teams/*/ext/calendar`); `*` stands for any document id, and every other id
   must be the one in the document's path. Use it whenever a document's shape
   depends on an id: when each module of `teams/{team}/ext/{module}` keeps
   different fields, a definition describes one module, and `paths` keeps the
   records of the other modules out. A document of the collection at another
   path is **not copied and not dumped**: the report counts it as an *other
   parent* (`other_parents`, with its path and every unpinned id shown as `*`).
   A pattern must have the collection's own collection ids.

   **`mask`** paths are dot-separated (`members.*.displayName`: `*` is every key
   of a map or every item of a list). A masked value keeps its kind where it can,
   so a required column still holds one: a string becomes `"***"`, a map `{}`
   and a list `[]`, a whole untyped (`any`) column `"***"` whatever it holds; a
   number, a bool, a timestamp or another tagged value is removed (a list item
   becomes `null`). The policy check refuses a mask path that does not name a
   declared field: its first name must be a column of the collection (`emial`
   is an error), and each next name must be something the column's type holds
   (an item, `*` or an index, of a `[]T`; any key of a `map[K]V`; nothing below
   a scalar). **A path below an untyped value is refused** (`settings.phone`
   when `settings` is `map[string]any`, `contacts.*.phone` when `contacts` is
   `[]any`): it cannot be checked, so a typo there would leave the data in
   clear; mask the untyped value as a whole (`settings`, `contacts`).
   Document ids and map keys are not values and cannot be masked.

   **A mask never makes the mirror lose a whole record.** The check also refuses
   a mask whose masked value the definition would refuse, which would get every
   record holding it rejected: a required number or bool (removed, so
   missing), a column with an `enum`, a length that `"***"` breaks, a `format`
   or a `foreign_key`, and the items of a list of numbers or bools (`null`).
   The error says to exclude the collection instead, or to leave the column
   unmasked. (The mirror writes no placeholder value: there is no neutral value
   of a number, a bool or an enum that would not read as data.)


### Commands

```sh
export DATATUG_FIRESTORE_SOURCE_URL='firestore://my-project'     # or firestore://PROJECT/DATABASE

# Count the documents of each selected collection; nothing is written.
datatug db copy --schema=destination --from env:DATATUG_FIRESTORE_SOURCE_URL --policy mirrors/backup.yaml --dry-run

# Copy one space (the documents under spaces/<ID>) into its own folder.
datatug db copy --schema=destination --from env:DATATUG_FIRESTORE_SOURCE_URL --policy mirrors/backup.yaml \
  --space s1 --to data/space-s1

# Copy everything the policy allows.
datatug db copy --schema=destination --from env:DATATUG_FIRESTORE_SOURCE_URL --policy mirrors/backup.yaml --progress
ingitdb validate --path data/backup
```

`--from` also takes `firestore://PROJECT[/DATABASE]` directly, or a
`datatug://PROJECT/DB?env=ENV` reference to a project source whose connection
descriptor names the environment variable (`{"dsnEnv": "DATATUG_FIRESTORE_SOURCE_URL"}`).

The flags of `db copy` that mean the same thing in both modes are the same flags:

| Flag | With `--schema=destination` |
|------|---------|
| `--from` | The Firestore source (required). |
| `--to` | Output folder (or `ingitdb://folder`); optional here: default the policy's `output`, else `data/<policy name>` in the project. |
| `--include` | Comma-separated collections of the policy's `copy` list (definition ids such as `teams/ext/events`, not source table names); `teams/**` adds every collection below `teams`. An excluded or undeclared name is an error. |
| `--dry-run` | Count, with Firestore count queries, and write nothing. |
| `--progress` | One line on stderr per page read. |
| `--json` | The run's report as JSON on stdout. |

These apply only with `--schema=destination`; without it each is refused with
`--NAME applies only with --schema=destination` (exit 2):

| Flag | Meaning |
|------|---------|
| `--policy` | The mirror policy (required). |
| `--definitions` | The mirror folder, instead of the policy's `definitions`. |
| `--space ID` | Only the documents under `spaces/ID` (the same as `--path spaces/ID`). It assumes a database that keeps each space's data under a root collection named `spaces`; for any other layout use `--path`. |
| `--path P` | Only the documents at or below the Firestore path `P`: a document (`teams/t1`) or a collection (`teams/t1/ext`). A path no defined collection (at a path its `paths` allow) is under is an error. |
| `--max-reads N` | Stop before more than `N` billed reads (default 10000; `0` is no limit). |
| `--page-size N` | Documents per query page (default 300). |
| `--keep-rejected` | Also write each rejected document under `.datatug-mirror/rejected/`, with only the policy's masks applied: **its undeclared fields and the values that break its definition are written in clear**. The run prints a warning line when it is used. Off by default. |
| `--continue-on-long-ids` | Do not stop at a document whose id is too long for one file per record: copy the rest, count it in the report, exit 3. |
| `--prune` | Delete record files whose document no longer exists, in the collections this run read completely. |
| `--restart` | Start over instead of resuming a run that stopped at its read budget. |
| `--snapshot` | Read every document as it was when the run started (see below). |

The flags of the other copies do not apply here, and each is refused with
`--NAME does not apply with --schema=destination` (exit 2): `--exclude`,
`--where`, `--limit` and `--filter-config` (the policy says what is copied),
`--overwrite`, `--from-schema`, `--to-schema`, `--fail-on-warning` (an
incomplete copy always exits 3), `--decimals`, `--parallel-streams` and
`--recover-job-ref`.

Exit codes: `0` complete; `1` a read or write failed, a document's id is
too long for its collection's one-file-per-record form, or a record file would
not read back as written; `2` usage or policy
error (also an output of another source);
`3` finished but incomplete (the read budget was reached, also by a dry run,
documents were rejected, a selected collection is unsupported, or validation
found problems); `4` the source cannot be opened.

### What is read, and paid for

Every page of a query is billed one read per document (one when it returns
none); a count is billed one read per 1000 documents counted; a document read is
one read. The plan reads as narrowly as the selection allows:

- When every parent id of a collection is fixed, by `--space`/`--path` or by a
  `paths` pattern, the run queries that one collection
  (`collection teams/t1/ext/calendar/events`), and when the document id is fixed
  too it reads that document. **Nothing outside it is read.**
- Otherwise it runs one collection-group query per collection id, shared by the
  defined collections with that id, ordered by document name, and limited by a
  document-name range to the longest fixed path prefix (`spaces/s1` for
  `--space s1`). The range is exact: `spaces/s1` never reads `spaces/s10` or
  `spaces/s1-old`. With `--space` or `--path`, **nothing outside that path is
  read**.
- **A collection-group query still reads, and bills, every same-named collection
  inside its range**, wherever its parents are: the `events` of another module,
  or an `events` collection the mirror does not define. Such documents are not
  copied: the report lists them as *other parents* or as *read outside the
  selection*, so the cost is visible. Pin the parent ids in `paths` where they
  are known to avoid paying for them; a dry run shows the counts first (its
  counts of a group are of every collection it reads).

`--max-reads` is checked before each request, so a run never passes it; a dry
run caps each count at what the budget pays for (and then says `≥N`). A run that
stops at the budget leaves `.datatug-mirror/resume.json`: the position of each
read, the counts so far, and the record files and folders written (case-folded,
so a resumed run still finds a name that differs only in case). **The next run
with the same source, selection, definitions and policy continues from
there**, so a budgeted backup makes progress run by run instead of reading the
same first documents again. It compares digests of each (the definitions'
files by content, the policy's copy, exclude, mask and paths): when one
changed, it says which and starts over. `--restart` starts over too. A finished
run removes the file.

`--snapshot` reads every document as it was when the run started, so documents
changed during a long run do not mix two moments; Firestore keeps old versions
for an hour (7 days with point-in-time recovery). A resumed run reuses the
first run's time while it is under 55 minutes old, and otherwise says that its
parts were read at different times. Counts are not read at the snapshot time.

### What a run writes

- The records, in the form and format their definitions declare
  ([Storage forms](#storage-forms)), with sorted keys and a fixed format, so
  **a re-run over unchanged data changes no file** (only changed records are
  rewritten). A document whose parent document does not exist is still written
  at its path, and counted as `below a missing parent`.
- `.datatug-mirror/output.json`: which database the output is a mirror of
  ([One output, one database](#one-output-one-database)) and the key encoding
  of its file names.
- The definitions of the mirror (only the definition files; the mirror's own
  example records are not copied), so the output is a complete inGitDB database.
- `.datatug-mirror/report.json`: per collection, the action, reads, records,
  rejected documents, other parents and masked fields, plus the root
  collections the mirror does not define, documents read outside the selection,
  and records whose document no longer exists (`stale`: a file, or
  `<file>#<encoded id>` for a record kept in one file; reported, and deleted only
  with `--prune`, and only for a collection this run read from its start to its
  end, so a run stopped by its budget deletes nothing it did not see).
- **An id that changed only in case** between runs (`Bob` became `bob`): on a
  filesystem that folds case (macOS and Windows by default), `Bob.yaml` and
  `bob.yaml` are one file, kept under its old name. **A run never deletes or
  writes over a live record because of that.** It renames the record file, and
  the folder of its sub-collections, to the new case (through a temporary
  name) only when the source proves the old id gone: one read of the old
  document, and one per defined sub-collection below it (Firestore keeps the
  sub-collections of a deleted document). These reads come out of
  `--max-reads`: when they would pass it, the run stops as at its budget, and
  the resume renames. Each rename is listed in the report
  (`renamed_record_files`) and the log (`renamed: old -> new`). When the old id
  is still in the source, or the old name is not one the mirror writes, the run
  stops (exit 1) with an error that names both ids, and writes neither: copy
  into a folder on a case-sensitive filesystem, or give one of them another id.
  On macOS a case-sensitive folder is a case-sensitive APFS volume: Disk
  Utility > Add Volume > format "APFS (Case-sensitive)", or
  `diskutil apfs addVolume <container disk> "Case-sensitive APFS" <name>`.
  Whether a folder folds case is found on the folder itself (are the two names
  one file?), never from the operating system's name. `--prune` never deletes
  the file of a document the run read (written or rejected) under another
  case: such a file is listed as kept (`kept_case_folded_files`, `kept:` in the
  log), not stale. On a case-sensitive filesystem the two names are two files:
  nothing is renamed, and the old one is stale.
- **What an interrupted run left** in a folder the run writes to: a rename's
  temporary name (`.tmp-case-…-<old name>`) is put back under its old name
  when that name is free, and the rename starts again; one that cannot be put
  back is left as it is and makes the run incomplete. A temporary file of an
  interrupted write (`.tmp-` and digits, a regular file) is removed: the file
  it was to replace was never touched. Any other `.tmp-…` entry is left as it
  is and reported. Both are listed in the report (`recovered_leftovers`) and the log
  (`recovered:`).
- **Rejected documents** (an undeclared field, a missing required field, a wrong
  type, a value a column does not allow, a date that does not parse, a name that
  differs only in case from another's) are not written into the database. The
  report keeps their path, the reasons and the names of the **declared** fields
  concerned, **never their values** (a reason that would quote a value, such as
  an enum check, names only the field). A field name the definition does not
  declare, and a map key, can be data (an email used as a key), so neither is
  shown: the report gives their count and a short fingerprint (an HMAC keyed by
  a random value of the run, so it cannot be looked up, and equal sets of names
  have equal fingerprints within one run); a value of a type the mirror cannot
  write is named by its column alone. Only `--keep-rejected` writes the record,
  with only the policy's masks applied, under
  `.datatug-mirror/rejected/<path>.json` (or notes that its path is too long).

The output is then checked by inGitDB's own validator (the checks of `ingitdb
validate`), and its findings are reported, without values.

#### Storage forms

The schema decides the storage form, never the mirror. A collection's
definition declares it in `record_file`, and the mirror writes:

| `record_file.type` | Written as | Keys |
|---|---|---|
| `map[string]any`, `name` with `{key}` alone (`{key}.yaml`) | one file per record | the encoded id ([Key encoding](#key-encoding)) |
| `map[$record_id]map[$field_name]any`, a fixed `name` (`members.yaml`) | all records of the collection in one file; for a sub-collection one file per parent document | the real document ids, as map keys |
| `[]map[string]any` | not written (`unsupported`): a list keeps no Firestore id | |

The format is `yaml` (or `yml`) or `json`; another format is `unsupported`. A
parent of a sub-collection must keep one file per record, since its records are
the folders of the sub-collection. A `record_file.name` or `records_dir` that
starts with `.tmp-` is `unsupported`: that is the prefix of what an
interrupted run leaves. Defaults, in the owner's words: "By default
we export to YAML", and "default is all records in single yaml file": when the
mirror writes a definition, it is one YAML file of all records,

```yaml
record_file:
  name: members.yaml
  type: "map[$record_id]map[$field_name]any"
  format: yaml
```

inGitDB itself requires every definition to declare its `record_file`, and the
mirror reads only existing definitions, so **a declared form always wins**: a
mirror whose definitions declare one JSON file per record is written that way.

**One file per record holds an id only when its file name fits.** In the
owner's words: "if schema record per file and we hit long id we fail". A
document whose file name (the encoded id, with the name's prefix and suffix)
is over 255 bytes, or whose path is longer than the system writes (259
characters on Windows, 1023 elsewhere), **fails the run**: it stops at that
document with exit code 1 and an error naming the collection, the document and
the length, and the fix (declare the collection with all its records in one
file). Its report says `stopped:` and is incomplete; its resume state is kept,
so the next run starts again at that document's page. Only
`--continue-on-long-ids` copies the rest instead: each such document is counted
as `not_copied_id_too_long` (never silently skipped) and the run exits 3. A dry
run, and a run before its first read, fail on the ids they know up front: those
of `--space`/`--path` and those the policy's `paths` name. The ids of documents
a query returns are known only once read (finding them in a dry run would cost
a full read), so the run finds those.

**One YAML file of all records.** Its top-level keys are the real document ids
(any id, of any length: a key is not a file name), in byte order, each holding
its record; a record is written, masked and validated exactly as a file of its
own would be (inGitDB's validator gets the real id as its key). The file is
rewritten (atomically: a temporary file, then a rename) only when the
collection, or the collection-group query that reads it, has been read to the
end. A run that stops at its budget in the middle keeps what it read as chunks
under `.datatug-mirror/partial/` (which inGitDB does not read), one small file
per page and record file, each record written once; **the record file itself
is not written, so no half-read collection looks complete**. The run that
resumes reads the chunks back and writes the whole file. Names that differ only
in case are two keys of one map, which no filesystem folds, so the case check
covers only the folders of the parents. A record no longer in the source stays
in the file and is reported as stale (`<file>#<id>`); `--prune` removes its key,
and the file when no key is left.

Size and memory, honestly: the records of every file of the collection being
read stay in memory until that collection is read to the end, and each file is
then encoded and written whole. For 100k records of about 1 KB that is about
100 MB of YAML, a few hundred MB of memory while it is read, and one write of
the whole file; a re-run over unchanged data rewrites nothing. A collection far
larger than memory should keep one file per record.

#### YAML

The mirror writes its own canonical YAML: keys in byte order, two-space
indent, and **every string and every key double-quoted**, with escapes for
line breaks, controls and other special characters. So no YAML reader, 1.1
(PyYAML) or 1.2 (yaml.v3, which inGitDB uses), takes a string or a document id
for a bool (`yes`, `on`, `no`, `true`), a number (`010`, `0x1F`, `1:20`,
`1e3`), a date, null (`~`, `null`), a merge key (`<<`) or a tag (`=`), and
leading or trailing line breaks and spaces are kept. A double always has a
decimal point in its mantissa (`1.0`, `-0.0`, `1.0e+21`, `5.0e-324`), which
both versions read as a double; an integer is decimal (int64 exact); bools are
`true`/`false` and null is `null`. `$time`, `$map` and the other tags are
ordinary keys.

**Every file is read back before it is written.** The mirror decodes the
content it is about to write (YAML with inGitDB's own parser) and compares it
with what it means to write; a decode error or any difference stops the run
with exit code 1 (`a record file would not read back as written`), its report
says `stopped:`, and that file (a record, a file of records by id, a partial
chunk, a pruned file) is not written.

What the tests prove: a corpus of strings YAML readers are known to take for
something else (each as a value, a key, a list item, a nested key, and as the
id of a file kept by id), the doubles and int64 extremes, and 3000 random
strings of any code point read back exactly with inGitDB's parser; every
scalar written is checked against the YAML 1.1 and 1.2 resolution rules
(strings and keys double-quoted, doubles matching both float rules, decimal
integers); and the corpus is read with PyYAML (YAML 1.1) too where python3 has
it, comparing types and values.

#### Key encoding

A Firestore id can hold any byte but `/` and be up to 1500 bytes long; a file
name cannot. Every id written as a file or folder name (a document of a
one-file-per-record collection, and each parent document) is written by the
encoding `tilde-hex/v1`, recorded in `.datatug-mirror/output.json`. It is to be
declared in the inGitDB schema and implemented once in ingitdb-go; the mirror
implements it until then. Exactly:

- **Encode** an id (a non-empty byte string with no `/`) byte by byte. A byte
  is kept when it is `A-Z`, `a-z`, `0-9` or one of `-` `_` `.` `@` `+` `=` `,`,
  except: a `.` that is the first or the last byte, and the first byte of an id
  that is a Windows device name (`CON`, `PRN`, `AUX`, `NUL`, `COM0`-`COM9`,
  `LPT0`-`LPT9`, in any case, alone or before a `.`: `con.json`, `COM1.txt`).
  Every other byte, `~` itself, every byte of a non-ASCII character, and those
  exceptions become `~` and the byte as two upper-case hex digits.
- **Decode** a name: `~` and two upper-case hex digits are that byte; every
  other byte is itself. A name is a valid encoding only when it decodes to a
  non-empty id **and** encoding that id gives the name back (so `~41`, `a~3a`
  and `~` alone are no names; each id has exactly one name, and no two ids share
  one).
- Examples: `.hidden` is `~2Ehidden`, `x.` is `x~2E`, `a:b` is `a~3Ab`, `a~b` is
  `a~7Eb`, `Zürich` is `Z~C3~BCrich`, `CON` is `~43ON`, `con.json` is
  `~63on.json`, `..\x` is `~2E.~5Cx`.
- The name of a one-file-per-record file is the `record_file.name` with `{key}`
  replaced by the encoded id, and must be at most 255 bytes.

So no id can leave its folder, be hidden, or clash with a reserved name, and
the run reads its files back to the same ids. Firestore's generated ids and the
usual slugs do not change. Two ids that differ only in case (`c` and `C`) would
be one file on macOS and Windows: the second is rejected and named, also when a
resumed run reads it, and also for two parents whose folders differ only in
case. inGitDB takes a one-file-per-record key from its file name as it is, so
**such a record's inGitDB key is the encoded id** until inGitDB decodes keys by
the schema-declared encoding.

#### One output, one database

The first run into an output records the database it reads in
`.datatug-mirror/output.json`: the resolved project id, the database id, and
the emulator host when `FIRESTORE_EMULATOR_HOST` is set (never a credential).
**Every later run into that output, its resume and its `--prune`, must read the
same database**, or it is refused before anything is read or written: another
project or database, the emulator instead of production (or another emulator,
or production instead of an emulator), or `--from env:NAME` whose variable now
names another project. So a budget-stopped production backup is never resumed
from an emulator, and `--prune` from an emulator never deletes production
records; an output created from an emulator is pruned only from that emulator.
An output folder that is not empty and has no `output.json` is refused too (also
with `--restart`): choose an empty folder.

#### Values

Values keep their Firestore type: timestamps are RFC 3339 text in UTC with
exactly 6 fraction digits (Firestore keeps microseconds), so they sort as text.
In a column typed `datetime`, or an item of a `[]datetime` column, a timestamp is
that text; everywhere else (in an `any`, a map or a list) it is
`{"$time": "2026-11-01T09:00:00.000000Z"}`, so it is not read back as a string.
The types JSON has no form for are tagged objects too: `{"$bytes": "<base64>"}`,
`{"$ref": "teams/t1"}`, `{"$geo": {"latitude": 1, "longitude": 2}}`,
`{"$vector": [..]}`, and `{"$float": "NaN" | "Infinity" | "-Infinity"}`; a map of
the source whose only key is one of these tags is written as `{"$map": {...}}`.
Whole doubles keep a `.0`, so an int and a double stay distinct.

What is not kept: a string written into a `datetime` column by an application
stays a string (and must parse, or the document is rejected); integers beyond
2^53 are exact in the file, but most JSON readers decode them as doubles and
lose digits; and masking removes or replaces what it masks.

### Safety

- **Read-only, enforced at the client.** Every request of the client the
  command opens passes a filter that lets through only the four read RPCs
  (`RunQuery`, `RunAggregationQuery`, `BatchGetDocuments`,
  `ListCollectionIds`); any other, a write, a transaction or a listen, is
  refused before it is sent, whatever code holds the client. Tests send writes
  through that client to a fake server and to the emulator and assert they are
  refused and never arrive; a test also parses the package source and fails on
  any call named like a Firestore write method. Use a read-only identity as
  well (for example `roles/datastore.viewer`).
- **Credentials only from the environment**: Google Application Default
  Credentials (`gcloud auth application-default login`, or
  `GOOGLE_APPLICATION_CREDENTIALS`), and the source URL from `--from` or the
  variable it names. Nothing is stored in the project.
- **Out of git, checked closed.** The output must be outside the mirror. It is
  allowed only when it is outside any git work tree, or when git ignores it by a
  (non-negated) rule of a `.gitignore` file that the repository tracks, so
  every clone ignores it too (`/data/` in the project's `.gitignore`). An
  output ignored only by this machine (the global ignore file,
  `core.excludesFile`, `.git/info/exclude`) or by an untracked `.gitignore`, an
  output git does not ignore, and any git failure all refuse the run. **The
  rule must be committed**: a `.gitignore` with changes not in `HEAD`, staged or
  not, does not count (other clones would not have them). Git runs
  without the `GIT_DIR`/`GIT_WORK_TREE` of the environment. Without git
  installed, an output below a `.git` folder is refused. The rejected records
  `--keep-rejected` writes are inside the output, under the same rule.
- **Bounded cost.** `--max-reads` stops a run before the budget is passed,
  `--dry-run` shows the size of a copy first, and runs resume.

The emulator enforces no index; if a production database lacks an index a query
needs, Firestore refuses it with `FAILED_PRECONDITION` and a link that creates
the index, and the run stops with exit code 1 (the records written so far stay,
and a re-run once the index is built resumes after a budget stop). Try
`--dry-run` first: it runs the same queries as counts. For tests, set
`FIRESTORE_EMULATOR_HOST` and use a `demo-` project; the run prints that it
reads the emulator.
