package sqlexecute

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/google/uuid"
	"github.com/mitchellh/go-homedir"
	"github.com/strongo/slice"
)

// Executor executes DataTug commands
type Executor struct {
	getDbByID         func(envID, dbID string) (*datatug.EnvDb, error)
	getCatalogSummary func(server datatug.ServerRef, catalogID string) (*datatug.DbCatalogSummary, error)
}

// NewExecutor creates new executor
func NewExecutor(
	getDbByID func(envID, dbID string) (*datatug.EnvDb, error),
	getCatalogSummary func(server datatug.ServerRef, catalogID string) (*datatug.DbCatalogSummary, error),
) Executor {
	return Executor{
		getDbByID:         getDbByID,
		getCatalogSummary: getCatalogSummary,
	}
}

// Execute executes DataTug commands
func (e Executor) Execute(request Request) (response Response, err error) {
	if len(request.Commands) == 1 {
		return e.ExecuteSingle(request.Commands[0])
	}
	return e.executeMulti(request)
}

// ExecuteSingle executes single DB command
func (e Executor) ExecuteSingle(command RequestCommand) (response Response, err error) {
	var recordset datatug.Recordset
	if recordset, err = e.executeCommand(command); err != nil {
		return
	}
	response.Commands = []*CommandResponse{
		{
			Items: []CommandResponseItem{
				{
					Type:  "recordset",
					Value: recordset,
				},
			},
		},
	}
	response.Duration = recordset.Duration
	return
}

func (e Executor) executeMulti(request Request) (response Response, err error) {
	started := time.Now()
	var wg sync.WaitGroup
	wg.Add(len(request.Commands))
	response.Commands = make([]*CommandResponse, 0, len(request.Commands))
	// Each goroutine writes only its own slot, so no lock is needed.
	commandErrs := make([]error, len(request.Commands))
	for i, command := range request.Commands {
		var commandResponse CommandResponse
		response.Commands = append(response.Commands, &commandResponse)
		go func(i int, cmd RequestCommand) {
			var (
				recordset  datatug.Recordset
				commandErr error
			)
			if recordset, commandErr = e.executeCommand(cmd); commandErr != nil {
				commandErrs[i] = commandErr
				wg.Done()
				return
			}
			commandResponse.Items = []CommandResponseItem{
				{
					Type:  "recordset",
					Value: recordset,
				},
			}
			wg.Done()
		}(i, command)
	}
	wg.Wait()
	err = errors.Join(commandErrs...)
	response.Duration = time.Since(started)
	return
}

var reParameter = regexp.MustCompile(`@\w+`)

// maxPort is the highest port number.
const maxPort = 65535

// serverConnectionParams builds the connection of a server that is not a file from the parts
// of the server, which are checked first: the host is a host name or an address (see
// dbcopy.IsRecordableHost: no character that separates the keys of the connection string) and the port a port number, written as the port of the string. The errors say
// which part is refused and nothing of its value. The user, the password and the database of
// the command are the caller's own, and are written as they are given.
func serverConnectionParams(server datatug.ServerRef, command RequestCommand) (dbconnection.Params, error) {
	if server.Host != "" && !dbcopy.IsRecordableHost(server.Host) {
		return nil, errors.New("the host is not a host name or an address")
	}
	if server.Port < 0 || server.Port > maxPort {
		return nil, errors.New("the port is not a port number")
	}
	var options []string
	if server.Port != 0 {
		options = append(options, "port="+strconv.Itoa(server.Port))
	}
	return dbconnection.NewConnectionString(server.Driver, server.Host, command.Username, command.Password, command.DB, options...)
}

var (
	closeRowsSeam   = func(rows *sql.Rows) error { return rows.Close() }
	columnTypesSeam = func(rows *sql.Rows) ([]*sql.ColumnType, error) { return rows.ColumnTypes() }
	scanRowSeam     = func(rows *sql.Rows, dest ...interface{}) error { return rows.Scan(dest...) }
)

