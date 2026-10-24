set -eu
: "${DATATUG_MCP_SOURCE_SHA:?Set the accepted CLI source revision}"
: "${DATATUG_DEMO_REVISION:?Set the accepted demo fixture revision}"
: "${DATATUG_MCP_SOURCE_DIR:?Set the absolute source checkout path}"
: "${DATATUG_MCP_TEST_BINARY:?Set the absolute downloaded CLI binary path}"
: "${DATATUG_DEMO_CHECKOUT:?Set the absolute demo checkout path}"

prepare_checkout() (
  set -eu
  directory=$1 repository=$2 revision=$3
  case "$directory" in /*) ;; *) echo "Use an absolute checkout path" >&2; exit 1 ;; esac
  if [ -e "$directory" ]; then
    git -C "$directory" rev-parse --is-inside-work-tree >/dev/null
    git -C "$directory" fetch origin
    [ "$(git -C "$directory" rev-parse HEAD)" = "$revision" ] || {
      echo "Existing checkout is at another revision; use an isolated checkout" >&2; exit 1;
    }
    [ -z "$(git -C "$directory" status --porcelain)" ] || {
      echo "Existing checkout has local changes; preserve them and use an isolated checkout" >&2; exit 1;
    }
  else
    git clone --no-checkout "$repository" "$directory"
    git -C "$directory" checkout --detach "$revision"
  fi
)
prepare_checkout "$DATATUG_MCP_SOURCE_DIR" https://github.com/datatug/datatug-cli.git "$DATATUG_MCP_SOURCE_SHA"
prepare_checkout "$DATATUG_DEMO_CHECKOUT" https://github.com/datatug/datatug-demo-project.git "$DATATUG_DEMO_REVISION"
cd "$DATATUG_MCP_SOURCE_DIR"
GOPRIVATE=github.com/datatug/datatug-core go mod download
npm --prefix pkg/mcpserver/app ci --ignore-scripts
npm --prefix pkg/mcpserver/app exec -- playwright install chromium
DATATUG_MCP_TEST_BINARY="$DATATUG_MCP_TEST_BINARY" DATATUG_DEMO_PROJECT_DIR="$DATATUG_DEMO_CHECKOUT/demo-project-1" DATATUG_TELEMETRY=0 DO_NOT_TRACK=1 npm --prefix pkg/mcpserver/app run test:browser
