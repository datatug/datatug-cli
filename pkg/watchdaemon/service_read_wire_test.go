package watchdaemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataaccess"
)

func serviceResponseFixture() ServiceReadResponse {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	c := dataaccess.ConnectionRef{Space: "space", ID: "ap-id", Fingerprint: dataaccess.Fingerprint{Engine: "sqlite", Database: "shop", EngineIdentity: "owned-native-identity"}}
	s := dataaccess.Subject{Realm: "realm", Kind: dataaccess.Service, ID: "enrollment"}
	v := dataaccess.Resolution{Subject: s, Connection: c, Protection: dataaccess.ConnectionProtection{ID: c.ID, Fingerprint: c.Fingerprint, Name: "shop", ReadOnly: true, ReadPosture: dataaccess.ReadExact, AgentRead: dataaccess.AgentNone, Revision: 1}, Grants: []dataaccess.Grant{}, ServerTime: now, ValidUntil: now.Add(ServiceReadLease), ExactRead: &dataaccess.ExactReadPolicy{Version: 1, Subject: s, Connection: c, Baseline: []dataaccess.ObjectRef{{Namespace: "main", Name: "A"}}}}
	return ServiceReadResponse{Contract: ServiceReadContract, Phase: ServiceReadResolve, QueryID: "q", QueryRevision: "1", BundleDigest: strings.Repeat("a", 64), SourceID: strings.Repeat("b", 64), EnrollmentRevision: 1, RunCeiling: now.Add(time.Minute), Nonce: strings.Repeat("a", 32), WatchBindingSHA256: strings.Repeat("c", 64), Resolution: &v}
}

