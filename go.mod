module github.com/datatug/datatug-cli

go 1.26.4

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
// No replace directives needed.

require (
	cloud.google.com/go/firestore v1.23.0
	github.com/DATA-DOG/go-sqlmock v1.5.2
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/dal-go/dalgo2sql v0.9.2
	github.com/dal-go/dalgo2sqlite v0.0.20
	github.com/datatug/cliformat v0.0.1
	github.com/datatug/filetug v0.0.21
	github.com/datatug/sql2csv v0.0.0-20260327145511-68fc0416403d
	github.com/denisenkom/go-mssqldb v0.12.3
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/go-git/go-git/v5 v5.19.1
	github.com/google/go-github/v88 v88.0.0
	github.com/google/go-github/v89 v89.0.0
	github.com/google/uuid v1.6.0
	github.com/gosuri/uitable v0.0.4
	github.com/ingitdb/dalgo2ingitdb v0.2.1
	github.com/ingitdb/ingitdb-go/ingitdb v0.0.1
	github.com/mattn/go-sqlite3 v1.14.47
	github.com/mitchellh/go-homedir v1.1.0
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/posthog/posthog-go v1.17.5
	github.com/rivo/tview v0.42.0
	github.com/stretchr/testify v1.11.1
	github.com/strongo/logus v0.4.1
	github.com/strongo/strongo-tui v0.0.1
	github.com/strongo/validation v0.0.9
	github.com/urfave/cli/v3 v3.10.1
	github.com/xo/dburl v0.24.2
	github.com/zalando/go-keyring v0.2.8
	go.uber.org/mock v0.6.0
	golang.org/x/oauth2 v0.36.0
	google.golang.org/api v0.288.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/RoaringBitmap/roaring/v2 v2.19.0 // indirect
	github.com/andybalholm/brotli v1.1.1 // indirect
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/georgysavva/scany/v2 v2.1.4 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/gofrs/flock v0.13.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ingr-io/ingr-go v0.0.2 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.3.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	go.starlark.net v0.0.0-20260613233743-8ba36ccb83fb // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.53.0 // indirect
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.20.0 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/longrunning v1.0.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/atotto/clipboard v0.1.4
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/crediterra/money v0.3.1 // indirect
	github.com/cyphar/filepath-securejoin v0.6.1 // indirect
	github.com/dal-go/dalgo v0.62.10
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.17 // indirect
	github.com/googleapis/gax-go/v2 v2.23.0 // indirect
	//github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/julienschmidt/httprouter v1.3.0
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.21 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/qri-io/jsonpointer v0.1.1 // indirect
	github.com/qri-io/jsonschema v0.2.1
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.2 // indirect
	github.com/sneat-co/sneat-go-core v0.58.6
	github.com/strongo/analytics v0.2.5 // indirect
	github.com/strongo/decimal v0.1.1 // indirect
	github.com/strongo/random v0.0.1
	github.com/strongo/slice v0.3.5
	github.com/strongo/strongoapp v0.31.41 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.68.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.68.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/term v0.44.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20260420184626-e10c466a9529 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260630182238-925bb5da69e7 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260630182238-925bb5da69e7 // indirect
	google.golang.org/grpc v1.82.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
)
