package endpoints

import (
	"reflect"
	"strings"
	"testing"
)

// encoding/json matches a key to a struct field case-insensitively and keeps
// the last spelling, so "project" and "Project" in one object are duplicates
// the contract decoder must reject - where a struct field is the target.
func TestDecodeContractBody_RejectsCaseVariantDuplicateKeys(t *testing.T) {
	const query = `"query":{"folderPath":"","id":"q","title":"t","purpose":"p","source":"s","dtql":"from: X\n","parameters":[],"bindingOrigins":[]}`
	rejected := map[string]string{
		"top level": `{"project":"a","Project":"b","environment":"e","securityContextId":"s","ifNoneMatch":true,` + query + `}`,
		"nested":    `{"project":"a","environment":"e","securityContextId":"s","ifNoneMatch":true,"query":{"folderPath":"","id":"q","ID":"r","title":"t","purpose":"p","source":"s","dtql":"from: X\n","parameters":[],"bindingOrigins":[]}}`,
		"in an array element": `{"project":"a","environment":"e","securityContextId":"s","ifNoneMatch":true,"query":{"folderPath":"","id":"q","title":"t","purpose":"p","source":"s","dtql":"from: X\n",` +
			`"parameters":[{"id":"P","ID":"Q","type":"string","isRequired":true}],"bindingOrigins":[]}}`,
	}
	for name, body := range rejected {
		t.Run(name, func(t *testing.T) {
			var req captureQueryRequest
			err := decodeCaptureBody([]byte(body), &req)
			if err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
				t.Fatalf("expected a duplicate-key error, got %v (Project=%q)", err, req.Project)
			}
		})
	}
}

type foldTarget struct {
	Kind   string         `json:"kind"`
	Name   string         `json:"name"`
	Params map[string]int `json:"params"`
	Any    any            `json:"any"`
	Items  []foldItem     `json:"items"`
	foldEmbedded
}

type foldItem struct {
	A int `json:"a"`
}

type foldEmbedded struct {
	Inner string `json:"inner"`
}

func TestCheckNoDuplicateKeys_CaseVariantsOnlyWhereAFieldIsTheTarget(t *testing.T) {
	target := reflect.TypeOf(&foldTarget{})
	for _, body := range []string{
		`{"params":{"id":1,"ID":2}}`,
		`{"any":{"k":1,"K":2}}`,
		`{"name":"x","inner":"y","items":[{"a":1},{"A":2}]}`,
	} {
		if err := checkNoDuplicateKeys([]byte(body), target); err != nil {
			t.Errorf("checkNoDuplicateKeys(%s) = %v, want nil", body, err)
		}
	}
	for _, body := range []string{
		"{\"kind\":\"x\",\"Kind\":\"y\"}", // the Kelvin sign folds with "k"
		`{"name":"x","NAME":"y"}`,
		`{"inner":"x","Inner":"y"}`,
		`{"items":[{"a":1,"A":2}]}`,
		`{"params":{"id":1,"id":2}}`,
	} {
		if err := checkNoDuplicateKeys([]byte(body), target); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
			t.Errorf("checkNoDuplicateKeys(%s) = %v, want a duplicate-key error", body, err)
		}
	}
}
