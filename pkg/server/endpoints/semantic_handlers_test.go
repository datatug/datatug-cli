package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestSemanticColumnsHandler_HTTP exercises semanticColumnsHandler as an
// actual http.HandlerFunc (query-string parsing, JSON response, status
// code), not just its internal compute function.
func TestSemanticColumnsHandler_HTTP(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	q := url.Values{urlParamProjectID: {projectID}, "source": {"chinook"}, "collection": {"Customer"}}
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+q.Encode(), nil)
	rec := httptest.NewRecorder()

	semanticColumnsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Columns) == 0 {
		t.Fatalf("Columns is empty")
	}
}

func TestSemanticColumnsHandler_HTTP_BadRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns", nil) // no project/source/collection
	rec := httptest.NewRecorder()

	semanticColumnsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	var se structuredError
	if err := json.Unmarshal(rec.Body.Bytes(), &se); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if se.Code != "BAD_REQUEST" || se.Field == "" {
		t.Fatalf("error response = %+v, want BAD_REQUEST with a Field", se)
	}
}

func TestSemanticRelatedHandler_HTTP(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	policiesDir := projectDir + "/policies"
	q := url.Values{
		urlParamProjectID: {projectID},
		"entity":          {"Customer"}, "field": {"ID"}, "value": {"5"},
		"source": {"chinook"}, "collection": {"Customer"}, "column": {"CustomerId"},
		"provenance": {"declared"},
		"as":         {"alice"}, "role": {"admin"}, "policiesDir": {policiesDir},
	}
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/related?"+q.Encode(), nil)
	rec := httptest.NewRecorder()

	semanticRelatedHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RelatedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Related) == 0 {
		t.Fatalf("Related is empty")
	}
}

func TestSemanticRelatedHandler_HTTP_NoPrincipal(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	q := url.Values{
		urlParamProjectID: {projectID},
		"entity":          {"Customer"}, "field": {"ID"}, "value": {"5"},
		"source": {"chinook"}, "collection": {"Customer"}, "column": {"CustomerId"},
	}
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/related?"+q.Encode(), nil)
	rec := httptest.NewRecorder()

	semanticRelatedHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no principal, no noPolicies); body = %s", rec.Code, rec.Body.String())
	}
}

func TestSemanticRelatedRowsHandler_HTTP(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	lookupID := encodeLookupID("chinook", "Invoice", "CustomerId")
	q := url.Values{
		urlParamProjectID: {projectID}, "lookupId": {lookupID}, "value": {"5"},
		"as": {"alice"}, "role": {"admin"}, "policiesDir": {projectDir + "/policies"},
	}
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/related/rows?"+q.Encode(), nil)
	rec := httptest.NewRecorder()

	semanticRelatedRowsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RelatedRowsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Rows) != 7 {
		t.Fatalf("len(Rows) = %d, want 7", len(resp.Rows))
	}
}

func TestSemanticApplicableHandler_HTTP(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	body, err := json.Marshal(ApplicableRequest{
		Values: []ApplicableValue{
			{Entity: "Customer", Field: "ID", Value: 5, Source: "chinook", Collection: "Customer", Column: "CustomerId", Provenance: "declared"},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/datatug/queries/applicable?"+urlParamProjectID+"="+projectID, bytes.NewReader(body))
	rec := httptest.NewRecorder()

	semanticApplicableHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ApplicableResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Applicable) == 0 {
		t.Fatalf("Applicable is empty")
	}
}

func TestSemanticApplicableHandler_HTTP_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/datatug/queries/applicable", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()

	semanticApplicableHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}
