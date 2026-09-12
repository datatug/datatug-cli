package incidentstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func TestRepositoryStoreRejectsInvalidRequests(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Events(ctx, incidents.IncidentRef{}, 0)
	require.Error(t, err)
	_, err = store.Projection(ctx, incidents.IncidentRef{}, nil)
	require.Error(t, err)

	other := incidents.IncidentRef{StoreID: "other", IncidentID: "INC-1"}
	_, err = store.Events(ctx, other, 0)
	require.ErrorContains(t, err, `cannot read "other"`)

	_, err = store.Append(ctx, incidents.Mutation{})
	require.Error(t, err)
	wrongStore := createdMutation(t, "wrong-store", "INC-1")
	wrongStore.Incident.StoreID = "other"
	_, err = store.Append(ctx, wrongStore)
	require.ErrorContains(t, err, `cannot write "other"`)

	_, err = store.Merge(ctx, incidents.MergeMutation{})
	require.Error(t, err)
	_, err = store.Merge(ctx, incidents.MergeMutation{
		MutationID: "wrong-store-merge",
		Source:     incidents.IncidentRef{StoreID: "other", IncidentID: "INC-1"},
		Into:       incidents.IncidentRef{StoreID: "other", IncidentID: "INC-2"},
	})
	require.ErrorContains(t, err, `cannot merge "other"`)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.Append(canceled, createdMutation(t, "canceled", "INC-1"))
	require.ErrorIs(t, err, context.Canceled)
}

func TestRepositoryStoreRejectsInvalidRootsAndPaths(t *testing.T) {
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	_, err := OpenLocation(incidents.StoreLocation{}, RepositoryRoots{})
	require.Error(t, err)
	_, err = NewRepositoryStore(incidents.StoreLocation{}, t.TempDir())
	require.Error(t, err)
	_, err = NewRepositoryStore(location, filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err = NewRepositoryStore(location, file)
	require.ErrorContains(t, err, "must be a real directory")

	store := newTestStore(t)
	_, err = store.receiptPath("../bad")
	require.Error(t, err)
	_, err = store.mergeIntentPath("../bad")
	require.Error(t, err)
	_, err = store.relative(filepath.Join(store.root, "..", "outside"))
	require.ErrorContains(t, err, "escapes root")
	_, err = store.capabilityPath(store.root)
	require.ErrorContains(t, err, "escapes incidents scope")
	_, err = store.capabilityPath(filepath.Join(store.root, "other", "file.json"))
	require.ErrorContains(t, err, "escapes incidents scope")
	require.Error(t, store.writeJSONAtomic(filepath.Join(store.root, "outside.json"), struct{}{}, 0o600))
	_, err = store.readFile(filepath.Join(store.root, "outside.json"))
	require.Error(t, err)
	_, err = store.readDir(filepath.Join("..", "outside"))
	require.Error(t, err)

	_, err = OpenLocation(location, RepositoryRoots{})
	require.ErrorContains(t, err, "repository root is not configured")

	blockedMetadataRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(blockedMetadataRoot, "incidents"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(blockedMetadataRoot, "incidents", ".store"), nil, 0o600))
	_, err = NewRepositoryStore(location, blockedMetadataRoot)
	require.ErrorContains(t, err, "prepare private incident metadata")

	missingManifestRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(missingManifestRoot, ".ingitdb", "access"), 0o700))
	_, err = NewRepositoryStore(location, missingManifestRoot)
	require.ErrorContains(t, err, "open incident DALgo store")

	securedRoot := t.TempDir()
	accessDir := filepath.Join(securedRoot, ".ingitdb", "access")
	require.NoError(t, os.MkdirAll(accessDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(accessDir, "manifest.yaml"), []byte("enabled: true\ndatabase: incidents-test\npolicies: [deny.yaml]\n"), 0o600))
	policy := "apiVersion: dtql.org/access/v1\nkind: AccessPolicy\nmetadata: {name: deny-all}\ntarget: {database: incidents-test}\ncomposition: dalgo-hierarchical-v1\ndefault: deny\nscopes: []\n"
	require.NoError(t, os.WriteFile(filepath.Join(accessDir, "deny.yaml"), []byte(policy), 0o600))
	_, err = NewRepositoryStore(location, securedRoot)
	require.ErrorContains(t, err, "open incident file capability")
}

func TestRepositoryStoreReportsAbsolutePathFailure(t *testing.T) {
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	testErr := errors.New("absolute path failed")
	_, err := newRepositoryStore(location, ".", func(string) (string, error) { return "", testErr })
	require.ErrorContains(t, err, "incident store root")
	require.ErrorIs(t, err, testErr)
}

