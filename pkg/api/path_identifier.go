package api

import (
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/strongo/validation"
)

// ValidateIdentifier refuses an environment, catalog or project-item ID that a
// client sent and that becomes a folder or file name: it returns nil when id is a
// plain name (see dbcopy.IsPlainSourceID: letters and digits of any script, "."
// "_" and "-", at most 128 characters, starting with a letter or a digit) and a
// bad-request error for field otherwise.
//
// A plain name is one path segment whatever the platform: it holds no path
// separator of either kind, no drive letter, no NUL, no "%" (so a separator that
// is still percent-encoded after the request's own decoding stays inert) and is
// never "." or "..". The error names the field and the rule and nothing of id: id
// may be a whole source string, or the text of an attack, and is not echoed.
func ValidateIdentifier(field, id string) error {
	if dbcopy.IsPlainSourceID(id) {
		return nil
	}
	return validation.NewErrBadRequestFieldValue(field, "must be a plain name: letters, digits, '.', '_' and '-', at most 128 characters, starting with a letter or a digit")
}

// ValidateCatalogIdentifiers is the check of the two IDs that GET
// /datatug/catalog-tables turns into folder names, environment first. Both the
// route and GetCatalogTables apply it, so the refusal is the same wherever it is
// made.
func ValidateCatalogIdentifiers(environmentID, catalogID string) error {
	if err := ValidateIdentifier("environment", environmentID); err != nil {
		return err
	}
	return ValidateIdentifier("catalog", catalogID)
}
