package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// The IDs of a board, an entity, a recordset definition, a db server and a folder, the
// host of a db server and the project that holds them are joined into file paths by the
// project store. Each entry of this package that takes one refuses what is not a plain
// name (or, for an ID with folders, not plain names between slashes; for a host, not a
// host) before the store is asked, with an answer that names the field and nothing of
// the value. These tests send every unsafe text to every entry, and count the project
// stores that were asked for.

func TestValidatePathIdentifier(t *testing.T) {
	for _, id := range []string{"a", "a/b", "folder/set-1/data.v2", "données/été", strings.Repeat("a", 128) + "/b"} {
		if err := ValidatePathIdentifier("recordset", id); err != nil {
			t.Errorf("ValidatePathIdentifier(%q) = %v, want a path of plain names accepted", id, err)
		}
	}
	for _, c := range sourcecases.UnsafePathIdentifiers() {
		assertRefusal(t, c.Name, "recordset", c.ID, ValidatePathIdentifier("recordset", c.ID))
	}
}

func TestValidateHost(t *testing.T) {
	for _, host := range []string{"localhost", "db.example.com", "10.0.0.1", "::1", "fe80::1", "my_host-1", "DB1", strings.Repeat("a", 253)} {
		if err := validateHost("host", host); err != nil {
			t.Errorf("validateHost(%q) = %v, want a host accepted", host, err)
		}
	}
	for _, c := range sourcecases.UnsafeHosts() {
		if c.ID == "" {
			continue // an empty host is for the server reference's own missing-field answer
		}
		assertRefusal(t, c.Name, "host", c.ID, validateHost("host", c.ID))
	}
}

func TestValidateServerRef(t *testing.T) {
	if err := ValidateServerRef(datatug.ServerRef{Driver: "sqlserver", Host: "localhost", Port: 1433}); err != nil {
		t.Errorf("a plain server reference is refused: %v", err)
	}
	// An empty driver or host is the missing-field answer of ServerRef.Validate, which
	// the routes give next; sqlite3 has no host at all.
	if err := ValidateServerRef(datatug.ServerRef{Driver: "sqlite3"}); err != nil {
		t.Errorf("a server reference with no host is refused: %v", err)
	}
	for _, c := range sourcecases.UnsafeIdentifiers() {
		if c.ID == "" {
			continue
		}
		assertRefusal(t, c.Name, "driver", c.ID, ValidateServerRef(datatug.ServerRef{Driver: c.ID, Host: "localhost"}))
	}
	for _, c := range sourcecases.UnsafeHosts() {
		if c.ID == "" {
			continue
		}
		assertRefusal(t, c.Name, "host", c.ID, ValidateServerRef(datatug.ServerRef{Driver: "sqlserver", Host: c.ID}))
	}
	for _, c := range unrecordedServers() {
		assertRefusal(t, c.name, c.field, c.value, ValidateServerRef(c.server))
	}
}

// unrecordedServer is a server reference that passes the rules of a plain name and of a
// host, and that the validation of datatug.ServerRef refuses with a message that quotes
// the value: the driver is not one it knows, or the server is a sqlite3 one that has a
// host (a user name and a password typed with a colon are a host to the rule of a host).
type unrecordedServer struct {
	name   string
	field  string
	value  string
	server datatug.ServerRef
}

func unrecordedServers() []unrecordedServer {
	return []unrecordedServer{
		{"a plain driver that no store records", "driver", "mongodb", datatug.ServerRef{Driver: "mongodb", Host: "localhost"}},
		{"a plain driver with a long name", "driver", "oracle2-private", datatug.ServerRef{Driver: "oracle2-private", Host: "localhost"}},
		{"a user and a password typed as the host of a sqlite3 server", "host", "alice:s3cretpw", datatug.ServerRef{Driver: "sqlite3", Host: "alice:s3cretpw"}},
		{"a host name given to a sqlite3 server", "host", "db.example.com", datatug.ServerRef{Driver: "sqlite3", Host: "db.example.com"}},
	}
}

