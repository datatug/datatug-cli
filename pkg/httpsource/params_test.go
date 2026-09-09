package httpsource

import (
	"testing"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
)

func TestClassifyParams(t *testing.T) {
	t.Run("query-embedded placeholder", func(t *testing.T) {
		params := datatug.Parameters{{ID: "name", Type: "string", IsRequired: true}}
		got, err := classifyParams("country-facts", "https://countriesnow.space/api/v0.1/countries/currency/q?country={name}", params)
		if err != nil {
			t.Fatalf("classifyParams: %v", err)
		}
		if got["name"].Location != dalgo2http.ParamQuery {
			t.Fatalf("Location = %q, want %q", got["name"].Location, dalgo2http.ParamQuery)
		}
	})

	t.Run("path placeholder", func(t *testing.T) {
		params := datatug.Parameters{{ID: "code", Type: "string", IsRequired: true}}
		got, err := classifyParams("country-info", "https://date.nager.at/api/v3/CountryInfo/{code}", params)
		if err != nil {
			t.Fatalf("classifyParams: %v", err)
		}
		if got["code"].Location != dalgo2http.ParamPath {
			t.Fatalf("Location = %q, want %q", got["code"].Location, dalgo2http.ParamPath)
		}
	})

	t.Run("declared parameter missing from template fails closed", func(t *testing.T) {
		params := datatug.Parameters{{ID: "to", Type: "string", IsRequired: true}}
		if _, err := classifyParams("currency-rate", "https://api.frankfurter.dev/v1/latest?from=USD", params); err == nil {
			t.Fatalf("classifyParams() = nil error, want error naming the missing placeholder")
		}
	})

	t.Run("template placeholder with no declared parameter fails closed", func(t *testing.T) {
		if _, err := classifyParams("currency-rate", "https://api.frankfurter.dev/v1/latest?from=USD&to={to}", nil); err == nil {
			t.Fatalf("classifyParams() = nil error, want error naming the undeclared placeholder")
		}
	})
}
