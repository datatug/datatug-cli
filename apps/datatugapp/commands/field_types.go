package commands

import "github.com/datatug/datatug-core/pkg/datatug"

// datatugTypeCurrency is a datatug-cli-local entity field type: the vendored
// copy of the model this migration replaced (pkg/datatug-core/datatug/types.go)
// carried it as datatug.TypeCurrency, but datatug-core v0.17.0 does not. No
// production code depends on it (only apps/datatugapp/commands/cmd_entity_test.go),
// so it is restored here via datatug.KnownTypes - an exported, mutable
// package var the model itself provides as an extension point - rather than
// forking datatug.EntityField.Validate()'s type check.
const datatugTypeCurrency = "currency"

func init() {
	datatug.KnownTypes = append(datatug.KnownTypes, datatugTypeCurrency)
}
