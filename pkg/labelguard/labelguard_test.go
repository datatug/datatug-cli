package labelguard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

func labelled(t *testing.T, changes ...meaninglabels.Change) meaninglabels.Document {
	t.Helper()
	doc := meaninglabels.NewDocument()
	for _, change := range changes {
		var err error
		change.Reason = "test"
		doc, _, err = meaninglabels.Apply(doc, change, meaninglabels.Actor{ID: "owner"}, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
	}
	return doc
}

func set(table, column string, class meaninglabels.Class) meaninglabels.Change {
	return meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "shop", Table: table, Column: column}, Class: class}
}

func TestStoredLabels(t *testing.T) {
	doc := labelled(t, set("Customer", "Note", meaninglabels.ClassPII), set("Secret", "", meaninglabels.ClassSecret), set("Customer", "Name", meaninglabels.ClassNone))
	if !StoredLabelCovers(doc, "SHOP", "Customer", "note") || !StoredLabelCovers(doc, "shop", "Secret", "anything") {
		t.Fatal("a stored protected label covers its column, and a table label every column")
	}
	if StoredLabelCovers(doc, "shop", "Customer", "Name") || StoredLabelCovers(doc, "other", "Customer", "Note") {
		t.Fatal("a none label, or another source, covers nothing")
	}
	if !SourceHasProtectedLabel(doc, "shop") || SourceHasProtectedLabel(doc, "other") {
		t.Fatal("SourceHasProtectedLabel")
	}
	if l, ok := StoredTableLabel(doc, "shop", "Secret"); !ok || l.Column != "" || l.Class != meaninglabels.ClassSecret {
		t.Fatalf("table label = %+v %v", l, ok)
	}
	if l, ok := StoredTableLabel(doc, "shop", "Customer"); !ok || l.Column != "Note" {
		t.Fatalf("column label = %+v %v", l, ok)
	}
	if _, ok := StoredTableLabel(doc, "shop", "Invoice"); ok {
		t.Fatal("an unlabelled table")
	}
}

func TestCheckColumns(t *testing.T) {
	doc := labelled(t, set("Customer", "Note", meaninglabels.ClassPII))
	// A stored label refuses everyone.
	if refused, _ := CheckColumns(doc, "shop", "Customer", false, []string{"Id", "Note"}, false); refused == nil || refused.Column != "Note" {
		t.Fatalf("refused = %+v", refused)
	}
	// A suggestion refuses a strict principal; anyone else is told.
	if refused, _ := CheckColumns(doc, "shop", "Customer", false, []string{"Email"}, true); refused == nil || refused.Column != "Email" {
		t.Fatalf("strict: %+v", refused)
	}
	refused, suggested := CheckColumns(doc, "shop", "Customer", false, []string{"Phone", "Id", "Email"}, false)
	if refused != nil || strings.Join(suggested, ",") != "Customer.Email,Customer.Phone" {
		t.Fatalf("refused = %+v suggested = %v", refused, suggested)
	}
	// An unreviewed view is masked whole, for everyone.
	if refused, _ := CheckColumns(doc, "shop", "v", true, []string{"Id"}, false); refused == nil {
		t.Fatal("unreviewed view")
	}
	if refused, suggested := CheckColumns(doc, "shop", "Customer", false, []string{"Id"}, true); refused != nil || suggested != nil {
		t.Fatal("unlabelled column")
	}
}

func TestSuggestedColumnsAndWarner(t *testing.T) {
	names := SuggestedColumns(meaninglabels.Document{}, "shop", map[string][]meaninglabels.Column{
		"Customer": Columns([]string{"Email", "Id"}), "Invoice": Columns([]string{"BillingAddress"}),
	})
	if strings.Join(names, ",") != "Customer.Email,Invoice.BillingAddress" {
		t.Fatalf("names = %v", names)
	}
	var out bytes.Buffer
	w := NewWarner(&out)
	w.Warn("this read", "shop", names)
	w.Warn("this read", "shop", names[:1])
	w.Warn("this read", "shop", nil)
	if got := out.String(); strings.Count(got, "warning:") != 1 || !strings.Contains(got, "Customer.Email, Invoice.BillingAddress of source shop") {
		t.Fatalf("out = %q", got)
	}
}
