# Query MongoDB

DataTug reads MongoDB collections directly using TugQL and the DALgo MongoDB adapter. Store credentials in an environment variable and use a MongoDB account with the `read` role for your database:

```sh
export SHOP_MONGO_URL='mongodb://reader:password@localhost:27017/shop?authSource=shop'
datatug query run --db env:SHOP_MONGO_URL --from products --no-policies --format json
```

Atlas URLs (`mongodb+srv://reader:password@cluster.example/shop`) work too. The URI must select the database. Configure TLS for your deployment.

A TugQL file can filter, project and order the collection:

```yaml
from: {name: products}
columns:
  - field: name
  - field: price
where:
  op: '>='
  left: {field: price}
  right: {param: minPrice}
orderBy:
  - field: price
    desc: true
limit: 20
```

```sh
datatug query run --db env:SHOP_MONGO_URL -f products.tugql.yaml --var minPrice=10 --no-policies --format json
```

`--no-policies` explicitly selects unrestricted reads for this local command. Configured access policies instead pass through the shared structured-query executor. Never grant unrestricted source access to an untrusted principal.

Supported: one root collection; top-level scalar equality and range comparisons; AND/OR; projection; ordering; offsets up to 100000; limits up to 1000. An omitted limit returns at most 1000 documents. Results also have a 16 MiB BSON budget. MongoDB's native null, missing-field and comparison semantics apply. Unsupported expressions fail explicitly. Nested field paths, joins, aggregation, cursors, raw Mongo commands, JavaScript and writes are unavailable. Use TugQL for MongoDB; `--sql` is for SQL sources. Copying into MongoDB is refused.

Run the real command journey using `DATATUG_TEST_MONGODB_URI=mongodb://127.0.0.1:27017/dalgo_cli_test go test ./apps/datatugapp/commands -run TestMongoDBQueryJourney`. That database is disposable; the test creates and drops its products collection.
