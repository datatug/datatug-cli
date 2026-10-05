package api

import (
	"context"
	"errors"
	"log"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

// What the route that lists the databases of a db server answers when it refuses a server,
// as sentences that are fixed: they name nothing the client sent.
const (
	// dbServerNotRecordedSentence is the answer for a server that the served project does not
	// record (or whose record cannot be used): nothing is connected to.
	dbServerNotRecordedSentence = "the project does not record this db server"
	// dbServerDatabasesDriverSentence is the answer for a server that is not a SQL Server one:
	// the text that lists the databases is the one of SQL Server.
	dbServerDatabasesDriverSentence = "the databases of a db server are listed for a sqlserver server only"
)

// listDatabasesText lists the databases of a SQL Server server that are not its own.
const listDatabasesText = "select name from sys.databases where owner_sid > 0x01"

// GetServerDatabases returns list of databases hosted at a server.
//
// It connects only to a server that the served project records, and with the parts that the
// project records for it: the project is checked as on every other route (a plain name, and a
// project this process serves), then the server reference as the other db-server routes check
// theirs, then the reference is looked up in the project's db servers. A reference that the
// project does not record is refused, before anything is dialled, with a fixed sentence, and
// the executor is given the recorded server, which passes the same checks, and never the
// client's own values. Every answer is a sentence built here from the recorded server: the
// text of the driver, which quotes a host, a user and an address, is logged and not answered.
func GetServerDatabases(ctx context.Context, request dto.GetServerDatabasesRequest) (databases []*datatug.DbCatalog, err error) {
	if err = request.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	if err = ValidateProjectIdentifier("project", request.Project); err != nil {
		return nil, err
	}
	// The project comes from the query and not from a route that resolved the store of its
	// project, so a project this process does not serve is refused here, as a bad request.
	if _, err = servedProjectDir(request.Project); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	if err = validateDbServer(request.ServerRef); err != nil {
		return nil, err
	}
	if request.Driver != "sqlserver" {
		return nil, validation.NewBadRequestError(errors.New(dbServerDatabasesDriverSentence))
	}
	server, err := recordedDbServer(ctx, request.Project, request.ServerRef)
	if err != nil {
		return nil, err
	}

	command := sqlexecute.RequestCommand{ServerRef: server, Text: listDatabasesText}
	response, err := executeSingleSeam(sqlexecute.NewExecutor(nil, nil), command)
	if err != nil {
		return nil, itemWriteFailed("list", "the databases of db server", serverShown(server), err)
	}
	names, ok := databaseNames(response)
	if !ok {
		return nil, itemWriteFailed("list", "the databases of db server", serverShown(server), errors.New("the answer is not a list of names"))
	}
	databases = make([]*datatug.DbCatalog, len(names))
	for i, name := range names {
		databases[i] = new(datatug.DbCatalog)
		databases[i].ID = name
	}
	return databases, nil
}

// recordedDbServer looks the server that a client named up in the db servers of the project,
// and returns the server as the project records it. A server that the project does not
// record, a record that cannot be read, and a record that holds another server than the one
// named, are the same refusal: a fixed sentence, a bad request. What the store said goes to
// the log.
func recordedDbServer(ctx context.Context, projectID string, requested datatug.ServerRef) (datatug.ServerRef, error) {
	refuse := func(cause error) (datatug.ServerRef, error) {
		log.Printf("api: db server %q is not recorded by project %q: %v", serverShown(requested), dbcopy.SourceIDDisplay(projectID), cause)
		return datatug.ServerRef{}, validation.NewBadRequestError(errors.New(dbServerNotRecordedSentence))
	}
	store, err := projectStoreForID("", projectID)
	if err != nil {
		return datatug.ServerRef{}, err
	}
	record, err := store.DbServersStore(requested.Driver).LoadProjDbServer(ctx, requested.GetID())
	if err != nil {
		return refuse(err)
	}
	if record == nil {
		return refuse(errors.New("the store has no record"))
	}
	// The record is the server that was named, part for part: so its parts are the ones that
	// passed the checks of the route (a record that holds a host that is not a host holds
	// another server than any that passes them).
	server := record.Server
	if server.Driver != requested.Driver || server.Host != requested.Host || server.Port != requested.Port {
		return refuse(errors.New("the record holds another server"))
	}
	return server, nil
}

// databaseNames reads the names of the databases out of the answer of the executor: one name
// in the first column of each row of the one recordset. It reports false for an answer of any
// other shape, which is a failure and not a panic.
func databaseNames(response sqlexecute.Response) (names []string, ok bool) {
	if len(response.Commands) == 0 || response.Commands[0] == nil || len(response.Commands[0].Items) == 0 {
		return nil, false
	}
	recordset, isRecordset := response.Commands[0].Items[0].Value.(datatug.Recordset)
	if !isRecordset {
		return nil, false
	}
	names = make([]string, len(recordset.Rows))
	for i, row := range recordset.Rows {
		if len(row) == 0 {
			return nil, false
		}
		if names[i], ok = row[0].(string); !ok {
			return nil, false
		}
	}
	return names, true
}
