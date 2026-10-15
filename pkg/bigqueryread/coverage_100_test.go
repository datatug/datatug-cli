package bigqueryread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/dal-go/dalgo/dal"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func TestDecode_And_DecodeLimit(t *testing.T) {
	t.Parallel()

	var target struct {
		Name string `json:"name"`
	}

	// Limit < 1
	assert.ErrorIs(t, DecodeLimit(strings.NewReader("{}"), &target, 0), ErrInput)

	// Limit > MaxResponseBytes
	assert.ErrorIs(t, DecodeLimit(strings.NewReader("{}"), &target, bigquery.MaxResponseBytes+1), ErrInput)

	// Reader error
	assert.ErrorIs(t, DecodeLimit(iotest.ErrReader(errors.New("read error")), &target, 100), ErrInput)

	// Stream exceeds limit
	assert.ErrorIs(t, DecodeLimit(strings.NewReader(`{"name":"value"}`), &target, 5), ErrInput)

	// ParseJSON failure
	assert.ErrorIs(t, DecodeLimit(strings.NewReader("not json"), &target, 100), ErrInput)

	// Unknown field rejected
	assert.ErrorIs(t, DecodeLimit(strings.NewReader(`{"unknown_field": 1}`), &target, 100), ErrInput)

	// Success via DecodeLimit
	assert.NoError(t, DecodeLimit(strings.NewReader(`{"name":"valid"}`), &target, 100))
	assert.Equal(t, "valid", target.Name)

	// Success via Decode
	target.Name = ""
	assert.NoError(t, Decode(strings.NewReader(`{"name":"decoded"}`), &target))
	assert.Equal(t, "decoded", target.Name)
}

