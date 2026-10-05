package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// The databases of a db server are listed by connecting to it, so the route that does it
// connects only to a server that the served project records, with the parts that the project
// records, and answers with sentences that it builds: nothing a client sent and nothing a
// driver said. These tests count the calls that reach the executor, which is where a
// connection would be made.

// serverDatabasesFixture serves one project, "p1", whose db servers are the ones recorded, and
// counts what the route does with them.
type serverDatabasesFixture struct {
	// executed are the commands that reached the executor.
	executed []sqlexecute.RequestCommand
	// looked are the (driver, ID) pairs that were looked up in the project's db servers.
	looked []string
	// stores are the project stores that were asked for.
	stores int
	// executorAnswer is what the executor answers; the default is a list of two databases.
	executorAnswer func(command sqlexecute.RequestCommand) (sqlexecute.Response, error)
}

// recordedServer is a db server as a project records it.
func recordedServer(driver, host string, port int) datatug.ProjDbServer {
	server := datatug.ProjDbServer{Server: datatug.ServerRef{Driver: driver, Host: host, Port: port}}
	server.ID = server.Server.GetID()
	return server
}

func databasesResponse(names ...string) sqlexecute.Response {
	rows := make([][]interface{}, len(names))
	for i, name := range names {
		rows[i] = []interface{}{name}
	}
	return sqlexecute.Response{Commands: []*sqlexecute.CommandResponse{{Items: []sqlexecute.CommandResponseItem{{Value: datatug.Recordset{Rows: rows}}}}}}
}

func newServerDatabasesFixture(t *testing.T, recorded ...datatug.ProjDbServer) *serverDatabasesFixture {
	t.Helper()
	f := &serverDatabasesFixture{}
	f.executorAnswer = func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		return databasesResponse("master", "shop"), nil
	}
	previousSeam := executeSingleSeam
	t.Cleanup(func() { executeSingleSeam = previousSeam })
	executeSingleSeam = func(_ sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		f.executed = append(f.executed, command)
		return f.executorAnswer(command)
	}
	servers := func(driver string) datatug.ProjDbServersStore {
		return mockDbServersStore{loadProjDbServerFunc: func(_ context.Context, id string, _ ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
			f.looked = append(f.looked, driver+" "+id)
			for _, server := range recorded {
				if server.Server.Driver == driver && server.ID == id {
					found := server
					return &found, nil
				}
			}
			return nil, fmt.Errorf("failed to load %s from project: %w", id, os.ErrNotExist)
		}}
	}
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			f.stores++
			return mockProjectStore{dbServersStoreFunc: servers}
		}}, nil
	}
	serveProjects(t, "p1")
	return f
}

func databasesRequest(project string, server datatug.ServerRef) dto.GetServerDatabasesRequest {
	return dto.GetServerDatabasesRequest{Project: project, Environment: "local", ServerRef: server}
}

func TestGetServerDatabases_RefusesAnUnsafeValueInEveryPositionBeforeAnythingIsDialled(t *testing.T) {
	f := newServerDatabasesFixture(t, recordedServer("sqlserver", "localhost", 1433))
	ctx := context.Background()
	plain := datatug.ServerRef{Driver: "sqlserver", Host: "localhost", Port: 1433}
	type position struct {
		name   string
		field  string
		unsafe []sourcecases.UnsafeIdentifier
		build  func(value string) dto.GetServerDatabasesRequest
	}
	positions := []position{
		{"project", "project", sourcecases.UnsafeIdentifiers(), func(v string) dto.GetServerDatabasesRequest { return databasesRequest(v, plain) }},
		{"driver", "driver", sourcecases.UnsafeIdentifiers(), func(v string) dto.GetServerDatabasesRequest {
			return databasesRequest("p1", datatug.ServerRef{Driver: v, Host: "localhost", Port: 1433})
		}},
		{"host", "host", sourcecases.UnsafeHosts(), func(v string) dto.GetServerDatabasesRequest {
			return databasesRequest("p1", datatug.ServerRef{Driver: "sqlserver", Host: v, Port: 1433})
		}},
	}
	for _, c := range unrecordedServers() {
		c := c
		positions = append(positions, position{c.name, c.field, []sourcecases.UnsafeIdentifier{{Name: c.name, ID: c.value}},
			func(string) dto.GetServerDatabasesRequest { return databasesRequest("p1", c.server) }})
	}
	for _, p := range positions {
		for _, c := range p.unsafe {
			f.executed, f.looked, f.stores = nil, nil, 0
			databases, err := GetServerDatabases(ctx, p.build(c.ID))
			name := fmt.Sprintf("%s: %s", p.name, c.Name)
			if c.ID == "" {
				// An empty value is the missing-field answer of the request, or a plain refusal.
				if err == nil || !validation.IsBadRequestError(err) {
					t.Errorf("%s: an empty %s gave %v, want a bad-request answer", name, p.name, err)
				}
			} else {
				assertRefusal(t, name, p.field, c.ID, err)
			}
			if databases != nil {
				t.Errorf("%s: databases were listed: %v", name, databases)
			}
			if len(f.executed) != 0 || len(f.looked) != 0 || f.stores != 0 {
				t.Errorf("%s: reached the executor %d times, the db servers %d times and the project store %d times, want none", name, len(f.executed), len(f.looked), f.stores)
			}
		}
	}
}

