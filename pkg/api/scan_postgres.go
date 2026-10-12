package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/dal-go/dalgo2postgres"
	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// DriverPostgres is the driver name a PostgreSQL server and catalog carry in a
// project.
const DriverPostgres = "postgres"

// postgresDescriptorFolder is the project folder that holds connection
// descriptors: a PostgreSQL catalog's path points at one of them.
const postgresDescriptorFolder = "connections"

// recordableHost is a host name or an address: what a scan accepts of the host of its URL.
// A host list, or the path of a socket, is refused. (The
// project records no host: the server of a PostgreSQL project is its driver.)
var recordableHost = regexp.MustCompile(`^[A-Za-z0-9:][A-Za-z0-9._:-]*$`)

// PostgresScanParams are the connection parameters of a PostgreSQL scan. The
// connection is an environment variable that holds the whole URL, so these
// parameters print as the variable's name, never as the URL: no verb of fmt shows
// the password or the rest of the URL, for the parameters or a pointer to them
// (String and GoString). Their accessors do carry the host, port and user of the
// URL, as dbconnection.Params has them; the scan records none of them in a project
// (ScannedServer), and its log names only the environment variable.
type PostgresScanParams struct {
	dsnEnv         string
	catalog        string
	ref            dbcopy.BackendRef
	target         dbcopy.PostgresTarget
	descriptorPath string
}

var _ dbconnection.Params = (*PostgresScanParams)(nil)