// The drivers that ValidateServerRef lets through are the ones datatug.ServerRef accepts, so
// that its own refusal, which quotes the driver, is never the one a client reads. A driver
// that datatug-core learns to accept makes this test fail, until it is added to dbServerDrivers.
func TestDbServerDrivers_AreTheOnesTheServerRefAccepts(t *testing.T) {
	for _, driver := range []string{"sqlite3", "sqlserver", "mysql", "oracle", "postgres", "mongodb", "ingitdb", "openvaultdb", "sqlite"} {
		server := datatug.ServerRef{Driver: driver, Host: "localhost"}
		if driver == "sqlite3" {
			server.Host = ""
		}
		accepted := server.Validate() == nil
		listed := false
		for _, known := range dbServerDrivers {
			listed = listed || known == driver
		}
		if accepted != listed {
			t.Errorf("driver %q: datatug.ServerRef accepts it: %v, dbServerDrivers lists it: %v", driver, accepted, listed)
		}
		if err := ValidateServerRef(server); (err == nil) != listed {
			t.Errorf("driver %q: ValidateServerRef = %v, want a refusal exactly when the driver is not listed", driver, err)
		}
	}
}

// A project that this process serves is a key of the served projects and needs no more;
// any other is refused unless it is a plain name, which the routes' own unknown-project
// answer then takes.
func TestValidateProjectIdentifier(t *testing.T) {
	session := secureread.Session{Unrestricted: true}
	ConfigureSecureSession(session, map[string]string{"My Project (old)": t.TempDir()}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })

	for _, id := range []string{"My Project (old)", "demo-project-1", "not-served"} {
		if err := ValidateProjectIdentifier("project", id); err != nil {
			t.Errorf("ValidateProjectIdentifier(%q) = %v, want it accepted", id, err)
		}
	}
	for _, c := range sourcecases.UnsafeIdentifiers() {
		assertRefusal(t, c.Name, "project", c.ID, ValidateProjectIdentifier("project", c.ID))
	}
}

// assertRefusal is a check that err is a bad-request answer that names field and does
// not echo value.
func assertRefusal(t *testing.T, name, field, value string, err error) {
	t.Helper()
	if err == nil || !validation.IsBadRequestError(err) {
		t.Errorf("%s: %q gave %v, want a bad-request refusal", name, value, err)
		return
	}
	if !strings.Contains(err.Error(), field) {
		t.Errorf("%s: the refusal does not name the field %q: %v", name, field, err)
	}
	if len(value) >= 3 && strings.Contains(err.Error(), value) {
		t.Errorf("%s: the refusal echoes the value: %v", name, err)
	}
}

// askedStores replaces the store factory with one that counts the project stores it
// hands out, and whose project store answers every call a route makes, so a request
// that passes its checks reaches the store. It restores the factory when the test ends.
func askedStores(t *testing.T) *int {
	t.Helper()
	asked := new(int)
	servers := mockDbServersStore{
		loadProjDbServerFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
			return &datatug.ProjDbServer{}, nil
		},
	}
	projectStore := mockProjectStore{
		dbServersStoreFunc: func(string) datatug.ProjDbServersStore { return servers },
		loadBoardFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Board, error) {
			return &datatug.Board{}, nil
		},
		loadEntityFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Entity, error) {
			return &datatug.Entity{}, nil
		},
		loadRecordsetDefinitionFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.RecordsetDefinition, error) {
			return &datatug.RecordsetDefinition{}, nil
		},
	}
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			*asked++
			return projectStore
		}}, nil
	}
	return asked
}

// idEntry is one entry of this package that takes an ID a client sent. call runs it
// with the value in the position under test and plain values everywhere else.
type idEntry struct {
	name string
	// position says what the value is: the field the refusal must name.
	position string
	// unsafe is the list of texts the position must refuse.
	unsafe []sourcecases.UnsafeIdentifier
	// emptyIsOK is true when an empty value is valid: the path of a folder at the root
	// of the folders, and the ID of a db server that is not given.
	emptyIsOK bool
	call      func(value string) error
}

