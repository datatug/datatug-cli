package watchdaemon

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/detective"
)

func serviceIntentFixture(t *testing.T) (ServiceReadBinding, map[string]apicontract.TypedValueOrSet) {
	t.Helper()
	q := datatug.QueryDefWithFolderPath{}
	q.ID, q.Title, q.CreatedAt, q.Type, q.ConnectionID, q.StructuredBody = "q", "Read A", "2026-11-04T00:00:00Z", datatug.QueryTypeTugQL, "C1", true
	q.Text = "from: {name: A}\nselect: [{field: id}]\n"
	q.Parameters = datatug.Parameters{{ID: "id", Type: "integer", IsRequired: true}, {ID: "rate", Type: "decimal", DefaultValue: "0.5"}, {ID: "label", Type: "string"}, {ID: "cohort", Type: "string", IsMultiValue: true}}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ServiceConnectionSource(q.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	b := CloudExecutionBundle{Contract: "watch-execution-bundle/1", Scope: watch.Scope{Namespace: "space", Project: "project", Environment: "prod"}, CheckID: "check", CheckVersion: "1", Catalog: detective.Catalog{Checks: map[string]detective.Check{"check": {ID: "check", Title: "Read", Version: 1, Steps: []detective.Step{{ID: "read", Kind: "query", QueryID: "q"}}}}}, Queries: map[string]CloudBundleQuery{"q": {Revision: "query-r1", JSON: raw}}, Sources: []CloudBundleSource{source}}
	claim := sealCloudTestBundle(t, &b)
	claim.ClaimID = claim.RunID
	claim.Definition.ID = "watch"
	claim.Definition.Version = 1
	claim.Deadline = time.Date(2026, 11, 4, 0, 1, 0, 0, time.UTC)
	claim.LeaseUntil = claim.Deadline.Add(-time.Second)
	set, err := apicontract.NewTypedValueSet(apicontract.NewStringValue("B"), apicontract.NewStringValue("A"))
	if err != nil {
		t.Fatal(err)
	}
	root := apicontract.NewStringValue("café<&>\u2028")
	claim.Definition.Inputs = map[string]detective.InputValue{"window": {Range: &detective.ValueRange{Type: "range", Start: apicontract.NewIntegerValue("1"), End: apicontract.NewIntegerValue("3")}}, "title": {Scalar: &root}, "cohort": {Set: &set}}
	return ServiceReadBinding{Claim: claim, Bundle: b, QueryID: "q", SourceID: source.ID, Connection: dataaccess.ConnectionRef{Space: "space", ID: "ap-owned", Fingerprint: dataaccess.Fingerprint{Engine: "sqlite", Database: "shop", EngineIdentity: "sqlite-file-route-v1:" + strings.Repeat("d", 64), Address: "owned-source"}}}, map[string]apicontract.TypedValueOrSet{"id": apicontract.ScalarValue(apicontract.NewIntegerValue("1")), "label": apicontract.ScalarValue(root), "cohort": apicontract.SetValue(set)}
}

func serviceReseal(t *testing.T, b *ServiceReadBinding) {
	t.Helper()
	claim := sealCloudTestBundle(t, &b.Bundle)
	b.Claim.Definition.SourceRevision = claim.Definition.SourceRevision
}

func TestServiceReadIntentGoldenAndCoreHash(t *testing.T) {
	b, params := serviceIntentFixture(t)
	got, err := BuildServiceReadIntent(context.Background(), b, params, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	wantParams := `{"cohort":{"type":"set","values":[{"type":"string","value":"A"},{"type":"string","value":"B"}]},"id":{"type":"integer","value":"1"},"label":{"type":"string","value":"café\u003c\u0026\u003e\u2028"},"rate":{"type":"decimal","value":"0.5"}}`
	if got.Operation.Parameters != wantParams || got.Operation.MaxRows != 100 || got.Operation.MaxBytes != 262144 {
		t.Fatal("canonical operation/default/escaping changed")
	}
	expected, err := got.Operation.RequestFor(strings.Repeat("a", 32))
	if err != nil || got.Request != expected || got.WatchBindingSHA256 == got.Request.RequestSHA256 {
		t.Fatal("Core operation and Watch hash were conflated")
	}
	// Frozen protocol vectors: fixed caps, Go JSON escaping, scalar default,
	// canonical set, retained range/root inputs and opaque source identity.
	if got.Request.RequestSHA256 != "fa56350ac76991b8e91179036300ee7a827c5a94dd340cef543a3d05851dbd77" || got.WatchBindingSHA256 != "05d16a898c66ba2587fb7b5e61ef24fd403b16b9934be0d47723aea78b3b0437" {
		t.Fatal("canonical Service Watch golden changed")
	}
	if b.SourceID != "d3b27a265470d363a822fa23aca85de8d4dc288533db4e0441bdaf8b89ea0487" {
		t.Fatal("descriptor ID changed")
	}
	// Changing a mapped occurrence of the SAME query changes both hashes.
	params["id"] = apicontract.ScalarValue(apicontract.NewIntegerValue("2"))
	second, err := BuildServiceReadIntent(context.Background(), b, params, strings.Repeat("a", 32))
	if err != nil || second.Request.RequestSHA256 == got.Request.RequestSHA256 || second.WatchBindingSHA256 == got.WatchBindingSHA256 {
		t.Fatal("mapped operation not bound")
	}
	if second.Request.RequestSHA256 != "c28894e9bf27a05f83c6edfa0fd532839c0c53ab884bf2526f92bf77137099bc" || second.WatchBindingSHA256 != "c061eb43effddb53c1ef9b4ae5fc1b35208e02bdbb4af2b007d1addcd4e36922" {
		t.Fatal("mapped occurrence golden changed")
	}
	// Root Check intent changes independently from its mapped query operation.
	root := apicontract.NewStringValue("other")
	b.Claim.Definition.Inputs["title"] = detective.InputValue{Scalar: &root}
	third, err := BuildServiceReadIntent(context.Background(), b, params, strings.Repeat("a", 32))
	if err != nil || third.Request != second.Request || third.WatchBindingSHA256 == second.WatchBindingSHA256 {
		t.Fatal("retained root intent not independently bound")
	}
	if third.WatchBindingSHA256 != "da7656a4ea5c737a7e49796aef0b65539e0ee0cd6e38c993c461c4d863efff4f" {
		t.Fatal("retained root golden changed")
	}
	// Returned bytes are detached from later caller parameter edits.
	if !strings.Contains(got.Operation.Parameters, `"value":"1"`) {
		t.Fatal("operation changed after construction")
	}
}

func TestServiceReadIntentBindingDiscriminators(t *testing.T) {
	b, params := serviceIntentFixture(t)
	base, err := BuildServiceReadIntent(context.Background(), b, params, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ServiceReadBinding){
		func(b *ServiceReadBinding) { b.Claim.Fence++ }, func(b *ServiceReadBinding) { b.Claim.RunID = "other"; b.Claim.ClaimID = "other" },
		func(b *ServiceReadBinding) { b.Claim.Definition.ID = "other" }, func(b *ServiceReadBinding) { b.Claim.Definition.Version++ },
		func(b *ServiceReadBinding) { b.Claim.Definition.SourceFingerprint = strings.Repeat("e", 64) },
		func(b *ServiceReadBinding) { b.Connection.Fingerprint.EngineIdentity = "other-owned-identity" },
		func(b *ServiceReadBinding) { b.Connection.Fingerprint.Address = "other-owned-address" },
	} {
		candidate, p := serviceIntentFixture(t)
		mutate(&candidate)
		got, e := BuildServiceReadIntent(context.Background(), candidate, p, strings.Repeat("a", 32))
		if e != nil || got.Request != base.Request || got.WatchBindingSHA256 == base.WatchBindingSHA256 {
			t.Fatal("metadata intent not bound")
		}
	}
	reordered := map[string]apicontract.TypedValueOrSet{"label": params["label"], "cohort": params["cohort"], "id": params["id"]}
	got, err := BuildServiceReadIntent(context.Background(), b, reordered, strings.Repeat("a", 32))
	if err != nil || got != base {
		t.Fatal("map insertion order changed digest")
	}
	got, err = BuildServiceReadIntent(context.Background(), b, params, strings.Repeat("b", 32))
	if err != nil || got.Request.RequestSHA256 != base.Request.RequestSHA256 || got.WatchBindingSHA256 == base.WatchBindingSHA256 {
		t.Fatal("nonce binding changed Core intent or was omitted")
	}
}

func TestServiceReadIntentRejectsUnacceptedMetadata(t *testing.T) {
	for _, mutate := range []func(*ServiceReadBinding){
		func(b *ServiceReadBinding) { b.Claim.ClaimID = "" }, func(b *ServiceReadBinding) { b.Claim.Fence = 0 }, func(b *ServiceReadBinding) { b.Claim.Definition.Version = 0 },
		func(b *ServiceReadBinding) { b.Claim.Deadline = time.Time{} },
		func(b *ServiceReadBinding) { b.Claim.LeaseUntil = b.Claim.Deadline.Add(time.Nanosecond) },
		func(b *ServiceReadBinding) { b.Connection.Space = "foreign" }, func(b *ServiceReadBinding) { b.Connection.LocalAuthorityID = "local" },
		func(b *ServiceReadBinding) { b.Bundle.BundleDigest = strings.Repeat("f", 64) },
		func(b *ServiceReadBinding) { b.QueryID = "unaccepted" }, func(b *ServiceReadBinding) { b.SourceID = strings.Repeat("f", 64) },
		func(b *ServiceReadBinding) { b.Bundle.Sources = nil; serviceReseal(t, b) },
		func(b *ServiceReadBinding) {
			b.Bundle.Sources = append(b.Bundle.Sources, b.Bundle.Sources[0])
			serviceReseal(t, b)
		},
		func(b *ServiceReadBinding) {
			q := b.Bundle.Queries["q"]
			q.JSON = json.RawMessage(`[]`)
			b.Bundle.Queries["q"] = q
			serviceReseal(t, b)
		},
		func(b *ServiceReadBinding) {
			q := b.Bundle.Queries["q"]
			q.JSON = json.RawMessage(`{"id":"q"}`)
			b.Bundle.Queries["q"] = q
			serviceReseal(t, b)
		},
		func(b *ServiceReadBinding) { b.Claim.Definition.Inputs["invalid"] = detective.InputValue{} },
		func(b *ServiceReadBinding) {
			v := apicontract.NewStringValue(string([]byte{0xff}))
			b.Claim.Definition.Inputs["invalid"] = detective.InputValue{Scalar: &v}
		},
		func(b *ServiceReadBinding) {
			v := apicontract.NewStringValue(strings.Repeat("x", 1<<20))
			b.Claim.Definition.Inputs["huge"] = detective.InputValue{Scalar: &v}
		},
	} {
		b, p := serviceIntentFixture(t)
		mutate(&b)
		got, err := BuildServiceReadIntent(context.Background(), b, p, strings.Repeat("a", 32))
		if err == nil || got != (ServiceReadIntent{}) {
			t.Fatal("unaccepted metadata produced intent")
		}
	}
	b, p := serviceIntentFixture(t)
	for _, ctx := range []context.Context{nil, func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }()} {
		if _, err := BuildServiceReadIntent(ctx, b, p, strings.Repeat("a", 32)); err == nil {
			t.Fatal("unusable context admitted")
		}
	}
	if _, err := BuildServiceReadIntent(context.Background(), b, p, "old"); err == nil {
		t.Fatal("invalid nonce admitted")
	}
	delete(p, "id")
	if _, err := BuildServiceReadIntent(context.Background(), b, p, strings.Repeat("a", 32)); err == nil {
		t.Fatal("missing mapped input accepted")
	}
	p["id"] = apicontract.ScalarValue(apicontract.NewIntegerValue("1"))
	p["label"] = apicontract.ScalarValue(apicontract.NewStringValue(strings.Repeat("x", 65536)))
	if _, err := BuildServiceReadIntent(context.Background(), b, p, strings.Repeat("a", 32)); err == nil {
		t.Fatal("oversized mapped input accepted")
	}
}

func TestServiceReadParameterRefusalsAndDefaults(t *testing.T) {
	b, _ := serviceIntentFixture(t)
	var q datatug.QueryDefWithFolderPath
	if json.Unmarshal(b.Bundle.Queries["q"].JSON, &q) != nil {
		t.Fatal("fixture")
	}
	base := map[string]apicontract.TypedValueOrSet{"id": apicontract.ScalarValue(apicontract.NewIntegerValue("1"))}
	for _, mutate := range []func(*datatug.QueryDef, map[string]apicontract.TypedValueOrSet){
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) { q.Type = datatug.QueryTypeSQL },
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) { q.StructuredBody = false },
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) {
			q.Parameters = append(q.Parameters, q.Parameters[0])
		},
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) { q.Parameters[0].ID = "" },
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) {
			q.Parameters[0].IsCredential = true
		},
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) {
			q.Parameters[1].Type = "unsupported"
		},
		func(q *datatug.QueryDef, _ map[string]apicontract.TypedValueOrSet) {
			q.Parameters[1].IsMultiValue = true
		},
		func(_ *datatug.QueryDef, p map[string]apicontract.TypedValueOrSet) { delete(p, "id") },
		func(_ *datatug.QueryDef, p map[string]apicontract.TypedValueOrSet) { p["unknown"] = p["id"] },
		func(_ *datatug.QueryDef, p map[string]apicontract.TypedValueOrSet) {
			p["id"] = apicontract.ScalarValue(apicontract.NewStringValue("1"))
		},
		func(_ *datatug.QueryDef, p map[string]apicontract.TypedValueOrSet) {
			p["id"] = apicontract.TypedValueOrSet{}
		},
		func(_ *datatug.QueryDef, p map[string]apicontract.TypedValueOrSet) {
			s, _ := apicontract.NewTypedValueSet(apicontract.NewIntegerValue("1"))
			p["id"] = apicontract.SetValue(s)
		},
	} {
		query := q.QueryDef
		query.Parameters = append(datatug.Parameters{}, q.Parameters...)
		p := map[string]apicontract.TypedValueOrSet{"id": base["id"]}
		mutate(&query, p)
		if _, err := normalizeServiceReadParameters(query, p); err == nil {
			t.Fatal("unsupported parameter shape accepted")
		}
	}
	for _, values := range []map[string]apicontract.TypedValueOrSet{nil, {"a.b": base["id"]}, {"a": apicontract.ScalarValue(apicontract.NewNumberValue(math.NaN()))}, {"a": apicontract.ScalarValue(apicontract.NewStringValue(string([]byte{0xff})))}, {"a": apicontract.ScalarValue(apicontract.NewStringValue(strings.Repeat("x", 65536)))}} {
		if _, err := serviceReadParametersJSON(values); err == nil {
			t.Fatal("invalid parameter JSON accepted")
		}
	}
	if _, err := ServiceConnectionSource(" "); err == nil {
		t.Fatal("invalid connection descriptor accepted")
	}
	invalidSet := apicontract.TypedValueSet{Values: []apicontract.TypedValue{apicontract.NewStringValue(string([]byte{0xff}))}}
	if serviceReadInput(detective.InputValue{Set: &invalidSet}) {
		t.Fatal("lossy root set accepted")
	}
}