// NewPostgresScanParams resolves the environment variable dsnEnv, which must
// hold a postgres:// URL, and names the catalog of environment that the scan
// will write. lookupEnv reads the environment.
//
// The environment and the catalog each name a file under the project's
// connections folder, so each must be a plain name (dbcopy.IsPlainSourceID, the one
// definition: the routes that read the project apply the same). The host of the URL is
// required to be one host name or address, which dbconnection.Params.Server may expose to callers.
func NewPostgresScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*PostgresScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if ref.Scheme != "postgres" {
		return nil, fmt.Errorf("environment variable %s must hold a postgres:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	// ParseWithEnv has read the URL with this same function and refused one it cannot read, so the error is
	// never set here; a target that came back empty would be refused below, as one that names no host.
	target, _ := dbcopy.ParsePostgresTarget(ref.Path)
	if target.Host == "" {
		return nil, fmt.Errorf("the URL in environment variable %s names no host (a unix-socket connection cannot be scanned yet)", dsnEnv)
	}
	if !recordableHost.MatchString(target.Host) {
		// Even though the scan does not save the host, generic Params callers can read Server().
		return nil, fmt.Errorf("the host in environment variable %s cannot be recorded in a project: use a host name or an address", dsnEnv)
	}
	if !dbcopy.IsPlainSourceID(environment) || !dbcopy.IsPlainSourceID(catalog) {
		// The flags are what the user typed, and a source string can be typed where
		// a name belongs: only a plain name is echoed.
		return nil, fmt.Errorf("environment %q and database %q must each be a plain name (letters, digits, '.', '_' and '-', at most 128 characters, starting with a letter or a digit) to name the connection descriptor under %s/", dbcopy.SourceIDDisplay(environment), dbcopy.SourceIDDisplay(catalog), postgresDescriptorFolder)
	}
	return &PostgresScanParams{
		dsnEnv:         dsnEnv,
		catalog:        catalog,
		ref:            ref,
		target:         target,
		descriptorPath: path.Join(postgresDescriptorFolder, environment, catalog+".json"),
	}, nil
}

// Driver implements dbconnection.Params.
func (*PostgresScanParams) Driver() string { return DriverPostgres }

// Mode implements dbconnection.Params: a scan only reads.
func (*PostgresScanParams) Mode() dbconnection.Mode { return dbconnection.ModeReadOnly }

// Server implements dbconnection.Params: the host of the URL.
func (p *PostgresScanParams) Server() string { return p.target.Host }

// Port implements dbconnection.Params: the port of the URL, 0 when it names none.
func (p *PostgresScanParams) Port() int { return p.target.Port }

// Catalog implements dbconnection.Params: the catalog ID in the project.
func (p *PostgresScanParams) Catalog() string { return p.catalog }

// User implements dbconnection.Params: the user of the URL.
func (p *PostgresScanParams) User() string { return p.target.User }

// ConnectionString implements dbconnection.Params. It is the "env:NAME" source
// and never the URL: nothing that formats these parameters can print a password.
func (p *PostgresScanParams) ConnectionString() string { return p.ref.Raw }

// String implements dbconnection.Params, with the same text as ConnectionString.
// It has a value receiver so that the parameters print the same through a
// pointer and as a value: fmt cannot call a method on the unexported fields that
// hold the URL, and would print them as they are.
func (p PostgresScanParams) String() string { return p.ref.Raw }

// GoString makes %#v print the same text as String, not the fields.
func (p PostgresScanParams) GoString() string { return p.String() }

// LogTarget is what the scan logs of what it connects to: where the connection string is
// read from (the environment variable), the same words the failure of the scan uses, and
// nothing the string holds: not its host, port, database, user, password or query string.
func (p *PostgresScanParams) LogTarget() string { return p.ref.ConnectionHint() }

// DSNEnv is the name of the environment variable that holds the URL.
func (p *PostgresScanParams) DSNEnv() string { return p.dsnEnv }

// Path is the project-relative path of the connection descriptor the scan
// writes, which UpdateDbSchema records as the catalog's path.
func (p *PostgresScanParams) Path() string { return p.descriptorPath }

// SourceRef is the source the scan opens: its Raw is "env:NAME", its Path the
// real URL, which only the open path may use.
func (p *PostgresScanParams) SourceRef() dbcopy.BackendRef { return p.ref }

// WriteDescriptor writes the connection descriptor into projectDir: a file that
// names the environment variable and nothing else, so the descriptor holds no
// password, user or host, and neither does any other file the scan writes (the
// project's server entry is the driver alone). It replaces a descriptor already
// there, and does not write one that is as it would be written.
//
// It writes only a plain file in plain folders of the project, as every write of a scan does
// (see package plainfs): it does not write through a link. A project is not trusted
// (ResolveDescriptorPath refuses a descriptor that a link leads out of the folder to), so a link
// at connections, at connections/<env> or at the descriptor itself is refused, naming the path
// in the project that it is, before anything is read through it or written. A folder or a file
// the project has in a place of the path that is not a plain folder, or not a plain file, is
// refused the same way.
//
// It returns the function that takes the write back, for a scan whose save fails: the
// descriptor this write made is removed, with the folders it made, and a descriptor
// that was there before is put back as it was.
func (p *PostgresScanParams) WriteDescriptor(projectDir string) (undo func() error, err error) {
	// A struct of one string cannot fail to marshal.
	data, _ := json.MarshalIndent(dbcopy.PostgresDescriptor{DSNEnv: p.dsnEnv}, "", "  ")
	content := append(data, '\n')
	tree := scanTree(projectDir)
	file := filepath.Join(projectDir, filepath.FromSlash(p.descriptorPath))
	// The folders first: they are looked at with Lstat, so the descriptor is never read
	// through a link in front of it. A descriptor that is there has its folders there, so
	// when nothing has to be written nothing was made.
	made, err := tree.MakeFolders(filepath.Dir(file))
	if err != nil {
		_ = undoFolders(tree, made) // the folders this write made are not left behind
		return nil, fmt.Errorf("create the connection descriptor folder: %w", err)
	}
	previous, readErr := tree.ReadFile(file)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		_ = undoFolders(tree, made)
		return nil, fmt.Errorf("write the connection descriptor: %w", readErr)
	}
	existed := readErr == nil
	if existed && bytes.Equal(previous, content) {
		return func() error { return nil }, nil
	}
	if err = tree.WriteFile(file, content, 0o644); err != nil {
		_ = undoFolders(tree, made)
		return nil, fmt.Errorf("write the connection descriptor: %w", err)
	}
	return func() error {
		tree := scanTree(projectDir) // the walk of the undo is as much a walk as the write's: nothing is trusted that was looked at before
		if existed {
			if err := tree.WriteFile(file, previous, 0o644); err != nil {
				return fmt.Errorf("put back the connection descriptor: %w", err)
			}
			return nil
		}
		if err := tree.Remove(file); err != nil {
			return fmt.Errorf("remove the connection descriptor: %w", err)
		}
		return undoFolders(tree, made)
	}, nil
}