func plainProject() dto.ProjectRef { return dto.ProjectRef{ProjectID: "p1", StoreID: "files"} }

func itemRef(id string) dto.ProjectItemRef {
	return dto.ProjectItemRef{ProjectRef: plainProject(), ID: id}
}

func plainBoard(id string) datatug.Board {
	return datatug.Board{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}}}
}

func plainEntity(id, title string) *datatug.Entity {
	return &datatug.Entity{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: title}}}
}

func plainServer() datatug.ServerRef {
	return datatug.ServerRef{Driver: "sqlserver", Host: "localhost", Port: 1433}
}

func projectOf(value string) dto.ProjectRef {
	return dto.ProjectRef{ProjectID: value, StoreID: "files"}
}

// idEntries lists every entry of the package that takes a board, entity, recordset,
// db server or folder ID, the host or driver of a db server, or the project of any of
// them, each in every position.
func idEntries() []idEntry {
	ctx := context.Background()
	identifiers := sourcecases.UnsafeIdentifiers()
	paths := sourcecases.UnsafePathIdentifiers()
	hosts := sourcecases.UnsafeHosts()
	var entries []idEntry
	add := func(e idEntry) { entries = append(entries, e) }

	// Boards.
	add(idEntry{name: "GetBoard", position: "boardID", unsafe: identifiers, call: func(v string) error {
		_, err := GetBoard(ctx, itemRef(v))
		return err
	}})
	add(idEntry{name: "DeleteBoard", position: "boardID", unsafe: identifiers, call: func(v string) error {
		return DeleteBoard(ctx, itemRef(v))
	}})
	add(idEntry{name: "SaveBoard", position: "boardID", unsafe: identifiers, call: func(v string) error {
		_, err := SaveBoard(ctx, plainProject(), plainBoard(v))
		return err
	}})
	add(idEntry{name: "CreateBoard", position: "boardID", unsafe: identifiers, call: func(v string) error {
		_, err := CreateBoard(ctx, plainProject(), plainBoard(v))
		return err
	}})
	for _, p := range []struct {
		name string
		call func(project string) error
	}{
		{"GetBoard", func(v string) error {
			_, err := GetBoard(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "b1"})
			return err
		}},
		{"DeleteBoard", func(v string) error { return DeleteBoard(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "b1"}) }},
		{"SaveBoard", func(v string) error { _, err := SaveBoard(ctx, projectOf(v), plainBoard("b1")); return err }},
		{"CreateBoard", func(v string) error { _, err := CreateBoard(ctx, projectOf(v), plainBoard("b1")); return err }},
		{"GetEntity", func(v string) error {
			_, err := GetEntity(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "e1"})
			return err
		}},
		{"GetAllEntities", func(v string) error { _, err := GetAllEntities(ctx, projectOf(v)); return err }},
		{"DeleteEntity", func(v string) error { return DeleteEntity(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "e1"}) }},
		{"SaveEntity", func(v string) error { return SaveEntity(ctx, projectOf(v), plainEntity("e1", "")) }},
		{"GetDatasetDefinition", func(v string) error {
			_, err := GetDatasetDefinition(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "d1"})
			return err
		}},
		{"GetRecordsetsSummary", func(v string) error { _, err := GetRecordsetsSummary(ctx, projectOf(v)); return err }},
		{"CreateFolder", func(v string) error {
			_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: projectOf(v), Path: "f", Name: "n"})
			return err
		}},
		{"DeleteFolder", func(v string) error { return DeleteFolder(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "f1"}) }},
		{"AddDbServer", func(v string) error {
			return AddDbServer(ctx, projectOf(v), datatug.ProjDbServer{Server: plainServer()})
		}},
		{"UpdateDbServer", func(v string) error {
			return UpdateDbServer(ctx, projectOf(v), datatug.ProjDbServer{Server: plainServer()})
		}},
		{"DeleteDbServer", func(v string) error { return DeleteDbServer(ctx, projectOf(v), plainServer()) }},
		{"GetDbServerSummary", func(v string) error { _, err := GetDbServerSummary(ctx, projectOf(v), plainServer()); return err }},
		{"GetProjectSummary", func(v string) error { _, err := GetProjectSummary(ctx, projectOf(v)); return err }},
		{"GetProjectFull", func(v string) error { _, err := GetProjectFull(ctx, projectOf(v)); return err }},
		{"GetEnvironmentSummary", func(v string) error {
			_, err := GetEnvironmentSummary(ctx, dto.ProjectItemRef{ProjectRef: projectOf(v), ID: "local"})
			return err
		}},
	} {
		add(idEntry{name: p.name + " (project)", position: "project", unsafe: identifiers, call: p.call})
	}

	// Entities.
	add(idEntry{name: "GetEntity", position: "entityID", unsafe: identifiers, call: func(v string) error {
		_, err := GetEntity(ctx, itemRef(v))
		return err
	}})
	add(idEntry{name: "DeleteEntity", position: "entityID", unsafe: identifiers, call: func(v string) error {
		return DeleteEntity(ctx, itemRef(v))
	}})
	add(idEntry{name: "SaveEntity", position: "entityID", unsafe: identifiers, call: func(v string) error {
		return SaveEntity(ctx, plainProject(), plainEntity(v, ""))
	}})
	add(idEntry{name: "SaveEntity (the title is the ID of an entity with none)", position: "entityID", unsafe: identifiers, call: func(v string) error {
		return SaveEntity(ctx, plainProject(), plainEntity("", v))
	}})

	// Recordset definitions: an ID with folders.
	add(idEntry{name: "GetDatasetDefinition", position: "recordsetID", unsafe: paths, call: func(v string) error {
		_, err := GetDatasetDefinition(ctx, itemRef(v))
		return err
	}})

	// Folders.
	add(idEntry{name: "CreateFolder (path)", position: "path", unsafe: paths, emptyIsOK: true, call: func(v string) error {
		_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: plainProject(), Path: v, Name: "n"})
		return err
	}})
	add(idEntry{name: "CreateFolder (name)", position: "name", unsafe: identifiers, call: func(v string) error {
		_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: plainProject(), Path: "f", Name: v})
		return err
	}})
	add(idEntry{name: "DeleteFolder", position: "folderID", unsafe: paths, call: func(v string) error {
		return DeleteFolder(ctx, itemRef(v))
	}})

	// Db servers: the driver and the host, and the ID of a server that is saved.
	for _, route := range []struct {
		name string
		call func(server datatug.ServerRef) error
	}{
		{"AddDbServer", func(s datatug.ServerRef) error {
			return AddDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: s})
		}},
		{"UpdateDbServer", func(s datatug.ServerRef) error {
			return UpdateDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: s})
		}},
		{"DeleteDbServer", func(s datatug.ServerRef) error { return DeleteDbServer(ctx, plainProject(), s) }},
		{"GetDbServerSummary", func(s datatug.ServerRef) error { _, err := GetDbServerSummary(ctx, plainProject(), s); return err }},
	} {
		route := route
		add(idEntry{name: route.name + " (driver)", position: "driver", unsafe: identifiers, call: func(v string) error {
			return route.call(datatug.ServerRef{Driver: v, Host: "localhost"})
		}})
		add(idEntry{name: route.name + " (host)", position: "host", unsafe: hosts, call: func(v string) error {
			return route.call(datatug.ServerRef{Driver: "sqlserver", Host: v})
		}})
	}
	// Values that pass the rules of a name and of a host, and that the validation of
	// datatug.ServerRef refuses with a message that quotes them.
	for _, route := range []struct {
		name string
		call func(server datatug.ServerRef) error
	}{
		{"AddDbServer", func(s datatug.ServerRef) error {
			return AddDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: s})
		}},
		{"UpdateDbServer", func(s datatug.ServerRef) error {
			return UpdateDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: s})
		}},
		{"DeleteDbServer", func(s datatug.ServerRef) error { return DeleteDbServer(ctx, plainProject(), s) }},
		{"GetDbServerSummary", func(s datatug.ServerRef) error { _, err := GetDbServerSummary(ctx, plainProject(), s); return err }},
	} {
		route := route
		for _, c := range unrecordedServers() {
			c := c
			add(idEntry{name: route.name + " (" + c.name + ")", position: c.field,
				unsafe: []sourcecases.UnsafeIdentifier{{Name: c.name, ID: c.value}},
				call:   func(string) error { return route.call(c.server) }})
		}
	}
	for _, name := range []string{"AddDbServer", "UpdateDbServer"} {
		name := name
		add(idEntry{name: name + " (id)", position: "id", unsafe: hosts, emptyIsOK: true, call: func(v string) error {
			server := datatug.ProjDbServer{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: v}}, Server: plainServer()}
			if name == "AddDbServer" {
				return AddDbServer(context.Background(), plainProject(), server)
			}
			return UpdateDbServer(context.Background(), plainProject(), server)
		}})
	}

	// Recordset rows: not implemented, and a recordset's name is still checked.
	add(idEntry{name: "AddRowsToRecordset", position: "recordset", unsafe: paths, call: func(v string) error {
		_, err := AddRowsToRecordset(RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: "p1", Recordset: v}, Data: "d"}, nil)
		return err
	}})
	add(idEntry{name: "RemoveRowsFromRecordset", position: "recordset", unsafe: paths, call: func(v string) error {
		_, err := RemoveRowsFromRecordset(RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: "p1", Recordset: v}, Data: "d"}, nil)
		return err
	}})
	add(idEntry{name: "UpdateRowsInRecordset", position: "recordset", unsafe: paths, call: func(v string) error {
		_, err := UpdateRowsInRecordset(RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: "p1", Recordset: v}, Data: "d"}, nil)
		return err
	}})
	add(idEntry{name: "AddRowsToRecordset (project)", position: "project", unsafe: identifiers, call: func(v string) error {
		_, err := AddRowsToRecordset(RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: v, Recordset: "r1"}, Data: "d"}, nil)
		return err
	}})
	return entries
}

