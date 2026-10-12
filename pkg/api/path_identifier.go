package api

import (
	"slices"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
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
	return validation.NewErrBadRequestFieldValue(field, PlainNameRule)
}

// PlainNameRule is the sentence that says what a plain name is. Every refusal made
// by ValidateIdentifier, and the contract routes' own answer for an environment,
// carry it, and nothing of the value they refused.
const PlainNameRule = "must be a plain name: letters, digits, '.', '_' and '-', at most 128 characters, starting with a letter or a digit"

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

// MaxPathIdentifierLength is the most bytes an ID made of folders has in all, whatever its
// parts are: a folder tree of a project is nowhere near it, and a longer ID is a text that
// no file system opens, which would be quoted whole in an answer and in the log.
const MaxPathIdentifierLength = 512

// PlainPathRule is the sentence that says what an ID made of folders is: the folders
// and the name of a recordset definition or of a folder, each a plain name, and at most
// MaxPathIdentifierLength bytes in all.
const PlainPathRule = "must be plain names separated by '/': each of letters, digits, '.', '_' and '-', at most 128 characters, starting with a letter or a digit; at most 512 bytes in all"

// ValidatePathIdentifier refuses an ID with folders that a client sent and that becomes
// a path under the project (a recordset definition, a folder): it returns nil when
// every "/"-separated part of id is a plain name (see ValidateIdentifier), and a
// bad-request error for field otherwise. A part that is empty, "." or ".." is not a
// plain name, so there is no absolute path, no "a//b", no trailing slash and no way
// out of the folder that holds the item; so is an id of more than MaxPathIdentifierLength
// bytes in all. The error names the field and the rule and nothing of id.
func ValidatePathIdentifier(field, id string) error {
	if len(id) > MaxPathIdentifierLength {
		return validation.NewErrBadRequestFieldValue(field, PlainPathRule)
	}
	for _, part := range strings.Split(id, "/") {
		if !dbcopy.IsPlainSourceID(part) {
			return validation.NewErrBadRequestFieldValue(field, PlainPathRule)
		}
	}
	return nil
}

// HostRule is the sentence that says what the host of a db server is.
const HostRule = "must be a host name or an address: letters, digits, '.', '_', ':' and '-', at most 253 characters, starting with a letter, a digit or ':'"

// validateHost refuses a host that is not one a project can record (see
// dbcopy.IsRecordableHost, the one rule for it): the host is part of the ID of a db server,
// which is part of a file name, and of the connection string that reaches the server.
func validateHost(field, host string) error {
	if !dbcopy.IsRecordableHost(host) {
		return validation.NewErrBadRequestFieldValue(field, HostRule)
	}
	return nil
}

// dbServerDrivers are the drivers datatug.ServerRef.Validate accepts (datatug-core
// v0.42.3: the drivers of a file or a URL, then the drivers of a server). A driver that is
// a plain name and is not one of them is refused by ValidateServerRef with a fixed
// message: the one of datatug.ServerRef quotes the driver.
var dbServerDrivers = []string{"sqlite3", "ingitdb", "openvaultdb", "https-json", "sqlserver", "mysql", "oracle", "postgres"}

// hostlessDrivers are the drivers of dbServerDrivers whose server is a file or a URL and
// has no host: datatug.ServerRef.Validate refuses a host for them, and for sqlite3 it
// quotes it.
var hostlessDrivers = []string{"sqlite3", "ingitdb", "openvaultdb", "https-json"}

// DriverRule is the sentence that says which drivers a db server can have.
var DriverRule = "must be one of " + strings.Join(dbServerDrivers, ", ")

// HostlessRule is the sentence that says a db server of a driver of hostlessDrivers has
// no host; driver is one of them, a plain name.
func HostlessRule(driver string) string { return "cannot be used with " + driver }

