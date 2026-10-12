package accesspolicies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalQueryID_Unstable(t *testing.T) {
	orig := canonicalRoundFunc
	defer func() { canonicalRoundFunc = orig }()
	i := 0
	canonicalRoundFunc = func(s string) string {
		i++
		return fmt.Sprintf("unstable-%d", i)
	}
	res := CanonicalQueryID("abc")
	assert.Equal(t, unstableCanonicalID, res)
}

func TestResolveDir_And_Load_UserHomeDirError(t *testing.T) {
	origHome := userHomeDir
	defer func() { userHomeDir = origHome }()
	userHomeDir = func() (string, error) {
		return "", errors.New("no home dir")
	}
	_, _, err := ResolveDir("")
	assert.Error(t, err)

	_, err = Load(LoadOptions{})
	assert.Error(t, err)
}

func TestLoad_LoadDirOtherError(t *testing.T) {
	tmpDir := t.TempDir()
	badFile := filepath.Join(tmpDir, "broken.yaml")
	require.NoError(t, os.WriteFile(badFile, []byte(": bad yaml"), 0644))
	_, err := Load(LoadOptions{Dir: tmpDir})
	assert.Error(t, err)

	_, err = Load(LoadOptions{Files: []string{filepath.Join(tmpDir, "nonexistent.yaml")}})
	assert.Error(t, err)
}

func TestFingerprint_Variants(t *testing.T) {
	fp1 := Fingerprint(nil, true, nil)
	assert.NotEmpty(t, fp1)

	fp2 := Fingerprint(nil, false, nil)
	assert.NotEmpty(t, fp2)

	loaded := []Loaded{{digest: [32]byte{1, 2, 3}}}
	fpLoaded := Fingerprint(loaded, false, nil)
	assert.NotEmpty(t, fpLoaded)

	fp3 := Fingerprint(nil, false, &access.Principal{
		ID:     "user1",
		Roles:  []string{"roleB", "roleA"},
		Groups: []string{"groupY", "groupX"},
	})
	assert.NotEmpty(t, fp3)
}

func TestBaseResource_Variants(t *testing.T) {
	col := dal.NewCollectionRef("users", "", nil)
	r1 := BaseResource(dal.From(&col).NewQuery().SelectIntoRecordset())
	assert.Equal(t, "/users", r1.String())

	// default branch on query whose base is not collection ref/group
	qBase := dal.From(dal.NewCollectionRef("users", "", nil)).NewQuery().SelectIntoRecordset()
	r2 := BaseResource(mockNonCollectionQuery{qBase})
	assert.NotEmpty(t, r2.String())
}

type mockNonCollectionQuery struct {
	dal.StructuredQuery
}

func (m mockNonCollectionQuery) From() dal.FromSource {
	return mockFromSource{}
}

type mockFromSource struct {
	dal.FromSource
}

func (mockFromSource) Base() dal.RecordsetSource {
	return mockRecordsetSource{}
}

type mockRecordsetSource struct {
	dal.RecordsetSource
}

func TestBindingsFor_Duplicate(t *testing.T) {
	bindings := map[string]any{"v": 123}
	res := bindingsFor("$v > 0 && $v < 10", bindings)
	assert.Equal(t, []string{"v=123"}, res)
}

func TestFieldLists_EmptyAndTerminal(t *testing.T) {
	res := fieldLists(&access.WriteResidual{
		Alternatives: []access.WriteAlternative{
			{Fields: nil},
		},
		Terminal: &access.WriteAlternative{
			Fields: []string{"f1", "f2"},
		},
	})
	assert.Equal(t, [][]string{{"f1", "f2"}}, res)
}

func TestFields_PointerTypes_And_Errors(t *testing.T) {
	f := dal.NewFieldRef("", "f1")
	fID := dal.NewFieldRef("", "ID")
	q := dal.From(dal.NewCollectionRef("users", "", nil)).NewQuery().
		SelectColumns(dal.Column{Expression: &f, Alias: "f1"}, dal.Column{Expression: &fID, Alias: "ID"})
	names, err := referencedFields(q)
	require.NoError(t, err)
	assert.Contains(t, names, "f1")

	comp := dal.Comparison{Left: &f, Operator: dal.Equal, Right: dal.NewConstant("val")}
	grp := dal.NewGroupCondition(dal.And, &comp)
	q2 := dal.From(dal.NewCollectionRef("users", "", nil)).NewQuery().
		Where(&grp).
		SelectColumns(dal.Column{Expression: f, Alias: "f1"})
	names2, err := referencedFields(q2)
	require.NoError(t, err)
	assert.Contains(t, names2, "f1")

	aliases := aliasedColumns(dal.From(dal.NewCollectionRef("users", "", nil)).NewQuery().
		SelectColumns(dal.Column{Expression: f, Alias: "f1"}))
	assert.Empty(t, aliases)

	assert.False(t, FieldAllowed([]string{"a.b.c"}, "a.b"))
	assert.True(t, FieldAllowed([]string{"*"}, "anything"))
}

