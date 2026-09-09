package endpoints

import "testing"

// TestSemanticApplicable_CustomerInvoicesApplicable_InvoiceLinesNotYet is
// AC:applicable-with-chain-server: given Customer.ID=5, customer-invoices is
// applicable with a chain, and invoice-lines is not-yet with missing
// Invoice.ID.
func TestSemanticApplicable_CustomerInvoicesApplicable_InvoiceLinesNotYet(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	resp, err := computeSemanticApplicable(projectID, ApplicableRequest{
		Values: []ApplicableValue{
			{
				Entity: "Customer", Field: "ID", Value: float64(5),
				Source: "chinook", Collection: "Customer", Column: "CustomerId",
				Provenance: "declared",
			},
		},
	})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}

	var invoices *ApplicableEntry
	for i := range resp.Applicable {
		if resp.Applicable[i].Query == "customer-invoices" {
			invoices = &resp.Applicable[i]
		}
	}
	if invoices == nil {
		t.Fatalf("customer-invoices not applicable; got applicable=%+v notYet=%+v", resp.Applicable, resp.NotYet)
	}
	if len(invoices.Bindings) != 1 || invoices.Bindings[0].Parameter != "customerId" {
		t.Fatalf("customer-invoices bindings = %+v", invoices.Bindings)
	}
	if len(invoices.Chain) == 0 || invoices.Chain[0] == "" {
		t.Fatalf("customer-invoices chain is empty, want resolution chain text")
	}

	var lines *NotYetEntry
	for i := range resp.NotYet {
		if resp.NotYet[i].Query == "invoice-lines" {
			lines = &resp.NotYet[i]
		}
	}
	if lines == nil {
		t.Fatalf("invoice-lines not in notYet; got notYet=%+v", resp.NotYet)
	}
	if len(lines.Missing) != 1 || lines.Missing[0] != "Invoice.ID" {
		t.Fatalf("invoice-lines missing = %+v, want [Invoice.ID]", lines.Missing)
	}
}

func TestSemanticApplicable_NoValues_EverythingNotYet(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	resp, err := computeSemanticApplicable(projectID, ApplicableRequest{})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}
	if len(resp.Applicable) != 0 {
		t.Fatalf("Applicable = %+v, want none with no values on hand", resp.Applicable)
	}
	if len(resp.NotYet) != 2 {
		t.Fatalf("len(NotYet) = %d, want 2 (both queries)", len(resp.NotYet))
	}
}

func TestSemanticApplicable_UnknownProject(t *testing.T) {
	if _, err := computeSemanticApplicable("no-such-project-xyz", ApplicableRequest{}); err == nil {
		t.Fatalf("want error for unknown project")
	}
}