// ValidateServerRef refuses a db server whose driver or host a project could not record
// under the file name of the server (the driver is a folder of the project, and the driver
// and the host are the ID of the server, which is a file name): the driver must be a plain
// name that a db server can have, and the host a host, and none for a server of a file or a
// URL (sqlite3 and the like). An empty driver or host passes, as it is the missing-field
// answer of datatug.ServerRef.Validate. The error names the field and the rule and nothing
// of the value: the refusals of datatug.ServerRef.Validate that are left for the routes to
// give do not quote the driver or the host (see DriverRule and HostlessRule).
func ValidateServerRef(server datatug.ServerRef) error {
	if server.Driver != "" {
		if err := ValidateIdentifier("driver", server.Driver); err != nil {
			return err
		}
		if !slices.Contains(dbServerDrivers, server.Driver) {
			return validation.NewErrBadRequestFieldValue("driver", DriverRule)
		}
	}
	if server.Host != "" {
		if err := validateHost("host", server.Host); err != nil {
			return err
		}
		if slices.Contains(hostlessDrivers, server.Driver) {
			return validation.NewErrBadRequestFieldValue("host", HostlessRule(server.Driver))
		}
	}
	return nil
}

// ValidateProjectIdentifier refuses a project ID that a client sent unless this process
// serves that project (the ID is then a key it holds, whatever the project is called) or
// the ID is a plain name. A project ID is never joined into a path by this package, but
// it names the project directory the store opens, so an ID that is neither served nor a
// plain name is refused before a store is asked for it. It does not say that a plain name
// is served: a route that resolves the store of its project does (see ResolveStoreID), and
// so does each entry that takes the project from the body of the request itself and opens
// its store (ExecuteSelect, ExecuteCommands, CreateFolder, CreateQuery, UpdateQuery; see
// servedProjectDir), with the same answer, before a project store is asked for.
func ValidateProjectIdentifier(field, id string) error {
	if _, served := projectDir(id); served {
		return nil
	}
	return ValidateIdentifier(field, id)
}

// validateProjectItem is the check of a project and of the ID of an item of it that the
// store joins into a path (a board, an entity): the project first, then the ID, which
// must be a plain name.
func validateProjectItem(projectID, idField, id string) error {
	if err := ValidateProjectIdentifier("project", projectID); err != nil {
		return err
	}
	return ValidateIdentifier(idField, id)
}

// validateProjectPathItem is validateProjectItem for an ID with folders.
func validateProjectPathItem(projectID, idField, id string) error {
	if err := ValidateProjectIdentifier("project", projectID); err != nil {
		return err
	}
	return ValidatePathIdentifier(idField, id)
}

// validateDbServer is the check every db-server route makes before the project store is
// asked: the driver and the host pass their rules (see ValidateServerRef), and then the
// reference is the valid one of the routes that always checked it.
func validateDbServer(server datatug.ServerRef) error {
	if err := ValidateServerRef(server); err != nil {
		return err
	}
	if err := server.Validate(); err != nil {
		return validation.NewBadRequestError(err)
	}
	return nil
}

// ValidateProjDbServerNames refuses a db server that is saved whose driver, host or ID a
// project could not record: the driver and the host pass their rules (see
// ValidateServerRef), and an ID that is given is the ID of its reference, which is the
// file name the server is saved under. It checks names only, so the route that takes a
// server in its body can run it before the validation the server has of its own, which
// quotes the driver and the IDs it refuses.
func ValidateProjDbServerNames(server datatug.ProjDbServer) error {
	if err := ValidateServerRef(server.Server); err != nil {
		return err
	}
	if server.ID != "" && server.ID != server.Server.GetID() {
		return validation.NewErrBadRequestFieldValue("id", "must be the ID of the server: its driver, host and port")
	}
	return nil
}

// validateProjDbServer is validateDbServer for a server that is saved.
func validateProjDbServer(server datatug.ProjDbServer) error {
	if err := ValidateProjDbServerNames(server); err != nil {
		return err
	}
	if err := server.Server.Validate(); err != nil {
		return validation.NewBadRequestError(err)
	}
	return nil
}