func TestFields_Errors(t *testing.T) {
	badExp := mockBadExpression{}
	err := walkComparison(dal.Comparison{Left: badExp}, func(e dal.Expression) error {
		if _, ok := e.(mockBadExpression); ok {
			return errors.New("bad expr")
		}
		return nil
	})
	assert.Error(t, err)

	err = walkGroup(dal.NewGroupCondition(dal.And, mockBadCondition{}), func(c dal.Condition) error {
		return errors.New("bad cond")
	})
	assert.Error(t, err)

	baseQ := dal.From(dal.NewCollectionRef("users", "", nil)).NewQuery().SelectIntoRecordset()
	qHaving := mockQueryWrapper{StructuredQuery: baseQ, having: mockBadCondition{}}
	_, err = referencedFields(qHaving)
	assert.Error(t, err)

	qOrder := mockQueryWrapper{StructuredQuery: baseQ, order: []dal.OrderExpression{dal.Ascending(badExp)}}
	_, err = referencedFields(qOrder)
	assert.Error(t, err)

	qGroup := mockQueryWrapper{StructuredQuery: baseQ, group: []dal.Expression{badExp}}
	_, err = referencedFields(qGroup)
	assert.Error(t, err)
}

type mockBadExpression struct{ dal.Expression }
type mockBadCondition struct{ dal.Condition }

type mockQueryWrapper struct {
	dal.StructuredQuery
	having dal.Condition
	order  []dal.OrderExpression
	group  []dal.Expression
}

func (m mockQueryWrapper) Having() dal.Condition {
	if m.having != nil {
		return m.having
	}
	return m.StructuredQuery.Having()
}

func (m mockQueryWrapper) OrderBy() []dal.OrderExpression {
	if m.order != nil {
		return m.order
	}
	return m.StructuredQuery.OrderBy()
}

func (m mockQueryWrapper) GroupBy() []dal.Expression {
	if m.group != nil {
		return m.group
	}
	return m.StructuredQuery.GroupBy()
}

func TestProjectWrites_EdgeCases(t *testing.T) {
	refusal := nestedScopeRefusal("rule1", "/path", "seg", 2)
	assert.Contains(t, refusal, "a record id")

	seg := pathSegment{value: "test"}
	assert.Equal(t, `"test"`, seg.String())

	rw := &rewriter{}
	rw.scope(&access.DocumentScope{Path: ""}, "rs", nil, false)
	rw.scope(&access.DocumentScope{Path: "relative/path"}, "rs", nil, false)

	_, _, ok := rw.rewritePath(&access.DocumentScope{Path: "relative/path"}, nil)
	assert.False(t, ok)

	_, _, ok = rw.rewritePath(&access.DocumentScope{Path: "/bad/%ZZ/path"}, nil)
	assert.False(t, ok)

	orig := canonicalRoundFunc
	defer func() { canonicalRoundFunc = orig }()
	canonicalRoundFunc = func(s string) string { return "*" }
	scope := &access.DocumentScope{Path: "/datatug_projects/p/queries/some_query"}
	_, _, ok = rw.rewritePath(scope, nil)
	assert.False(t, ok)
	assert.Contains(t, rw.refusal, "whose canonical form")
}

func TestWalkParams_PointerTypes(t *testing.T) {
	p := dal.NewParam("param1")
	comp := dal.Comparison{Left: p, Operator: dal.Equal, Right: p}
	var visited []string
	walkParams(&comp, false, func(e dal.Expression) {
		visited = append(visited, paramNames(e)...)
	})
	assert.Equal(t, []string{"$param1", "$param1"}, visited)

	grp := dal.NewGroupCondition(dal.And, &comp)
	var visitedGrp []string
	walkParams(&grp, false, func(e dal.Expression) {
		visitedGrp = append(visitedGrp, paramNames(e)...)
	})
	assert.Equal(t, []string{"$param1", "$param1"}, visitedGrp)

	assert.Equal(t, []string{"$param1"}, paramNames(&p))
}

func TestAuthorizeWrite_DeniedWithoutExplanation(t *testing.T) {
	mockP := mockDenyNoExplanationPolicy{}
	loaded := Loaded{
		Policy: mockP,
		Source: "test.yaml",
		writes: &projectWrites{
			policy: mockP,
		},
	}
	err := AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{loaded}}, access.Insert, ProjectQueryResource("p1", "q1"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no rule allows it")
}

type mockDenyNoExplanationPolicy struct{ access.Policy }

func (mockDenyNoExplanationPolicy) Name() string { return "deny_no_exp" }
func (mockDenyNoExplanationPolicy) Decide(context.Context, access.Request) access.Decision {
	return access.Decision{Allowed: false, Explanation: ""}
}

type mockAccessCodec struct {
	decodeErr        error
	encodeErr        error
	decodeCall       int
	failOnDecodeCall int
}

func (m *mockAccessCodec) Decode(r io.Reader, doc *access.Document) error {
	m.decodeCall++
	if m.decodeCall == m.failOnDecodeCall || m.decodeErr != nil {
		return errors.New("mock decode error")
	}
	return access.YAMLCodec{}.Decode(r, doc)
}

func (m *mockAccessCodec) Encode(w io.Writer, doc access.Document) error {
	if m.encodeErr != nil {
		return m.encodeErr
	}
	return access.YAMLCodec{}.Encode(w, doc)
}

func TestPrepareProjectWrites_CodecErrors(t *testing.T) {
	yamlData := []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata:\n  name: test\ndefault: deny\n")

	c1 := &mockAccessCodec{encodeErr: errors.New("encode failed")}
	res1 := prepareProjectWrites(yamlData, c1)
	assert.Contains(t, res1.refusal, "encode failed")

	c2 := &mockAccessCodec{failOnDecodeCall: 2}
	res2 := prepareProjectWrites(yamlData, c2)
	assert.Contains(t, res2.refusal, "mock decode error")

	c3 := &mockAccessCodec{failOnDecodeCall: 3}
	res3 := prepareProjectWrites(yamlData, c3)
	assert.Contains(t, res3.refusal, "mock decode error")
}