func TestEntries_RefuseAnUnsafeIDBeforeAnyProjectStoreIsAsked(t *testing.T) {
	asked := askedStores(t)
	for _, e := range idEntries() {
		for _, c := range e.unsafe {
			if c.ID == "" && e.emptyIsOK {
				continue
			}
			*asked = 0
			err := e.call(c.ID)
			if c.ID == "" {
				// An empty value is the missing-field answer that was given before, or a
				// plain refusal: either way a bad request.
				if err == nil || !validation.IsBadRequestError(err) {
					t.Errorf("%s: an empty %s gave %v, want a bad-request answer", e.name, e.position, err)
				}
			} else {
				assertRefusal(t, fmt.Sprintf("%s in %s: %s", e.name, e.position, c.Name), e.position, c.ID, err)
			}
			if *asked != 0 {
				t.Errorf("%s in %s: %s reached %d project stores, want 0", e.name, e.position, c.Name, *asked)
			}
		}
	}
}

// The count above means something only if a request with plain values does reach the
// store: each of these does, and is not refused.
func TestEntries_APlainRequestReachesTheStore(t *testing.T) {
	asked := askedStores(t)
	serveProjects(t, "p1") // CreateFolder takes its project from the request: it must be served
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"GetBoard":      func() error { _, err := GetBoard(ctx, itemRef("b1")); return err },
		"DeleteBoard":   func() error { return DeleteBoard(ctx, itemRef("b1")) },
		"SaveBoard":     func() error { _, err := SaveBoard(ctx, plainProject(), plainBoard("b1")); return err },
		"CreateBoard":   func() error { _, err := CreateBoard(ctx, plainProject(), plainBoard("b1")); return err },
		"GetEntity":     func() error { _, err := GetEntity(ctx, itemRef("Customer")); return err },
		"DeleteEntity":  func() error { return DeleteEntity(ctx, itemRef("Customer")) },
		"SaveEntity":    func() error { return SaveEntity(ctx, plainProject(), plainEntity("Customer", "")) },
		"GetDatasetDef": func() error { _, err := GetDatasetDefinition(ctx, itemRef("sub/support-notes")); return err },
		"CreateFolder": func() error {
			_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: plainProject(), Path: "a/b", Name: "c"})
			return err
		},
		"CreateRootFolder": func() error {
			_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: plainProject(), Name: "c"})
			return err
		},
		"DeleteFolder": func() error { return DeleteFolder(ctx, itemRef("a/c")) },
		"AddDbServer":  func() error { return AddDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: plainServer()}) },
		"AddDbServerWithID": func() error {
			return AddDbServer(ctx, plainProject(), datatug.ProjDbServer{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: plainServer().GetID()}}, Server: plainServer()})
		},
		"UpdateDbServer":     func() error { return UpdateDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: plainServer()}) },
		"DeleteDbServer":     func() error { return DeleteDbServer(ctx, plainProject(), plainServer()) },
		"GetDbServerSummary": func() error { _, err := GetDbServerSummary(ctx, plainProject(), plainServer()); return err },
		"GetDbServerSummary on an IPv6 host": func() error {
			_, err := GetDbServerSummary(ctx, plainProject(), datatug.ServerRef{Driver: "mysql", Host: "::1"})
			return err
		},
	} {
		*asked = 0
		if err := call(); err != nil {
			t.Errorf("%s: a request with plain values was refused: %v", name, err)
		}
		if *asked == 0 {
			t.Errorf("%s: a request with plain values never reached the project store, so the counts prove nothing", name)
		}
	}
	// A db server whose ID is not the ID of its reference is refused, naming the field.
	err := AddDbServer(ctx, plainProject(), datatug.ProjDbServer{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "other"}}, Server: plainServer()})
	assertRefusal(t, "a db server with an ID of another server", "id", "other", err)
}