func (e Executor) executeCommand(command RequestCommand) (recordset datatug.Recordset, err error) {

	var dbServer datatug.ServerRef

	if e.getDbByID != nil {
		var envDb *datatug.EnvDb
		if envDb, err = e.getDbByID(command.Env, command.DB); err != nil {
			return
		}
		dbServer = envDb.Server
	} else {
		strings.Split(command.DB, ":")
		dbServer = datatug.ServerRef{Host: command.Host, Port: command.Port, Driver: command.Driver}
		if err = dbServer.Validate(); err != nil {
			return recordset, fmt.Errorf("execute command does not have valid server parameters: %w", err)
		}
	}
	// connectionString is what the driver is opened with, and shown is the same without a
	// secret in it.
	var connectionString, shown string

	switch dbServer.Driver {
	case "sqlite3":
		var catalogSummary *datatug.DbCatalogSummary
		if catalogSummary, err = e.getCatalogSummary(dbServer, command.DB); err != nil {
			return datatug.Recordset{}, err
		}
		fullPath, err := homedir.Expand(catalogSummary.Path)
		if err != nil {
			err = fmt.Errorf("failed to expand path for SQLite3 connection string: %w", err)
			return datatug.Recordset{}, err
		}
		// A "file:" URI, read-only: a path with a "?", a "#" or a "%" in it is the file it
		// names (see dbcopy.SQLiteFileURIMode), and a file that is not there is not made.
		connectionString = dbcopy.SQLiteFileURIMode(fullPath, dbconnection.ModeReadOnly)
		shown = connectionString
	default:
		var connParams dbconnection.Params
		if connParams, err = serverConnectionParams(dbServer, command); err != nil {
			err = fmt.Errorf("invalid connection parameters: %w", err)
			return
		}
		connectionString = connParams.ConnectionString()
		shown = connParams.String()
	}

	// The connection string holds the password: print it redacted.
	fmt.Println(dbcopy.RedactTextWithSecrets(shown, command.Password))
	//fmt.Println(envDb.ServerRef.driver, connParams.String())
	//fmt.Println(command.Text)
	var db *sql.DB
	if db, err = sql.Open(dbServer.Driver, connectionString); err != nil {
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Failed to close DB: %v", err)
		}
	}()

	//var stmt *sql.Stmt
	//if stmt, err = db.Prepare(command.Text); err != nil {
	//	return
	//}

	queryText := command.Text
	var args []interface{}
	for i, p := range command.Parameters {
		k := "@" + p.ID
		j := strings.Index(queryText, k)
		if j >= 0 {
			queryText = strings.ReplaceAll(queryText, k, ":"+strconv.Itoa(i+1))
		}
		args = append(args, p.Value)
	}

	if recordset, err = e.executeQuery(db, dbServer.Driver, queryText, args); err != nil {
		parameters := reParameter.FindAllString(queryText, -1)
		if strings.HasPrefix(err.Error(), "not enough args to execute query:") {
			if len(parameters) > 0 {
				for i, p := range parameters {
					if slice.Index(parameters[:i], p) >= 0 {
						continue
					}
					args = append(args, nil)
					queryText = strings.ReplaceAll(queryText, p, fmt.Sprintf(":%v", len(args)))
				}
				return e.executeQuery(db, dbServer.Driver, queryText, args)
			}
		}
		return
	}
	return
}

// TODO: Return slice of recordsets
func (e Executor) executeQuery(db *sql.DB, driver, text string, args []interface{}) (recordset datatug.Recordset, err error) {
	started := time.Now()
	var rows *sql.Rows
	if rows, err = db.Query(text, args...); err != nil {
		log.Printf("Failed to execute %v: %v", text, err)
		return
	}
	defer func() {
		if err := closeRowsSeam(rows); err != nil {
			log.Printf("Failed to close rows reader: %v", err)
		}
	}()
	var columnTypes []*sql.ColumnType
	if columnTypes, err = columnTypesSeam(rows); err != nil {
		return
	}

	for _, col := range columnTypes {
		recordset.Columns = append(recordset.Columns, datatug.RecordsetColumn{
			Name:   col.Name(),
			DbType: col.DatabaseTypeName(),
		})
	}
	var rowNumber int
	for rows.Next() {
		rowNumber++
		row := make([]interface{}, len(recordset.Columns))
		valPointers := make([]interface{}, len(recordset.Columns))
		for i := range row {
			valPointers[i] = &row[i]
		}
		if err = scanRowSeam(rows, valPointers...); err != nil {
			err = fmt.Errorf("failed to scan values for row #%v: %w", rowNumber, err)
			return
		}
		for i, col := range recordset.Columns {
			switch col.DbType {
			case "UNIQUEIDENTIFIER":
				if row[i] != nil {
					v := row[i].([]byte)
					if driver == "sqlserver" {
						// Workaround for GUID - see inspiration here https://github.com/satori/go.uuid/issues/19
						binary.BigEndian.PutUint32(v[0:4], binary.LittleEndian.Uint32(v[0:4]))
						binary.BigEndian.PutUint16(v[4:6], binary.LittleEndian.Uint16(v[4:6]))
						binary.BigEndian.PutUint16(v[6:8], binary.LittleEndian.Uint16(v[6:8]))
					}
					if row[i], err = uuid.FromBytes(v); err != nil {
						return
					}
				}
			}
		}
		recordset.Rows = append(recordset.Rows, row)
	}
	recordset.Duration = time.Since(started)
	return
}
