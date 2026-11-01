package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/jackc/pgx/v5/pgconn"
)

const poolerRevision = "f03e9bf263d7ccd5d1d152a61811bf3258dd6b87"

var poolerDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var poolerContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var poolerIdentifier = regexp.MustCompile(`^[a-z0-9_]+$`)

// No Docker, listener, database or credential-file operation occurs before this
// CI-only barrier. These are native session facts, not an authorization policy,
// credential attestation, tier promotion or completion of the Task 2 matrix.
func TestPostgresNativeReadonlyThroughTransactionPooler(t *testing.T) {
	requireNativeReadonlyCI(t)
	if !poolerCI(runtime.GOOS, os.Getenv) {
		t.Skip("pooler native proof requires the owned Linux PostgreSQL CI job")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	server := newRealPgServer(t)
	poolerCheck(t, server.host == "127.0.0.1" && server.port == "54329", "owned PostgreSQL endpoint")
	poolerCheck(t, strconv.Itoa(realPgServerMajor(t, server.scanURL)) == os.Getenv(realPgMajorVar), "actual PostgreSQL major")
	observer := poolerSQL(t, server.scanURL)
	_, err := observer.ExecContext(ctx, `CREATE TABLE pooler_guard_probe(id integer PRIMARY KEY, note text); INSERT INTO pooler_guard_probe VALUES(1,'original')`)
	poolerCheck(t, err == nil, "create owned mutation probe")
	endpoint := startNativePooler(t, ctx, server)
	applicationA, applicationB := "dt_pool_a_"+randomHex(t, 6), "dt_pool_b_"+randomHex(t, 6)
	clientURL := func(application string) string {
		parsed, parseErr := url.Parse(server.scanURL)
		poolerCheck(t, parseErr == nil, "parse owned client URL")
		parsed.Host = endpoint
		query := parsed.Query()
		query.Set("default_transaction_read_only", "on") // deliberately ignored by the proxy
		query.Set("application_name", application)
		query.Set("connect_timeout", "5")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	unchanged := func() {
		var count int
		var note string
		poolerCheck(t, observer.QueryRowContext(ctx, `SELECT count(*),min(note) FROM pooler_guard_probe`).Scan(&count, &note) == nil && count == 1 && note == "original", "independent unchanged database effects")
	}
	raw := poolerSQL(t, clientURL(applicationA))
	var mode string
	poolerCheck(t, raw.QueryRowContext(ctx, `SHOW transaction_read_only`).Scan(&mode) == nil && mode == "off", "N-PG11 lost startup parameter discriminator")
	// The owned role is actually writable outside DataTug. Do not call its
	// reachable credential read-only because a verified transaction blocks it.
	_, err = raw.ExecContext(ctx, `INSERT INTO pooler_guard_probe VALUES(2,'writable baseline'); DELETE FROM pooler_guard_probe WHERE id=2`)
	poolerCheck(t, err == nil, "writable credential positive control")
	poolerCheck(t, raw.Close() == nil, "close initial unguarded client")
	unchanged()

	guarded := func(application string, worker dal.ROTxWorker) error {
		ref, parseErr := dbcopy.Parse(clientURL(application))
		poolerCheck(t, parseErr == nil, "parse guarded client")
		db, openErr := ref.Open(ctx)
		poolerCheck(t, openErr == nil, "open guarded client")
		closer, ok := dal.As[io.Closer](db)
		poolerCheck(t, ok && closer != nil, "owned guarded client closer")
		defer func() { poolerCheck(t, closer.Close() == nil, "close guarded client") }()
		return dbcopy.RunVerifiedPostgresReadonly(ctx, db, worker)
	}
	var firstPID int
	checkRead := func(application string) int {
		var pid int
		readErr := guarded(application, func(readCtx context.Context, tx dal.ReadTransaction) error {
			row, readErr := poolerRecord(readCtx, tx, `SELECT pg_backend_pid()::text AS pid,current_setting('transaction_read_only') AS mode,current_setting('application_name') AS application`)
			if readErr != nil {
				return readErr
			}
			pid, readErr = strconv.Atoi(row["pid"])
			poolerCheck(t, readErr == nil && row["mode"] == "on" && row["application"] == application, "fresh verified transaction and client identity")
			return nil
		})
		poolerCheck(t, readErr == nil && pid > 0, "guarded native read")
		return pid
	}
	assertWriteDenied := func() {
		writeErr := guarded(applicationB, func(readCtx context.Context, tx dal.ReadTransaction) error {
			_, queryErr := poolerRecord(readCtx, tx, `INSERT INTO pooler_guard_probe VALUES(3,'forbidden') RETURNING id::text AS id`)
			return queryErr
		})
		var native *pgconn.PgError
		poolerCheck(t, errors.As(writeErr, &native) && native.Code == "25006", "native readonly refusal, not lexical or prior-aborted refusal")
		unchanged()
	}
	firstPID = checkRead(applicationA)
	poolerCheck(t, checkRead(applicationB) == firstPID, "two independently opened clients reuse one actual backend")
	assertWriteDenied()

	// With server_reset_query_always=0 this deliberate session pollution really
	// survives a transaction-pool handoff. The next guard must still say ON.
	raw = poolerSQL(t, clientURL(applicationA))
	poolerCheck(t, raw.QueryRowContext(ctx, `SELECT set_config('default_transaction_read_only','on',false)`).Scan(&mode) == nil && mode == "on", "set owned pooled session default")
	poolerCheck(t, raw.Close() == nil, "close session-pollution client")
	raw = poolerSQL(t, clientURL(applicationB))
	poolerCheck(t, raw.QueryRowContext(ctx, `SHOW default_transaction_read_only`).Scan(&mode) == nil && mode == "on", "N-PG10 actual default survives handoff")
	poolerCheck(t, raw.QueryRowContext(ctx, `SELECT set_config('default_transaction_read_only','off',false)`).Scan(&mode) == nil && mode == "off", "pollute pooled session to writable default")
	poolerCheck(t, raw.Close() == nil, "close writable-default client")
	poolerCheck(t, checkRead(applicationB) == firstPID, "verified transaction after real pooled mode change")
	assertWriteDenied()

	rollback := errors.New("owned pooler rollback discriminator")
	err = guarded(applicationA, func(readCtx context.Context, tx dal.ReadTransaction) error {
		row, queryErr := poolerRecord(readCtx, tx, `SELECT set_config('default_transaction_read_only','on',false) AS mode`)
		if queryErr != nil {
			return queryErr
		}
		poolerCheck(t, row["mode"] == "on", "set rollback discriminator")
		return rollback
	})
	poolerCheck(t, errors.Is(err, rollback), "worker failure preserved through rollback")
	raw = poolerSQL(t, clientURL(applicationB))
	poolerCheck(t, raw.QueryRowContext(ctx, `SHOW default_transaction_read_only`).Scan(&mode) == nil && mode == "off", "transaction rollback removed session pollution")
	poolerCheck(t, raw.Close() == nil, "close rollback observer")

	// Cancellation happens after the guard and an actual in-flight native read.
	// The independent observer waits for pg_sleep, rather than a timing-only nap.
	cancelled, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	done := make(chan error, 1)
	ref, err := dbcopy.Parse(clientURL(applicationA))
	poolerCheck(t, err == nil, "parse cancellation client")
	db, err := ref.Open(ctx)
	poolerCheck(t, err == nil, "open cancellation client")
	closer, ok := dal.As[io.Closer](db)
	poolerCheck(t, ok && closer != nil, "cancellation client closer")
	closed := false
	defer func() {
		if !closed {
			poolerCheck(t, closer.Close() == nil, "close cancellation client")
		}
	}()
	go func() {
		done <- dbcopy.RunVerifiedPostgresReadonly(cancelled, db, func(readCtx context.Context, tx dal.ReadTransaction) error {
			_, queryErr := poolerRecord(readCtx, tx, `SELECT pg_sleep(15)::text AS waited`)
			return queryErr
		})
	}()
	poolerWait(t, func() bool {
		var running bool
		queryErr := observer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=$1 AND usename=$2 AND application_name=$3 AND state='active' AND query LIKE '%pg_sleep(15)%')`, server.database, server.user, applicationA).Scan(&running)
		return queryErr == nil && running
	}, "observe guarded native sleep before cancellation")
	cancelRead()
	select {
	case err = <-done:
		poolerCheck(t, err != nil && cancelled.Err() != nil, "native cancellation returned an error")
	case <-time.After(10 * time.Second):
		t.Fatal("native pooler: cancelled operation did not return")
	}
	poolerCheck(t, closer.Close() == nil, "release cancelled client before recovery")
	closed = true
	checkRead(applicationB)
	assertWriteDenied()

	// Terminate only a freshly revalidated backend belonging to this randomized
	// database/user/client. PID plus backend_start closes the PID-reuse gap.
	priorPID := checkRead(applicationB)
	admin := poolerSQL(t, os.Getenv(realPgAdminVar))
	var started time.Time
	poolerCheck(t, admin.QueryRowContext(ctx, `SELECT backend_start FROM pg_stat_activity WHERE pid=$1 AND datname=$2 AND usename=$3 AND application_name=$4 AND state='idle'`, priorPID, server.database, server.user, applicationB).Scan(&started) == nil, "independent exact backend ownership before replacement")
	var terminated bool
	poolerCheck(t, admin.QueryRowContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid=$1 AND datname=$2 AND usename=$3 AND application_name=$4 AND backend_start=$5 AND state='idle'`, priorPID, server.database, server.user, applicationB, started).Scan(&terminated) == nil && terminated, "terminate only revalidated owned backend")
	poolerWait(t, func() bool {
		var exists bool
		return admin.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2)`, priorPID, started).Scan(&exists) == nil && !exists
	}, "owned backend exited")
	poolerCheck(t, checkRead(applicationA) != priorPID, "replacement has a different actual backend")
	assertWriteDenied()
	unchanged()
}

func poolerCI(goos string, getenv func(string) string) bool {
	major := getenv(realPgMajorVar)
	return goos == "linux" && getenv("GITHUB_ACTIONS") == "true" && getenv("RUNNER_OS") == "Linux" && getenv("DATATUG_NATIVE_POOLER_CI") == "1" && (major == "17" || major == "18")
}

func poolerCheck(t *testing.T, condition bool, stage string) {
	t.Helper()
	if !condition {
		// Never interpolate SQL, DSNs, driver errors or process output here.
		t.Fatalf("native pooler: %s", stage)
	}
}

func poolerWait(t *testing.T, ready func() bool, stage string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	poolerCheck(t, false, stage)
}

func poolerSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	poolerCheck(t, err == nil, "open native SQL connection")
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { poolerCheck(t, db.Close() == nil, "close native SQL connection") })
	return db
}

func poolerRecord(ctx context.Context, tx dal.ReadTransaction, text string) (result map[string]string, err error) {
	reader, err := tx.ExecuteQueryToRecordsReader(ctx, dal.NewTextQuery(text, nil))
	if err != nil {
		if reader != nil {
			err = errors.Join(err, reader.Close())
		}
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("native pooler: missing native reader")
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	row, err := reader.Next()
	if err != nil || row == nil {
		if err == nil {
			err = errors.New("native pooler: missing native record")
		}
		return nil, err
	}
	if row.Error() != nil {
		return nil, row.Error()
	}
	values, ok := row.Data().(map[string]any)
	if !ok {
		return nil, errors.New("native pooler: expected record map")
	}
	result = make(map[string]string, len(values))
	for key, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("native pooler: expected text field")
		}
		result[key] = text
	}
	if _, err = reader.Next(); !errors.Is(err, dal.ErrNoMoreRecords) {
		return nil, errors.New("native pooler: expected one record")
	}
	err = nil
	return result, nil
}

type poolerCommand func(context.Context, string, ...string) ([]byte, error)

func poolerExec(ctx context.Context, command string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(bounded, command, args...).Output()
}

type poolerOwner struct {
	name, image, id string
	port            int
	run             poolerCommand
}

type poolerInspection struct {
	ID     string `json:"Id"`
	Name   string
	Image  string
	Config struct {
		Labels map[string]string
		User   string
	}
	HostConfig struct {
		Memory, MemorySwap, NanoCPUs, PidsLimit int64
		ReadonlyRootfs                          bool
		NetworkMode                             string
	}
	State struct {
		Running bool
		PID     int `json:"Pid"`
	}
}

func (o *poolerOwner) inspect(ctx context.Context, id string) (poolerInspection, error) {
	var value poolerInspection
	raw, err := o.run(ctx, "docker", "inspect", "--format", "{{json .}}", id)
	if err != nil || json.Unmarshal(raw, &value) != nil || !poolerContainerID.MatchString(value.ID) || value.Name != "/"+o.name || value.Image != o.image || value.Config.Labels["dev.datatug.pooler-runtime"] != o.name {
		return value, errors.New("native pooler: container ownership unverified")
	}
	return value, nil
}

func (o *poolerOwner) cleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Listing succeeds with an empty result only when absence is established;
	// a daemon/inspect error is never silently accepted as cleanup.
	raw, err := o.run(ctx, "docker", "container", "ls", "--all", "--no-trunc", "--filter", "name=^/"+o.name+"$", "--format", "{{.ID}}")
	if err != nil {
		return errors.New("native pooler: cleanup inventory failed")
	}
	id := strings.TrimSpace(string(raw))
	if id != "" {
		if !poolerContainerID.MatchString(id) || o.id != "" && id != o.id {
			return errors.New("native pooler: cleanup ID differs")
		}
		if _, err = o.inspect(ctx, id); err != nil {
			return err
		}
		if _, err = o.run(ctx, "docker", "rm", "--force", id); err != nil {
			return errors.New("native pooler: owned container removal failed")
		}
		raw, err = o.run(ctx, "docker", "container", "ls", "--all", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
		if err != nil || strings.TrimSpace(string(raw)) != "" {
			return errors.New("native pooler: owned container remains")
		}
	}
	if o.port != 0 {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			connection, dialErr := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.port)), 100*time.Millisecond)
			if connection != nil {
				_ = connection.Close()
			}
			if errors.Is(dialErr, syscall.ECONNREFUSED) {
				return nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		return errors.New("native pooler: owned listener absence unverified")
	}
	return nil
}

func startNativePooler(t *testing.T, ctx context.Context, server realPgServer) string {
	t.Helper()
	image := os.Getenv("DATATUG_NATIVE_POOLER_IMAGE")
	poolerCheck(t, poolerDigest.MatchString(image), "immutable CI fixture image required")
	base := os.Getenv("RUNNER_TEMP")
	poolerCheck(t, filepath.IsAbs(base), "absolute owned CI temporary directory")
	directory, err := os.MkdirTemp(base, "dt-native-pooler-")
	poolerCheck(t, err == nil, "create owned private fixture directory")
	resourcesGone := true // no native resource has been attempted yet
	t.Cleanup(func() {
		// Keep the auth files for the exact always-step fallback when native
		// cleanup cannot prove the container gone. Never delete them first.
		poolerCheck(t, resourcesGone, "private files retained for owned cleanup retry")
		poolerCheck(t, os.RemoveAll(directory) == nil, "remove owned private files")
		_, statErr := os.Stat(directory)
		poolerCheck(t, os.IsNotExist(statErr), "private fixture files removed")
	})
	poolerCheck(t, poolerIdentifier.MatchString(server.user) && poolerIdentifier.MatchString(server.database) && regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(server.password), "owned configuration values")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	poolerCheck(t, err == nil, "reserve candidate CI loopback port")
	port := listener.Addr().(*net.TCPAddr).Port
	poolerCheck(t, listener.Close() == nil, "release candidate before owned container bind")
	name := "dt-native-pooler-" + randomHex(t, 12)
	owner := &poolerOwner{name: name, image: image, port: port, run: poolerExec}
	creationAttempted := false
	t.Cleanup(func() {
		if !creationAttempted {
			return
		}
		cleanupErr := owner.cleanup()
		resourcesGone = cleanupErr == nil
		poolerCheck(t, resourcesGone, "owned container/listener cleanup")
	})
	config := fmt.Sprintf("[databases]\n%s = host=127.0.0.1 port=54329 dbname=%s user=%s\n[pgbouncer]\nlisten_addr=127.0.0.1\nlisten_port=%d\nunix_socket_dir=\nauth_type=plain\nauth_file=/fixture/users.txt\npool_mode=transaction\ndefault_pool_size=1\nmin_pool_size=0\nreserve_pool_size=0\nmax_db_connections=1\nmax_client_conn=8\nserver_reset_query_always=0\nignore_startup_parameters=default_transaction_read_only,statement_timeout,timezone,extra_float_digits\nserver_tls_sslmode=disable\nclient_tls_sslmode=disable\nlog_connections=0\nlog_disconnections=0\nlog_pooler_errors=0\npidfile=/tmp/pgbouncer.pid\n", server.database, server.database, server.user, port)
	configPath, usersPath := filepath.Join(directory, "pgbouncer.ini"), filepath.Join(directory, "users.txt")
	marker, err := json.Marshal(map[string]string{"runtime": name, "image": image})
	poolerCheck(t, err == nil && os.WriteFile(filepath.Join(directory, ".owner.json"), marker, 0600) == nil, "private cleanup ownership marker")
	poolerCheck(t, os.WriteFile(configPath, []byte(config), 0600) == nil && os.WriteFile(usersPath, []byte(fmt.Sprintf("\"%s\" \"%s\"\n", server.user, server.password)), 0600) == nil, "write private fixture configuration")
	_, err = poolerExec(ctx, "sudo", "-n", "chgrp", "10001", directory, configPath, usersPath)
	poolerCheck(t, err == nil, "set only private fixture group")
	poolerCheck(t, os.Chmod(directory, 0750) == nil && os.Chmod(configPath, 0640) == nil && os.Chmod(usersPath, 0640) == nil, "private non-root configuration permissions")
	// The workflow's always step can recover this exact name after an abnormal
	// test-binary exit. It must verify our label and immutable image before rm.
	env, err := os.OpenFile(os.Getenv("GITHUB_ENV"), os.O_APPEND|os.O_WRONLY, 0600)
	poolerCheck(t, err == nil, "owned CI cleanup channel")
	poolerCheck(t, !strings.ContainsAny(directory, "\r\n"), "canonical cleanup directory")
	_, err = fmt.Fprintf(env, "DATATUG_NATIVE_POOLER_RUNTIME=%s\nDATATUG_NATIVE_POOLER_DIRECTORY=%s\n", name, directory)
	closeErr := env.Close()
	poolerCheck(t, err == nil && closeErr == nil, "publish only non-secret cleanup identity")
	creationAttempted, resourcesGone = true, false
	raw, err := poolerExec(ctx, "docker", "create", "--name", name, "--label", "dev.datatug.pooler-runtime="+name, "--network", "host", "--memory", "128m", "--memory-swap", "128m", "--cpus", "0.5", "--pids-limit", "64", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--log-driver", "none", "--restart", "no", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=1m", "--mount", "type=bind,src="+directory+",dst=/fixture,readonly", image, "-q", "/fixture/pgbouncer.ini")
	if err == nil {
		owner.id = strings.TrimSpace(string(raw))
	}
	poolerCheck(t, err == nil && poolerContainerID.MatchString(owner.id), "create owned bounded container")
	inspection, err := owner.inspect(ctx, owner.id)
	poolerCheck(t, err == nil && inspection.Config.User == "10001:10001" && inspection.Config.Labels["org.opencontainers.image.revision"] == poolerRevision && inspection.HostConfig.Memory == 128<<20 && inspection.HostConfig.MemorySwap == 128<<20 && inspection.HostConfig.NanoCPUs == 500000000 && inspection.HostConfig.PidsLimit == 64 && inspection.HostConfig.ReadonlyRootfs && inspection.HostConfig.NetworkMode == "host", "verify actual native container bounds and provenance")
	_, err = poolerExec(ctx, "docker", "start", owner.id)
	poolerCheck(t, err == nil, "start owned CI pooler")
	endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	poolerWait(t, func() bool {
		connection, dialErr := net.DialTimeout("tcp4", endpoint, 100*time.Millisecond)
		if connection != nil {
			_ = connection.Close()
		}
		return dialErr == nil
	}, "owned pooler listener ready")
	inspection, err = owner.inspect(ctx, owner.id)
	poolerCheck(t, err == nil && inspection.State.Running && inspection.State.PID > 0, "owned container still running after listener readiness")
	return endpoint
}
