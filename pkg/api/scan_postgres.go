package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

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

// recordableHost is a host name or an address: what a scan accepts of the host of its URL,
// which it names on its log line. A host list, or the path of a socket, is refused. (The
// project records no host: the server of a PostgreSQL project is its driver.)
var recordableHost = regexp.MustCompile(`^[A-Za-z0-9:][A-Za-z0-9._:-]*$`)

// PostgresScanParams are the connection parameters of a PostgreSQL scan. The
// connection is an environment variable that holds the whole URL, so these
// parameters print as the variable's name, never as the URL: no verb of fmt shows
// the password or the rest of the URL, for the parameters or a pointer to them
// (String and GoString). Their accessors do carry the host, port and user of the
// URL, as dbconnection.Params has them; the scan records none of them in a project
// (ScannedServer), and names what it connects to by Display, which holds no user and
// no password.
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
// named on the log line of the scan, so it must be a host name or an address.
func NewPostgresScanParams(lookupEnv func(string) (string, bool), dsnEnv, environment, catalog string) (*PostgresScanParams, error) {
	ref, err := dbcopy.ParseWithEnv("env:"+dsnEnv, lookupEnv)
	if err != nil {
		return nil, err
	}
	if ref.Scheme != "postgres" {
		return nil, fmt.Errorf("environment variable %s must hold a postgres:// URL, not a %s source", dsnEnv, ref.Scheme)
	}
	target, err := dbcopy.ParsePostgresTarget(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("environment variable %s: %w", dsnEnv, err)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("the URL in environment variable %s names no host (a unix-socket connection cannot be scanned yet)", dsnEnv)
	}
	if !recordableHost.MatchString(target.Host) {
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

// Display is what the scan says it connects to: the scheme, host, port and database of
// the URL (dbcopy.SourceDisplay), never its user, its password or its query string. It
// is built from parts that each passed a strict check, never from a scrubbed copy of the
// URL.
func (p *PostgresScanParams) Display() string { return dbcopy.SourceDisplay(p.ref.Path) }

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
// It does not follow a link. A project is not trusted (ResolveDescriptorPath refuses a
// descriptor that a link leads out of the folder to), so a link at connections, at
// connections/<env> or at the descriptor itself is refused, naming the path in the project
// that it is, before anything is read through it or written: the scan would otherwise read
// a file of the machine into memory, truncate it and write the descriptor there. A folder
// or a file the project has in a place of the path that is not a plain folder, or not a
// plain file, is refused the same way.
//
// It returns the function that takes the write back, for a scan whose save fails: the
// descriptor this write made is removed, with the folders it made, and a descriptor
// that was there before is put back as it was.
func (p *PostgresScanParams) WriteDescriptor(projectDir string) (undo func() error, err error) {
	// A struct of one string cannot fail to marshal.
	data, _ := json.MarshalIndent(dbcopy.PostgresDescriptor{DSNEnv: p.dsnEnv}, "", "  ")
	content := append(data, '\n')
	file := filepath.Join(projectDir, filepath.FromSlash(p.descriptorPath))
	// The folders first: they are looked at with Lstat, so the descriptor is never read
	// through a link in front of it. A descriptor that is there has its folders there, so
	// when nothing has to be written nothing was made.
	made, err := makeFolders(projectDir, path.Dir(p.descriptorPath))
	if err != nil {
		return nil, fmt.Errorf("create the connection descriptor folder: %w", err)
	}
	if err = checkDescriptorFile(file, p.descriptorPath); err != nil {
		_ = undoFolders(made) // the folders this write made are not left behind
		return nil, fmt.Errorf("write the connection descriptor: %w", err)
	}
	previous, readErr := os.ReadFile(file)
	existed := readErr == nil
	if existed && bytes.Equal(previous, content) {
		return func() error { return nil }, nil
	}
	if err = scanWriteFile(file, content, 0o644); err != nil {
		_ = undoFolders(made) // the folders this write made are not left behind
		return nil, fmt.Errorf("write the connection descriptor: %w", err)
	}
	return func() error {
		if existed {
			if err := scanWriteFile(file, previous, 0o644); err != nil {
				return fmt.Errorf("put back the connection descriptor: %w", err)
			}
			return nil
		}
		if err := scanRemove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the connection descriptor: %w", err)
		}
		return undoFolders(made)
	}, nil
}

// checkDescriptorFile is an error when what the project has at file, whose path in the project
// is rel, is not a plain file: a link (to anything, a file that is not there included), a folder
// or anything else that is not a regular file. A file that is not there is fine, and so is one
// that cannot be looked at for a reason that is not "it is not there" (it is refused, with the
// reason).
func checkDescriptorFile(file, rel string) error {
	info, err := scanLstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("look at %s: %w", rel, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a link, and a scan does not write through a link: remove it, or put the descriptor in a file of the project", rel)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a file, and a scan does not replace what is not a file: remove it, or scan into another environment or database", rel)
	}
	return nil
}