// A project store that fails answers the same fixed text whatever the failure was: the
// store's own text quotes the path it built, and none of that reaches the client. The
// text names the item, and the ID only when it is a plain name.
func TestEntries_AStoreFailureIsOneFixedAnswerThatQuotesNoPath(t *testing.T) {
	ctx := context.Background()
	serveProjects(t, "p1") // CreateFolder takes its project from the request: it must be served
	causes := []error{
		fmt.Errorf("failed to load board[b1] from project: %w", errors.New("file does not exist")),
		errors.New("open /Users/operator/some-project/boards/b1/board.json: no such file or directory"),
		errors.New("read /Users/operator/some-project/boards/b1/board.json: is a directory"),
		errors.New("open /Users/operator/some-project/boards/b1/board.json: not a directory"),
		errors.New("open /Users/operator/some-project/boards/b1/board.json: permission denied"),
	}
	failing := func(project mockProjectStore) {
		previous := storage.NewDatatugStore
		t.Cleanup(func() { storage.NewDatatugStore = previous })
		storage.NewDatatugStore = func(string) (storage.Store, error) {
			return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return project }}, nil
		}
	}
	for _, cause := range causes {
		cause := cause
		stores := mockProjectStore{
			loadBoardFunc:    func(context.Context, string, ...datatug.StoreOption) (*datatug.Board, error) { return nil, cause },
			saveBoardFunc:    func(context.Context, *datatug.Board) error { return cause },
			deleteBoardFunc:  func(context.Context, string) error { return cause },
			loadEntityFunc:   func(context.Context, string, ...datatug.StoreOption) (*datatug.Entity, error) { return nil, cause },
			saveEntityFunc:   func(context.Context, *datatug.Entity) error { return cause },
			deleteEntityFunc: func(context.Context, string) error { return cause },
			loadRecordsetDefinitionFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.RecordsetDefinition, error) {
				return nil, cause
			},
			saveFolderFunc:   func(context.Context, string, *datatug.Folder) error { return cause },
			deleteFolderFunc: func(context.Context, string) error { return cause },
			dbServersStoreFunc: func(string) datatug.ProjDbServersStore {
				return mockDbServersStore{
					saveProjDbServerFunc:   func(context.Context, *datatug.ProjDbServer, ...datatug.StoreOption) error { return cause },
					deleteProjDbServerFunc: func(context.Context, string) error { return cause },
					loadProjDbServerFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
						return nil, cause
					},
				}
			},
		}
		failing(stores)
		for _, r := range []struct {
			name string
			want string
			err  error
		}{
			{"GetBoard", `board "b1" not found`, func() error { _, err := GetBoard(ctx, itemRef("b1")); return err }()},
			{"SaveBoard", `could not save board "b1"`, func() error { _, err := SaveBoard(ctx, plainProject(), plainBoard("b1")); return err }()},
			{"CreateBoard", `could not save board "b1"`, func() error { _, err := CreateBoard(ctx, plainProject(), plainBoard("b1")); return err }()},
			{"DeleteBoard", `could not delete board "b1"`, DeleteBoard(ctx, itemRef("b1"))},
			{"GetEntity", `entity "Customer" not found`, func() error { _, err := GetEntity(ctx, itemRef("Customer")); return err }()},
			{"SaveEntity", `could not save entity "Customer"`, SaveEntity(ctx, plainProject(), plainEntity("Customer", ""))},
			{"DeleteEntity", `could not delete entity "Customer"`, DeleteEntity(ctx, itemRef("Customer"))},
			{"GetDatasetDefinition", `recordset "sub/notes" not found`, func() error { _, err := GetDatasetDefinition(ctx, itemRef("sub/notes")); return err }()},
			{"CreateFolder", `could not create folder "a/b"`, func() error {
				_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: plainProject(), Path: "a", Name: "b"})
				return err
			}()},
			{"DeleteFolder", `could not delete folder "a/b"`, DeleteFolder(ctx, itemRef("a/b"))},
			{"AddDbServer", `could not save db server "sqlserver:localhost:1433"`, AddDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: plainServer()})},
			{"UpdateDbServer", `could not save db server "sqlserver:localhost:1433"`, UpdateDbServer(ctx, plainProject(), datatug.ProjDbServer{Server: plainServer()})},
			{"DeleteDbServer", `could not delete db server "sqlserver:localhost:1433"`, DeleteDbServer(ctx, plainProject(), plainServer())},
			{"GetDbServerSummary", `db server "sqlserver:localhost:1433" not found`, func() error { _, err := GetDbServerSummary(ctx, plainProject(), plainServer()); return err }()},
		} {
			if r.err == nil || r.err.Error() != r.want {
				t.Errorf("%s: store failure %q gave %v, want exactly %q", r.name, cause, r.err, r.want)
			}
			if r.err != nil && errors.Is(r.err, cause) {
				t.Errorf("%s: the answer wraps the store's own error, whose text says what the path was", r.name)
			}
		}
	}
}

