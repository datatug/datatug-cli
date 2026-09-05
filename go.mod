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
	charm.land/fang/v2 v2.0.1
	cloud.google.com/go/firestore v1.25.0
	github.com/DATA-DOG/go-sqlmock v1.5.2
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/atotto/clipboard v0.1.4
	github.com/dal-go/dalgo v0.79.1
	github.com/dal-go/dalgo2sql v0.11.4
	github.com/dal-go/dalgo2sqlite v0.1.8
	github.com/dal-go/record v0.1.3
	github.com/datatug/cliformat v0.0.3
	github.com/datatug/sql2csv v0.0.0-20260826045256-b0d582f72f50
	github.com/denisenkom/go-mssqldb v0.12.3
	github.com/filetug/filetug v0.3.0
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/go-git/go-git/v5 v5.19.2
	github.com/google/go-github/v91 v91.0.0
	github.com/google/go-github/v91 v91.0.0
	github.com/google/uuid v1.6.0
	github.com/gosuri/uitable v0.0.4
	github.com/ingitdb/dalgo2ingitdb v0.3.5
	github.com/ingitdb/ingitdb-go/ingitdb v0.6.0
	github.com/julienschmidt/httprouter v1.3.0
	github.com/mattn/go-sqlite3 v1.14.50
	github.com/mitchellh/go-homedir v1.1.0
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/posthog/posthog-go v1.24.3
	github.com/qri-io/jsonschema v0.2.1
	github.com/rivo/tview v0.42.0
	github.com/sneat-co/sneat-go-core v0.67.3
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.12.1
	github.com/strongo/buildinfo v0.2.1
	github.com/strongo/logus v0.4.3
	github.com/strongo/random v0.0.1
	github.com/strongo/slice v0.3.9
	github.com/strongo/strongo-tui v0.1.0
	github.com/strongo/validation v0.0.12
	github.com/xo/dburl v0.24.2
	github.com/zalando/go-keyring v0.2.8
	go.uber.org/mock v0.6.0
	golang.org/x/oauth2 v0.36.0
	google.golang.org/api v0.296.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	charm.land/lipgloss/v2 v2.0.1 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.23.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/longrunning v1.2.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/RoaringBitmap/roaring/v2 v2.26.0 // indirect
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/bits-and-blooms/bitset v1.24.6 // indirect
	github.com/catppuccin/go v0.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/glamour v1.0.0 // indirect
	github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260205113103-524a6607adb8 // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/charmtone v0.0.0-20250603201427-c31516f43444 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20260803091719-3755ebad01b1 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudflare/circl v1.6.4 // indirect
	github.com/crediterra/money v0.3.8 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dlclark/regexp2/v2 v2.5.2 // indirect
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
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/gofrs/flock v0.13.0 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.20 // indirect
	github.com/googleapis/gax-go/v2 v2.24.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ingr-io/ingr-go v0.0.2 // indirect
	//github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/jlaffaye/ftp v0.2.2 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
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
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/qri-io/jsonpointer v0.1.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/strongo/analytics v0.2.8 // indirect
	github.com/strongo/decimal v0.1.2 // indirect
	github.com/strongo/dsstore v0.0.8 // indirect
	github.com/strongo/strongoapp v0.31.55 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yuin/goldmark v1.8.5 // indirect
	github.com/yuin/goldmark-emoji v1.0.6 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.70.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.70.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.starlark.net v0.0.0-20260708150628-5395d018f003 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/image v0.44.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.57.0 // indirect
)
