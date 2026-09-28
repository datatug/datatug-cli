package gcloudui

import (
	"context"
	"errors"
	"net"
	"testing"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type mockFirestoreServer struct {
	firestorepb.UnimplementedFirestoreServer
	runQueryFunc func(*firestorepb.RunQueryRequest, firestorepb.Firestore_RunQueryServer) error
}

func (m *mockFirestoreServer) RunQuery(req *firestorepb.RunQueryRequest, srv firestorepb.Firestore_RunQueryServer) error {
	if m.runQueryFunc != nil {
		return m.runQueryFunc(req, srv)
	}
	return nil
}

func startMockFirestore(t *testing.T) (*firestore.Client, *mockFirestoreServer) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	mock := &mockFirestoreServer{}
	firestorepb.RegisterFirestoreServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	client, err := firestore.NewClient(context.Background(), "test-proj",
		option.WithEndpoint(lis.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, mock
}

func TestOpenGCloudProjectsScreen_Success(t *testing.T) {
	origTUI := newDatatugTUIFunc
	defer func() { newDatatugTUIFunc = origTUI }()

	tui, _ := newTestTUI(t)
	newDatatugTUIFunc = func() *sneatnav.TUI {
		return tui
	}

	projects := []*cloudresourcemanager.Project{
		{ProjectId: "p1", DisplayName: "Project 1", Name: "projects/1234567890"},
	}
	err := OpenGCloudProjectsScreen(projects)
	if err != nil {
		t.Fatalf("OpenGCloudProjectsScreen failed: %v", err)
	}
}

func TestShowGCloudProjects_Interaction(t *testing.T) {
	origSchedule := scheduleUpdate
	defer func() { scheduleUpdate = origSchedule }()
	scheduleUpdate = func(_ *tview.Application, f func()) { f() }

	origRect := tableGetInnerRectFunc
	defer func() { tableGetInnerRectFunc = origRect }()
	tableGetInnerRectFunc = func(_ *tview.Table) (int, int, int, int) {
		return 0, 0, 80, 20
	}

	gctx, _ := newTestGCloudContext(t)
	gctx.projects = []*cloudresourcemanager.Project{
		{ProjectId: "p1", DisplayName: "Project 1", Name: "projects/1234567890"},
		{ProjectId: "p2", DisplayName: "Project 2", Name: "projects/9876543210"},
	}

	if err := GoGCloudProjects(gctx, sneatnav.FocusToContent); err != nil {
		t.Fatalf("GoGCloudProjects failed: %v", err)
	}

	table := lastProjectsTable
	if table == nil {
		t.Fatal("lastProjectsTable is nil")
	}

	// Test table input capture
	ic := table.GetInputCapture()
	if ic == nil {
		t.Fatal("table input capture is nil")
	}
	if ev := ic(tcell.NewEventKey(tcell.KeyLeft, ' ', tcell.ModNone)); ev != nil {
		t.Fatalf("KeyLeft should return nil, got %v", ev)
	}
	if ev := ic(tcell.NewEventKey(tcell.KeyEscape, ' ', tcell.ModNone)); ev != nil {
		t.Fatalf("KeyEscape should return nil, got %v", ev)
	}
	keyOther := tcell.NewEventKey(tcell.KeyEnter, ' ', tcell.ModNone)
	if ev := ic(keyOther); ev != keyOther {
		t.Fatalf("KeyEnter should be returned unchanged, got %v", ev)
	}

	// Test flex focus func
	if lastProjectsFlexFocusFunc != nil {
		lastProjectsFlexFocusFunc()
	}

	// Test selection changed func (updateScrollbar)
	if lastProjectsTableSelectionChangedFunc != nil {
		table.Select(1, 0)
		lastProjectsTableSelectionChangedFunc(1, 0)
		table.Select(2, 0)
		lastProjectsTableSelectionChangedFunc(2, 0)
		// Selection > total rows
		table.Select(999, 0)
		lastProjectsTableSelectionChangedFunc(999, 0)

		tableGetInnerRectFunc = func(_ *tview.Table) (int, int, int, int) {
			return 0, 0, 80, -5 // test negative height
		}
		lastProjectsTableSelectionChangedFunc(1, 0)
	}

	// Test table selected func
	if lastProjectsTableSelectedFunc == nil {
		t.Fatal("lastProjectsTableSelectedFunc is nil")
	}
	// row 0: header
	lastProjectsTableSelectedFunc(0, 0)
	// cell nil: row beyond range
	lastProjectsTableSelectedFunc(999, 0)

	// cell with nil reference
	table.SetCell(2, 0, tview.NewTableCell("no ref"))
	// Empty table scrollbar update
	table.Clear()
	lastProjectsTableSelectionChangedFunc(0, 0)

	// test panic from goGCloudProjectFunc
	origGoProj := goGCloudProjectFunc
	defer func() { goGCloudProjectFunc = origGoProj }()
	goGCloudProjectFunc = func(_ *CGProjectContext) error {
		return errors.New("boom")
	}
	func() {
		table.SetCell(1, 0, tview.NewTableCell("p1").SetReference(NewProjectContext(gctx, gctx.projects[0])))
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic from project selected")
			}
		}()
		lastProjectsTableSelectedFunc(1, 0)
	}()

	// test panic with unexpected reference type
	table.SetCell(3, 0, tview.NewTableCell("bad").SetReference("unexpected-string"))
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unexpected reference type")
		}
	}()
	lastProjectsTableSelectedFunc(3, 0)
}

