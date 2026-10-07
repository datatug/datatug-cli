# Compare project databases

`datatug compare <left-database> <right-database> --project <project>` compares the two configured source identities. It reads the complete table inventories and every row, compares schema metadata, and reports counts of added, removed, changed, and unchanged records. `--details` prints up to `--limit` difference examples; the limit never truncates the comparison.

To keep memory bounded while sorting unordered providers, normalized rows are staged in private local temporary SQLite files and removed when the comparison finishes. Temporary disk use scales with the data being compared.

The existing agent-backed query comparison remains available with `--query`, `--left`, and `--right` and no positional database names.

## Compare two environments

The DemoDB project declares the SQLite edition in `dev` and the PostgreSQL edition in `QA`. A hosted-pending edition is metadata about a future public endpoint; the CLI never opens that metadata URL. Supply an explicit source for each side when one is not otherwise executable. Populate PostgreSQL connection variables through a secret manager or another secure input method, then pass `env:VARIABLE`; this keeps the DSN out of DataTug's command arguments and process list. Avoid typing a literal DSN in an `export` command if your shell records commands in history.

```sh
datatug compare chinook-sqlite chinook-postgresql \
  --project ./demo-project-1 \
  --left-environment dev --right-environment QA \
  --left-source env:DEMO_CHINOOK_SQLITE_URL \
  --right-source env:DEMO_CHINOOK_QA_PG_URL \
  --right-schema chinook --details --limit 20
```

An explicit source binding must use the provider declared by that project connection. It supplies the executable source, not a replacement identity. The edition must still belong to the selected environment. If a binding is omitted, a not-ready hosted edition is refused. The command does not read `source`, `descriptor`, manifest, or webpage URLs from the connection registry as database URLs.

The command prints a line like this after the full comparison completes:

```text
dev/chinook-sqlite -> QA/chinook-postgresql: +<added> added, -<removed> removed, ~<changed> changed, <unchanged> unchanged
```

The counts come from the selected data at run time. A keyless table is compared as a multiset: identical duplicates are preserved, while a changed row counts as one removal and one addition. Matching declared primary keys are used when both sources expose a compatible key. Use an explicit key map when a target does not expose key metadata.

## Relation-name and key mappings

No relation is paired by column shape, position, or row count. If providers use different physical names, provide an explicit mapping. A small mapping can use repeatable flags:

```sh
datatug compare adventureworks-sqlite adventureworks-bigquery \
  --project ./demo-project-1 \
  --table-map 'Production.Document=production_document' \
  --key-map 'Production.Document=ProductID,DocumentID'
```

For many relations, put the mappings in a project-relative JSON file and pass `--mapping-file compare-map.json`:

```json
{
  "format": "datatug-database-compare-mappings/v1",
  "relations": [
    {
      "left": "Production.Document",
      "right": "production_document",
      "keyColumns": ["ProductID", "DocumentID"]
    }
  ]
}
```

The file is limited to 4 MiB, rejects unknown fields, and requires a one-to-one relation mapping. `keyColumns` is optional; without it, the source primary keys are used if both sides report the same usable key, otherwise the relation is compared as a multiset. Conflicting flags and file entries, unknown relation names, incompatible types, duplicate keys, and null key values fail closed.

## Source behavior

The command supports registered DALgo source schemes and the read-only BigQuery source adapter. BigQuery is read through table metadata and physical table-data APIs; this command does not submit a BigQuery query job. BigQuery declared keys and provider indexes are not assumed to exist unless both source adapters expose and validate them. Cross-provider view SQL bodies are not compared as equivalent executable SQL; view inventory and ordered columns are compared separately.

Source-specific limitations are reported or rejected rather than converted silently. Exact decimal, integer, binary, boolean, and temporal values are normalized using declared source semantics. An unsupported or incompatible value stops the comparison instead of producing a false equality result.

For PostgreSQL, `--left-schema` and `--right-schema` scope catalog discovery and each row read is schema-qualified. The selected schema therefore wins even when the connection's `search_path` points at a different schema containing a table with the same name.
