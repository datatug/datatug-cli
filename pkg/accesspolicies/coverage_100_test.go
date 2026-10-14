package accesspolicies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Not parallel: modifies global canonicalRoundFunc
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

// Not parallel: modifies global userHomeDir
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
	t.Parallel()
	tmpDir := t.TempDir()
	badFile := filepath.Join(tmpDir, "broken.yaml")
	require.NoError(t, os.WriteFile(badFile, []byte(": bad yaml"), 0644))
	_, err := Load(LoadOptions{Dir: tmpDir})
	assert.Error(t, err)

	_, err = Load(LoadOptions{Files: []string{filepath.Join(tmpDir, "nonexistent.yaml")}})
	assert.Error(t, err)
}

func TestFingerprint_Variants(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	bindings := map[string]any{"v": 123}
	res := bindingsFor("$v > 0 && $v < 10", bindings)
	assert.Equal(t, []string{"v=123"}, res)
}

func TestFieldLists_EmptyAndTerminal(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Not parallel: modifies global canonicalRoundFunc
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
	t.Parallel()
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
	t.Parallel()
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

func TestPrepareRecordset_And_ExactVariables(t *testing.T) {
	t.Parallel()

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("T", ""))).SelectColumns()
	// PrepareRecordset tests
	_, err := PrepareRecordset(context.Background(), q, Options{Unrestricted: true}, nil)
	assert.ErrorIs(t, err, ErrInvalidQuery)

	_, err = PrepareRecordset(context.Background(), q, Options{Unrestricted: true}, func(query dal.Query) error {
		return errors.New("guard fail")
	})
	assert.EqualError(t, err, "guard fail")

	// prepareRead fails (no policies and not unrestricted)
	_, err = PrepareRecordset(context.Background(), q, Options{}, func(query dal.Query) error { return nil })
	assert.ErrorIs(t, err, ErrNoPolicies)

	res, err := PrepareRecordset(context.Background(), q, Options{Unrestricted: true}, func(query dal.Query) error { return nil })
	assert.NoError(t, err)
	assert.NotNil(t, res.Query)

	tmp := t.TempDir()
	pPath := filepath.Join(tmp, "allow.yaml")
	policyDoc := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: allow
default: deny
scopes:
  - path: /T
    rules:
      - id: allow-all
        effect: allow
        operations: [query]
`
	require.NoError(t, os.WriteFile(pPath, []byte(policyDoc), 0600))
	loaded, err := LoadFile(pPath)
	require.NoError(t, err)
	res, err = PrepareRecordset(context.Background(), q, Options{Policies: []Loaded{loaded}}, func(query dal.Query) error { return nil })
	assert.NoError(t, err)
	assert.NotNil(t, res.Query)

	callCount := 0
	_, err = PrepareRecordset(context.Background(), q, Options{Unrestricted: true}, func(q dal.Query) error {
		callCount++
		if callCount > 1 {
			return errors.New("fail on capture")
		}
		return nil
	})
	assert.EqualError(t, err, "fail on capture")

	rc := &recordsetCapture{}
	assert.ErrorIs(t, rc.Get(context.Background(), nil), dal.ErrNotSupported)
	assert.ErrorIs(t, rc.GetMulti(context.Background(), nil), dal.ErrNotSupported)
	_, err = rc.Exists(context.Background(), nil)
	assert.ErrorIs(t, err, dal.ErrNotSupported)
	_, err = rc.ExecuteQueryToRecordsReader(context.Background(), nil)
	assert.ErrorIs(t, err, dal.ErrNotSupported)

	// ExactVariables tests
	// depth > 64
	var deeplyNested any = 1
	for i := 0; i < 70; i++ {
		deeplyNested = []any{deeplyNested}
	}
	_, err = ExactVariables(map[string]any{"deep": deeplyNested})
	assert.ErrorContains(t, err, "exceeds 64 levels")

	// uint64 valid and invalid
	v, err := ExactVariables(map[string]any{"u64": uint64(123)})
	assert.NoError(t, err)
	assert.Equal(t, uint64(123), v["u64"])

	_, err = ExactVariables(map[string]any{"u64_bad": uint64(9007199254740992)})
	assert.ErrorContains(t, err, "exceeds the executor's exact comparison range")

	// uint valid
	v, err = ExactVariables(map[string]any{"u": uint(123)})
	assert.NoError(t, err)

	// float32 & float64 valid
	v, err = ExactVariables(map[string]any{"f32": float32(1.5), "f64": float64(2.5)})
	assert.NoError(t, err)

	// json.Number: invalid, float exact (0.5), float non-exact (0.1)
	_, err = ExactVariables(map[string]any{"bad_num": json.Number("not_a_num")})
	assert.ErrorContains(t, err, "exact numeric binding is unsupported")

	v, err = ExactVariables(map[string]any{"exact_float": json.Number("0.5")})
	assert.NoError(t, err)
	assert.Equal(t, 0.5, v["exact_float"])

	_, err = ExactVariables(map[string]any{"inexact_float": json.Number("0.1")})
	assert.ErrorContains(t, err, "exact decimal binding is unsupported")

	// []byte
	bytesVal := []byte("hello")
	v, err = ExactVariables(map[string]any{"b": bytesVal})
	assert.NoError(t, err)
	assert.Equal(t, bytesVal, v["b"])

	// []any valid and error
	v, err = ExactVariables(map[string]any{"arr": []any{int(1), "two"}})
	assert.NoError(t, err)

	_, err = ExactVariables(map[string]any{"arr_err": []any{struct{}{}}})
	assert.Error(t, err)

	// reflect types: int8, uint8, float, struct/map
	type MyInt8 int8
	type MyUint8 uint8
	type MyFloat float32
	v, err = ExactVariables(map[string]any{
		"mi8":  MyInt8(8),
		"mu8":  MyUint8(8),
		"mf":   MyFloat(1.25),
		"str":  "text",
		"bool": true,
	})
	assert.NoError(t, err)

	// array error
	_, err = ExactVariables(map[string]any{"bad_arr": [1]struct{}{{}}})
	assert.Error(t, err)

	// map / struct error
	_, err = ExactVariables(map[string]any{"map": map[string]int{"a": 1}})
	assert.ErrorContains(t, err, "unsupported parameter container")

	// json.Number with float overflow
	huge := "1" + strings.Repeat("0", 400) + ".5"
	_, err = ExactVariables(map[string]any{"huge": json.Number(huge)})
	assert.ErrorContains(t, err, "exact numeric binding is unsupported")

	// Test capture.calls != 1 via wrapRecordsetSession
	origWrap := wrapRecordsetSession
	defer func() { wrapRecordsetSession = origWrap }()
	wrapRecordsetSession = func(s dal.ReadSession) dal.ReadSession {
		return &stubNoopRecordsetSession{}
	}
	_, err = PrepareRecordset(context.Background(), q, Options{Unrestricted: true}, func(query dal.Query) error { return nil })
	wrapRecordsetSession = origWrap
	assert.ErrorContains(t, err, "policy capture did not yield exactly one query")
}

type stubNoopRecordsetSession struct{}

func (stubNoopRecordsetSession) Get(context.Context, record.Record) error        { return nil }
func (stubNoopRecordsetSession) GetMulti(context.Context, []record.Record) error { return nil }
func (stubNoopRecordsetSession) Exists(context.Context, *record.Key) (bool, error) {
	return false, nil
}
func (stubNoopRecordsetSession) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return nil, nil
}
func (stubNoopRecordsetSession) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, nil
}