func TestFirestoreCollection_AllBranchesWithMockServer(t *testing.T) {
	client, mock := startMockFirestore(t)

	origClient := newFirestoreClientFunc
	defer func() { newFirestoreClientFunc = origClient }()
	newFirestoreClientFunc = func(_ context.Context, _ string) (*firestore.Client, error) {
		return client, nil
	}

	// 1. Success with documents returned from server
	mock.runQueryFunc = func(req *firestorepb.RunQueryRequest, srv firestorepb.Firestore_RunQueryServer) error {
		return srv.Send(&firestorepb.RunQueryResponse{
			Document: &firestorepb.Document{
				Name: "projects/test-proj/databases/(default)/documents/users/doc1",
				Fields: map[string]*firestorepb.Value{
					"colA": {ValueType: &firestorepb.Value_StringValue{StringValue: "valA"}},
				},
				CreateTime: timestamppb.Now(),
				UpdateTime: timestamppb.Now(),
			},
			ReadTime: timestamppb.Now(),
		})
	}

	pgctx, _ := newTestCGProjectContext(t)
	col := &schemers.Collection{ID: "users"}

	wait := withSyncSchedule(t)
	if err := goFirestoreCollection(pgctx, col, sneatnav.FocusToContent); err != nil {
		t.Fatalf("goFirestoreCollection failed: %v", err)
	}
	wait()

	table := lastFirestoreCollectionTable
	if table == nil {
		t.Fatal("lastFirestoreCollectionTable is nil")
	}
	ic := table.GetInputCapture()
	if ic == nil {
		t.Fatal("input capture is nil")
	}

	// KeyUp at row 1
	table.Select(1, 0)
	if ev := ic(tcell.NewEventKey(tcell.KeyUp, ' ', tcell.ModNone)); ev != nil {
		t.Fatalf("KeyUp at row 1 should return nil, got %v", ev)
	}
	// KeyUp at row 2
	table.Select(2, 0)
	keyUp := tcell.NewEventKey(tcell.KeyUp, ' ', tcell.ModNone)
	if ev := ic(keyUp); ev != keyUp {
		t.Fatalf("KeyUp at row 2 should return event, got %v", ev)
	}

	// KeyLeft at col 0
	table.Select(1, 0)
	if ev := ic(tcell.NewEventKey(tcell.KeyLeft, ' ', tcell.ModNone)); ev != nil {
		t.Fatalf("KeyLeft at col 0 should return nil, got %v", ev)
	}
	// KeyLeft at col 1
	table.Select(1, 1)
	keyLeft := tcell.NewEventKey(tcell.KeyLeft, ' ', tcell.ModNone)
	if ev := ic(keyLeft); ev != keyLeft {
		t.Fatalf("KeyLeft at col 1 should return event, got %v", ev)
	}
	// Default event
	keyEnter := tcell.NewEventKey(tcell.KeyEnter, ' ', tcell.ModNone)
	if ev := ic(keyEnter); ev != keyEnter {
		t.Fatalf("KeyEnter should return event, got %v", ev)
	}

	// 2. Success with 0 documents returned (immediate EOF)
	client2, mock2 := startMockFirestore(t)
	newFirestoreClientFunc = func(_ context.Context, _ string) (*firestore.Client, error) {
		return client2, nil
	}
	mock2.runQueryFunc = func(req *firestorepb.RunQueryRequest, srv firestorepb.Firestore_RunQueryServer) error {
		return nil
	}
	wait2 := withSyncSchedule(t)
	if err := goFirestoreCollection(pgctx, col, sneatnav.FocusToContent); err != nil {
		t.Fatalf("goFirestoreCollection empty failed: %v", err)
	}
	wait2()

	// 3. Error loading documents
	client3, mock3 := startMockFirestore(t)
	newFirestoreClientFunc = func(_ context.Context, _ string) (*firestore.Client, error) {
		return client3, nil
	}
	mock3.runQueryFunc = func(req *firestorepb.RunQueryRequest, srv firestorepb.Firestore_RunQueryServer) error {
		return status.Error(codes.Internal, "simulated firestore internal error")
	}
	wait3 := withSyncSchedule(t)
	if err := goFirestoreCollection(pgctx, col, sneatnav.FocusToContent); err != nil {
		t.Fatalf("goFirestoreCollection error failed: %v", err)
	}
	wait3()

	// Also test closeFirestoreClient(nil) and closeFirestoreClient(client)
	if err := closeFirestoreClient(nil); err != nil {
		t.Fatalf("closeFirestoreClient(nil) returned error: %v", err)
	}
}