func TestServiceReadWireRoundTrips(t *testing.T) {
	op := dataaccess.StructuredRead{TugQL: "intent", Parameters: "{}", MaxRows: ServiceReadMaxRows, MaxBytes: ServiceReadMaxBytes}
	req, err := op.RequestFor(strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []ServiceReadRequest{{Contract: ServiceReadContract, Phase: ServiceReadCatalog}, {Contract: ServiceReadContract, Phase: ServiceReadResolve, QueryID: "q", Parameters: map[string]apicontract.TypedValueOrSet{}, Request: &req}} {
		raw, e := json.Marshal(original)
		if e != nil {
			t.Fatal(e)
		}
		var got ServiceReadRequest
		if e = json.Unmarshal(raw, &got); e != nil {
			t.Fatal(e)
		}
		if got.Phase != original.Phase || got.QueryID != original.QueryID {
			t.Fatal("variant lost")
		}
		if original.Phase == ServiceReadCatalog && string(raw) != `{"contract":"watch-service-read/1","phase":"catalog"}` {
			t.Fatal("catalog contains authority fields")
		}
	}
	for _, original := range []ServiceReadResponse{{Contract: ServiceReadContract, Phase: ServiceReadCatalog, Mode: "protected", ProtectedSources: []string{strings.Repeat("a", 64)}}, serviceResponseFixture()} {
		raw, e := json.Marshal(original)
		if e != nil {
			t.Fatal(e)
		}
		var got ServiceReadResponse
		if e = json.Unmarshal(raw, &got); e != nil {
			t.Fatal(e)
		}
		if got.Phase != original.Phase {
			t.Fatal("variant lost")
		}
		if got.Phase == ServiceReadCatalog && (strings.Contains(string(raw), "runCeiling") || got.Resolution != nil) {
			t.Fatal("bootstrap gained authority")
		}
	}
}

func TestServiceReadWireClosedRequest(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{`, `{"contract":"watch-service-read/1","phase":"catalog"} {}`,
		`{"contract":"watch-service-read/1","Contract":"watch-service-read/1","phase":"catalog"}`,
		`{"Contract":"watch-service-read/1","phase":"catalog"}`,
		`{"contract":"watch-service-read/1","phase":"catalog","parameters":null}`,
		`{"contract":"watch-service-read/1","phase":"catalog","subject":{"kind":"service"}}`,
		`{"contract":"watch-service-read/2","phase":"catalog"}`,
		`{"contract":"watch-service-read/1","phase":"other"}`,
		`{"contract":"watch-service-read/1","phase":"resolve"}`,
		`{"contract":"watch-service-read/1","phase":"catalog","request":null}`,
		string([]byte{'{', 0xff, '}'}), strings.Repeat(" ", 1<<20+1),
	} {
		var got ServiceReadRequest
		got.QueryID = "stale"
		if json.Unmarshal([]byte(raw), &got) == nil {
			t.Fatal("invalid request admitted")
		}
		// Unmarshal's reset applies when encoding/json actually invokes it.
		if json.Valid([]byte(raw)) && got.QueryID != "" {
			t.Fatal("partial request escaped")
		}
	}
	base := ServiceReadRequest{Contract: ServiceReadContract, Phase: ServiceReadResolve, QueryID: "q", Parameters: map[string]apicontract.TypedValueOrSet{}}
	if base.Validate() == nil {
		t.Fatal("missing request admitted")
	}
	bad := dataaccess.ResultRequest{Nonce: "old", RequestSHA256: strings.Repeat("a", 64)}
	base.Request = &bad
	if base.Validate() == nil {
		t.Fatal("invalid nonce admitted")
	}
	base.Phase = ServiceReadCatalog
	if _, err := json.Marshal(base); err == nil {
		t.Fatal("cross-variant marshaled")
	}
	base.Phase = ServiceReadResolve
	validNonce, _ := (dataaccess.StructuredRead{TugQL: "intent", MaxRows: ServiceReadMaxRows, MaxBytes: ServiceReadMaxBytes}).RequestFor(strings.Repeat("a", 32))
	base.Request = &validNonce
	base.Parameters["invalid"] = apicontract.TypedValueOrSet{}
	if base.Validate() == nil {
		t.Fatal("invalid typed parameter accepted")
	}
	if serviceReadKeys([]byte("bad"), nil) {
		t.Fatal("malformed object accepted")
	}
}

func TestServiceReadWireClosedResponse(t *testing.T) {
	for _, mutate := range []func(*ServiceReadResponse){
		func(r *ServiceReadResponse) { r.Contract = "old" }, func(r *ServiceReadResponse) { r.Phase = "old" }, func(r *ServiceReadResponse) { r.Mode = "protected" },
		func(r *ServiceReadResponse) { r.Nonce = "old" }, func(r *ServiceReadResponse) { r.Resolution = nil }, func(r *ServiceReadResponse) {
			r.Resolution.Grants = []dataaccess.Grant{{Permissions: []dataaccess.Permission{dataaccess.DataWrite}}}
		},
		func(r *ServiceReadResponse) { r.Resolution.Protection.ReadOnly = false }, func(r *ServiceReadResponse) { r.Resolution.Subject.Kind = dataaccess.User },
		func(r *ServiceReadResponse) {
			r.Resolution.ValidUntil = r.Resolution.ServerTime.Add(ServiceReadLease + time.Nanosecond)
		},
		func(r *ServiceReadResponse) { r.RunCeiling = r.Resolution.ValidUntil.Add(-time.Nanosecond) },
	} {
		r := serviceResponseFixture()
		mutate(&r)
		if r.Validate() == nil {
			t.Fatal("invalid response accepted")
		}
		if _, err := json.Marshal(r); err == nil {
			t.Fatal("invalid response marshaled")
		}
	}
	for _, r := range []ServiceReadResponse{{Contract: ServiceReadContract, Phase: ServiceReadCatalog, Mode: "protected"}, {Contract: ServiceReadContract, Phase: ServiceReadCatalog, Mode: "protected", ProtectedSources: []string{strings.Repeat("a", 64), strings.Repeat("a", 64)}}, {Contract: ServiceReadContract, Phase: ServiceReadCatalog, Mode: "protected", ProtectedSources: []string{"invalid"}}} {
		if r.Validate() == nil {
			t.Fatal("invalid catalog classification accepted")
		}
	}
	valid, _ := json.Marshal(serviceResponseFixture())
	for _, raw := range []string{`{`, strings.Replace(string(valid), `"phase":"resolve"`, `"phase":"catalog"`, 1), strings.Replace(string(valid), `"phase":"resolve"`, `"phase":"resolve","privatePolicyGeneration":1`, 1), strings.Replace(string(valid), `"readOnly":true`, `"readOnly":true,"write":true`, 1), strings.Replace(string(valid), `"queryId":"q"`, `"QueryId":"q"`, 1), strings.Replace(string(valid), `"readOnly":true`, `"readOnly":false`, 1)} {
		var got ServiceReadResponse
		if json.Unmarshal([]byte(raw), &got) == nil {
			t.Fatal("invalid reply decoded")
		}
	}
}
