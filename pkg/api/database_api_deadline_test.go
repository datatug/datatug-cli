package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/strongo/validation"
)

// The executor is given the context of the request, with a deadline of its own: a server that
// accepts the connection and never answers ends the route with the built failure sentence and
// does not hold the handler.

// shortDbServerTimeout makes the deadline of the databases route short for the length of the test.
func shortDbServerTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	saved := dbServerDatabasesTimeout
	t.Cleanup(func() { dbServerDatabasesTimeout = saved })
	dbServerDatabasesTimeout = timeout
}

func TestGetServerDatabases_AServerThatNeverAnswersEndsWithTheBuiltFailureSentence(t *testing.T) {
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	f := newServerDatabasesFixture(t, recorded)
	shortDbServerTimeout(t, 20*time.Millisecond)

	// The executor ignores its context, as a driver that cannot be interrupted does: the route
	// still answers when the deadline passes.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.executorAnswer = func(sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		<-release
		return databasesResponse("master"), nil
	}

	type answer struct {
		databases []*datatug.DbCatalog
		err       error
	}
	done := make(chan answer, 1)
	go func() {
		databases, err := GetServerDatabases(context.Background(), databasesRequest("p1", recorded.Server))
		done <- answer{databases, err}
	}()
	select {
	case got := <-done:
		const want = `could not list the databases of db server "sqlserver:db1.example.com:1433"`
		if got.err == nil || got.err.Error() != want {
			t.Fatalf("got %v, want exactly %q", got.err, want)
		}
		if validation.IsBadRequestError(got.err) || got.databases != nil {
			t.Errorf("a server that never answers is a failure of the server, with no databases: %v, %v", got.err, got.databases)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the route did not answer when its deadline passed")
	}
}

// The executor's context is the request's: it carries the deadline, and it ends when the
// request's does.
func TestGetServerDatabases_TheExecutorIsGivenTheContextOfTheRequest(t *testing.T) {
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	f := newServerDatabasesFixture(t, recorded)

	var mu sync.Mutex
	var seen context.Context
	previous := executeSingleSeam
	executeSingleSeam = func(ctx context.Context, e sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		mu.Lock()
		seen = ctx
		mu.Unlock()
		return previous(ctx, e, command)
	}
	t.Cleanup(func() { executeSingleSeam = previous })
	_ = f

	type key struct{}
	request, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "the request"))
	defer cancel()
	if _, err := GetServerDatabases(request, databasesRequest("p1", recorded.Server)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen == nil {
		t.Fatal("the executor was not reached")
	}
	if seen.Value(key{}) != "the request" {
		t.Error("the executor was not given the context of the request")
	}
	deadline, ok := seen.Deadline()
	if !ok || time.Until(deadline) > dbServerDatabasesTimeout {
		t.Errorf("the executor's context has no deadline of %v: %v, %v", dbServerDatabasesTimeout, deadline, ok)
	}
	cancel()
	if !errors.Is(seen.Err(), context.Canceled) {
		t.Errorf("the executor's context did not end with the request's: %v", seen.Err())
	}
}

// A request that has already ended is answered with the failure sentence, with no connection.
func TestGetServerDatabases_ARequestThatEndedHasNoConnection(t *testing.T) {
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	f := newServerDatabasesFixture(t, recorded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GetServerDatabases(ctx, databasesRequest("p1", recorded.Server))
	if err == nil || err.Error() != `could not list the databases of db server "sqlserver:db1.example.com:1433"` {
		t.Fatalf("err = %v", err)
	}
	if len(f.executed) != 0 {
		t.Errorf("the executor was reached %d times for a request that had ended", len(f.executed))
	}
}

// The route reads the server out of a project folder that the file store saved: that a recorded
// server reaches the executor is proved above with a mock store, and here with the real store of
// the files, so a change in how the store names or shapes the record of a db server (a bump of
// datatug-core) makes the route refuse every server and fails this test and not only the answer.
func TestGetServerDatabases_AServerSavedWithTheFileStoreReachesTheExecutorOnce(t *testing.T) {
	projectDir := t.TempDir()
	recorded := recordedServer("sqlserver", "db1.example.com", 1433)
	if err := filestore.NewProjectStore("p1", projectDir).DbServersStore("sqlserver").SaveProjDbServer(context.Background(), &recorded); err != nil {
		t.Fatalf("the file store did not save the server: %v", err)
	}

	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	storage.NewDatatugStore = func(id string) (storage.Store, error) {
		return filestore.NewStore(id, map[string]string{"p1": projectDir})
	}
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	ConfigureSecureSession(session, map[string]string{"p1": projectDir}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })

	var executed []sqlexecute.RequestCommand
	previous := executeSingleSeam
	t.Cleanup(func() { executeSingleSeam = previous })
	executeSingleSeam = func(_ context.Context, _ sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		executed = append(executed, command)
		return databasesResponse("master", "shop"), nil
	}

	databases, err := GetServerDatabases(context.Background(), databasesRequest("p1", recorded.Server))
	if err != nil {
		t.Fatalf("a server saved with the file store was refused: %v", err)
	}
	if len(databases) != 2 || databases[0].ID != "master" || databases[1].ID != "shop" {
		t.Errorf("databases = %+v, want master and shop", databases)
	}
	if len(executed) != 1 {
		t.Fatalf("the executor was reached %d times, want once", len(executed))
	}
	if got := executed[0].ServerRef; got.Driver != "sqlserver" || got.Host != "db1.example.com" || got.Port != 1433 {
		t.Errorf("the executor was given the server %+v, want sqlserver db1.example.com 1433", got)
	}

	// A server that is not saved is not reached.
	executed = nil
	if _, err = GetServerDatabases(context.Background(), databasesRequest("p1", datatug.ServerRef{Driver: "sqlserver", Host: "db2.example.com", Port: 1433})); err == nil {
		t.Error("a server that the project does not record was answered")
	}
	if len(executed) != 0 {
		t.Errorf("a server that the project does not record reached the executor %d times", len(executed))
	}
}
