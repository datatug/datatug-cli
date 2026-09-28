package endpoints

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/personalqueries"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/semantic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTypedValueConvert_FullCoverage(t *testing.T) {
	// nativeValue error on huge integer
	hugeInt := apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "99999999999999999999999999999999999999"}
	_, err := nativeValue(hugeInt)
	assert.Error(t, err)

	// nativeValue number
	nvNum, err := nativeValue(apicontract.NewNumberValue(12.34))
	assert.NoError(t, err)
	assert.Equal(t, 12.34, nvNum)

	// nativeValue boolean
	nvBool, err := nativeValue(apicontract.NewBooleanValue(true))
	assert.NoError(t, err)
	assert.Equal(t, true, nvBool)

	// nativeValue null
	nvNull, err := nativeValue(apicontract.NewNullValue())
	assert.NoError(t, err)
	assert.Nil(t, nvNull)

	// nativeValue unknown type
	_, err = nativeValue(apicontract.TypedValue{Type: "unknown"})
	assert.Error(t, err)

	// typedValueEqual number
	assert.True(t, typedValueEqual(apicontract.NewNumberValue(1.5), apicontract.NewNumberValue(1.5)))
	assert.False(t, typedValueEqual(apicontract.NewNumberValue(1.5), apicontract.NewNumberValue(2.5)))

	// typedValueEqual boolean
	assert.True(t, typedValueEqual(apicontract.NewBooleanValue(true), apicontract.NewBooleanValue(true)))
	assert.False(t, typedValueEqual(apicontract.NewBooleanValue(true), apicontract.NewBooleanValue(false)))

	// typedValueEqual null
	assert.True(t, typedValueEqual(apicontract.NewNullValue(), apicontract.NewNullValue()))

	// newIntegerText validate error
	_, err = newIntegerText("invalid-int")
	assert.Error(t, err)

	// newDecimalText validate error
	_, err = newDecimalText("invalid-decimal")
	assert.Error(t, err)

	// newDateText validate error
	_, err = newDateText("invalid-date")
	assert.Error(t, err)

	// newDateTimeValue
	dtVal := newDateTimeValue(time.Now())
	assert.Equal(t, apicontract.ValueTypeDatetime, dtVal.Type)

	// fromGoValue apicontract.TypedValue
	origTV := apicontract.NewStringValue("hello")
	tv, err := fromGoValue(origTV, "")
	assert.NoError(t, err)
	assert.Equal(t, origTV, tv)

	// fromGoValue []byte
	tv, err = fromGoValue([]byte("world"), "")
	assert.NoError(t, err)
	assert.Equal(t, "world", tv.Str)

	// fromGoValue bool
	tv, err = fromGoValue(true, "")
	assert.NoError(t, err)
	assert.True(t, tv.Bool)

	// fromGoValue int, int32, int64
	tv, err = fromGoValue(int(10), "")
	assert.NoError(t, err)
	assert.Equal(t, "10", tv.Str)
	tv, err = fromGoValue(int32(20), "")
	assert.NoError(t, err)
	assert.Equal(t, "20", tv.Str)
	tv, err = fromGoValue(int64(30), "")
	assert.NoError(t, err)
	assert.Equal(t, "30", tv.Str)

	// fromGoValue float32, float64
	tv, err = fromGoValue(float32(1.25), "")
	assert.NoError(t, err)
	assert.Equal(t, 1.25, tv.Num)
	tv, err = fromGoValue(float64(3.75), "")
	assert.NoError(t, err)
	assert.Equal(t, 3.75, tv.Num)

	// fromGoValue time.Time as date and datetime
	now := time.Now()
	tv, err = fromGoValue(now, "date")
	assert.NoError(t, err)
	assert.Equal(t, apicontract.ValueTypeDate, tv.Type)
	tv, err = fromGoValue(now, "datetime")
	assert.NoError(t, err)
	assert.Equal(t, apicontract.ValueTypeDatetime, tv.Type)

	// fromGoValue unsupported
	_, err = fromGoValue(struct{}{}, "")
	assert.Error(t, err)

	// typedFromString with integer, decimal, date, datetime
	tv, err = typedFromString("123", "integer")
	assert.NoError(t, err)
	assert.Equal(t, "123", tv.Str)
	tv, err = typedFromString("123.45", "decimal")
	assert.NoError(t, err)
	assert.Equal(t, "123.45", tv.Str)
	tv, err = typedFromString("2026-01-01", "date")
	assert.NoError(t, err)
	assert.Equal(t, "2026-01-01", tv.Str)
	tv, err = typedFromString("2026-01-01T00:00:00Z", "datetime")
	assert.NoError(t, err)
	assert.Equal(t, apicontract.ValueTypeDatetime, tv.Type)
	tv, err = typedFromString("not-a-datetime", "datetime")
	assert.NoError(t, err)
	assert.Equal(t, apicontract.ValueTypeString, tv.Type)

	// numberOrDecimal with decimal
	tv, err = numberOrDecimal(45.67, "decimal")
	assert.NoError(t, err)
	assert.Equal(t, "45.67", tv.Str)

	// numberOrDecimal with NaN and Inf
	_, err = numberOrDecimal(math.NaN(), "")
	assert.Error(t, err)
	_, err = numberOrDecimal(math.Inf(1), "")
	assert.Error(t, err)
}

