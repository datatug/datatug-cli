# DataTug agent server

It is started using `serve` command like:

> datatug serve /t=<path_to_project_folder>

## OpenVaultDB targets

Protected browser queries use fixed targets from `~/datatug/.datatug.yaml`.
The bearer is read from the named environment variable and is never stored in
a DataTug project or returned to the browser:

```yaml
webui:
  origin: https://datatug.app
openvaultdb:
  targets:
    crm:
      baseUrl: https://vault.example
      databaseId: crm
      tokenEnv: DATATUG_CRM_OVDB_TOKEN
```

```sh
export DATATUG_CRM_OVDB_TOKEN='<OpenVaultDB bearer>'
datatug serve --project ./my-project
```

When at least one target is configured, `serve` launches the web UI with a
random per-process agent token in the URL fragment. The web UI sends it only
in `X-Datatug-Agent-Token` to the configured origin. The protected routes
accept target IDs; they never accept an upstream URL, bearer, or caller
principal.

`POST /datatug/ovdb/targets` returns configured target IDs and database IDs
only. The same protected session also exposes `query`, `explain`, `update`
and `evidence` routes below `/datatug/ovdb/`.

This will start HTTP server that listens by default on port # 8989.
You can check it started by opening http://localhost:8989.

The port can be changed with `--port=<####>` parameter.

Run `datatug serve --help` to see all available parameters. 

## [Endpoints](endpoints)
You can learn about server endpoints [here](endpoints).