// The database model a catalog names is read from the catalog's own file and joined into
// the path of the model's tables: a file that names anything but a plain name leads out of
// the project's models, and is refused before the folder is looked at. The answer names the
// catalog and the environment (plain names, or not shown) and nothing of the model.
func TestCatalogDbModel_RefusesAModelThatIsNotAPlainName(t *testing.T) {
	elsewhere := sourcecases.UnsafeIdentifier{Name: "a model that leads to another folder with a table in it", ID: "../../elsewhere"}
	for _, c := range append(sourcecases.UnsafeIdentifiers(), elsewhere) {
		if c.ID == "" {
			continue // an empty model is the "has no dbModel set" answer, tested with the other catalog errors
		}
		root := t.TempDir()
		dir := filepath.Join(root, "project")
		// Where the model above leads: a folder with a table in it.
		mustMkdir(t, filepath.Join(root, "elsewhere", "main", "tables", "other_table"))
		mustMkdir(t, filepath.Join(dir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "shop"))
		model, err := json.Marshal(map[string]string{"dbModel": c.ID})
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "shop", storage.JsonFileName("shop", storage.DbCatalogFileSuffix)), string(model))

		tables, tablesErr := GetCatalogTables(dir, "dev", "shop")
		schema, schemaErr := GetCatalogSchema(dir, "dev", "shop")

		if tablesErr == nil || schemaErr == nil || tables != nil || schema != nil {
			t.Errorf("%s: the model %q was used: tables %v (%v), schema %v (%v)", c.Name, c.ID, tables, tablesErr, schema, schemaErr)
			continue
		}
		for _, err := range []error{tablesErr, schemaErr} {
			if errors.Is(err, ErrCatalogNotFound) || !strings.Contains(err.Error(), `catalog "shop"`) {
				t.Errorf("%s: the answer is not the refusal of this catalog's model: %v", c.Name, err)
			}
			if len(c.ID) >= 3 && strings.Contains(err.Error(), c.ID) {
				t.Errorf("%s: the answer echoes the model: %v", c.Name, err)
			}
		}
	}
	// A model that is a plain name is read as it always was.
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "shop"))
	mustMkdir(t, filepath.Join(root, storage.DbModelsFolder, "retail-model", "main", "tables", "Customer"))
	mustWrite(t, filepath.Join(root, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "shop", storage.JsonFileName("shop", storage.DbCatalogFileSuffix)), `{"dbModel":"retail-model"}`)
	tables, err := GetCatalogTables(root, "dev", "shop")
	if err != nil || len(tables.Tables) != 1 || tables.Tables[0].Name != "Customer" {
		t.Fatalf("a plain model: tables %+v, err %v", tables, err)
	}
}

