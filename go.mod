module github.com/datatug/datatug-cli

go 1.25.5

//replace github.com/datatug/datatug-core => ../datatug-core

//replace github.com/dal-go/dalgo => ../../dal-go/dalgo
//
//replace github.com/dal-go/dalgo2sql => ../../dal-go/dalgo2sql

require (
	cloud.google.com/go/firestore v1.20.0
	github.com/alecthomas/chroma/v2 v2.22.0
	github.com/dal-go/dalgo2sql v0.4.19
	github.com/datatug/filetug v0.0.21
	github.com/datatug/sql2csv v0.0.0-20200927071952-a7fb986d1ec2
	github.com/denisenkom/go-mssqldb v0.12.3
	github.com/gdamore/tcell/v2 v2.13.7
	github.com/go-git/go-git/v5 v5.16.4
	github.com/google/go-github/v81 v81.0.0
	github.com/google/uuid v1.6.0
	github.com/gosuri/uitable v0.0.4
	github.com/mattn/go-sqlite3 v1.14.33
	github.com/mitchellh/go-homedir v1.1.0
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/posthog/posthog-go v1.8.2
	github.com/rivo/tview v0.42.0
	github.com/stretchr/testify v1.11.1
	github.com/strongo/logus v0.4.0
	github.com/strongo/validation v0.0.7
	github.com/urfave/cli/v3 v3.6.1
	github.com/xo/dburl v0.24.2
	github.com/zalando/go-keyring v0.2.6
	go.uber.org/mock v0.6.0
	golang.org/x/oauth2 v0.34.0
	google.golang.org/api v0.259.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/RoaringBitmap/roaring/v2 v2.14.4 // indirect
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/georgysavva/scany/v2 v2.1.4 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
)

require (
	al.essio.dev/pkg/shellescape v1.6.0 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.18.0 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/longrunning v0.8.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.3.0 // indirect
	github.com/atotto/clipboard v0.1.4
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.2 // indirect
	github.com/crediterra/money v0.3.0 // indirect
	github.com/cyphar/filepath-securejoin v0.6.1 // indirect
	github.com/dal-go/dalgo v0.41.1
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.7.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.9 // indirect
	github.com/googleapis/gax-go/v2 v2.16.0 // indirect
	//github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/julienschmidt/httprouter v1.3.0
	github.com/kevinburke/ssh_config v1.4.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/pjbgf/sha1cd v0.5.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/qri-io/jsonpointer v0.1.1 // indirect
	github.com/qri-io/jsonschema v0.2.1
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.2 // indirect
	github.com/sneat-co/sneat-go-core v0.54.11
	github.com/strongo/analytics v0.2.3 // indirect
	github.com/strongo/decimal v0.1.1 // indirect
	github.com/strongo/random v0.0.1
	github.com/strongo/slice v0.3.3
	github.com/strongo/strongoapp v0.31.13 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.64.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.64.0 // indirect
	go.opentelemetry.io/otel v1.39.0 // indirect
	go.opentelemetry.io/otel/metric v1.39.0 // indirect
	go.opentelemetry.io/otel/trace v1.39.0 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/term v0.39.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/genproto v0.0.0-20260112192933-99fd39fd28a9 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260112192933-99fd39fd28a9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260112192933-99fd39fd28a9 // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
)
