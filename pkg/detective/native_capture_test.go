package detective

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestNativeCaptureOwnsTransitiveDefinitionsAndQueries(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for _, id := range []string{"one", "two"} {
		if _, err := s.SaveValidator(ctx, PublishedValidator{ID: id, Title: id, Validator: Validator{ID: id, Kind: "non-null", Scope: "row", Severity: "error", Field: "id"}}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"detail", "list"} {
		if _, err := s.SaveQueryValidators(ctx, QueryValidators{QueryID: id, Validators: []Validator{{ID: "local", Kind: "non-null", Scope: "row", Severity: "error", Field: "id"}}}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []Check{
		{ID: "grand", Title: "Grand", Steps: []Step{{ID: "query", Kind: "query", QueryID: "list"}}},
		{ID: "child", Title: "Child", Steps: []Step{{ID: "query", Kind: "query", QueryID: "detail"}, {ID: "nested", Kind: "check", CheckID: "grand"}}},
		{ID: "root", Title: "Root", Steps: []Step{{ID: "query", Kind: "query", QueryID: "q", Validators: []Validator{{ID: "one", ValidatorID: "one"}, {ID: "two", ValidatorID: "two"}}}, {ID: "child", Kind: "check", CheckID: "child"}}},
	} {
		if _, err := s.SaveCheck(ctx, check, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	ref := apicontract.SessionInputReference{SchemaVersion: 1, StorageID: "local", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns", SessionID: "session", InputSnapshotID: "input", ContextSetID: "context", EnvironmentID: "qa", IdentityDomain: "tenant"}
	seen := map[string]bool{}
	capture, err := s.CaptureNativeInput(ctx, NativeCaptureRequest{CheckID: "root", NativeOperationID: "native", Actor: "alice", Reference: ref}, func(_ context.Context, id string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		seen[id] = true
		raw, err := json.Marshal(map[string]string{"id": id, "title": id, "type": "SQL", "text": "select accepted"})
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: id, Revision: "revision", DefinitionJSON: raw}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := capture.Pin.Definition
	if len(definition.Dependencies) != 2 || len(definition.PublishedValidators) != 2 || len(definition.QueryValidators) != 2 || len(definition.Queries) != 3 || len(seen) != 3 {
		t.Fatal("incomplete transitive pin", definition)
	}
	reader := pinnedNativeCatalog{definition}
	catalog, err := core.LoadCatalog(ctx, "root", "", reader)
	if err != nil || len(catalog.Checks) != 3 {
		t.Fatal("captured graph not executable by Core", err)
	}
	if _, err = reader.GetCheck(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = reader.GetPublishedValidator(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, ok, err := reader.GetQueryValidators(ctx, "absent"); err != nil || ok {
		t.Fatal(ok, err)
	}
	if ok, err := reader.QueryExists(ctx, "absent"); err != nil || ok {
		t.Fatal(ok, err)
	}
	if _, ok := NativeAcceptedQuery(ctx, "q"); ok {
		t.Fatal("query snapshot appeared without a sealed context")
	}
}

func TestNativeCaptureRejectsSnapshotAndCaseBoundaryFaults(t *testing.T) {
	for _, kind := range []string{"missing-check", "query-read", "wrong-query", "invalid-query", "missing-case", "inactive-case", "case-digest", "invalid-reference", "mismatched-provenance", "binding-fault"} {
		t.Run(kind, func(t *testing.T) {
			s, capture, _ := nativeCaptureFixture(t, true)
			req := NativeCaptureRequest{CheckID: "check", CaseID: "case", NativeOperationID: "another", Actor: "alice", Reference: capture.Pin.Reference}
			oldDigest, oldBind := caseContextDigest, nativeBindOperation
			defer func() { caseContextDigest = oldDigest; nativeBindOperation = oldBind }()
			snapshot := func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
				return capture.Pin.Definition.Queries[0], nil
			}
			failed := errors.New("native capture fault")
			switch kind {
			case "missing-check":
				req.CheckID = "absent"
			case "query-read":
				snapshot = func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
					return sessioncontext.DetectiveAcceptedQueryPin{}, failed
				}
			case "wrong-query":
				snapshot = func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
					q := capture.Pin.Definition.Queries[0]
					q.QueryID = "other"
					return q, nil
				}
			case "invalid-query":
				snapshot = func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
					q := capture.Pin.Definition.Queries[0]
					q.DefinitionJSON = json.RawMessage(`{}`)
					return q, nil
				}
			case "missing-case":
				req.CaseID = "absent"
			case "inactive-case":
				item := *capture.Case
				item.Status = "closed"
				item.ClosureReason = "Reviewed"
				if _, err := s.SaveCase(context.Background(), item, "alice", "update-case"); err != nil {
					t.Fatal(err)
				}
			case "case-digest":
				caseContextDigest = func(map[string]InputValue) (string, error) { return "", failed }
			case "mismatched-provenance":
				req.Reference.Bindings = []apicontract.SessionInputBindingRef{{InputName: "ghost", Origin: "manual"}}
			case "invalid-reference":
				req.Reference.NamespaceID = "/invalid"
			case "binding-fault":
				nativeBindOperation = func(string, string, string, string, any) (core.OperationBinding, error) {
					return core.OperationBinding{}, failed
				}
			}
			if _, err := s.CaptureNativeInput(context.Background(), req, snapshot); err == nil {
				t.Fatal("bad capture boundary accepted")
			}
		})
	}
	s, _, _ := nativeCaptureFixture(t, false)
	if _, err := s.CaptureNativeInput(context.Background(), NativeCaptureRequest{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestNativeCaptureUsesAuthoritativeCaseDefaultsAndTypedManualData(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.SaveCheck(ctx, Check{ID: "input-check", Title: "Typed Check", Inputs: []Input{{Name: "id", Type: apicontract.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []Step{{ID: "query", Kind: "query", QueryID: "q", Bindings: []Binding{{Input: "id", FromInput: "id"}}}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	value := apicontract.NewIntegerValue("9007199254740993")
	item, err := s.SaveCase(ctx, Case{ID: "case", Title: "Case", Context: map[string]InputValue{"id": {Scalar: &value}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	ref := apicontract.SessionInputReference{SchemaVersion: 1, StorageID: "local", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns", SessionID: "session", InputSnapshotID: "input", ContextSetID: "context", EnvironmentID: "qa", IdentityDomain: "tenant"}
	snapshot := func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: "q", Revision: "revision", DefinitionJSON: json.RawMessage(`{"id":"q","title":"Query","type":"SQL","text":"select accepted"}`)}, nil
	}
	req := NativeCaptureRequest{CheckID: "input-check", CaseID: item.ID, NativeOperationID: "native", Actor: "alice", Reference: ref}
	capture, err := s.CaptureNativeInput(ctx, req, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if capture.Pin.EffectiveInputs["id"].Scalar.Str != value.Str || len(capture.Pin.Reference.Bindings) != 1 || capture.Pin.Reference.Bindings[0].Origin != "case" || capture.Pin.Reference.Bindings[0].CaseVersion != item.Version {
		t.Fatal("Case default/provenance changed", capture.Pin)
	}
	different := apicontract.NewIntegerValue("6")
	req.Inputs = map[string]InputValue{"id": {Scalar: &different}}
	if _, err = s.CaptureNativeInput(ctx, req, snapshot); err == nil {
		t.Fatal("manual value overrode authoritative Case context")
	}
	req.CaseID = ""
	req.Inputs = map[string]InputValue{"id": {Scalar: &value}}
	capture, err = s.CaptureNativeInput(ctx, req, snapshot)
	if err != nil || capture.Pin.Reference.Bindings[0].Origin != "manual" {
		t.Fatal("typed manual origin", err)
	}
	req.Reference.Bindings = []apicontract.SessionInputBindingRef{{InputName: "id", Origin: "bag", Entries: []apicontract.SessionInputEntryRef{{BagID: "bag", BagVersion: 1, EntryID: "entry", RevisionID: "revision"}}}}
	capture, err = s.CaptureNativeInput(ctx, req, snapshot)
	if err != nil || capture.Pin.Reference.Bindings[0].Origin != "bag" {
		t.Fatal("explicit selected bag provenance", err)
	}
	req.Inputs = map[string]InputValue{"other": {Scalar: &value}}
	if _, err = s.CaptureNativeInput(ctx, req, snapshot); err == nil {
		t.Fatal("unknown Check input accepted")
	}
}
