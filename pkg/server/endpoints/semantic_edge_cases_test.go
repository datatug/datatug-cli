package endpoints

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStructuredError_Error(t *testing.T) {
	withField := &structuredError{Code: "BAD_REQUEST", Message: "boom", Field: "source"}
	if withField.Error() != "BAD_REQUEST: source: boom" {
		t.Fatalf("Error() = %q", withField.Error())
	}
	withoutField := &structuredError{Code: "ACCESS_DENIED", Message: "no"}
	if withoutField.Error() != "ACCESS_DENIED: no" {
		t.Fatalf("Error() = %q", withoutField.Error())
	}
}

func TestWriteSemanticError_StatusCodes(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
	}{
		{newFieldError("x", "bad"), 400},
		{newAccessDeniedError("no"), 403},
		{&structuredError{Code: "NOT_FOUND", Message: "gone"}, 404},
		{&structuredError{Code: "SOMETHING_ELSE", Message: "?"}, 500},
		{errors.New("plain error, not structured"), 500},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeSemanticError(rec, nil, tc.err)
		if rec.Code != tc.wantStatus {
			t.Fatalf("writeSemanticError(%v) status = %d, want %d", tc.err, rec.Code, tc.wantStatus)
		}
	}
}

func TestSemanticSessionFromQuery_Unrestricted(t *testing.T) {
	q := map[string][]string{"noPolicies": {"true"}}
	session, err := semanticSessionFromQuery(q)
	if err != nil {
		t.Fatalf("semanticSessionFromQuery: %v", err)
	}
	if !session.Unrestricted {
		t.Fatalf("session.Unrestricted = false, want true")
	}
}

func TestSemanticSessionFromQuery_NoPrincipal(t *testing.T) {
	_, err := semanticSessionFromQuery(map[string][]string{})
	if err == nil {
		t.Fatalf("want error for no principal and no noPolicies")
	}
}

func TestSemanticSessionFromQuery_NoPolicies_ButNotUnrestricted(t *testing.T) {
	q := map[string][]string{"as": {"alice"}, "policiesDir": {"/does/not/exist/xyz"}}
	if _, err := semanticSessionFromQuery(q); err == nil {
		t.Fatalf("want error for a nonexistent explicit policies dir")
	}
}

func TestWalkJSONFiles_MissingDir(t *testing.T) {
	err := walkJSONFiles("/does/not/exist/xyz-abc", ".json", func(string, []byte) error {
		t.Fatalf("onFile should not be called for a missing dir")
		return nil
	})
	if err != nil {
		t.Fatalf("walkJSONFiles(missing dir) = %v, want nil", err)
	}
}

func TestDecodeLookupID_WrongPartCount(t *testing.T) {
	bad := base64.RawURLEncoding.EncodeToString([]byte("only-one-part"))
	if _, _, _, err := decodeLookupID(bad); err == nil {
		t.Fatalf("want error for a lookupId with the wrong number of parts")
	}
}

func TestSemanticRelatedRowsHandler_HTTP_BadLimit(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	rawQuery := urlParamProjectID + "=" + projectID + "&lookupId=x&value=5&limit=not-a-number&noPolicies=true"
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/related/rows?"+rawQuery, nil)
	rec := httptest.NewRecorder()

	semanticRelatedRowsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-integer limit; body=%s", rec.Code, rec.Body.String())
	}
}