// The count above means something only if a request that passes every check does reach the
// executor, with the executor seam the same.
func TestGetServerDatabases_ARecordedServerReachesTheExecutorOnceWithExactlyTheRecordedParts(t *testing.T) {
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	other := recordedServer("sqlserver", "db2.example.com", 0)
	f := newServerDatabasesFixture(t, other, recorded)

	request := databasesRequest("p1", datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1433})
	// A client may send credentials in the body of the request; the route does not use them.
	request.Credentials = &datatug.Credentials{Username: "client-user", Password: "client-secret"}
	databases, err := GetServerDatabases(context.Background(), request)
	if err != nil {
		t.Fatalf("a recorded server was refused: %v", err)
	}
	if len(databases) != 2 || databases[0].ID != "master" || databases[1].ID != "shop" {
		t.Errorf("databases = %+v, want master and shop", databases)
	}
	if len(f.executed) != 1 {
		t.Fatalf("the executor was reached %d times, want once", len(f.executed))
	}
	command := f.executed[0]
	if command.ServerRef != recorded.Server {
		t.Errorf("the executor was given the server %+v, want exactly the recorded %+v", command.ServerRef, recorded.Server)
	}
	if command.Username != "" || command.Password != "" || command.Env != "" || command.DB != "" || len(command.Parameters) != 0 {
		t.Errorf("the executor was given more than the recorded server and the fixed text: %+v", command)
	}
	if command.Text != "select name from sys.databases where owner_sid > 0x01" {
		t.Errorf("the executor was given the text %q", command.Text)
	}
	if len(f.looked) != 1 || f.looked[0] != "sqlserver sqlserver:db1.example.com:1433" {
		t.Errorf("the db servers of the project were asked for %v, want the server by its driver and ID once", f.looked)
	}

	// A server with no port is recorded under its ID without one.
	f.executed, f.looked = nil, nil
	if _, err = GetServerDatabases(context.Background(), databasesRequest("p1", other.Server)); err != nil {
		t.Fatalf("a recorded server with no port was refused: %v", err)
	}
	if len(f.executed) != 1 || f.executed[0].ServerRef != other.Server {
		t.Errorf("executed = %+v, want the recorded server with no port once", f.executed)
	}
}