func TestDALQuery_Validation_And_Traversal(t *testing.T) {
	t.Parallel()

	// Columns > 128
	cols := make([]Column, 129)
	for i := range cols {
		cols[i] = Column{Field: "c"}
	}
	in := Input{Query: QueryShape{From: From{Name: "t"}, Columns: cols}}
	_, err := in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// OrderBy > 16
	orders := make([]Order, 17)
	for i := range orders {
		orders[i] = Order{Field: "c"}
	}
	in = Input{Query: QueryShape{From: From{Name: "t"}, Columns: []Column{{Field: "c"}}, OrderBy: orders}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Depth >= 8 in Where
	deepCond := &Condition{Op: "="}
	curr := deepCond
	for i := 0; i < 9; i++ {
		curr.And = []Condition{{Op: "="}}
		curr = &curr.And[0]
	}
	in = Input{Query: QueryShape{From: From{Name: "t"}, Columns: []Column{{Field: "c"}}, Where: deepCond}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Nodes > 128 in Where
	manyConds := make([]Condition, 130)
	for i := range manyConds {
		manyConds[i] = Condition{Op: "="}
	}
	in = Input{Query: QueryShape{From: From{Name: "t"}, Columns: []Column{{Field: "c"}}, Where: &Condition{And: manyConds}}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Expression.Value with invalid JSON
	in = Input{Query: QueryShape{
		From:    From{Name: "t"},
		Columns: []Column{{Field: "c"}},
		Where: &Condition{
			Left: &Expression{Value: json.RawMessage("invalid-json")},
		},
	}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Expression.Value with map[string]any
	in = Input{Query: QueryShape{
		From:    From{Name: "t"},
		Columns: []Column{{Field: "c"}},
		Where: &Condition{
			Right: &Expression{Value: json.RawMessage(`{"a":1}`)},
		},
	}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Expression.Value with []any > 1000
	hugeList := make([]string, 1001)
	for i := range hugeList {
		hugeList[i] = "1"
	}
	hugeJSON, _ := json.Marshal(hugeList)
	in = Input{Query: QueryShape{
		From:    From{Name: "t"},
		Columns: []Column{{Field: "c"}},
		Where: &Condition{
			Left: &Expression{Value: json.RawMessage(hugeJSON)},
		},
	}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Expression.Value with nested map in list
	in = Input{Query: QueryShape{
		From:    From{Name: "t"},
		Columns: []Column{{Field: "c"}},
		Where: &Condition{
			Left: &Expression{Value: json.RawMessage(`[{"a":1}]`)},
		},
	}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Expression.Value with nested list in list
	in = Input{Query: QueryShape{
		From:    From{Name: "t"},
		Columns: []Column{{Field: "c"}},
		Where: &Condition{
			Left: &Expression{Value: json.RawMessage(`[[1, 2]]`)},
		},
	}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Or branch error propagation
	in = Input{Query: QueryShape{
		From:    From{Name: "t"},
		Columns: []Column{{Field: "c"}},
		Where: &Condition{
			Or: []Condition{{Left: &Expression{Value: json.RawMessage(`{"bad":true}`)}}},
		},
	}}
	_, err = in.DALQuery()
	assert.ErrorIs(t, err, ErrInput)

	// Guard error (e.g. empty Columns or From)
	in = Input{Query: QueryShape{
		From: From{Name: ""},
	}}
	_, err = in.DALQuery()
	assert.Error(t, err)

	// Guard error from invalid limit (> 10000)
	in = Input{Query: QueryShape{
		From:    From{Name: "table"},
		Columns: []Column{{Field: "id"}},
		Limit:   10001,
	}}
	_, err = in.DALQuery()
	assert.Error(t, err)

	// Valid query
	in = Input{Query: QueryShape{
		From:    From{Name: "table"},
		Columns: []Column{{Field: "id"}, {Field: "name"}},
		Where: &Condition{
			Op:    "==",
			Left:  &Expression{Field: "id"},
			Right: &Expression{Value: json.RawMessage(`"123"`)},
		},
		Limit: 10,
	}}
	q, err := in.DALQuery()
	require.NoError(t, err)
	assert.NotNil(t, q)
}

// Not parallel: mutates jsonMarshal, dtqlDeserialize
func TestDALQuery_SeamFailures(t *testing.T) {
	in := Input{Query: QueryShape{
		From:    From{Name: "table"},
		Columns: []Column{{Field: "id"}},
		Limit:   10,
	}}

	origMarshal := jsonMarshal
	jsonMarshal = func(v any) ([]byte, error) { return nil, errors.New("simulated marshal err") }
	_, err := in.DALQuery()
	jsonMarshal = origMarshal
	assert.ErrorIs(t, err, ErrInput)

	origDTQL := dtqlDeserialize
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) { return nil, errors.New("simulated dtql err") }
	_, err = in.DALQuery()
	dtqlDeserialize = origDTQL
	assert.ErrorIs(t, err, ErrInput)
}

func TestDriverQuery_And_ValidINArray(t *testing.T) {
	t.Parallel()

	// Non-StructuredQuery passes through
	passthrough, err := driverQuery(nil, false)
	assert.NoError(t, err)
	assert.Nil(t, passthrough)

	baseQuery := dal.NewQueryBuilder(dal.From(dal.NewCollectionRef("sample", "", nil))).Limit(1).SelectColumns(dal.Column{Expression: dal.NewFieldRef("", "c")})

	// Depth >= 8 in driverQuery
	var cond dal.Condition = dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, dal.String("v"))
	for i := 0; i < 9; i++ {
		cond = dal.NewGroupCondition(dal.And, cond)
	}
	deepQuery := dal.WithWhere(baseQuery, cond)
	_, err = driverQuery(deepQuery, false)
	assert.ErrorIs(t, err, ErrInput)

	// Comparison pointers: *dal.Comparison nil
	var nilComp *dal.Comparison
	nilCompQuery := dal.WithWhere(baseQuery, nilComp)
	_, err = driverQuery(nilCompQuery, false)
	assert.ErrorIs(t, err, ErrInput)

	// *dal.Comparison non-nil
	validComp := dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, dal.String("v"))
	ptrCompQuery := dal.WithWhere(baseQuery, &validComp)
	adapted, err := driverQuery(ptrCompQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	// GroupCondition pointers: *dal.GroupCondition nil
	var nilGroup *dal.GroupCondition
	nilGroupQuery := dal.WithWhere(baseQuery, nilGroup)
	_, err = driverQuery(nilGroupQuery, false)
	assert.ErrorIs(t, err, ErrInput)

	// *dal.GroupCondition non-nil
	validGroup := dal.NewGroupCondition(dal.And, validComp)
	ptrGroupQuery := dal.WithWhere(baseQuery, &validGroup)
	adapted, err = driverQuery(ptrGroupQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	// Right is *dal.Param == nil
	var nilParam *dal.Param
	paramNilQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, nilParam))
	_, err = driverQuery(paramNilQuery, true)
	assert.ErrorIs(t, err, ErrInput)

	// Right is *dal.Param != nil
	p := dal.NewParam("p")
	paramPtrQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, &p))
	adapted, err = driverQuery(paramPtrQuery, true)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	// Right is dal.Param with parameters = true and false
	paramQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, dal.NewParam("p")))
	adaptedTrue, err := driverQuery(paramQuery, true)
	assert.NoError(t, err)
	assert.NotNil(t, adaptedTrue)
	adaptedFalse, err := driverQuery(paramQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adaptedFalse)

	// Right is *dal.Array == nil
	var nilArray *dal.Array
	arrayNilQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.In, nilArray))
	_, err = driverQuery(arrayNilQuery, false)
	assert.ErrorIs(t, err, ErrInput)

	// Right is *dal.Array != nil
	arr := dal.Array{Value: []string{"a", "b"}}
	arrayPtrQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.In, &arr))
	adapted, err = driverQuery(arrayPtrQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	// Right is dal.Array with operator != dal.In
	arrayNotINQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, arr))
	adapted, err = driverQuery(arrayNotINQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	// In operator with invalid array
	invalidINQuery := dal.WithWhere(baseQuery, dal.NewComparison(dal.NewFieldRef("", "c"), dal.In, dal.Array{Value: []int{1, 2}}))
	_, err = driverQuery(invalidINQuery, false)
	assert.ErrorIs(t, err, ErrInput)

	// Default condition branch (e.g. UnaryCondition or nil Condition)
	nilCondQuery := dal.WithWhere(baseQuery, nil)
	adapted, err = driverQuery(nilCondQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	otherQuery := dal.WithWhere(baseQuery, otherCondition{})
	adapted, err = driverQuery(otherQuery, false)
	assert.NoError(t, err)
	assert.NotNil(t, adapted)

	// validINArray checks
	assert.False(t, validINArray(nil))
	assert.False(t, validINArray(123))
	assert.False(t, validINArray("string"))
	assert.False(t, validINArray(make([]string, 1001)))
	assert.True(t, validINArray([]bool{true, false}))
	assert.True(t, validINArray([]string{"a", "b"}))
	assert.False(t, validINArray([]int{1, 2}))
	assert.False(t, validINArray([]any{"a", true}))
}

// Not parallel: mutates bigqueryCompile, driverQueryHook
func TestPrepare_And_PrepareWithReport(t *testing.T) {
	validInput := Input{
		Profile: bigquery.SourceProfile{SourceProject: "proj", DatasetID: "ds", TableID: "t"},
		Query: QueryShape{
			From:    From{Name: "table"},
			Columns: []Column{{Field: "c"}},
			Limit:   10,
		},
	}

	validOptions := func() (accesspolicies.Options, error) {
		return accesspolicies.Options{Unrestricted: true}, nil
	}

	// Success via Prepare and PrepareWithReport
	origCompile := bigqueryCompile
	defer func() { bigqueryCompile = origCompile }()
	bigqueryCompile = func(profile bigquery.SourceProfile, query dal.Query) (bigquery.ReadPlan, error) {
		return bigquery.ReadPlan{Version: 1}, nil
	}

	preparer := Preparer{Input: validInput, Options: validOptions}
	plan, fingerprint, lines, err := preparer.PrepareWithReport(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, fingerprint)
	assert.Nil(t, lines)
	assert.NotNil(t, plan)

	plan2, fingerprint2, err := preparer.Prepare(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fingerprint, fingerprint2)
	assert.NotNil(t, plan2)

	// Input.DALQuery() error
	invalidInput := Input{Query: QueryShape{From: From{Name: ""}}}
	preparerBadInput := Preparer{Input: invalidInput, Options: validOptions}
	_, _, _, err = preparerBadInput.PrepareWithReport(context.Background())
	assert.Error(t, err)

	// Options() error
	badOptions := func() (accesspolicies.Options, error) {
		return accesspolicies.Options{}, errors.New("simulated options err")
	}
	preparerBadOpts := Preparer{Input: validInput, Options: badOptions}
	_, _, _, err = preparerBadOpts.PrepareWithReport(context.Background())
	assert.ErrorContains(t, err, "simulated options err")

	// accesspolicies.PrepareRecordset error (no policies and not unrestricted)
	deniedOptions := func() (accesspolicies.Options, error) {
		return accesspolicies.Options{Unrestricted: false}, nil
	}
	preparerDenied := Preparer{Input: validInput, Options: deniedOptions}
	_, _, _, err = preparerDenied.PrepareWithReport(context.Background())
	assert.Error(t, err)

	// driverQueryHook error
	origDriver := driverQueryHook
	driverQueryHook = func(query dal.Query, parameters bool) (dal.Query, error) {
		return nil, errors.New("simulated driverQuery error")
	}
	_, _, _, err = preparer.PrepareWithReport(context.Background())
	driverQueryHook = origDriver
	assert.ErrorContains(t, err, "simulated driverQuery error")

	// bigqueryCompile error
	bigqueryCompile = func(profile bigquery.SourceProfile, query dal.Query) (bigquery.ReadPlan, error) {
		return bigquery.ReadPlan{}, errors.New("simulated compile error")
	}
	_, _, _, err = preparer.PrepareWithReport(context.Background())
	bigqueryCompile = origCompile
	assert.ErrorContains(t, err, "simulated compile error")
}

// Not parallel: mutates findADC, randRead, osOpenFile, osSameFile, httpNewRequestWithContext
func TestProvider_CoverageSeams(t *testing.T) {
	// NewGoogleProvider with invalid directory
	_, err := NewGoogleProvider("relative/dir", false)
	assert.ErrorIs(t, err, ErrIdentity)

	// NewADCProvider with invalid directory
	_, err = NewADCProvider("relative/dir", false)
	assert.ErrorIs(t, err, ErrIdentity)

	// NewADCProvider tokenSource findADC error
	validDir := filepath.Join(t.TempDir(), "session")
	_, err = bigquery.NewFileLedger(validDir)
	require.NoError(t, err)

	origFindADC := findADC
	findADC = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return nil, errors.New("findADC error")
	}
	pADC, err := NewADCProvider(validDir, false)
	require.NoError(t, err)
	_, _, err = pADC.Authorize(context.Background(), http.DefaultTransport)
	findADC = origFindADC
	assert.ErrorIs(t, err, ErrIdentity)

	// NewADCProvider tokenSource bad JSON in credentials
	findADC = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{JSON: []byte("not-json")}, nil
	}
	pADC, err = NewADCProvider(validDir, false)
	require.NoError(t, err)
	_, _, err = pADC.Authorize(context.Background(), http.DefaultTransport)
	findADC = origFindADC
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce: randRead error
	nonceDir := filepath.Join(t.TempDir(), "nonce")
	_, err = bigquery.NewFileLedger(nonceDir)
	require.NoError(t, err)

	origRand := randRead
	randRead = func(b []byte) (int, error) { return 0, errors.New("rand fail") }
	_, err = sessionNonce(nonceDir)
	randRead = origRand
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce: osOpenFile error (not os.IsExist)
	origOpen := osOpenFile
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("open fail")
	}
	_, err = sessionNonce(nonceDir)
	osOpenFile = origOpen
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce: file truncated (len != 64)
	nonceFile := filepath.Join(nonceDir, "authorization-session")
	require.NoError(t, os.WriteFile(nonceFile, []byte("short"), 0600))
	_, err = sessionNonce(nonceDir)
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce: invalid hex
	require.NoError(t, os.WriteFile(nonceFile, []byte(strings.Repeat("z", 64)), 0600))
	_, err = sessionNonce(nonceDir)
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce: osSameFile returns false
	require.NoError(t, os.Remove(nonceFile))
	origSame := osSameFile
	osSameFile = func(fi1, fi2 os.FileInfo) bool { return false }
	_, err = sessionNonce(nonceDir)
	osSameFile = origSame
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce with relative dir
	_, err = sessionNonce("relative/dir")
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce write error (line 104)
	nonceDir2 := filepath.Join(t.TempDir(), "nonce2")
	_, err = bigquery.NewFileLedger(nonceDir2)
	require.NoError(t, err)

	origOpen = osOpenFile
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDONLY, perm)
	}
	_, err = sessionNonce(nonceDir2)
	osOpenFile = origOpen
	assert.ErrorIs(t, err, ErrIdentity)

	// sessionNonce osOpen error (line 115)
	origOsOpen := osOpen
	osOpen = func(name string) (*os.File, error) {
		return nil, errors.New("open error")
	}
	_, err = sessionNonce(nonceDir)
	osOpen = origOsOpen
	assert.ErrorIs(t, err, ErrIdentity)

	// Authorize: tokenSource error
	p, err := NewGoogleProvider(validDir, false)
	require.NoError(t, err)
	p.tokenSource = func(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
		return nil, errors.New("source error")
	}
	_, _, err = p.Authorize(context.Background(), http.DefaultTransport)
	assert.ErrorIs(t, err, ErrIdentity)

	// Authorize: source.Token() error (line 146)
	p.tokenSource = func(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
		return errTokenSource{}, nil
	}
	_, _, err = p.Authorize(context.Background(), http.DefaultTransport)
	assert.ErrorIs(t, err, ErrIdentity)

	// AuthorizeToken success
	now := time.Now()
	token := tokenWithScopes("valid-token", readScope+" openid", now.Add(time.Hour))
	pValid, err := NewGoogleProvider(validDir, false)
	require.NoError(t, err)
	pValid.transport = providerRT(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sub":"user-123"}`)), Header: make(http.Header)}, nil
	})
	ident, _, err := pValid.AuthorizeToken(context.Background(), token, http.DefaultTransport)
	require.NoError(t, err)
	assert.Equal(t, "user-123", ident.Principal.Subject)

	// authorizeToken: httpNewRequestWithContext error
	origReq := httpNewRequestWithContext
	httpNewRequestWithContext = func(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
		return nil, errors.New("request creation error")
	}
	_, _, err = pValid.AuthorizeToken(context.Background(), token, http.DefaultTransport)
	httpNewRequestWithContext = origReq
	assert.ErrorIs(t, err, ErrIdentity)

	// authorizeToken: UserInfo body JSON is not map
	pValid.transport = providerRT(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`"string-not-map"`)), Header: make(http.Header)}, nil
	})
	_, _, err = pValid.AuthorizeToken(context.Background(), token, http.DefaultTransport)
	assert.ErrorIs(t, err, ErrIdentity)

	// authorizeToken: token expires during UserInfo check (line 217)
	pValid.transport = providerRT(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sub":"user-123"}`)), Header: make(http.Header)}, nil
	})
	callCount := 0
	pValid.now = func() time.Time {
		callCount++
		if callCount > 1 {
			return token.Expiry.Add(time.Second)
		}
		return token.Expiry.Add(-time.Second)
	}
	_, _, err = pValid.AuthorizeToken(context.Background(), token, http.DefaultTransport)
	assert.ErrorIs(t, err, ErrIdentity)
}

type otherCondition struct{}

func (otherCondition) String() string { return "other" }

type errTokenSource struct{}

func (errTokenSource) Token() (*oauth2.Token, error) {
	return nil, errors.New("simulated token source error")
}