func TestUtilErrorHandling_FullCoverage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// 1. ErrAccessDenied
	w := httptest.NewRecorder()
	handleError(secureread.ErrAccessDenied, w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "ACCESS_DENIED")

	// 2. ErrOpaqueSQLNotGranted
	w = httptest.NewRecorder()
	handleError(secureread.ErrOpaqueSQLNotGranted, w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "UNSUPPORTED_PROTECTED_EXECUTION")

	// 3. ErrInvalidProjectID
	w = httptest.NewRecorder()
	handleError(personalqueries.ErrInvalidProjectID, w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_REQUEST")
	assert.Contains(t, w.Body.String(), "project")
}

func TestDecodeLookupID_Coverage(t *testing.T) {
	// Base64 decode error
	_, _, _, err := decodeLookupID("!!!not-base64")
	assert.ErrorContains(t, err, "invalid lookupId")

	// Malformed (not 3 parts)
	_, _, _, err = decodeLookupID("YWJj") // "abc"
	assert.ErrorContains(t, err, "malformed")
}

func TestSemanticSchema_Coverage(t *testing.T) {
	ctx := context.Background()
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "admin", []string{"admin"})
	_ = scope

	projStore, err := api.ProjectStoreFor(projectID)
	require.NoError(t, err)

	// 1. resolveSource error from api.ResolveSource (unknown source)
	_, err = resolveSource(ctx, projStore, projectDir, semanticTestEnv, "unknown-src", "tbl")
	assert.Error(t, err)

	// 2. resolveSource inGitDB missing recordset definition
	_, err = resolveSource(ctx, projStore, projectDir, semanticTestEnv, "support-notes", "tbl")
	// If recordset exists, let's test a non-existent path
	tmpDir := t.TempDir()
	_, err = resolveSource(ctx, projStore, tmpDir, semanticTestEnv, "support-notes", "tbl")
	assert.Error(t, err)

	// 3. resolveSource HTTP source
	_, err = resolveSource(ctx, projStore, projectDir, semanticTestEnv, "country-facts", "tbl")
	// HTTP source resolves query
	assert.NoError(t, err)

	// 4. resolveSQLSourceURL invalid URL
	_, err = resolveSQLSourceURL(ctx, "invalid:////", "tbl")
	assert.Error(t, err)

	// 5. resolveSQLSourceURL non-existent DB file (dbcopy.ErrSourceFileMissing)
	_, err = resolveSQLSourceURL(ctx, "sqlite://file:"+filepath.Join(tmpDir, "missing.db"), "tbl")
	assert.Error(t, err)

	// 6. resolveRecordsetSource with PrimaryKey and ForeignKeys
	recsetJSON := `{
		"columns": [{"name": "id", "type": "integer"}],
		"primaryKey": {"name": "pk", "columns": ["id"], "isClustered": true},
		"foreignKeys": [{"name": "fk1", "columns": ["id"], "refTable": {"name": "other"}}]
	}`
	recsetFile := filepath.Join(tmpDir, "test.recordset.json")
	require.NoError(t, os.WriteFile(recsetFile, []byte(recsetJSON), 0644))
	rs, err := resolveRecordsetSource("ingitdb://test", recsetFile, "tbl")
	assert.NoError(t, err)
	assert.NotNil(t, rs.Schema.PrimaryKey)
	assert.Len(t, rs.Schema.ForeignKeys, 1)

	// 7. resolveRecordsetSource read and parse errors
	_, err = resolveRecordsetSource("ingitdb://test", filepath.Join(tmpDir, "nonexistent.json"), "tbl")
	assert.ErrorContains(t, err, "read")
	badJSONFile := filepath.Join(tmpDir, "bad.json")
	require.NoError(t, os.WriteFile(badJSONFile, []byte("bad json"), 0644))
	_, err = resolveRecordsetSource("ingitdb://test", badJSONFile, "tbl")
	assert.ErrorContains(t, err, "parse")

	// 8. resolveHTTPSource errors and branches
	badQDir := t.TempDir()
	badQFile := filepath.Join(badQDir, "queries", "bad.query.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(badQFile), 0755))
	require.NoError(t, os.WriteFile(badQFile, []byte("{invalid-json"), 0644))
	_, err = resolveHTTPSource(badQDir, "q1", "tbl")
	assert.Error(t, err)

	// query not found
	_, err = resolveHTTPSource(projectDir, "nonexistent-query", "tbl")
	assert.ErrorContains(t, err, "not found")

	// 9. httpDeclaredColumns errors and branches
	_, err = httpDeclaredColumns(badQDir, "q1")
	assert.Error(t, err)
	cols, err := httpDeclaredColumns(projectDir, "country-facts")
	assert.NoError(t, err)
	assert.NotNil(t, cols)
}