func TestFirestoreCollections_ActionCallback(t *testing.T) {
	client, _ := startMockFirestore(t)

	origClient := newFirestoreClientFunc
	defer func() { newFirestoreClientFunc = origClient }()
	newFirestoreClientFunc = func(_ context.Context, _ string) (*firestore.Client, error) {
		return client, nil
	}

	pgctx, _ := newTestCGProjectContext(t)
	pgctx.schema = &fakeSchemaProvider{
		collections: []*schemers.Collection{{ID: "customers"}},
	}

	wait := withSyncSchedule(t)
	if err := goFirestoreCollections(pgctx); err != nil {
		t.Fatalf("goFirestoreCollections failed: %v", err)
	}
	wait()

	list := lastFirestoreCollectionsList
	if list == nil {
		t.Fatal("lastFirestoreCollectionsList is nil")
	}
	action := list.GetItemSelectedFunc(0)
	if action == nil {
		t.Fatal("action for collection item is nil")
	}
	action()

	// Test panic from action callback
	origGoCol := goFirestoreCollectionFunc
	defer func() { goFirestoreCollectionFunc = origGoCol }()
	goFirestoreCollectionFunc = func(_ *CGProjectContext, _ *schemers.Collection, _ sneatnav.FocusTo) error {
		return errors.New("boom")
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic from collection item action")
			}
		}()
		action()
	}()
}

func TestNewFirestoreClient_Branches(t *testing.T) {
	// Empty project ID
	if _, err := newFirestoreClient(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty project ID")
	}

	origRT := getRefreshTokenFunc
	defer func() { getRefreshTokenFunc = origRT }()

	origNew := firestoreNewClient
	defer func() { firestoreNewClient = origNew }()

	// Valid refresh token, client succeeds
	getRefreshTokenFunc = func() (string, error) {
		return "sample-refresh-token", nil
	}
	firestoreNewClient = func(_ context.Context, _ string, _ ...option.ClientOption) (*firestore.Client, error) {
		return &firestore.Client{}, nil
	}
	client, err := newFirestoreClient(context.Background(), "my-proj")
	if err != nil || client == nil {
		t.Fatalf("expected client success, got client=%v, err=%v", client, err)
	}

	// Valid refresh token, token client fails, ADC fallback succeeds
	callCount := 0
	firestoreNewClient = func(_ context.Context, _ string, opts ...option.ClientOption) (*firestore.Client, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("invalid grant")
		}
		return &firestore.Client{}, nil
	}
	client, err = newFirestoreClient(context.Background(), "my-proj")
	if err != nil || client == nil {
		t.Fatalf("expected ADC fallback success, got client=%v, err=%v", client, err)
	}
}

func TestFirestoreMainMenu_Actions(t *testing.T) {
	pgctx, _ := newTestCGProjectContext(t)
	_ = firestoreMainMenu(pgctx, firestoreScreenCollections, "Title")

	list := lastFirestoreMainMenu
	if list == nil {
		t.Fatal("lastFirestoreMainMenu is nil")
	}

	// Item 0: Collections
	if fn := list.GetItemSelectedFunc(0); fn != nil {
		fn()
	}
	// Item 1: Indexes
	if fn := list.GetItemSelectedFunc(1); fn != nil {
		fn()
	}
}