func TestGetServerDatabases_AServerTheProjectDoesNotRecordIsRefusedWithAFixedAnswer(t *testing.T) {
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	// A project whose files hold a server that is filed under the ID of another, and one whose
	// host is not a host a connection string may be built from.
	inRecordOfAnother := recordedServer("sqlserver", "db1.example.com", 1433)
	inRecordOfAnother.Server.Host = "db9.example.com"
	withSemicolon := recordedServer("sqlserver", "db3.example.com;key=value", 1433)
	withSemicolon.ID = "sqlserver:db3.example.com"
	withSemicolon.Server.Host = "db3.example.com;other=value"
	for _, tc := range []struct {
		name     string
		recorded []datatug.ProjDbServer
		server   datatug.ServerRef
	}{
		{"a server that is not there", []datatug.ProjDbServer{recorded}, datatug.ServerRef{Driver: "sqlserver", Host: "db-elsewhere.example.com", Port: 1433}},
		{"the host of a recorded server with another port", []datatug.ProjDbServer{recorded}, datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1434}},
		{"a project that records no server", nil, datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1433}},
		{"a record that holds another server than its ID says", []datatug.ProjDbServer{inRecordOfAnother}, datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1433}},
		{"a record whose host is not a host", []datatug.ProjDbServer{withSemicolon}, datatug.ServerRef{Driver: "sqlserver", Host: "db3.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServerDatabasesFixture(t, tc.recorded...)
			_, err := GetServerDatabases(context.Background(), databasesRequest("p1", tc.server))
			if err == nil || !validation.IsBadRequestError(err) || !strings.HasSuffix(err.Error(), dbServerNotRecordedSentence) {
				t.Fatalf("got %v, want a bad request that ends with the fixed sentence %q", err, dbServerNotRecordedSentence)
			}
			if strings.Contains(err.Error(), tc.server.Host) {
				t.Errorf("the answer names the host the client sent: %v", err)
			}
			if len(f.executed) != 0 {
				t.Errorf("the executor was reached %d times, want none", len(f.executed))
			}
			if len(f.looked) != 1 {
				t.Errorf("the db servers of the project were asked for %d times, want once, before anything else", len(f.looked))
			}
		})
	}

	t.Run("a record that cannot be read", func(t *testing.T) {
		f := newServerDatabasesFixture(t)
		previous := storage.NewDatatugStore
		storage.NewDatatugStore = func(string) (storage.Store, error) {
			return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
				return mockProjectStore{dbServersStoreFunc: func(string) datatug.ProjDbServersStore {
					return mockDbServersStore{loadProjDbServerFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
						return nil, errors.New("open /Users/operator/some-project/dbs/sqlserver/x.dbserver.json: permission denied")
					}}
				}}
			}}, nil
		}
		defer func() { storage.NewDatatugStore = previous }()
		_, err := GetServerDatabases(context.Background(), databasesRequest("p1", recorded.Server))
		if err == nil || !validation.IsBadRequestError(err) || !strings.HasSuffix(err.Error(), dbServerNotRecordedSentence) || strings.Contains(err.Error(), "operator") {
			t.Errorf("a record that cannot be read gave %v, want the fixed sentence and nothing of the store's text", err)
		}
		if len(f.executed) != 0 {
			t.Errorf("the executor was reached %d times, want none", len(f.executed))
		}
	})

	t.Run("a record that is nothing", func(t *testing.T) {
		f := newServerDatabasesFixture(t)
		previous := storage.NewDatatugStore
		storage.NewDatatugStore = func(string) (storage.Store, error) {
			return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
				return mockProjectStore{} // its db servers store loads (nil, nil)
			}}, nil
		}
		defer func() { storage.NewDatatugStore = previous }()
		_, err := GetServerDatabases(context.Background(), databasesRequest("p1", recorded.Server))
		if err == nil || !strings.HasSuffix(err.Error(), dbServerNotRecordedSentence) {
			t.Errorf("a store that loads nothing gave %v, want the fixed sentence", err)
		}
		if len(f.executed) != 0 {
			t.Errorf("the executor was reached %d times, want none", len(f.executed))
		}
	})

	t.Run("a store that cannot be had", func(t *testing.T) {
		f := newServerDatabasesFixture(t)
		previous := storage.NewDatatugStore
		storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, errors.New("no store") }
		defer func() { storage.NewDatatugStore = previous }()
		if _, err := GetServerDatabases(context.Background(), databasesRequest("p1", recorded.Server)); err == nil {
			t.Error("a store that cannot be had was not an error")
		}
		if len(f.executed) != 0 {
			t.Errorf("the executor was reached %d times, want none", len(f.executed))
		}
	})
}

func TestGetServerDatabases_AProjectThatIsNotServedIsRefusedBeforeAnythingIsDialled(t *testing.T) {
	f := newServerDatabasesFixture(t, recordedServer("sqlserver", "db1.example.com", 1433))
	_, err := GetServerDatabases(context.Background(), databasesRequest("elsewhere", datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1433}))
	_, want := ResolveStoreID("", "elsewhere")
	if err == nil || !validation.IsBadRequestError(err) || !strings.HasSuffix(err.Error(), want.Error()) {
		t.Fatalf("a project that is not served gave %v, want a bad request that says %q", err, want)
	}
	if len(f.executed) != 0 || len(f.looked) != 0 || f.stores != 0 {
		t.Errorf("reached the executor %d times, the db servers %d times and the project store %d times, want none", len(f.executed), len(f.looked), f.stores)
	}
}

