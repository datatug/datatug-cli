package endpoints

import (
	"context"
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"testing"
)

type nativeSelectionReader struct {
	snapshot                 sessioncontext.Snapshot
	revision                 sessioncontext.EntryRevision
	snapshotErr, revisionErr error
}

func (r nativeSelectionReader) GetSnapshot(context.Context, sessioncontext.Principal, string) (sessioncontext.Snapshot, error) {
	return r.snapshot, r.snapshotErr
}
func (r nativeSelectionReader) ReadEntryRevision(context.Context, sessioncontext.Principal, string, string, string) (sessioncontext.EntryRevision, error) {
	return r.revision, r.revisionErr
}

type nativeSelectionOwner struct {
	capture func(detective.NativeCaptureRequest) (detective.NativeCapture, error)
}

func (o nativeSelectionOwner) CaptureNativeInput(_ context.Context, r detective.NativeCaptureRequest, _ detective.NativeQuerySnapshot) (detective.NativeCapture, error) {
	return o.capture(r)
}
func TestNativeSelectedRevisionConstructionAndInvalidStorage(t *testing.T) {
	oldEncode := sessionDetectiveEncode
	t.Cleanup(func() { sessionDetectiveEncode = oldEncode })
	scope := sessioncontext.Scope{EnvironmentID: "qa", IdentityDomain: "tenant"}
	for _, kind := range []string{"encode-failure", "scalar", "set", "applied-version", "snapshot-failure", "revision-failure", "foreign-bag", "foreign-scope", "empty-set", "keys-set", "mixed-set", "unsupported-kind", "invalid-scalar", "duplicate", "capture-failure"} {
		t.Run(kind, func(t *testing.T) {
			sessionDetectiveEncode = oldEncode
			scalar := apicontract.NewIntegerValue("9007199254740993")
			reader := nativeSelectionReader{snapshot: sessioncontext.Snapshot{Session: sessioncontext.Session{Version: 2}, ContextSet: sessioncontext.AppliedContextSet{ID: "context"}, Bags: []sessioncontext.Bag{{ID: "bag", Scope: scope, Version: 7}}}, revision: sessioncontext.EntryRevision{BagID: "bag", Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &scalar}}}
			req := api.SessionDetectiveRequest{OperationID: "operation", SessionID: "session", ExpectedVersion: 2, ExpectedContextSetID: "context", Scope: scope, Bindings: []api.SessionDetectiveInputSelection{{InputName: "id", EntryID: "entry", RevisionID: "revision"}}}
			sentinel := errors.New("storage unavailable")
			wantErr := true
			bagVersion := int64(7)
			switch kind {
			case "scalar":
				wantErr = false
			case "set":
				reader.revision.Value = sessioncontext.ValueEnvelope{Kind: "set", Values: []apicontract.TypedValue{scalar}}
				wantErr = false
			case "applied-version":
				reader.snapshot.ContextSet.Slots = []sessioncontext.AppliedSlot{{Scope: scope, BagID: "bag", BagVersion: 3, Entries: []sessioncontext.EntryRef{{EntryID: "entry", RevisionID: "revision"}}}}
				bagVersion = 3
				wantErr = false
			case "encode-failure":
				sessionDetectiveEncode = func(any) ([]byte, error) { return nil, sentinel }
			case "snapshot-failure":
				reader.snapshotErr = sentinel
			case "revision-failure":
				reader.revisionErr = sentinel
			case "foreign-bag":
				reader.revision.BagID = "foreign"
			case "foreign-scope":
				reader.snapshot.Bags[0].Scope.IdentityDomain = "foreign"
			case "empty-set":
				reader.revision.Value = sessioncontext.ValueEnvelope{Kind: "set"}
			case "keys-set":
				reader.revision.Value = sessioncontext.ValueEnvelope{Kind: "set", Keys: []sessioncontext.EntityKey{{}}}
			case "mixed-set":
				reader.revision.Value = sessioncontext.ValueEnvelope{Kind: "set", Values: []apicontract.TypedValue{scalar, apicontract.NewStringValue("different")}}
			case "unsupported-kind":
				reader.revision.Value.Kind = "unknown"
			case "invalid-scalar":
				reader.revision.Value.Scalar = nil
			case "duplicate":
				req.Bindings = append(req.Bindings, req.Bindings[0])
			}
			captures := 0
			owner := nativeSelectionOwner{capture: func(c detective.NativeCaptureRequest) (detective.NativeCapture, error) {
				captures++
				if kind == "capture-failure" {
					return detective.NativeCapture{}, sentinel
				}
				if c.Reference.Bindings[0].Entries[0].BagVersion != bagVersion || c.Reference.Bindings[0].InputName != "id" || c.Inputs["id"].Validate() != nil {
					t.Fatal("server provenance/input lost", c)
				}
				return detective.NativeCapture{}, nil
			}}
			_, e := prepareSessionDetectivePin(context.Background(), reader, sessioncontext.Principal{Actor: "alice"}, owner, sessioncontext.NativeNamespace{}, req, "digest")
			if wantErr != (e != nil) {
				t.Fatal(kind, e)
			}
			if wantErr && kind != "capture-failure" && captures != 0 {
				t.Fatal("invalid storage reached capture")
			}
		})
	}
}