func TestFirestoreIndexes_KeyUpEvent(t *testing.T) {
	pgctx, _ := newTestCGProjectContext(t)
	if err := goFirestoreIndexes(pgctx); err != nil {
		t.Fatalf("goFirestoreIndexes failed: %v", err)
	}

	list := lastFirestoreIndexesList
	if list == nil {
		t.Fatal("lastFirestoreIndexesList is nil")
	}
	list.AddItem("Extra Item", "", 0, nil)
	list.SetCurrentItem(1)

	ic := list.GetInputCapture()
	keyUp := tcell.NewEventKey(tcell.KeyUp, ' ', tcell.ModNone)
	if ev := ic(keyUp); ev != keyUp {
		t.Fatalf("expected KeyUp to return event when current item != 0, got %v", ev)
	}
}

func TestRegisterAsViewer_And_Action(t *testing.T) {
	RegisterAsViewer()
	if lastRegisteredViewer.ID != viewerID {
		t.Fatalf("viewerID = %v, want %v", lastRegisteredViewer.ID, viewerID)
	}
	tui, _ := newTestTUI(t)
	if err := lastRegisteredViewer.Action(tui, sneatnav.FocusToContent); err != nil {
		t.Fatalf("viewer Action failed: %v", err)
	}
}

func TestMainMenu_Credentials_And_Changed(t *testing.T) {
	gctx, _ := newTestGCloudContext(t)
	_ = newMainMenu(gctx, ScreenProjects, false)

	list := lastMainMenuList
	if list == nil {
		t.Fatal("lastMainMenuList is nil")
	}

	// Credentials action
	if fn := list.GetItemSelectedFunc(1); fn != nil {
		fn()
	}

	// Changed func
	if lastMainMenuChangedFunc != nil {
		lastMainMenuChangedFunc(0, "Projects", "", 'p')
		lastMainMenuChangedFunc(1, "Credentials", "", 'c')
		lastMainMenuChangedFunc(99, "Other", "", 'o')
	}
}

func TestProjectUI_Actions_And_KeyUp(t *testing.T) {
	pgctx, _ := newTestCGProjectContext(t)
	_ = newGCloudProjectMenu(pgctx)

	list := lastGCloudProjectMenu
	if list == nil {
		t.Fatal("lastGCloudProjectMenu is nil")
	}

	// Item 0: Firestore Database
	if fn := list.GetItemSelectedFunc(0); fn != nil {
		fn()
	}
	// Item 1: Firebase Users (noop)
	if fn := list.GetItemSelectedFunc(1); fn != nil {
		fn()
	}

	// KeyUp at index 1
	list.SetCurrentItem(1)
	ic := list.GetInputCapture()
	keyUp := tcell.NewEventKey(tcell.KeyUp, ' ', tcell.ModNone)
	if ev := ic(keyUp); ev != keyUp {
		t.Fatalf("expected KeyUp to return event at index 1, got %v", ev)
	}
}

func TestCredentialsUI_Actions_And_Input(t *testing.T) {
	gctx, _ := newTestGCloudContext(t)
	if err := GoCredentials(gctx, sneatnav.FocusToContent); err != nil {
		t.Fatalf("GoCredentials failed: %v", err)
	}

	list := lastCredentialsList
	if list == nil {
		t.Fatal("lastCredentialsList is nil")
	}

	// Test Login action
	if fn := list.GetItemSelectedFunc(0); fn != nil {
		fn()
	}
	// Test Logout action
	if fn := list.GetItemSelectedFunc(1); fn != nil {
		fn()
	}

	// Test input capture
	ic := list.GetInputCapture()
	if ic == nil {
		t.Fatal("input capture is nil")
	}
	if ev := ic(tcell.NewEventKey(tcell.KeyLeft, ' ', tcell.ModNone)); ev != nil {
		t.Fatalf("KeyLeft should return nil, got %v", ev)
	}
	if ev := ic(tcell.NewEventKey(tcell.KeyEscape, ' ', tcell.ModNone)); ev != nil {
		t.Fatalf("KeyEscape should return nil, got %v", ev)
	}
	keyOther := tcell.NewEventKey(tcell.KeyEnter, ' ', tcell.ModNone)
	if ev := ic(keyOther); ev != keyOther {
		t.Fatalf("KeyEnter should return keyOther, got %v", ev)
	}
}

func TestDefaultSeams_Coverage(t *testing.T) {
	// Call default startInteractiveLoginFunc with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = startInteractiveLoginFunc(ctx, nil)

	// Call default deleteRefreshTokenFunc
	_ = deleteRefreshTokenFunc()
}
