package accesspolicies

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/policyscope"
)

const scopedPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: factories}
default: deny
scopes:
  - path: /countries
    rules:
      - {id: never-factory-e, effect: deny, operations: [query], instance: factory-e-prod/factory}
      - {id: read-countries, effect: allow, operations: [query]}
`

func allowsCountries(t *testing.T, item Loaded) bool {
	t.Helper()
	resource := access.CollectionResourceFor(nil, "countries")
	return item.Policy.Decide(context.Background(), access.Request{Operation: access.Query, Resources: []access.Resource{resource}}).Allowed
}

func TestDecodeLoadedSelectorDocumentDeniesUntilProjected(t *testing.T) {
	item, err := DecodeLoaded([]byte(scopedPolicy), access.YAMLCodec{}, "factories.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !item.Scoped() || item.Policy.Name() != "factories" || allowsCountries(t, item) {
		t.Fatalf("a selector document must load as a named policy that denies every read: scoped=%v name=%q", item.Scoped(), item.Policy.Name())
	}
	if refusal := item.writes.refusal; !strings.Contains(refusal, "selects environments or instances") {
		t.Errorf("project writes under a selector document are refused: %q", refusal)
	}
	a := policyscope.Target{Environment: "factory-a-prod", Catalog: "factory", Kind: datatug.EnvironmentKindProduction}
	e := policyscope.Target{Environment: "factory-e-prod", Catalog: "factory", Kind: datatug.EnvironmentKindProduction}
	plain, err := DecodeLoaded([]byte(strings.ReplaceAll(scopedPolicy, ", instance: factory-e-prod/factory", "")), access.YAMLCodec{}, "plain.yaml")
	if err != nil || plain.Scoped() {
		t.Fatalf("plain document: %v", err)
	}
	loaded := []Loaded{item, plain}
	forA, forE, unknown := ForTarget(loaded, a), ForTarget(loaded, e), ForTarget(loaded, policyscope.Target{})
	if !allowsCountries(t, forA[0]) || forA[0].Scoped() || forA[0].Source != "factories.yaml" {
		t.Error("factory A reads countries")
	}
	if forA[0].writes != item.writes {
		t.Error("a projection keeps refusing project writes")
	}
	if allowsCountries(t, forE[0]) || forE[0].Scoped() {
		t.Error("factory E is denied by its rule")
	}
	if allowsCountries(t, unknown[0]) || !unknown[0].Scoped() {
		t.Error("an unknown instance keeps the deny-every-read policy")
	}
	if forA[1].digest != plain.digest || forE[1].digest != plain.digest {
		t.Error("a document with no selector is kept as it is")
	}
	if Fingerprint(forA, false, nil) == Fingerprint(forE, false, nil) {
		t.Error("the policy fingerprint names the projection")
	}
	if len(ForTarget(nil, a)) != 0 {
		t.Error("no policies, none projected")
	}
}

func TestDecodeLoadedSelectorRefusals(t *testing.T) {
	bad := strings.Replace(scopedPolicy, "factory-e-prod/factory", "factory-e-prod", 1)
	if _, err := DecodeLoaded([]byte(bad), access.YAMLCodec{}, "bad.yaml"); !errors.Is(err, policyscope.ErrInvalidSelector) {
		t.Errorf("a malformed instance refuses the document: %v", err)
	}
	broken := strings.Replace(scopedPolicy, "operations: [query], instance", "operations: [querry], instance", 1)
	if _, err := DecodeLoaded([]byte(broken), access.YAMLCodec{}, "broken.yaml"); err == nil || errors.Is(err, policyscope.ErrInvalidSelector) {
		t.Errorf("DALgo checks a selector rule at load: %v", err)
	}
	if _, err := DecodeLoaded([]byte(scopedPolicy), nil, "x"); !errors.Is(err, policyscope.ErrUnsupportedCodec) {
		t.Errorf("an uninspectable codec refuses the document: %v", err)
	}
	if _, err := DecodeLoaded([]byte("kind: [\n"), access.YAMLCodec{}, "x.yaml"); err == nil || !strings.Contains(err.Error(), "access:") {
		t.Errorf("a broken document keeps DALgo's own error: %v", err)
	}
}

func TestDenyEveryReadNameFallback(t *testing.T) {
	if name := denyEveryRead([]byte("metadata: [")).Name(); name != "policy" {
		t.Errorf("name = %q", name)
	}
}
