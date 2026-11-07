# Cross-database queries

One TugQL text query can read tables of several databases. The query declares each database it reads in a
`databases (` block, and names a table of one as `alias::table`. DataTug binds each alias to a source, reads each
source on its own through your access policies, and joins the rows itself. No joined query is sent to any database.

The language itself is described in the TugQL reference,
[cross-database sources](https://github.com/tugql/tugql/blob/main/reference/cross-database.md). This page covers how
`datatug query run` binds the aliases. The rules are in the spec:
[REQ: cross-database-sources](../../spec/features/cli/query/README.md#req-cross-database-sources).

## Example: Chinook invoices and a countries table

Chinook's `Invoice` table holds each invoice's `BillingCountry` by name. A second database, `geo.sqlite`, holds a
`countries` table with each country's ISO `code`. `invoice-countries.tugql`:

```
databases (
  sales = 'datatug://datatug-demo/chinook'
  geo
)
from sales::Invoice as i
join geo::countries as c
  on i.BillingCountry = c.name
where i.InvoiceId <= 9
select i.InvoiceId, i.BillingCountry, c.code
```

`sales` is bound by its target, the `chinook` source of the registered project `datatug-demo` (`datatug demo install`
gives you that project). `geo` has no target, so the run binds it:

```bash
datatug query run -f invoice-countries.tugql --db geo=sqlite:///data/geo.sqlite --no-policies --format json
```

```json
[
{"$key":"…","InvoiceId":1,"BillingCountry":"Germany","code":"DE"},
{"$key":"…","InvoiceId":2,"BillingCountry":"Norway","code":"NO"},
…
]
```

## Binding an alias

| Where the query runs | Alias with a `datatug://` target | Alias with no target | Alias with a connection-string target |
|---|---|---|---|
| Ad hoc (`-f`) | that project source | `--db <alias>=<url\|datatug://...>` | allowed |
| Saved (`--project --query`) | that project source; `datatug:///<db>` is the query's own project | the project source of the same name | refused (exit 2) |

- `--db` is repeatable. `--db <alias>=...` binds an alias, and replaces the alias's declared target. A `--db` without an
  alias is the default database of a query that declares no databases. It has nothing to read next to a `databases`
  block, so DataTug refuses that combination.
- A declared alias that nothing binds is a usage error (exit 2) that names the alias. So is a `--db <alias>=` that the
  query does not declare.
- A target never carries a password: DALgo refuses one. Put credentials in an `env:NAME` source or in the project's
  connection descriptor, as for any `--db`.
- A saved query is project knowledge, so it names its sources by project reference, never by connection string.
- A `datatug://` target without `?env=` uses `--env` (or the only environment) in a saved query of its own project.
  Otherwise it is resolved as `--db datatug://...` is.

A saved text query is a `<id>.query.tugql` file beside its `<id>.query.json`:

```
databases (
  sales = 'datatug:///chinook'
  geo
)
from sales::Invoice as i
join geo::countries as c
  on i.BillingCountry = c.name
select i.InvoiceId, i.BillingCountry, c.code
```

```bash
datatug query run --project datatug-demo --query invoice-countries --as agent --role agent
```

## Policies and sensitivity labels

Each source is a separate secured read. The policies of the run apply to every one: `--policy`, `--policies-dir` or
`--no-policies` with `--as`/`--role`/`--group` for an ad-hoc run, and the project's own `policies/` for a saved query.
In the saved example, the demo's agent policy lets the agent read only Canadian invoices, so only those rows come back.

A source that is a project source is also checked against that project's sensitivity labels, by its catalog id,
whatever the alias is called. A federated read cannot mask yet, so a read of a labelled column is refused (exit 5). A
connection-string source belongs to no project, so no label applies to it, as for `--db <url>`.

## Result files

With `--result-id`, `meta.json` records the root source as `db`. Each participant's `db` is the full
`datatug://<project>/<db>?env=<env>` reference of its source, or `null` for a connection-string source. It never holds
a connection string, a path or a credential.