// The query that lists the databases is the one of SQL Server: a server of another driver is
// refused with a fixed sentence, before a connection is made, as the file of a SQLite server
// has no connection at all.
func TestGetServerDatabases_OnlyASqlServerServerIsConnectedTo(t *testing.T) {
	for _, server := range []datatug.ServerRef{
		{Driver: "sqlite3"},
		{Driver: "mysql", Host: "db1.example.com"},
		{Driver: "postgres", Host: "db1.example.com", Port: 5432},
	} {
		recorded := datatug.ProjDbServer{Server: server}
		recorded.ID = server.GetID()
		f := newServerDatabasesFixture(t, recorded)
		request := databasesRequest("p1", server)
		_, err := GetServerDatabases(context.Background(), request)
		if err == nil || !validation.IsBadRequestError(err) || !strings.HasSuffix(err.Error(), dbServerDatabasesDriverSentence) {
			t.Errorf("a %s server gave %v, want a bad request that ends with %q", server.Driver, err, dbServerDatabasesDriverSentence)
		}
		if len(f.executed) != 0 {
			t.Errorf("a %s server reached the executor %d times, want none", server.Driver, len(f.executed))
		}
	}
}

// Whatever went wrong between the executor and the answer, the answer is one sentence built
// from the recorded server: the driver's own text quotes a host, a user and an address.
func TestGetServerDatabases_EveryFailureIsABuiltSentence(t *testing.T) {
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	const want = `could not list the databases of db server "sqlserver:db1.example.com:1433"`
	driverText := errors.New(`mssql: login error: Login failed for user 'sa'. dial tcp 10.1.2.3:1433: connect: connection refused (server=db1.example.com;password=s3cretpw)`)
	for _, tc := range []struct {
		name   string
		answer func(sqlexecute.RequestCommand) (sqlexecute.Response, error)
	}{
		{"the driver fails", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) { return sqlexecute.Response{}, driverText }},
		{"no command answers", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) { return sqlexecute.Response{}, nil }},
		{"a command has no item", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
			return sqlexecute.Response{Commands: []*sqlexecute.CommandResponse{{}}}, nil
		}},
		{"a command is nil", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
			return sqlexecute.Response{Commands: []*sqlexecute.CommandResponse{nil}}, nil
		}},
		{"the item is not a recordset", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
			return sqlexecute.Response{Commands: []*sqlexecute.CommandResponse{{Items: []sqlexecute.CommandResponseItem{{Value: "text"}}}}}, nil
		}},
		{"a row has no cell", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
			return sqlexecute.Response{Commands: []*sqlexecute.CommandResponse{{Items: []sqlexecute.CommandResponseItem{{Value: datatug.Recordset{Rows: [][]interface{}{{}}}}}}}}, nil
		}},
		{"a name is not text", func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
			return sqlexecute.Response{Commands: []*sqlexecute.CommandResponse{{Items: []sqlexecute.CommandResponseItem{{Value: datatug.Recordset{Rows: [][]interface{}{{42}}}}}}}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServerDatabasesFixture(t, recorded)
			f.executorAnswer = tc.answer
			databases, err := GetServerDatabases(context.Background(), databasesRequest("p1", recorded.Server))
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want exactly %q", err, want)
			}
			if validation.IsBadRequestError(err) {
				t.Error("a failure of the server is not a bad request")
			}
			if errors.Is(err, driverText) || databases != nil {
				t.Errorf("the answer wraps what the driver said, or lists databases: %v, %v", err, databases)
			}
			if len(f.executed) != 1 {
				t.Errorf("the executor was reached %d times, want once", len(f.executed))
			}
		})
	}
}

// The request's own checks answer before anything is looked up.
func TestGetServerDatabases_AnIncompleteRequestIsABadRequest(t *testing.T) {
	f := newServerDatabasesFixture(t)
	for _, request := range []dto.GetServerDatabasesRequest{
		{},
		{Project: "p1"},
		{Project: "p1", Environment: "local", ServerRef: datatug.ServerRef{Driver: "sqlserver"}},
		{Project: "p1", Environment: "local", ServerRef: datatug.ServerRef{Host: "db1.example.com"}},
		{Project: "p1", ServerRef: datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com"}, Credentials: &datatug.Credentials{Username: "error"}},
	} {
		if _, err := GetServerDatabases(context.Background(), request); err == nil || !validation.IsBadRequestError(err) {
			t.Errorf("%+v gave %v, want a bad request", request, err)
		}
	}
	if len(f.executed) != 0 || len(f.looked) != 0 {
		t.Errorf("an incomplete request reached the executor %d times and the db servers %d times, want none", len(f.executed), len(f.looked))
	}
}
