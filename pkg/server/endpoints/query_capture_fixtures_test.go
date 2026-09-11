//go:build datatug_query_capture

package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/apicontract/fixtures"
)

// These tests pin this package's capture wire types and error envelopes to
// datatug-core's frozen capture fixtures (build tag datatug_query_capture:
// they need a datatug-core with the capture contract).

func readCoreFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fixtures.Read(name)
	if err != nil {
		t.Fatalf("fixtures.Read(%s): %v", name, err)
	}
	return data
}

// assertCanonicalRoundTrip decodes fixture strictly into v and proves
// re-encoding it the way core's fixtures are generated reproduces it byte
// for byte - this package's mirror has exactly core's fields.
func assertCanonicalRoundTrip(t *testing.T, name string, v any) {
	t.Helper()
	data := readCoreFixture(t, name)
	if err := decodeCaptureBody(data, v); err != nil {
		t.Fatalf("%s does not decode strictly into %T: %v", name, v, err)
	}
	reencoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	reencoded = append(reencoded, '\n')
	if !bytes.Equal(reencoded, data) {
		t.Errorf("%T does not reproduce %s:\n got %s\nwant %s", v, name, reencoded, data)
	}
}

func TestCaptureWireTypes_MatchCoreFixtures(t *testing.T) {
	for _, name := range []string{"capture_query_request_create.json", "capture_query_request_update.json"} {
		t.Run(name, func(t *testing.T) {
			var cli captureQueryRequest
			assertCanonicalRoundTrip(t, name, &cli)
			var core apicontract.CaptureQueryRequest
			if err := apicontract.DecodeStrict(readCoreFixture(t, name), &core); err != nil {
				t.Fatal(err)
			}
			if err := core.Validate(); err != nil {
				t.Fatalf("core rejects its own fixture: %v", err)
			}
			if _, err := validateCapturedQuery(cli.Query); err != nil {
				t.Errorf("the server rejects a query core's contract accepts: %v", err)
			}
		})
	}
	t.Run("capture_query_response.json", func(t *testing.T) {
		var cli captureQueryResponse
		assertCanonicalRoundTrip(t, "capture_query_response.json", &cli)
	})
}

func TestCaptureQuery_RealResponseSatisfiesCoreContract(t *testing.T) {
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	w := postCapture(t, validCaptureRequest(scope))
	var resp apicontract.CaptureQueryResponse
	if err := apicontract.DecodeStrict(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response does not decode strictly into core's CaptureQueryResponse: %v\n%s", err, w.Body.String())
	}
	if err := resp.Validate(); err != nil {
		t.Errorf("response fails core's Validate(): %v", err)
	}
}

// TestCaptureQuery_ErrorsMatchCoreFixtures produces each capture failure
// for real and compares its status, code and field with the frozen error
// fixture for that case.
func TestCaptureQuery_ErrorsMatchCoreFixtures(t *testing.T) {
	if codeRevisionConflict != apicontract.ErrCodeRevisionConflict {
		t.Fatalf("codeRevisionConflict = %q, core says %q", codeRevisionConflict, apicontract.ErrCodeRevisionConflict)
	}
	if got := httpStatusFor(codeRevisionConflict); got != apicontract.ErrCodeRevisionConflict.HTTPStatus() {
		t.Fatalf("httpStatusFor(REVISION_CONFLICT) = %d, core says %d", got, apicontract.ErrCodeRevisionConflict.HTTPStatus())
	}
	produce := map[string]func(t *testing.T) *httptest.ResponseRecorder{
		"error_revision_conflict.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
			created := decodeCaptureResponse(t, postCapture(t, validCaptureRequest(scope)))
			update := validCaptureRequest(scope)
			update.IfNoneMatch, update.IfMatch = false, created.Revision+"-stale"
			return postCapture(t, update)
		},
		"error_capture_already_exists.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
			postCapture(t, validCaptureRequest(scope))
			return postCapture(t, validCaptureRequest(scope))
		},
		"error_capture_invalid_location.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
			req := validCaptureRequest(scope)
			req.Query.FolderPath = "customers/.."
			return postCapture(t, req)
		},
		"error_capture_credentials.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
			req := validCaptureRequest(scope)
			req.Query.DTQL = strings.Replace(captureTestDTQL, "field: Total", "field: Total\n  - field: Note\n    alias: \"postgres://u:secret@db/prod\"", 1)
			return postCapture(t, req)
		},
		"error_capture_result_rows.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
			body := strings.Replace(string(marshalCapture(t, validCaptureRequest(scope))), `"query":`, `"rows":[[1]],"query":`, 1)
			return postCaptureBody(t, []byte(body))
		},
		"error_capture_incomplete_record.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
			fake.err = &captureStoreError{Kind: captureErrIncomplete, Field: "query.id", Reason: "metadata without its body"}
			update := validCaptureRequest(scope)
			update.IfNoneMatch, update.IfMatch = false, "rev-1"
			return postCapture(t, update)
		},
		"error_capture_write_denied.json": func(t *testing.T) *httptest.ResponseRecorder {
			scope, _ := captureTestSetup(t, "sam", []string{"support"}, true)
			return postCapture(t, validCaptureRequest(scope))
		},
	}
	for fixture, fn := range produce {
		t.Run(fixture, func(t *testing.T) {
			var want apicontract.ErrorEnvelope
			if err := json.Unmarshal(readCoreFixture(t, fixture), &want); err != nil {
				t.Fatal(err)
			}
			w := fn(t)
			got := decodeCaptureError(t, w)
			if err := got.Validate(); err != nil {
				t.Errorf("response envelope fails core's Validate(): %v", err)
			}
			if got.Error.Code != want.Error.Code || got.Error.Field != want.Error.Field {
				t.Errorf("got %s/%q, want %s/%q (%s)", got.Error.Code, got.Error.Field, want.Error.Code, want.Error.Field, w.Body.String())
			}
			if wantStatus := apicontract.ErrorCode(want.Error.Code).HTTPStatus(); w.Code != wantStatus {
				t.Errorf("status = %d, want %d", w.Code, wantStatus)
			}
		})
	}
}