// undoFolders removes the folders that a write made (outermost first, as MakeFolders lists
// them), innermost first. A folder that is not there any more is gone, and one that holds
// something is somebody else's now: both stay as they are.
func undoFolders(tree plainfs.Tree, made []string) error {
	var errs []error
	for i := len(made) - 1; i >= 0; i-- {
		if err := tree.RemoveEmptyFolder(made[i]); err != nil {
			errs = append(errs, fmt.Errorf("remove the folder the connection descriptor was written in: %w", err))
		}
	}
	return errors.Join(errs...)
}

// postgresScanSource is what scanPostgresCatalog needs of its parameters: the
// source to open.
type postgresScanSource interface {
	SourceRef() dbcopy.BackendRef
}

// openSchemaScan opens a source for a schema scan, a seam so tests scan without
// a server. Always dbcopy.BackendRef.OpenSchemaScan in production.
var openSchemaScan = dbcopy.BackendRef.OpenSchemaScan

// SetOpenSchemaScanForTest replaces how a PostgreSQL scan opens its source, for the
// tests of a package that runs the real scan command (the command's own package):
// a test of the scan must never dial a server, and every scan opens through here.
// It returns the function that puts the previous open back.
func SetOpenSchemaScanForTest(open func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error)) (restore func()) {
	previous := openSchemaScan
	openSchemaScan = open
	return func() { openSchemaScan = previous }
}

// OpenSchemaScanForTest is how a PostgreSQL scan opens its source now: what a test stood in
// with (SetOpenSchemaScanForTest), or else what the test binary stops the run with, or else the
// real open. A test of the package that runs the real scan command reads it to prove that its
// binary has the open that stops the run, not the one that dials.
func OpenSchemaScanForTest() func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
	return openSchemaScan
}

// isConnectionReadError identifies a failed connection, timeout or cancellation in a reader error.
func isConnectionReadError(err error) bool {
	var connectionErr *dalgo2postgres.ConnectionError
	return errors.As(err, &connectionErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// catalogReadFailure is the error the scan reports for err, the failure of its read of the catalog. A connection that
// failed during the read (the adapter's connection error, or the end of a context: the clock or a cancel) is the
// failure OpenFailure reports, which points at where the connection string is read from; the scan exits 4 for it. Any
// other failure, a statement the server refused or an error of the scanner, is not a failure to connect and is not
// told as one: it is one fixed sentence that says only that the read failed, with no hint and no word of the
// driver's, which can quote the connection string. The cause stays reachable through errors.Is and errors.As.
func catalogReadFailure(source dbcopy.BackendRef, err error) error {
	if isConnectionReadError(err) {
		return source.OpenFailure(err)
	}
	return &catalogReadError{cause: err}
}

// catalogReadError is the failure of a read of the catalog that is not a connection failure.
type catalogReadError struct{ cause error }

func (*catalogReadError) Error() string {
	return "the catalog could not be read (the server's own message is not shown: a driver can quote the connection string)"
}

func (e *catalogReadError) Unwrap() error { return e.cause }

// scanPostgresCatalog scans a PostgreSQL database through DALgo's schema reader.
// It opens the source through dbcopy, so a driver error comes back classified
// (see BackendRef.OpenFailure), never as the driver wrote it; what the scan
// itself reads is reported the same way. It counts no records: no project file
// holds a count, and a count would be a full read of every table of somebody's
// database, so the provider is given no counter.
func scanPostgresCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	params, ok := connectionParams.(postgresScanSource)
	if !ok {
		return nil, errors.New("a PostgreSQL scan needs connection parameters built from an environment variable (--dsn-env NAME)")
	}
	source := params.SourceRef()
	scanDB, err := openSchemaScan(source, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open PostgreSQL: %w", source.OpenFailure(err))
	}
	defer func() { _ = scanDB.Close() }()

	catalogID := connectionParams.Catalog()
	var readMu sync.Mutex
	var connectionReadError error
	provider := dalgoschema.NewSchemaProviderWithErrorObserver(scanDB, nil, catalogID, dbcopy.PostgresDefaultSchema, func(readErr error) {
		if !isConnectionReadError(readErr) {
			return
		}
		readMu.Lock()
		if connectionReadError == nil {
			connectionReadError = readErr
		}
		readMu.Unlock()
	})
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		readMu.Lock()
		if connectionReadError != nil {
			err = errors.Join(err, connectionReadError)
		}
		readMu.Unlock()
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", catalogReadFailure(source, err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverPostgres
	return dbCatalog, nil
}
