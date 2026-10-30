package watch

// ValidateDefinition validates the native Watch definition without reading or
// writing a catalog. It lets editors review embedded proposals using the same
// structural rules as Store.PutDefinition.
//
// This does not authorize saving or execution. PutDefinition must still enforce
// current scope, source, ownership, subscriptions, capacity and configured limits.
func ValidateDefinition(definition Definition) error {
	return validateDefinition(definition)
}
