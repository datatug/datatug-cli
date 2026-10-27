# Query a Firestore database

DataTug can query your own Firestore database through DALgo structured queries:

```sh
gcloud auth application-default login
datatug query run --db 'firestore://my-google-project/(default)' --from customers --no-policies --format json
```

Use `firestore://PROJECT/DATABASE` for a named database; omitting `/DATABASE`
selects `(default)`. Authentication uses Google Application Default Credentials,
including `GOOGLE_APPLICATION_CREDENTIALS` when configured. The Google identity
needs Firestore read permission in the selected project/database. This is a
server SDK connection governed by IAM; Firebase client Security Rules do not
apply. Use an identity with read-only IAM permission for query-only access.

For local tests, set `FIRESTORE_EMULATOR_HOST=127.0.0.1:PORT` and use a disposable
project such as `demo-datatug-firestore`. The Google SDK then connects without
production credentials. Never set that variable for a production query.

`--no-policies` performs a trusted operator query. To enforce DataTug owner
policies, configure and use the regular `--as` / `--role` query options instead.
Saved project catalogs use driver `firestore` and a connection descriptor in
`path` containing `dsnEnv: DATATUG_FIRESTORE_SOURCE_URL`; that environment variable holds
the Firestore URL. Credentials stay outside project files.

Filters, ordering, limits, offsets, and document cursors use the Firestore DALgo
adapter. Firestore indexes and provider query constraints still apply. SQL,
joins, projection aliases, computed projections, GROUP BY and HAVING are unsupported. Copying into Firestore and DALgo read-write transactions are rejected. The
query source still requires read-only IAM credentials as defense in depth;
DataTug cannot reduce permissions already granted to your Google identity.
This source is separate from Firestore used internally to store DataTug projects
or watch state; the URL always selects the user's queried database.
