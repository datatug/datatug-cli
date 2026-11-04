package accesspolicies

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

// A null test reads its operand's fields like any other reference: allowed
// when every field is visible, reported hidden otherwise (datatug-cli#316).
func TestHiddenReferencesIsNull(t *testing.T) {
	t.Parallel()
	lines := []Line{{FieldLists: [][]string{{"id", "name", "public_*"}}}}
	from := dal.From(dal.NewRootCollectionRef("customers", ""))
	allowedNull := dal.NewIsNullCondition(dal.Field("name"))
	allowed := dal.NewQueryBuilder(from).
		Where(allowedNull, dal.NewGroupCondition(dal.Or, dal.NewIsNotNullCondition(dal.Field("public_bio")), &allowedNull)).
		SelectColumns()
	if hidden, err := hiddenReferences(allowed, lines); err != nil || len(hidden) != 0 {
		t.Errorf("null test on allowed fields = %v, %v", hidden, err)
	}
	hiddenNull := dal.NewIsNotNullCondition(dal.Field("ssn"))
	for name, where := range map[string]dal.Condition{
		"value":   dal.NewIsNullCondition(dal.Field("passwordHash")),
		"pointer": &hiddenNull,
	} {
		probing := dal.NewQueryBuilder(from).Where(where).SelectColumns()
		hidden, err := hiddenReferences(probing, lines)
		if err != nil || len(hidden) != 1 || (hidden[0] != "passwordHash" && hidden[0] != "ssn") {
			t.Errorf("%s: null test on hidden field = %v, %v", name, hidden, err)
		}
	}
	// An operand the expression walker cannot read still fails closed.
	opaque := dal.NewQueryBuilder(from).Where(dal.NewIsNullCondition(fakeExpression{})).SelectColumns()
	if _, err := hiddenReferences(opaque, lines); err == nil || !strings.Contains(err.Error(), "unsupported expression") {
		t.Errorf("opaque operand = %v", err)
	}
}

// Under a field-restricted policy a null test on an allowed field runs and one
// on a hidden field is denied before the session sees it.
func TestRunIsNullUnderFieldPolicy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePolicy(t, dir, "global.yaml", "global")
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Principal: &access.Principal{ID: "alice"}, Policies: loaded}
	from := dal.From(dal.NewRootCollectionRef("customers", ""))
	stub := &stubSession{}
	allowed := dal.NewQueryBuilder(from).Where(dal.NewIsNullCondition(dal.Field("name"))).SelectColumns()
	if _, err := Run(context.Background(), stub, allowed, options); err != nil || stub.query == nil || !strings.Contains(stub.query.String(), "name IS NULL") {
		t.Errorf("allowed null test = %v, %v", err, stub.query)
	}
	stub.query = nil
	denied := dal.NewQueryBuilder(from).Where(dal.NewIsNotNullCondition(dal.Field("passwordHash"))).SelectColumns()
	if _, err := Run(context.Background(), stub, denied, options); !errors.Is(err, access.ErrAccessDenied) || !strings.Contains(err.Error(), "passwordHash") || stub.query != nil {
		t.Errorf("hidden null test = %v, %v", err, stub.query)
	}
}

// A parameter cannot hide inside a null test's operand: there is no
// right-hand side for it to legitimately occupy.
func TestMisplacedParamsInNullTest(t *testing.T) {
	t.Parallel()
	pointer := dal.NewIsNotNullCondition(dal.NewParam("ptr"))
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).
		Where(dal.NewIsNullCondition(dal.NewParam("val")), &pointer, dal.NewIsNullCondition(dal.Field("kind"))).
		SelectColumns()
	if got := strings.Join(misplacedParams(query), ","); got != "$ptr,$val" {
		t.Errorf("misplaced = %q", got)
	}
	if got := strings.Join(paramsIn(query.Where()), ","); got != "$ptr,$val" {
		t.Errorf("paramsIn = %q", got)
	}
	if _, err := Run(context.Background(), &stubSession{}, query, Options{Variables: map[string]any{"val": 1, "ptr": 1}, Unrestricted: true}); !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), "$ptr, $val") {
		t.Errorf("run = %v", err)
	}
}

// Policy row conditions keep rejecting null tests on purpose (layered-ACL
// acceptance V02: a missing field never matches); only queries gained them.
func TestPolicyConditionRejectsNullTest(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"isNull", "isNotNull"} {
		document := strings.Replace(strings.Replace(testPolicy, "%s", "nulls", 1),
			"op: \"==\"\n          left: { field: ownerID }\n          right: { param: currentUser }",
			key+": { field: ownerID }", 1)
		if !strings.Contains(document, key+":") {
			t.Fatal("policy fixture did not take the null test")
		}
		if _, err := DecodeLoaded([]byte(document), access.YAMLCodec{}, "nulls.yaml"); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s in a policy where must be rejected: %v", key, err)
		}
	}
}