// makeFolders makes the folder rel (slash separated) below base, with the folders above
// it, and returns the folders it made, outermost first: the ones that were not there. A
// folder is looked at with Lstat, and one that is a link, or is not a folder, is refused,
// naming its path below base, and the folders this call made are taken back: the descriptor
// goes into folders of the project, not into a place a link leads to.
func makeFolders(base, rel string) (made []string, err error) {
	current := base
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		known := strings.Join(parts[:i+1], "/")
		info, statErr := scanLstat(current)
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			if err = scanMkdir(current, 0o755); err != nil {
				_ = undoFolders(made)
				return nil, err
			}
			made = append(made, current)
		case statErr != nil:
			_ = undoFolders(made)
			return nil, fmt.Errorf("look at %s: %w", known, statErr)
		case info.Mode()&fs.ModeSymlink != 0:
			_ = undoFolders(made)
			return nil, fmt.Errorf("%s is a link, and a scan does not write through a link: remove it, or put the descriptor in a folder of the project", known)
		case !info.IsDir():
			_ = undoFolders(made)
			return nil, fmt.Errorf("%s is not a folder, and a scan does not replace what is not a folder: remove it, or scan into another environment or database", known)
		}
	}
	return made, nil
}

// undoFolders removes the folders, innermost first. A folder that cannot be listed any
// more is gone, and one that holds something is somebody else's now: both stay as they are.
func undoFolders(made []string) error {
	var errs []error
	for i := len(made) - 1; i >= 0; i-- {
		if entries, err := scanReadDir(made[i]); err != nil || len(entries) > 0 {
			continue
		}
		if err := scanRemove(made[i]); err != nil {
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

// scanPostgresCatalog scans a PostgreSQL database through DALgo's schema reader.
// It opens the source through dbcopy, so a driver error comes back classified
// (see BackendRef.OpenFailure), never as the driver wrote it; what the scan
// itself reads is reported the same way.
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
	provider := dalgoschema.NewSchemaProvider(scanDB, recordsCounterOf(scanDB, source), catalogID, dbcopy.PostgresDefaultSchema)
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", source.OpenFailure(err))
	}
	dbCatalog.ID = catalogID
	dbCatalog.Driver = DriverPostgres
	return dbCatalog, nil
}

// recordsCounterOf is what counts the records of the tables of a scan, or nil, which counts
// nothing. A scan counts only through a reader that can tell a view from a table
// (dalgoschema.ViewLister), so that a view is never counted: the reader of dalgo2postgres lists the
// views of a server with its tables and cannot tell them apart, so every view would be a table to
// the scanner, and a server that runs COUNT(*) natively (dalgo2sql declares it for PostgreSQL)
// would run a count on every table and every view of the database, with no timeout, for numbers
// that nothing stores. Through that reader a scan counts nothing; the day its reader lists its
// views, the tables are counted again.
func recordsCounterOf(scanDB dbcopy.SchemaScanDB, source dbcopy.BackendRef) dalgoschema.RecordsCounter {
	if _, tellsViews := scanDB.(dalgoschema.ViewLister); !tellsViews {
		return nil
	}
	return classifiedCounter{counter: dalgoschema.NewNativeCounter(scanDB), source: source}
}

// classifiedCounter reports the error of a count as the open of the source reports its
// own (BackendRef.OpenFailure): a fixed sentence built from the display form of the
// source, never the driver's words. The scanner logs the error of a count, and a driver
// can quote the connection string in whatever shape it likes.
type classifiedCounter struct {
	counter dalgoschema.RecordsCounter
	source  dbcopy.BackendRef
}

func (c classifiedCounter) CountRecords(ctx context.Context, schema, table string) (*int, error) {
	count, err := c.counter.CountRecords(ctx, schema, table)
	if err != nil {
		return nil, c.source.OpenFailure(err)
	}
	return count, nil
}
