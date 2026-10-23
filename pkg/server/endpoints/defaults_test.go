package endpoints

import (
	"encoding/json"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestExactDeclaredDefaultOrigin(t *testing.T) {
	var q datatug.QueryDef
	if err := json.Unmarshal([]byte(`{"parameters":[{"id":"p","type":"integer","defaultValue":9007199254740993},{"id":"d","type":"decimal","defaultValue":9007199254740992.10}]}`), &q); err != nil {
		t.Fatal(err)
	}
	req := apicontract.ExecutionRequest{Parameters: map[string]apicontract.TypedValueOrSet{"p": apicontract.ScalarValue(apicontract.NewIntegerValue("9007199254740993")), "d": apicontract.ScalarValue(apicontract.NewDecimalValue("9007199254740992.10"))}, BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "p", Origin: apicontract.BindingOriginDefault}, {ParameterID: "d", Origin: apicontract.BindingOriginDefault}}}
	if err := validateDefaultBindingOrigins(req, &q); err != nil {
		t.Fatal(err)
	}
	req.Parameters["p"] = apicontract.ScalarValue(apicontract.NewIntegerValue("9007199254740992"))
	if err := validateDefaultBindingOrigins(req, &q); err == nil {
		t.Fatal("rounded default accepted")
	}
}