type brokenReader struct{}

func (b brokenReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("read failed")
}
func (b brokenReader) Close() error { return nil }

func TestSemanticRelated_Coverage(t *testing.T) {
	ctx := context.Background()
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "admin", []string{"admin"})

	// 1. semanticRelatedRowsHandler read body error
	req := httptest.NewRequest(http.MethodPost, "/datatug/semantic/related/rows", brokenReader{})
	w := httptest.NewRecorder()
	semanticRelatedRowsHandler(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "failed to read request body")

	// 2. semanticRelatedRowsHandler decode error
	req = httptest.NewRequest(http.MethodPost, "/datatug/semantic/related/rows", strings.NewReader("bad-json"))
	w = httptest.NewRecorder()
	semanticRelatedRowsHandler(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// 3. computeSemanticRelatedRows validation errors
	// missing scope
	_, err := computeSemanticRelatedRows(ctx, apicontract.RelatedRowsRequest{})
	assert.Error(t, err)

	// invalid storeID
	badStoreReq := newRelatedRowsRequest(scope, encodeLookupID(semanticTestSource, "Customer", "CustomerId"), apicontract.NewIntegerValue("5"), nil)
	badStoreReq.StoreID = "bad-store"
	_, err = computeSemanticRelatedRows(ctx, badStoreReq)
	assert.Error(t, err)

	// invalid lookupId
	badLookupReq := newRelatedRowsRequest(scope, "bad-lookup-id", apicontract.NewIntegerValue("5"), nil)
	_, err = computeSemanticRelatedRows(ctx, badLookupReq)
	assert.Error(t, err)

	// unknown project
	badProjScope := scope
	badProjScope.Project = "nonexistent-project"
	badProjReq := newRelatedRowsRequest(badProjScope, encodeLookupID(semanticTestSource, "Customer", "CustomerId"), apicontract.NewIntegerValue("5"), nil)
	_, err = computeSemanticRelatedRows(ctx, badProjReq)
	assert.Error(t, err)

	// resolveSource error in computeSemanticRelatedRows
	badSourceLookupReq := newRelatedRowsRequest(scope, encodeLookupID("unknown-source", "Customer", "CustomerId"), apicontract.NewIntegerValue("5"), nil)
	_, err = computeSemanticRelatedRows(ctx, badSourceLookupReq)
	assert.Error(t, err)

	// nativeValue error in computeSemanticRelatedRows
	badValReq := newRelatedRowsRequest(scope, encodeLookupID(semanticTestSource, "Customer", "CustomerId"), apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "99999999999999999999999999999999999999"}, nil)
	_, err = computeSemanticRelatedRows(ctx, badValReq)
	assert.Error(t, err)

	// 4. computeSemanticRelated errors
	// bad physical source
	badFact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), "unknown-source", "Customer", "CustomerId")
	_, err = computeSemanticRelated(ctx, newRelatedRequest(scope, badFact, nil))
	assert.Error(t, err)

	// bad fact value
	badFactVal := declaredFact("f1", "Customer", "ID", apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "99999999999999999999999999999999999999"}, semanticTestSource, "Customer", "CustomerId")
	_, err = computeSemanticRelated(ctx, newRelatedRequest(scope, badFactVal, nil))
	assert.Error(t, err)

	// limit truncation in computeSemanticRelated
	fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
	lim := 1
	relResp, err := computeSemanticRelated(ctx, newRelatedRequest(scope, fact, &lim))
	assert.NoError(t, err)
	assert.True(t, relResp.Truncated)

	// 5. computeSemanticRelatedRows with Record: true returns record error when store unconfigured
	lookupID := encodeLookupID(semanticTestSource, "Invoice", "CustomerId")
	rowLim := 1
	rowsReqRecord := newRelatedRowsRequest(scope, lookupID, apicontract.NewIntegerValue("5"), &rowLim)
	rowsReqRecord.Record = true
	_, err = computeSemanticRelatedRows(ctx, rowsReqRecord)
	assert.ErrorContains(t, err, "execution evidence store")

	// computeSemanticRelatedRows success with Limit truncation
	rowsReq := newRelatedRowsRequest(scope, lookupID, apicontract.NewIntegerValue("5"), &rowLim)
	res, err := computeSemanticRelatedRows(ctx, rowsReq)
	assert.NoError(t, err)
	assert.True(t, res.Truncated)

	// 6. countRelated with resolveSource error and limitations
	projStore, err := api.ProjectStoreFor(projectID)
	require.NoError(t, err)
	executor, ok := api.SecureExecutor()
	require.True(t, ok)

	// lookup with unknown source
	badLookup := semantic.Lookup{Source: "unknown-src", Collection: "col", Column: "col"}
	_, err = countRelated(ctx, executor, projStore, projectDir, semanticTestEnv, badLookup, 5)
	assert.Error(t, err)

	// countRelated where executor returns limitations
	// We configure a session with access limitations
	limitedSession, err := secureread.NewSession(secureread.SessionOptions{
		As: "support", Roles: []string{"support"}, PoliciesDir: projectDir + "/policies",
	})
	require.NoError(t, err)
	limitedExec := secureread.NewExecutor(limitedSession)
	goodLookup := semantic.Lookup{Source: semanticTestSource, Collection: "Invoice", Column: "CustomerId"}
	// Customer 5 invoices under "support" role should return error or limitation or nil
	cnt, err := countRelated(ctx, limitedExec, projStore, projectDir, semanticTestEnv, goodLookup, 5)
	if err == nil {
		assert.Nil(t, cnt)
	}

	// 7. computeSemanticRelatedRows error paths: context deadline exceeded and access denied
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	rowsReqTimeout := newRelatedRowsRequest(scope, lookupID, apicontract.NewIntegerValue("5"), nil)
	_, err = computeSemanticRelatedRows(canceledCtx, rowsReqTimeout)
	assert.Error(t, err)
}
