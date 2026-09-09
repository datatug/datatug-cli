package endpoints

import (
	"context"
	"testing"
)

func TestSemanticColumns_ChinookCustomer_Declared(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	resp, err := computeSemanticColumns(context.Background(), projectID, "chinook", "Customer")
	if err != nil {
		t.Fatalf("computeSemanticColumns: %v", err)
	}
	byColumn := map[string]ColumnResolution{}
	for _, c := range resp.Columns {
		byColumn[c.Column] = c
	}
	custID, ok := byColumn["CustomerId"]
	if !ok {
		t.Fatalf("CustomerId not resolved; got %+v", resp.Columns)
	}
	if custID.Entity != "Customer" || custID.Field != "ID" || custID.Provenance != "declared" {
		t.Fatalf("CustomerId resolution = %+v, want Entity=Customer Field=ID Provenance=declared", custID)
	}
	email, ok := byColumn["Email"]
	if !ok || email.Provenance != "declared" {
		t.Fatalf("Email resolution = %+v (ok=%v), want declared", email, ok)
	}
	if _, ok := byColumn["FirstName"]; ok {
		t.Fatalf("FirstName resolved to %+v, want no entry (matches core-investigation-loop's AC:mapped-columns-server)", byColumn["FirstName"])
	}
}

func TestSemanticColumns_UnknownSource(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	if _, err := computeSemanticColumns(context.Background(), projectID, "no-such-source", "Customer"); err == nil {
		t.Fatalf("computeSemanticColumns() = nil error, want error")
	}
}

func TestSemanticColumns_MissingParams(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	if _, err := computeSemanticColumns(context.Background(), projectID, "", "Customer"); err == nil {
		t.Fatalf("want error for missing source")
	}
	if _, err := computeSemanticColumns(context.Background(), projectID, "chinook", ""); err == nil {
		t.Fatalf("want error for missing collection")
	}
	if _, err := computeSemanticColumns(context.Background(), "unknown-project-xyz", "chinook", "Customer"); err == nil {
		t.Fatalf("want error for unknown project")
	}
}

func TestSemanticColumns_SupportNotesRecordset(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	resp, err := computeSemanticColumns(context.Background(), projectID, "support-notes", "support-notes")
	if err != nil {
		t.Fatalf("computeSemanticColumns: %v", err)
	}
	var found bool
	for _, c := range resp.Columns {
		if c.Column == "CustomerId" {
			found = true
			if c.Entity != "Customer" || c.Field != "ID" || c.Provenance != "declared" {
				t.Fatalf("CustomerId resolution = %+v, want declared Customer.ID", c)
			}
		}
	}
	if !found {
		t.Fatalf("CustomerId not resolved from support-notes; got %+v", resp.Columns)
	}
}