// The two places every other route gets a project store from (the routes of the contract
// and the legacy routes that take the project in a body) refuse a project that is neither
// served nor a plain name, before the factory is asked for a project store.
func TestProjectStoreChokePoints_RefuseAnUnsafeProject(t *testing.T) {
	asked := askedStores(t)
	for _, c := range sourcecases.UnsafeIdentifiers() {
		_, err := projectStoreForID("files", c.ID)
		assertRefusal(t, c.Name+" (projectStoreForID)", "project", c.ID, err)
		_, err = ProjectStoreFor(c.ID)
		assertRefusal(t, c.Name+" (ProjectStoreFor)", "project", c.ID, err)
	}
	if *asked != 0 {
		t.Fatalf("an unsafe project reached %d project stores, want 0", *asked)
	}
	// The counter is live: a plain project reaches the factory through both.
	if _, err := projectStoreForID("files", "p1"); err != nil {
		t.Fatalf("projectStoreForID with a plain project: %v", err)
	}
	if _, err := ProjectStoreFor("p1"); err != nil {
		t.Fatalf("ProjectStoreFor with a plain project: %v", err)
	}
	if *asked != 2 {
		t.Fatalf("plain projects reached %d project stores, want 2", *asked)
	}
}

// The legacy exec routes take the project in the request and open its store, so their
// own Validate refuses a project that is neither served nor a plain name.
func TestExecuteRequests_RefuseAnUnsafeProject(t *testing.T) {
	for _, c := range sourcecases.UnsafeIdentifiers() {
		if c.ID == "" {
			continue // the missing-field answer they always gave
		}
		selectRequest := SelectRequest{Project: c.ID, Environment: "local", Database: "shop", From: "Customer"}
		assertRefusal(t, c.Name+" (select)", "project", c.ID, selectRequest.Validate())
		commands := ExecuteCommandsRequest{Project: c.ID, Commands: []ExecuteCommandRequest{{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "shop"}}}
		assertRefusal(t, c.Name+" (execute_commands)", "project", c.ID, commands.Validate())
	}
	plainSelect := SelectRequest{Project: "p1", Environment: "local", Database: "shop", From: "Customer"}
	if err := plainSelect.Validate(); err != nil {
		t.Errorf("a plain select request is refused: %v", err)
	}
	plainCommands := ExecuteCommandsRequest{Project: "p1", Commands: []ExecuteCommandRequest{{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "shop"}}}
	if err := plainCommands.Validate(); err != nil {
		t.Errorf("a plain execute_commands request is refused: %v", err)
	}
}