func TestRepositoryStoreRejectsCorruptMetadata(t *testing.T) {
	store := newTestStore(t)
	receiptPath, err := store.receiptPath("corrupt")
	require.NoError(t, err)
	intentPath, err := store.mergeIntentPath("corrupt")
	require.NoError(t, err)

	t.Run("append receipt", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{name: "malformed", raw: "{"},
			{name: "wrong kind", raw: `{"kind":"merge"}`},
			{name: "unknown field", raw: `{"kind":"append","unknown":true}`},
			{name: "invalid envelope", raw: `{"kind":"append"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				writeStoreRaw(t, receiptPath, tc.raw)
				_, found, readErr := store.readReceipt(receiptPath)
				require.False(t, found)
				require.Error(t, readErr)
			})
		}
	})

	t.Run("receipt kind", func(t *testing.T) {
		writeStoreRaw(t, receiptPath, "{")
		_, err := store.readReceiptKind(receiptPath)
		require.Error(t, err)
		writeStoreRaw(t, receiptPath, `{"kind":"unknown"}`)
		_, err = store.readReceiptKind(receiptPath)
		require.ErrorContains(t, err, "invalid mutation receipt kind")
	})

	t.Run("merge receipt", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{name: "malformed", raw: "{"},
			{name: "wrong kind", raw: `{"kind":"append"}`},
			{name: "unknown field", raw: `{"kind":"merge","unknown":true}`},
			{name: "invalid envelope", raw: `{"kind":"merge"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				writeStoreRaw(t, receiptPath, tc.raw)
				_, found, readErr := store.readMergeReceipt(receiptPath)
				require.False(t, found)
				require.Error(t, readErr)
			})
		}
	})

	t.Run("merge intent", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{name: "malformed", raw: "{"},
			{name: "invalid envelope", raw: `{"kind":"merge"}`},
			{name: "invalid mutation", raw: `{"kind":"merge","requestHash":"0000000000000000000000000000000000000000000000000000000000000000"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				writeStoreRaw(t, intentPath, tc.raw)
				_, found, readErr := store.readMergeIntent(intentPath)
				require.False(t, found)
				require.Error(t, readErr)
			})
		}
	})
}

func TestDecodeStrictAndMustMarshalFailures(t *testing.T) {
	var value map[string]any
	require.Error(t, decodeStrict([]byte("{"), &value))
	require.ErrorContains(t, decodeStrict([]byte(`{} {}`), &value), "trailing JSON data")
	require.Panics(t, func() { mustMarshal(func() {}) })
}

func writeStoreRaw(t *testing.T, path, raw string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
}

func writeStoreJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	writeStoreRaw(t, path, string(b))
}

func TestRepositoryStoreReadHelpersPropagateErrors(t *testing.T) {
	store := newTestStore(t)
	store.ops = errorRootedFileOps{err: errors.New("read failed")}
	_, err := store.readFile(store.join(filepath.Join("incidents", "x.json")))
	require.ErrorContains(t, err, "read failed")
	_, err = store.readDir(filepath.Join("incidents", "x"))
	require.ErrorContains(t, err, "read failed")
	path := store.join(filepath.Join("incidents", ".store", "mutations", "x.json"))
	_, _, err = store.readReceipt(path)
	require.ErrorContains(t, err, "read failed")
	_, err = store.readReceiptKind(path)
	require.ErrorContains(t, err, "read failed")
	_, _, err = store.readMergeReceipt(path)
	require.ErrorContains(t, err, "read failed")
	_, _, err = store.readMergeIntent(path)
	require.ErrorContains(t, err, "read failed")
}

func TestHasPendingMergeStates(t *testing.T) {
	t.Run("read directory failure", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = errorRootedFileOps{err: errors.New("directory failed")}
		_, err := store.hasPendingMerge("")
		require.ErrorContains(t, err, "directory failed")
	})

	t.Run("corrupt intent", func(t *testing.T) {
		store := newTestStore(t)
		path, err := store.mergeIntentPath("corrupt-pending")
		require.NoError(t, err)
		writeStoreRaw(t, path, "{")
		_, err = store.hasPendingMerge("")
		require.Error(t, err)
	})

	t.Run("pending and excepted", func(t *testing.T) {
		store := newTestStore(t)
		intent := mergeIntentFixture(t)
		path, err := store.mergeIntentPath(intent.Mutation.MutationID)
		require.NoError(t, err)
		writeStoreJSON(t, path, intent)
		pending, err := store.hasPendingMerge("")
		require.NoError(t, err)
		require.True(t, pending)
		pending, err = store.hasPendingMerge(intent.Mutation.MutationID)
		require.NoError(t, err)
		require.False(t, pending)
	})

	t.Run("irrelevant entries", func(t *testing.T) {
		store := newTestStore(t)
		dir := filepath.Join(store.root, "incidents", ".store", "merge-intents")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "directory.json"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "note.txt"), nil, 0o600))
		pending, err := store.hasPendingMerge("")
		require.NoError(t, err)
		require.False(t, pending)
	})
}

func TestFinishAppendFailureStages(t *testing.T) {
	testErr := errors.New("injected storage failure")

	t.Run("load events", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read-jsonl", failAt: 1, err: testErr}
		_, err := store.finishAppend(appendReceiptFixture(t).Event.Incident, appendReceiptFixture(t))
		require.ErrorIs(t, err, testErr)
	})

	t.Run("sequence conflict", func(t *testing.T) {
		store := newTestStore(t)
		receipt := appendReceiptFixture(t)
		receipt.Event.Seq = 2
		_, err := store.finishAppend(receipt.Event.Incident, receipt)
		require.ErrorIs(t, err, incidents.ErrSequenceConflict)
	})

	t.Run("append event", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "append", failAt: 1, err: testErr}
		receipt := appendReceiptFixture(t)
		_, err := store.finishAppend(receipt.Event.Incident, receipt)
		require.ErrorIs(t, err, testErr)
	})

	t.Run("commit receipt", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "write", failAt: 1, err: testErr}
		receipt := appendReceiptFixture(t)
		_, err := store.finishAppend(receipt.Event.Incident, receipt)
		require.ErrorContains(t, err, "commit mutation receipt")
	})

	t.Run("write projection", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "write", failAt: 1, err: testErr}
		receipt := appendReceiptFixture(t)
		receipt.Committed = true
		_, err := store.finishAppend(receipt.Event.Incident, receipt)
		require.ErrorContains(t, err, "write incident projection")
	})

	t.Run("publish receipt", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "write", failAt: 2, err: testErr}
		receipt := appendReceiptFixture(t)
		receipt.Committed = true
		_, err := store.finishAppend(receipt.Event.Incident, receipt)
		require.ErrorContains(t, err, "publish mutation receipt")
	})

	t.Run("existing event differs", func(t *testing.T) {
		store := newTestStore(t)
		receipt := appendReceiptFixture(t)
		require.NoError(t, store.appendEvent(receipt.Event.Incident, receipt.Event))
		receipt.Event.VisibleAt = receipt.Event.VisibleAt.Add(1)
		_, err := store.finishAppend(receipt.Event.Incident, receipt)
		require.ErrorIs(t, err, incidents.ErrMutationConflict)
	})
}

func appendReceiptFixture(t *testing.T) appendReceipt {
	t.Helper()
	mutation := createdMutation(t, "append-fixture", "INC-FIXTURE")
	event := incidents.Event{
		ID:        mutation.MutationID,
		Seq:       1,
		At:        mutation.Event.At.UTC(),
		VisibleAt: mutation.Event.At.UTC(),
		Incident:  mutation.Incident,
		Actor:     mutation.Event.Actor,
		Type:      mutation.Event.Type,
		Assertion: mutation.Event.Assertion,
		Refs:      mutation.Event.Refs,
		Payload:   mutation.Event.Payload,
	}
	projection, err := incidents.Fold([]incidents.Event{event}, nil)
	require.NoError(t, err)
	return appendReceipt{Kind: receiptKindAppend, RequestHash: requestHash([]byte("fixture")), Event: event, Projection: projection}
}

func TestValidateMergeIntentFailures(t *testing.T) {
	valid := mergeIntentFixture(t)
	require.NoError(t, validateMergeIntent(valid))

	tests := []struct {
		name   string
		mutate func(*mergeIntent)
	}{
		{name: "invalid envelope", mutate: func(intent *mergeIntent) { intent.Kind = "" }},
		{name: "invalid source event", mutate: func(intent *mergeIntent) { intent.SourceMergedEvent.ID = "" }},
		{name: "wrong source incident", mutate: func(intent *mergeIntent) {
			intent.SourceMergedEvent.Incident = intent.Mutation.Into
			intent.SourceMergedEvent.Refs[0].Incident = &intent.Mutation.Source
			intent.SourceMergedEvent.Payload = mustJSON(t, incidents.MergedPayload{
				Into: intent.Mutation.Source, MergeID: intent.Mutation.MutationID,
			})
		}},
		{name: "strict source payload", mutate: func(intent *mergeIntent) {
			var payload map[string]any
			require.NoError(t, json.Unmarshal(intent.SourceMergedEvent.Payload, &payload))
			payload["unexpected"] = true
			intent.SourceMergedEvent.Payload = mustJSON(t, payload)
		}},
		{name: "mismatched source payload", mutate: func(intent *mergeIntent) {
			intent.SourceMergedEvent.Payload = mustJSON(t, incidents.MergedPayload{
				Into: intent.Mutation.Into, MergeID: "different",
			})
		}},
		{name: "invalid imported event", mutate: func(intent *mergeIntent) { intent.ImportedEvents[0].ID = "" }},
		{name: "mismatched imported event", mutate: func(intent *mergeIntent) { intent.ImportedEvents[0].ImportedFrom.Seq++ }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intent := valid
			intent.ImportedEvents = append([]incidents.Event(nil), valid.ImportedEvents...)
			tc.mutate(&intent)
			require.Error(t, validateMergeIntent(intent))
		})
	}
}

func TestEnsureEventAndLoadEventFailures(t *testing.T) {
	testErr := errors.New("injected storage failure")
	wanted := appendReceiptFixture(t).Event

	t.Run("load failure", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read-jsonl", failAt: 1, err: testErr}
		require.ErrorIs(t, store.ensureEvent(wanted.Incident, wanted), testErr)
	})

	t.Run("existing event mismatch", func(t *testing.T) {
		store := newTestStore(t)
		require.NoError(t, store.appendEvent(wanted.Incident, wanted))
		different := wanted
		different.VisibleAt = different.VisibleAt.Add(1)
		require.ErrorIs(t, store.ensureEvent(wanted.Incident, different), incidents.ErrMutationConflict)
	})

	t.Run("sequence conflict", func(t *testing.T) {
		store := newTestStore(t)
		different := wanted
		different.Seq = 2
		require.ErrorIs(t, store.ensureEvent(wanted.Incident, different), incidents.ErrSequenceConflict)
	})

	t.Run("append failure", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "append", failAt: 1, err: testErr}
		require.ErrorIs(t, store.ensureEvent(wanted.Incident, wanted), testErr)
	})

	for _, tc := range []struct {
		name    string
		records []json.RawMessage
	}{
		{name: "malformed JSON", records: []json.RawMessage{json.RawMessage("{")}},
		{name: "noncontiguous sequence", records: []json.RawMessage{mustJSON(t, func() incidents.Event {
			event := wanted
			event.Seq = 2
			return event
		}())}},
		{name: "invalid event", records: []json.RawMessage{mustJSON(t, func() incidents.Event {
			event := wanted
			event.ID = ""
			return event
		}())}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			store.ops = recordsRootedFileOps{rootedFileOps: store.files, records: tc.records}
			_, err := store.loadEvents(wanted.Incident)
			require.Error(t, err)
		})
	}
}

func TestFinishMergeFailureStages(t *testing.T) {
	testErr := errors.New("injected merge failure")

	t.Run("invalid intent", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.finishMerge("intent", "receipt", mergeIntent{})
		require.Error(t, err)
	})

	t.Run("source load", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read-jsonl", failAt: 1, err: testErr}
		_, err := store.finishMerge("intent", "receipt", mergeIntentFixture(t))
		require.ErrorIs(t, err, testErr)
	})

	t.Run("source sequence", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.finishMerge("intent", "receipt", mergeIntentFixture(t))
		require.ErrorIs(t, err, incidents.ErrSequenceConflict)
	})

	t.Run("imported event mismatch", func(t *testing.T) {
		store, intent := seededMergeStore(t)
		intent.ImportedEvents[0].Actor.ID = "different-actor"
		_, err := store.finishMerge("intent", "receipt", intent)
		require.ErrorIs(t, err, incidents.ErrMutationConflict)
	})

	t.Run("commit imported event", func(t *testing.T) {
		store, intent := seededMergeStore(t)
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "append", failAt: 1, err: testErr}
		_, err := store.finishMerge("intent", "receipt", intent)
		require.ErrorContains(t, err, "commit imported event")
	})

	t.Run("after source merge step", func(t *testing.T) {
		store, intent := seededMergeStore(t)
		store.afterMergeStep = func(step int) error {
			if step == 2 {
				return testErr
			}
			return nil
		}
		_, err := store.finishMerge("intent", "receipt", intent)
		require.ErrorIs(t, err, testErr)
	})

	tests := []struct {
		name         string
		operation    string
		failAt       int
		wantContains string
	}{
		{name: "commit source merge event", operation: "append", failAt: 2, wantContains: "commit source merge event"},
		{name: "reload source", operation: "read-jsonl", failAt: 4},
		{name: "reload target", operation: "read-jsonl", failAt: 5},
		{name: "commit receipt", operation: "write", failAt: 1, wantContains: "commit merge receipt"},
		{name: "publish projection", operation: "write", failAt: 2, wantContains: "write merged incident projection"},
		{name: "commit intent", operation: "write", failAt: 4, wantContains: "commit merge intent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, intent := seededMergeStore(t)
			store.ops = &failRootedFileOps{
				rootedFileOps: store.files, operation: tc.operation, failAt: tc.failAt, err: testErr,
			}
			intentPath := store.join(filepath.Join("incidents", ".store", "merge-intents", "merge-fixture.json"))
			receiptPath := store.join(filepath.Join("incidents", ".store", "mutations", "merge-fixture.json"))
			_, err := store.finishMerge(intentPath, receiptPath, intent)
			require.ErrorIs(t, err, testErr)
			if tc.wantContains != "" {
				require.ErrorContains(t, err, tc.wantContains)
			}
		})
	}
}

func TestFinishAppendFoldFailure(t *testing.T) {
	store := newTestStore(t)
	receipt := appendReceiptFixture(t)
	note := receipt.Event
	note.ID = "note-first"
	note.Type = incidents.EventNoteAdded
	note.Payload = mustJSON(t, incidents.NoteAddedPayload{Body: "invalid first event"})
	require.NoError(t, store.appendEvent(note.Incident, note))
	receipt.Event.Seq = 2
	_, err := store.finishAppend(receipt.Event.Incident, receipt)
	require.ErrorContains(t, err, "first event must be incident.created")
}

func TestFinishMergeResumesAfterSourceEventAppend(t *testing.T) {
	store, intent := seededMergeStore(t)
	require.NoError(t, store.appendEvent(intent.Mutation.Source, intent.SourceMergedEvent))
	intentPath := store.join(filepath.Join("incidents", ".store", "merge-intents", "merge-fixture.json"))
	receiptPath := store.join(filepath.Join("incidents", ".store", "mutations", "merge-fixture.json"))
	result, err := store.finishMerge(intentPath, receiptPath, intent)
	require.NoError(t, err)
	require.Equal(t, incidents.StatusClosed, result.Source.Status)
}

func TestFinishMergeFoldFailures(t *testing.T) {
	tests := []struct {
		name   string
		failAt int
	}{
		{name: "source", failAt: 4},
		{name: "target", failAt: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, intent := seededMergeStore(t)
			ref := intent.Mutation.Source
			if tc.name == "target" {
				ref = intent.Mutation.Into
			}
			note := appendReceiptFixture(t).Event
			note.ID = "note-first"
			note.Incident = ref
			note.Type = incidents.EventNoteAdded
			note.Payload = mustJSON(t, incidents.NoteAddedPayload{Body: "invalid first event"})
			store.ops = &recordsAtRootedFileOps{
				rootedFileOps: store.files, replaceAt: tc.failAt, records: []json.RawMessage{mustJSON(t, note)},
			}
			_, err := store.finishMerge("intent", "receipt", intent)
			require.ErrorContains(t, err, "first event must be incident.created")
		})
	}
}

func TestRepositoryStoreMergeErrorStates(t *testing.T) {
	ctx := context.Background()

	t.Run("missing incidents", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.Merge(ctx, mergeIntentFixture(t).Mutation)
		require.ErrorIs(t, err, ErrIncidentNotFound)
	})

	t.Run("source fold", func(t *testing.T) {
		store := newTestStore(t)
		intent := mergeIntentFixture(t)
		note := intent.ImportedEvents[0]
		note.ID = "source-note"
		note.Seq = 1
		note.Incident = intent.Mutation.Source
		note.ImportedFrom = nil
		note.Type = incidents.EventNoteAdded
		note.Payload = mustJSON(t, incidents.NoteAddedPayload{Body: "first event is deliberately not created"})
		require.NoError(t, store.appendEvent(note.Incident, note))
		into := appendReceiptFixture(t).Event
		into.Incident = intent.Mutation.Into
		require.NoError(t, store.appendEvent(into.Incident, into))
		_, err := store.Merge(ctx, intent.Mutation)
		require.ErrorContains(t, err, "first event must be incident.created")
	})

	t.Run("target fold", func(t *testing.T) {
		store := newTestStore(t)
		intent := mergeIntentFixture(t)
		source := intent.ImportedEvents[0]
		source.ID = source.ImportedFrom.EventID
		source.Seq = 1
		source.VisibleAt = source.At
		source.Incident = intent.Mutation.Source
		source.ImportedFrom = nil
		require.NoError(t, store.appendEvent(source.Incident, source))
		note := appendReceiptFixture(t).Event
		note.ID = "target-note"
		note.Incident = intent.Mutation.Into
		note.Type = incidents.EventNoteAdded
		note.Payload = mustJSON(t, incidents.NoteAddedPayload{Body: "first event is deliberately not created"})
		require.NoError(t, store.appendEvent(note.Incident, note))
		_, err := store.Merge(ctx, intent.Mutation)
		require.ErrorContains(t, err, "first event must be incident.created")
	})

	t.Run("corrupt receipt", func(t *testing.T) {
		store := newTestStore(t)
		mutation := mergeIntentFixture(t).Mutation
		path, err := store.receiptPath(mutation.MutationID)
		require.NoError(t, err)
		writeStoreRaw(t, path, `{"kind":"merge","unknown":true}`)
		_, err = store.Merge(ctx, mutation)
		require.ErrorContains(t, err, "decode merge receipt")
	})

	t.Run("receipt request conflict", func(t *testing.T) {
		store := newTestStore(t)
		mutation := mergeIntentFixture(t).Mutation
		path, err := store.receiptPath(mutation.MutationID)
		require.NoError(t, err)
		writeStoreJSON(t, path, mergeReceipt{Kind: receiptKindMerge, RequestHash: requestHash([]byte("other")), Committed: true})
		_, err = store.Merge(ctx, mutation)
		require.ErrorIs(t, err, incidents.ErrMutationConflict)
	})

	t.Run("corrupt intent", func(t *testing.T) {
		store := newTestStore(t)
		mutation := mergeIntentFixture(t).Mutation
		path, err := store.mergeIntentPath(mutation.MutationID)
		require.NoError(t, err)
		writeStoreRaw(t, path, "{")
		_, err = store.Merge(ctx, mutation)
		require.ErrorContains(t, err, "recover pending incident mutation")
	})

	t.Run("intent request conflict", func(t *testing.T) {
		store := newTestStore(t)
		mutation := mergeIntentFixture(t).Mutation
		intent := mergeIntentFixture(t)
		intent.RequestHash = requestHash([]byte("other"))
		intent.Committed = true
		path, err := store.mergeIntentPath(mutation.MutationID)
		require.NoError(t, err)
		writeStoreJSON(t, path, intent)
		_, err = store.Merge(ctx, mutation)
		require.ErrorIs(t, err, incidents.ErrMutationConflict)
	})

	t.Run("already merged incident", func(t *testing.T) {
		store := newTestStore(t)
		source := createdMutation(t, "source", "INC-SOURCE")
		into := createdMutation(t, "into", "INC-INTO")
		other := createdMutation(t, "other", "INC-OTHER")
		_, err := store.Append(ctx, source)
		require.NoError(t, err)
		_, err = store.Append(ctx, into)
		require.NoError(t, err)
		_, err = store.Append(ctx, other)
		require.NoError(t, err)
		_, err = store.Merge(ctx, incidents.MergeMutation{MutationID: "first-merge", Source: source.Incident, Into: into.Incident})
		require.NoError(t, err)
		_, err = store.Merge(ctx, incidents.MergeMutation{MutationID: "second-merge", Source: source.Incident, Into: other.Incident})
		require.ErrorContains(t, err, "requires two active incidents")
	})

	t.Run("import visibility precedes target", func(t *testing.T) {
		store := newTestStore(t)
		source := createdMutation(t, "visibility-source", "INC-SOURCE")
		into := createdMutation(t, "visibility-target", "INC-TARGET")
		_, err := store.Append(ctx, source)
		require.NoError(t, err)
		_, err = store.Append(ctx, into)
		require.NoError(t, err)
		store.now = func() time.Time { return source.Event.At.Add(-time.Minute) }
		_, err = store.Merge(ctx, incidents.MergeMutation{MutationID: "visibility-merge", Source: source.Incident, Into: into.Incident})
		require.ErrorContains(t, err, "visibleAt precedes prior event")
	})

	t.Run("source merge visibility precedes source", func(t *testing.T) {
		store := newTestStore(t)
		source := createdMutation(t, "source-visibility-source", "INC-SOURCE")
		into := createdMutation(t, "source-visibility-target", "INC-TARGET")
		into.Event.At = source.Event.At.Add(-2 * time.Minute)
		_, err := store.Append(ctx, source)
		require.NoError(t, err)
		_, err = store.Append(ctx, into)
		require.NoError(t, err)
		store.now = func() time.Time { return source.Event.At.Add(-time.Minute) }
		_, err = store.Merge(ctx, incidents.MergeMutation{MutationID: "source-visibility-merge", Source: source.Incident, Into: into.Incident})
		require.ErrorContains(t, err, "visibleAt precedes prior event")
	})
}

func TestRepositoryStorePublicReadAndFoldErrors(t *testing.T) {
	store := newTestStore(t)
	ref := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-CORRUPT"}
	layout, err := incidents.LayoutFor(ref)
	require.NoError(t, err)
	eventsPath := store.join(layout.Events)
	writeStoreRaw(t, eventsPath, "not-json\n")
	_, err = store.Events(context.Background(), ref, 0)
	require.Error(t, err)

	ref.IncidentID = "INC-FOLD"
	note := appendReceiptFixture(t).Event
	note.ID = "note-first"
	note.Seq = 1
	note.Incident = ref
	note.Type = incidents.EventNoteAdded
	note.Payload = mustJSON(t, incidents.NoteAddedPayload{Body: "first"})
	require.NoError(t, store.appendEvent(ref, note))
	_, err = store.Projection(context.Background(), ref, nil)
	require.ErrorContains(t, err, "first event must be incident.created")
	_, err = store.Append(context.Background(), noteMutation(t, "second-note", ref))
	require.ErrorContains(t, err, "first event must be incident.created")
}

func TestRepositoryStorePublicAppendStorageFailures(t *testing.T) {
	testErr := errors.New("injected append storage failure")
	tests := []struct {
		name      string
		operation string
		failAt    int
	}{
		{name: "read merge intent", operation: "read", failAt: 1},
		{name: "read pending merges", operation: "read-dir", failAt: 1},
		{name: "read receipt", operation: "read", failAt: 2},
		{name: "load events", operation: "read-jsonl", failAt: 1},
		{name: "prepare receipt", operation: "write", failAt: 1},
		{name: "finish append reload", operation: "read-jsonl", failAt: 2},
		{name: "append event", operation: "append", failAt: 1},
		{name: "commit receipt", operation: "write", failAt: 2},
		{name: "write projection", operation: "write", failAt: 3},
		{name: "publish receipt", operation: "write", failAt: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			store.afterRecovery = func(locked *RepositoryStore) {
				locked.ops = &failRootedFileOps{
					rootedFileOps: locked.ops, operation: tc.operation, failAt: tc.failAt, err: testErr,
				}
			}
			_, err := store.Append(context.Background(), createdMutation(t, "public-append-failure", "INC-FAIL"))
			require.ErrorIs(t, err, testErr)
		})
	}
}

func TestRepositoryStorePublicMergeStorageFailures(t *testing.T) {
	testErr := errors.New("injected merge storage failure")
	tests := []struct {
		name      string
		operation string
		failAt    int
	}{
		{name: "read receipt", operation: "read", failAt: 1},
		{name: "read intent", operation: "read", failAt: 2},
		{name: "read pending merges", operation: "read-dir", failAt: 1},
		{name: "load source", operation: "read-jsonl", failAt: 1},
		{name: "load target", operation: "read-jsonl", failAt: 2},
		{name: "prepare intent", operation: "write", failAt: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			source := createdMutation(t, "create-source", "INC-SOURCE")
			into := createdMutation(t, "create-target", "INC-TARGET")
			_, err := store.Append(context.Background(), source)
			require.NoError(t, err)
			_, err = store.Append(context.Background(), into)
			require.NoError(t, err)
			store.afterRecovery = func(locked *RepositoryStore) {
				locked.ops = &failRootedFileOps{
					rootedFileOps: locked.ops, operation: tc.operation, failAt: tc.failAt, err: testErr,
				}
			}
			_, err = store.Merge(context.Background(), incidents.MergeMutation{
				MutationID: "public-merge-failure", Source: source.Incident, Into: into.Incident,
			})
			require.ErrorIs(t, err, testErr)
		})
	}
}

func TestRepositoryStorePublicPendingAndReplayStates(t *testing.T) {
	t.Run("append sees pending merge created after recovery", func(t *testing.T) {
		store := newTestStore(t)
		pending := mergeIntentFixture(t)
		pending.Mutation.MutationID = "other-pending-merge"
		pending.RequestHash = requestHash([]byte("other pending merge"))
		store.afterRecovery = func(locked *RepositoryStore) {
			path, err := locked.mergeIntentPath(pending.Mutation.MutationID)
			require.NoError(t, err)
			require.NoError(t, locked.writeJSONAtomic(path, pending, 0o600))
		}
		_, err := store.Append(context.Background(), createdMutation(t, "append-with-pending", "INC-PENDING"))
		require.ErrorIs(t, err, incidents.ErrSequenceConflict)
	})

	t.Run("merge resumes intent created after recovery", func(t *testing.T) {
		store, intent := seededMergeStore(t)
		store.afterRecovery = func(locked *RepositoryStore) {
			path, err := locked.mergeIntentPath(intent.Mutation.MutationID)
			require.NoError(t, err)
			request, err := json.Marshal(intent.Mutation)
			require.NoError(t, err)
			intent.RequestHash = requestHash(request)
			require.NoError(t, locked.writeJSONAtomic(path, intent, 0o600))
		}
		result, err := store.Merge(context.Background(), intent.Mutation)
		require.NoError(t, err)
		require.True(t, result.Replayed)
	})

	t.Run("merge sees unrelated pending intent", func(t *testing.T) {
		store := newTestStore(t)
		source := createdMutation(t, "pending-source", "INC-SOURCE")
		into := createdMutation(t, "pending-target", "INC-TARGET")
		_, err := store.Append(context.Background(), source)
		require.NoError(t, err)
		_, err = store.Append(context.Background(), into)
		require.NoError(t, err)
		pending := mergeIntentFixture(t)
		pending.Mutation.MutationID = "other-pending-merge"
		pending.RequestHash = requestHash([]byte("other pending merge"))
		store.afterRecovery = func(locked *RepositoryStore) {
			path, err := locked.mergeIntentPath(pending.Mutation.MutationID)
			require.NoError(t, err)
			require.NoError(t, locked.writeJSONAtomic(path, pending, 0o600))
		}
		_, err = store.Merge(context.Background(), incidents.MergeMutation{
			MutationID: "blocked-by-pending", Source: source.Incident, Into: into.Incident,
		})
		require.ErrorIs(t, err, incidents.ErrSequenceConflict)
	})

	t.Run("merge replay republishes projections", func(t *testing.T) {
		store := newTestStore(t)
		source := createdMutation(t, "replay-source", "INC-SOURCE")
		into := createdMutation(t, "replay-target", "INC-TARGET")
		_, err := store.Append(context.Background(), source)
		require.NoError(t, err)
		_, err = store.Append(context.Background(), into)
		require.NoError(t, err)
		mutation := incidents.MergeMutation{MutationID: "replay-merge", Source: source.Incident, Into: into.Incident}
		_, err = store.Merge(context.Background(), mutation)
		require.NoError(t, err)
		testErr := errors.New("projection publish failed")
		store.afterRecovery = func(locked *RepositoryStore) {
			locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "write", failAt: 1, err: testErr}
		}
		_, err = store.Merge(context.Background(), mutation)
		require.ErrorIs(t, err, testErr)
	})
}

func TestRepositoryStoreProjectionReadFailureAfterRecovery(t *testing.T) {
	store := newTestStore(t)
	ref := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-PROJECTION-FAIL"}
	testErr := errors.New("projection read failed")
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read-jsonl", failAt: 1, err: testErr}
	}
	_, err := store.Projection(context.Background(), ref, nil)
	require.ErrorIs(t, err, testErr)
}

func TestReceiptReadersWrapMalformedJSON(t *testing.T) {
	store := newTestStore(t)
	store.ops = rawReadRootedFileOps{rootedFileOps: store.files, raw: json.RawMessage("{")}
	path := store.join(filepath.Join("incidents", ".store", "mutations", "malformed.json"))
	_, _, err := store.readReceipt(path)
	require.ErrorContains(t, err, "decode mutation receipt")
	_, err = store.readReceiptKind(path)
	require.ErrorContains(t, err, "decode mutation receipt kind")
	_, _, err = store.readMergeReceipt(path)
	require.ErrorContains(t, err, "decode merge receipt")
	_, _, err = store.readMergeIntent(path)
	require.ErrorContains(t, err, "decode merge intent")
}

func TestRecoverPendingMutationsErrorStates(t *testing.T) {
	t.Run("receipt directory read", func(t *testing.T) {
		store := newTestStore(t)
		store.ops = errorRootedFileOps{err: errors.New("receipt directory failed")}
		require.ErrorContains(t, store.recoverPendingMutations(), "receipt directory failed")
	})

	t.Run("intent directory read", func(t *testing.T) {
		store := newTestStore(t)
		testErr := errors.New("intent directory failed")
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read-dir", failAt: 2, err: testErr}
		require.ErrorIs(t, store.recoverPendingMutations(), testErr)
	})

	t.Run("irrelevant entries", func(t *testing.T) {
		store := newTestStore(t)
		root := filepath.Join(store.root, "incidents", ".store")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "mutations", "directory.json"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "mutations", "note.txt"), nil, 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "merge-intents", "directory.json"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "merge-intents", "note.txt"), nil, 0o600))
		require.NoError(t, store.recoverPendingMutations())
	})

	t.Run("invalid receipt kind", func(t *testing.T) {
		store := newTestStore(t)
		path, err := store.receiptPath("invalid-kind")
		require.NoError(t, err)
		writeStoreRaw(t, path, `{"kind":"unknown"}`)
		require.ErrorContains(t, store.recoverPendingMutations(), "invalid mutation receipt kind")
	})

	t.Run("merge receipt is skipped", func(t *testing.T) {
		store := newTestStore(t)
		path, err := store.receiptPath("merge-kind")
		require.NoError(t, err)
		writeStoreRaw(t, path, `{"kind":"merge"}`)
		require.NoError(t, store.recoverPendingMutations())
	})

	t.Run("malformed append receipt", func(t *testing.T) {
		store := newTestStore(t)
		path, err := store.receiptPath("bad-append")
		require.NoError(t, err)
		writeStoreRaw(t, path, `{"kind":"append","unknown":true}`)
		require.ErrorContains(t, store.recoverPendingMutations(), "decode mutation receipt")
	})

	t.Run("unpublished append is finished", func(t *testing.T) {
		store := newTestStore(t)
		receipt := appendReceiptFixture(t)
		path, err := store.receiptPath(receipt.Event.ID)
		require.NoError(t, err)
		writeStoreJSON(t, path, receipt)
		require.NoError(t, store.recoverPendingMutations())
		events, err := store.loadEvents(receipt.Event.Incident)
		require.NoError(t, err)
		require.Len(t, events, 1)
	})

	t.Run("unpublished append finish failure", func(t *testing.T) {
		store := newTestStore(t)
		receipt := appendReceiptFixture(t)
		path, err := store.receiptPath(receipt.Event.ID)
		require.NoError(t, err)
		writeStoreJSON(t, path, receipt)
		testErr := errors.New("recover append failed")
		store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "append", failAt: 1, err: testErr}
		require.ErrorIs(t, store.recoverPendingMutations(), testErr)
	})

	t.Run("committed merge intent is skipped", func(t *testing.T) {
		store := newTestStore(t)
		intent := mergeIntentFixture(t)
		intent.Committed = true
		path, err := store.mergeIntentPath(intent.Mutation.MutationID)
		require.NoError(t, err)
		writeStoreJSON(t, path, intent)
		require.NoError(t, store.recoverPendingMutations())
	})

	t.Run("unfinished merge fails visibly", func(t *testing.T) {
		store := newTestStore(t)
		intent := mergeIntentFixture(t)
		path, err := store.mergeIntentPath(intent.Mutation.MutationID)
		require.NoError(t, err)
		writeStoreJSON(t, path, intent)
		require.ErrorIs(t, store.recoverPendingMutations(), incidents.ErrSequenceConflict)
	})
}

func seededMergeStore(t *testing.T) (*RepositoryStore, mergeIntent) {
	t.Helper()
	store := newTestStore(t)
	intent := mergeIntentFixture(t)
	imported := intent.ImportedEvents[0]
	original := imported
	original.ID = imported.ImportedFrom.EventID
	original.Seq = imported.ImportedFrom.Seq
	original.VisibleAt = original.At
	original.Incident = intent.Mutation.Source
	original.ImportedFrom = nil
	require.NoError(t, store.appendEvent(original.Incident, original))
	into := appendReceiptFixture(t).Event
	into.ID = "into-created"
	into.Incident = intent.Mutation.Into
	require.NoError(t, store.appendEvent(into.Incident, into))
	return store, intent
}

func mergeIntentFixture(t *testing.T) mergeIntent {
	t.Helper()
	source := appendReceiptFixture(t).Event
	source.Incident.IncidentID = "INC-SOURCE"
	into := incidents.IncidentRef{StoreID: source.Incident.StoreID, IncidentID: "INC-INTO"}
	mutation := incidents.MergeMutation{MutationID: "merge-fixture", Source: source.Incident, Into: into}
	mergeAt := source.At.Add(1)
	imported := source
	imported.ID = mutation.MutationID + "-import-1"
	imported.Seq = 2
	imported.VisibleAt = mergeAt
	imported.Incident = into
	imported.ImportedFrom = &incidents.ImportedEventRef{
		Incident: source.Incident, EventID: source.ID, Seq: source.Seq, MergeID: mutation.MutationID,
	}
	sourceMerged := incidents.Event{
		ID: mutation.MutationID + "-source", Seq: 2, At: mergeAt, VisibleAt: mergeAt,
		Incident: source.Incident, Actor: incidents.Actor{Kind: incidents.ActorSystem, ID: "incident-store"},
		Type: incidents.EventIncidentMerged, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim},
		Refs:    []incidents.ArtifactRef{{Kind: incidents.RefIncident, Incident: &into}},
		Payload: mustMarshal(incidents.MergedPayload{Into: into, MergeID: mutation.MutationID}),
	}
	return mergeIntent{
		Kind: receiptKindMerge, RequestHash: requestHash([]byte("merge fixture")), Mutation: mutation,
		ImportedEvents: []incidents.Event{imported}, SourceMergedEvent: sourceMerged,
	}
}

type failRootedFileOps struct {
	rootedFileOps
	operation string
	failAt    int
	calls     int
	err       error
}

type recordsRootedFileOps struct {
	rootedFileOps
	records []json.RawMessage
}

type recordsAtRootedFileOps struct {
	rootedFileOps
	replaceAt int
	calls     int
	records   []json.RawMessage
}

func (o *recordsAtRootedFileOps) ReadJSONLWithLimit(path string, limit int64) ([]json.RawMessage, error) {
	o.calls++
	if o.calls == o.replaceAt {
		return o.records, nil
	}
	return o.rootedFileOps.ReadJSONLWithLimit(path, limit)
}

type rawReadRootedFileOps struct {
	rootedFileOps
	raw json.RawMessage
}

func (o rawReadRootedFileOps) ReadJSON(_ string, target any) error {
	raw, ok := target.(*json.RawMessage)
	if !ok {
		return errors.New("target is not raw JSON")
	}
	*raw = append((*raw)[:0], o.raw...)
	return nil
}

func (o recordsRootedFileOps) ReadJSONLWithLimit(string, int64) ([]json.RawMessage, error) {
	return o.records, nil
}

func (o *failRootedFileOps) shouldFail(operation string) bool {
	if o.operation != operation {
		return false
	}
	o.calls++
	return o.calls == o.failAt
}

func (o *failRootedFileOps) AppendJSONL(path string, value any) error {
	if o.shouldFail("append") {
		return o.err
	}
	return o.rootedFileOps.AppendJSONL(path, value)
}

func (o *failRootedFileOps) ReadJSONLWithLimit(path string, limit int64) ([]json.RawMessage, error) {
	if o.shouldFail("read-jsonl") {
		return nil, o.err
	}
	return o.rootedFileOps.ReadJSONLWithLimit(path, limit)
}

func (o *failRootedFileOps) WriteJSONAtomicWithMode(path string, value any, mode os.FileMode) error {
	if o.shouldFail("write") {
		return o.err
	}
	return o.rootedFileOps.WriteJSONAtomicWithMode(path, value, mode)
}

func (o *failRootedFileOps) ReadJSON(path string, target any) error {
	if o.shouldFail("read") {
		return o.err
	}
	return o.rootedFileOps.ReadJSON(path, target)
}

func (o *failRootedFileOps) ReadDir(path string) ([]os.DirEntry, error) {
	if o.shouldFail("read-dir") {
		return nil, o.err
	}
	return o.rootedFileOps.ReadDir(path)
}

type errorRootedFileOps struct{ err error }

func (o errorRootedFileOps) AppendJSONL(string, any) error { return o.err }
func (o errorRootedFileOps) ReadJSONLWithLimit(string, int64) ([]json.RawMessage, error) {
	return nil, o.err
}
func (o errorRootedFileOps) WriteJSONAtomicWithMode(string, any, os.FileMode) error {
	return o.err
}
func (o errorRootedFileOps) ReadJSON(string, any) error            { return o.err }
func (o errorRootedFileOps) ReadDir(string) ([]os.DirEntry, error) { return nil, o.err }
