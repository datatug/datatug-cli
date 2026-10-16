package detective

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

type nativeSQLiteProcessConfig struct {
	ProjectDir, PrivateBase, SessionRoot string
	ExpectedVersion                      int64
}

func nativeSQLiteFixture(t *testing.T) (*LocalStore, *sessioncontext.Service, sessioncontext.Principal, *sessioncontext.DetectiveOwner, sessioncontext.DetectiveFence, sessioncontext.DetectiveInputPin, nativeSQLiteProcessConfig) {
	t.Helper()
	s, _, _ := nativeCaptureFixture(t, true)
	privateBase := filepath.Dir(filepath.Dir(s.privateDir))
	config := nativeSQLiteProcessConfig{ProjectDir: s.projectDir, PrivateBase: privateBase, SessionRoot: filepath.Join(privateBase, "sessions")}
	manager, err := sessionstore.New(config.SessionRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	repo, err := manager.NativeRepository("p", s.projectDir, privateBase)
	if err != nil {
		t.Fatal(err)
	}
	service, err := sessioncontext.NewNativePinService(repo, func(context.Context, sessioncontext.Access) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p := sessioncontext.Principal{Actor: "alice", DatabaseID: repo.DatabaseID(), ProjectID: "p", Executor: true}
	scope := sessioncontext.Scope{EnvironmentID: "qa", IdentityDomain: "tenant"}
	created, err := service.Execute(context.Background(), p, sessioncontext.Command{OpID: "create", Action: "createSession", SessionID: "session", BagID: "bag", DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, Title: "Private", Scope: &scope})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.GetSnapshot(context.Background(), p, "session")
	if err != nil {
		t.Fatal(err)
	}
	n := repo.NativeNamespace()
	ref := apicontract.SessionInputReference{SchemaVersion: 1, StorageID: n.StorageID, DatabaseID: n.DatabaseID, ProjectID: n.ProjectID, NamespaceID: n.NamespaceID, SessionID: "session", InputSnapshotID: "input", ContextSetID: snapshot.ContextSet.ID, EnvironmentID: scope.EnvironmentID, IdentityDomain: scope.IdentityDomain}
	capture, err := s.CaptureNativeInput(context.Background(), NativeCaptureRequest{CheckID: "check", CaseID: "case", NativeOperationID: "native", Actor: p.Actor, Reference: ref}, func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: "q", Revision: "old", DefinitionJSON: json.RawMessage(`{"id":"q","title":"Query","type":"SQL","text":"select old"}`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := service.PinDetectiveInput(context.Background(), p, sessioncontext.PinDetectiveInputCommand{ClientRequestDigest: strings.Repeat("a", 64), OpID: "pin", SessionID: "session", RunID: "session-run", InputSnapshotID: "input", ExpectedVersion: created.Receipt.SessionVersion, ExpectedContextSetID: snapshot.ContextSet.ID, Scope: scope, Pin: capture.Pin, Case: capture.Case, NativeBinding: capture.Binding})
	if err != nil {
		t.Fatal(err)
	}
	config.ExpectedVersion = pinned.Receipt.SessionVersion
	owner, err := sessioncontext.NewDetectiveOwner(service, func(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) error { return nil }, s.NativeObservation)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.ResolveOperation(context.Background(), p, "pin")
	if err != nil {
		t.Fatal(err)
	}
	fence, pin, first, err := owner.ClaimDispatchOnce(context.Background(), p, accepted.Identity)
	if err != nil || !first {
		t.Fatal("first claim", first, err)
	}
	if err = s.ConfigureNativeFence(func(ctx context.Context, token sessioncontext.DetectiveFence) error {
		return owner.CheckFence(ctx, p, token)
	}); err != nil {
		t.Fatal(err)
	}
	return s, service, p, owner, fence, pin, config
}

func nativeSQLiteChild(t *testing.T, config nativeSQLiteProcessConfig) (*exec.Cmd, *bufio.Scanner) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestNativeSQLiteStopChild$")
	cmd.Env = append(os.Environ(), "DATATUG_NATIVE_SQLITE_CHILD="+string(encoded))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child did not reach session write admission")
	}
	return cmd, scanner
}

func TestNativeSQLiteCrossProcessStopFencesKnownResult(t *testing.T) {
	s, _, _, _, fence, pin, config := nativeSQLiteFixture(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		close(started)
		<-release
		return apicontract.Result{}, nil
	}
	go func() { _, err := s.ExecuteNativePin(context.Background(), fence, pin, true, runner, nil); done <- err }()
	<-started
	cmd, scanner := nativeSQLiteChild(t, config)
	if !scanner.Scan() || scanner.Text() != "stopped" {
		t.Fatal("cross-process Stop did not persist while source was running")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, sessioncontext.ErrReadOnly) {
		t.Fatal("Stop lost finalization race", err)
	}
	observed, err := s.NativeObservation(context.Background(), fence.Identity)
	if err != nil || !observed.Produced || observed.Done {
		t.Fatal("known result lost", observed, err)
	}
	if _, err = s.ExecuteNativePin(context.Background(), fence, pin, false, runner, nil); !errors.Is(err, sessioncontext.ErrReadOnly) {
		t.Fatal("fenced replay", err)
	}
	if calls != 1 {
		t.Fatal("source replayed")
	}
	item, err := s.GetCase(context.Background(), "case")
	if err != nil || len(item.RunRefs) != 0 {
		t.Fatal("Case mutated after Stop", item, err)
	}
}

func TestNativeSQLiteFinalizationAdmissionPrecedesCrossProcessStop(t *testing.T) {
	s, _, p, owner, fence, pin, config := nativeSQLiteFixture(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var blocked atomic.Bool
	if err := s.ConfigureNativeFence(func(ctx context.Context, token sessioncontext.DetectiveFence) error {
		data, readErr := safeReadFile(s.privateItemDir("action-operations"), "native")
		if readErr == nil {
			var journal actionOperation
			if json.Unmarshal(data, &journal) != nil {
				return ErrConflict
			}
			if journal.Produced && blocked.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		return owner.CheckFence(ctx, p, token)
	}); err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	go func() { _, err := s.ExecuteNativePin(context.Background(), fence, pin, true, runner, nil); done <- err }()
	<-entered
	cmd, scanner := nativeSQLiteChild(t, config)
	childDone := make(chan bool, 1)
	go func() { childDone <- scanner.Scan() && scanner.Text() == "stopped" }()
	select {
	case <-childDone:
		t.Fatal("Stop bypassed held native finalization admission")
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal("admitted native finalization failed", err)
	}
	if !<-childDone {
		t.Fatal("Stop did not complete after native finalization released admission")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	observed, err := s.NativeObservation(context.Background(), fence.Identity)
	if err != nil || !observed.Done || observed.CaseLinkStatus != "linked" {
		t.Fatal("native evidence not committed before Stop", observed, err)
	}
	if _, err = safeReadFile(s.privateItemDir("case-run-links"), nativeCaseRunLinkID("case", observed.NativeRunID)); err != nil {
		t.Fatal("committed direct evidence missing", err)
	}
}

func TestNativeSQLiteStopChild(t *testing.T) {
	encoded := os.Getenv("DATATUG_NATIVE_SQLITE_CHILD")
	if encoded == "" {
		t.Skip("child only")
	}
	var config nativeSQLiteProcessConfig
	if json.Unmarshal([]byte(encoded), &config) != nil {
		t.Fatal("invalid child config")
	}
	manager, err := sessionstore.New(config.SessionRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	}()
	repo, err := manager.NativeRepository("p", config.ProjectDir, config.PrivateBase)
	if err != nil {
		t.Fatal(err)
	}
	service, err := sessioncontext.NewNativePinService(repo, func(context.Context, sessioncontext.Access) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p := sessioncontext.Principal{Actor: "alice", DatabaseID: repo.DatabaseID(), ProjectID: "p", Executor: true}
	fmt.Println("ready")
	if _, err = service.Execute(context.Background(), p, sessioncontext.Command{OpID: "stop", Action: "transitionSession", Transition: "stop", SessionID: "session", ExpectedVersion: config.ExpectedVersion}); err != nil {
		t.Fatal(err)
	}
	fmt.Println("stopped")
}
