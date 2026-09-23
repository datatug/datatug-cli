module github.com/datatug/datatug-cli

go 1.27.0

//replace github.com/datatug/datatug-core => ../datatug-core

// All sibling deps now ship the upstream changes db-copy depends on
// at tagged versions:
//   - dal-go/dalgo2sql v0.6.2       (ANSI/SQLite LIMIT N emission via
//                                    sqlite_emit.go — stopgap until
//                                    dialect-aware emission lands per
//                                    dal-go/dalgo/spec/ideas/
//                                    dalgo-dialect-aware-sql-emission)
//   - dal-go/dalgo2sqlite v0.0.1    (DATETIME / NUMERIC(p,s) recognition)
//   - ingitdb/ingitdb-cli v1.9.0    (record CRUD + auto-register +
//                                    Decimal/Bytes type mapping)
require (
	charm.land/bubbles/v2 v2.2.1
	charm.land/bubbletea/v2 v2.0.9
	charm.land/fang/v2 v2.0.1
	charm.land/lipgloss/v2 v2.0.6
	cloud.google.com/go/firestore v1.25.0
	github.com/DATA-DOG/go-sqlmock v1.5.2
	github.com/NimbleMarkets/ntcharts/v2 v2.2.0
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/atotto/clipboard v0.1.4
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/dal-go/dalgo v0.85.0
	github.com/dal-go/dalgo2http v0.2.0
	github.com/dal-go/dalgo2sql v0.18.0
	github.com/dal-go/dalgo2sqlite v0.1.11
	github.com/dal-go/record v0.1.3
	github.com/datatug/cliformat v0.0.3
	github.com/datatug/datatug-core v0.41.0
	github.com/datatug/sql2csv v0.0.0-20260826045256-b0d582f72f50
	github.com/denisenkom/go-mssqldb v0.12.3
	github.com/dimetron/pi-go v0.1.4
	github.com/evertras/bubble-table v0.23.0
	github.com/filetug/filetug v0.3.0
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/go-git/go-git/v5 v5.19.2
	github.com/google/go-github/v91 v91.0.0
	github.com/google/uuid v1.6.0
	github.com/gosuri/uitable v0.0.4
	github.com/ingitdb/dalgo2ingitdb v0.6.1
	github.com/ingitdb/ingitdb-go/ingitdb v0.7.3
	github.com/julienschmidt/httprouter v1.3.0
	github.com/mattn/go-sqlite3 v1.14.50
	github.com/mitchellh/go-homedir v1.1.0
	github.com/openvaultdb/openvaultdb-go v0.6.2
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/posthog/posthog-go v1.24.3
	github.com/rivo/tview v0.42.0
	github.com/sneat-co/sneat-go-core v0.67.3
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.12.1
	github.com/strongo/buildinfo v0.3.0
	github.com/strongo/cli-helpers v0.21.0
	github.com/strongo/deviceauth v0.1.0
	github.com/strongo/logus v0.4.3
	github.com/strongo/random v0.0.2
	github.com/strongo/slice v0.3.10
	github.com/strongo/strongo-tui v0.1.0
	github.com/strongo/validation v0.0.13
	github.com/xo/dburl v0.24.2
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/oauth2 v0.36.0
	golang.org/x/text v0.41.0
	google.golang.org/adk/v2 v2.4.0
	google.golang.org/api v0.296.0
	google.golang.org/genai v1.71.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.59.0
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.23.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/longrunning v1.2.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/LindsayBradford/go-dbf v1.0.0-alpha-5 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/RoaringBitmap/roaring/v2 v2.28.0 // indirect
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/anthropics/anthropic-sdk-go v1.71.0 // indirect
	github.com/axgle/mahonia v0.0.0-20180208002826-3358181d7394 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/bits-and-blooms/bitset v1.24.6 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/catppuccin/go v0.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/glamour v1.0.0 // indirect
	github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260812204455-68fa937c71be // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/charmtone v0.0.0-20250603201427-c31516f43444 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20260813141921-f091cedeaf78 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudflare/circl v1.6.4 // indirect
	github.com/crediterra/money v0.3.8 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/dal-go/dalgo2firestore v0.10.3 // indirect
	github.com/dal-go/dalgo2mysql v0.2.0 // indirect
	github.com/dal-go/dalgo2postgres v0.2.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dlclark/regexp2/v2 v2.6.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/georgysavva/scany/v2 v2.1.4 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.9.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-sql-driver/mysql v1.10.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/gofrs/flock v0.13.1 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-github/v88 v88.0.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/safehtml v0.1.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.21 // indirect
	github.com/googleapis/gax-go/v2 v2.24.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ingitdb/dalgo2ingitdb4github v0.2.3 // indirect
	github.com/ingr-io/ingr-go v0.0.2 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	//github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/jlaffaye/ftp v0.2.2 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/lrstanley/bubblezone/v2 v2.0.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.28 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/mango v0.1.0 // indirect
	github.com/muesli/mango-cobra v1.2.0 // indirect
	github.com/muesli/mango-pflag v0.1.0 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/roff v0.1.0 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/ollama/ollama v0.33.3 // indirect
	github.com/openai/openai-go/v3 v3.56.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/qri-io/jsonpointer v0.1.1 // indirect
	github.com/qri-io/jsonschema v0.2.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/richardlehane/mscfb v1.0.7 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/strongo/analytics v0.2.8 // indirect
	github.com/strongo/decimal v0.1.2 // indirect
	github.com/strongo/dsstore v0.0.8 // indirect
	github.com/strongo/strongoapp v0.31.55 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/excelize/v2 v2.11.0 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	github.com/yuin/goldmark v1.8.5 // indirect
	github.com/yuin/goldmark-emoji v1.0.6 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.70.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.70.0 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/log v0.22.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.starlark.net v0.0.0-20260708150628-5395d018f003 // indirect
	go.uber.org/mock v0.6.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	rsc.io/omap v1.2.0 // indirect
	rsc.io/ordered v1.1.1 // indirect
)
