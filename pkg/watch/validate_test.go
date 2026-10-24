package watch_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
)

func TestValidateEmbeddedDefinitionWithoutCatalogOrMutation(t *testing.T) {
	input := apicontract.NewIntegerValue("11045")
	definition := watch.Definition{ID: "embedded-prevention", Title: "Review receipt presence", CheckID: "existing-check", CheckVersion: "1", Visibility: "personal", Schedule: watch.Schedule{IntervalSeconds: 300}, Trigger: "becomes_false", InitialObservation: "baseline", Mode: "continuous", Inputs: map[string]detective.InputValue{"order_id": {Scalar: &input}}}
	before, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.ValidateDefinition(definition); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("validation mutated unsaved proposal")
	}
	definition.CheckVersion = "missing"
	if err := watch.ValidateDefinition(definition); !errors.Is(err, watch.ErrInvalid) {
		t.Fatalf("invalid Check pin accepted: %v", err)
	}
	definition.CheckVersion = "1"
	definition.Inputs = map[string]detective.InputValue{"credential_token": {Scalar: &input}}
	if err := watch.ValidateDefinition(definition); !errors.Is(err, watch.ErrInvalid) {
		t.Fatalf("credential binding accepted: %v", err)
	}
}
