package httpsource

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// countryFactsDef and currencyRateDef mirror the real
// datatug-demo-projects/demo-project-1 QueryDefs (see
// queries/reference/*.query.json) so this package's translation is proven
// against the actual shapes it must handle, without this repo depending on
// that sibling repo being checked out (see demo_test.go for the
// skippable integration test that does read the real files).

func countryFactsDef() *datatug.QueryDef {
	return &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "country-facts", Title: "Country facts (currency by country name)"}},
		Type:        datatug.QueryTypeHTTP,
		Parameters: datatug.Parameters{
			{ID: "name", Type: "string", IsRequired: true, Meta: &datatug.EntityFieldRef{Entity: "Country", Field: "Name"}},
		},
		Recordsets: []datatug.RecordsetDefinition{{
			Columns: datatug.RecordsetColumnDefs{
				{Name: "name", Type: "string"},
				{Name: "currency", Type: "string"},
			},
		}},
	}
}

const countryFactsURL = "https://countriesnow.space/api/v0.1/countries/currency/q?country={name}"

var countryFactsFixture = map[string]any{
	"error": false,
	"msg":   "Canada and currency retrieved",
	"data": map[string]any{
		"name":     "Canada",
		"currency": "CAD",
		"iso2":     "CA",
		"iso3":     "CAN",
	},
}

func currencyRateDef() *datatug.QueryDef {
	return &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "currency-rate", Title: "Exchange rate for the customer's currency"}},
		Type:        datatug.QueryTypeHTTP,
		Parameters: datatug.Parameters{
			{ID: "to", Type: "string", IsRequired: true, Meta: &datatug.EntityFieldRef{Entity: "Country", Field: "Currency"}},
		},
		Recordsets: []datatug.RecordsetDefinition{{
			Columns: datatug.RecordsetColumnDefs{
				{Name: "amount", Type: "number"},
				{Name: "base", Type: "string"},
				{Name: "date", Type: "string"},
				{Name: "rates", Type: "JSON"},
			},
		}},
	}
}

const currencyRateURL = "https://api.frankfurter.dev/v1/latest?from=USD&to={to}"

var currencyRateFixture = map[string]any{
	"amount": 1.0,
	"base":   "USD",
	"date":   "2026-09-08",
	"rates":  map[string]any{"CAD": 1.3805},
}

func TestBuildCollection_CountryFacts(t *testing.T) {
	coll, err := BuildCollection(countryFactsDef(), countryFactsURL, countryFactsFixture)
	if err != nil {
		t.Fatalf("BuildCollection: %v", err)
	}
	if coll.Name != "country-facts" {
		t.Fatalf("Name = %q", coll.Name)
	}
	if coll.RowsPath != "data" {
		t.Fatalf("RowsPath = %q, want %q", coll.RowsPath, "data")
	}
	if coll.KeyField != "name" {
		t.Fatalf("KeyField = %q, want %q (the response echoes the lookup parameter back)", coll.KeyField, "name")
	}
	if coll.Params["name"].Location != dalgo2http.ParamQuery {
		t.Fatalf("Params[name].Location = %q, want %q", coll.Params["name"].Location, dalgo2http.ParamQuery)
	}
	// KeyField coincides with a declared Param, so Get must be supported.
	db, err := dalgo2http.NewDB(dalgo2http.Config{Collections: []dalgo2http.Collection{coll}})
	if err != nil {
		t.Fatalf("dalgo2http.NewDB: %v", err)
	}
	provider, ok := dal.As[dalgo2http.CapabilitiesProvider](db)
	if !ok {
		t.Fatalf("dal.As[dalgo2http.CapabilitiesProvider] did not find the capability")
	}
	caps, err := provider.Capabilities(coll.Name)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.SupportsGet {
		t.Fatalf("SupportsGet = false, want true")
	}
}

func TestBuildCollection_CurrencyRate(t *testing.T) {
	coll, err := BuildCollection(currencyRateDef(), currencyRateURL, currencyRateFixture)
	if err != nil {
		t.Fatalf("BuildCollection: %v", err)
	}
	if coll.RowsPath != "" {
		t.Fatalf("RowsPath = %q, want root (empty)", coll.RowsPath)
	}
	if coll.KeyField != "amount" {
		t.Fatalf("KeyField = %q, want %q (\"to\" is not echoed back; falls back to the first present declared column)", coll.KeyField, "amount")
	}
	if coll.Params["to"].Location != dalgo2http.ParamQuery {
		t.Fatalf("Params[to].Location = %q, want %q", coll.Params["to"].Location, dalgo2http.ParamQuery)
	}
	// KeyField is NOT a declared Param, so Get must fail closed.
	db, err := dalgo2http.NewDB(dalgo2http.Config{Collections: []dalgo2http.Collection{coll}})
	if err != nil {
		t.Fatalf("dalgo2http.NewDB: %v", err)
	}
	provider, ok := dal.As[dalgo2http.CapabilitiesProvider](db)
	if !ok {
		t.Fatalf("dal.As[dalgo2http.CapabilitiesProvider] did not find the capability")
	}
	caps, err := provider.Capabilities(coll.Name)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.SupportsGet {
		t.Fatalf("SupportsGet = true, want false")
	}
}
