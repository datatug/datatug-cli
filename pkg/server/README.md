# DataTug agent server

It is started using `serve` command like:

> datatug serve /t=<path_to_project_folder>

This will start HTTP server that listens by default on port # 8989.
You can check it started by opening http://localhost:8989.

The port can be changed with `--port=<####>` parameter.

Run `datatug serve --help` to see all available parameters.

The server answers a request only when it comes from one of its own pages or from a tool on the
same machine: see [Who is answered](endpoints/README.md#who-is-answered). The routes that change
a project need `--allow-writes`, and the route that connects to a database server recorded in the
project needs `--allow-live-connections`; both are refused with a `403` by default.

## [Endpoints](endpoints)
You can learn about server endpoints [here](endpoints).
