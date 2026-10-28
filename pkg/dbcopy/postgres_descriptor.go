package dbcopy

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/datatug/datatug-cli/internal/strictjson"
)

const (
	// DescriptorEnvPrefix is the name prefix a project descriptor's dsnEnv may
	// always carry: a variable the operator named for DataTug.
	DescriptorEnvPrefix = "DATATUG_"

	// DescriptorEnvAllowList names the operator-set variable that lists, comma or
	// space separated, the other variables a project descriptor may name. It
	// follows the shape of the OpenVaultDB descriptor, whose token is bound to a
	// destination the operator sets (openvaultdb_source.go): the project
	// file proposes, the operator's own environment decides.
	DescriptorEnvAllowList = "DATATUG_DSN_ENV_ALLOW"
)

// maxPostgresDescriptorBytes caps a descriptor file; a real one is a few dozen bytes.
const maxPostgresDescriptorBytes = 65536

// ConnectionDescriptor is the project-owned file that points a source at its
// credentials. It names an environment variable and nothing else: the variable
// holds the whole connection URL (host, user and password together), so no
// project file can hold a password, and a project file cannot redirect a
// credential to a host of its own choosing. PostgreSQL, MySQL, Oracle, SQL
// Server, CouchDB and PostgREST catalogs all use this one shape.
type ConnectionDescriptor struct {
	// DSNEnv is the name of the environment variable that holds the source URL.
	// It must match ^[A-Z][A-Z0-9_]*$.
	DSNEnv string `json:"dsnEnv"`
}

// PostgresDescriptor is the connection descriptor of a PostgreSQL (or MySQL,
// Oracle, SQL Server or Firestore) catalog. Kept as the historical name of
// ConnectionDescriptor: the file shape is the same for every descriptor-backed
// source.
type PostgresDescriptor = ConnectionDescriptor

// SourceURL returns the source URL that resolves this descriptor: "env:NAME".
// It names the variable and never holds its value, so it is safe to pass
// through the chat store, logs and error messages.
func (d ConnectionDescriptor) SourceURL() string {
	return envPrefix + d.DSNEnv
}

// CheckDescriptorEnvName reports whether a project descriptor may name the
// environment variable name. A project file is not trusted: a cloned project
// could otherwise select any database whose URL the operator keeps in the
// environment. So a descriptor (of any descriptor-backed driver) may name only
// a variable whose name starts with DATATUG_, or one the operator lists in
// DATATUG_DSN_ENV_ALLOW. name must already match ValidEnvName, which keeps the
// error free to quote it.
func CheckDescriptorEnvName(name string, lookupEnv func(string) (string, bool)) error {
	if strings.HasPrefix(name, DescriptorEnvPrefix) {
		return nil
	}
	if listed, found := lookupEnv(DescriptorEnvAllowList); found {
		for _, allowed := range strings.FieldsFunc(listed, isAllowListSeparator) {
			if allowed == name {
				return nil
			}
		}
	}
	return fmt.Errorf(
		"connection descriptor names %s, which is not allowed: a project file may name only environment variables that start with %s or that the operator lists in %s",
		name, DescriptorEnvPrefix, DescriptorEnvAllowList,
	)
}

func isAllowListSeparator(r rune) bool { return r == ',' || unicode.IsSpace(r) }

// ReadPostgresDescriptor reads and strictly decodes the descriptor at path, and
// refuses a dsnEnv that CheckDescriptorEnvName does not allow in the process
// environment.
func ReadPostgresDescriptor(path string) (PostgresDescriptor, error) {
	return readPostgresDescriptor(path, os.LookupEnv)
}

// readPostgresDescriptor is ReadPostgresDescriptor over an injected environment
// lookup, so tests never touch the process environment.
func readPostgresDescriptor(path string, lookupEnv func(string) (string, bool)) (PostgresDescriptor, error) {
	return readConnectionDescriptor(path, "PostgreSQL", lookupEnv)
}

// ReadCouchDBDescriptor reads and strictly decodes the CouchDB connection
// descriptor at path, applying the same dsnEnv rule as ReadPostgresDescriptor:
// a project file may name only a variable the operator allowed, and the
// endpoint itself is never in the project.
func ReadCouchDBDescriptor(path string) (ConnectionDescriptor, error) {
	return readCouchDBDescriptor(path, os.LookupEnv)
}

// readCouchDBDescriptor is ReadCouchDBDescriptor over an injected environment
// lookup, so tests never touch the process environment.
func readCouchDBDescriptor(path string, lookupEnv func(string) (string, bool)) (ConnectionDescriptor, error) {
	return readConnectionDescriptor(path, "CouchDB", lookupEnv)
}

// ReadPostgRESTDescriptor reads and strictly decodes the PostgREST connection
// descriptor at path, applying the same dsnEnv rule as ReadPostgresDescriptor and
// ReadCouchDBDescriptor: a project file may name only a variable the operator
// allowed, and the API root is never in the project.
func ReadPostgRESTDescriptor(path string) (ConnectionDescriptor, error) {
	return readPostgRESTDescriptor(path, os.LookupEnv)
}

// readPostgRESTDescriptor is ReadPostgRESTDescriptor over an injected environment
// lookup, so tests never touch the process environment.
func readPostgRESTDescriptor(path string, lookupEnv func(string) (string, bool)) (ConnectionDescriptor, error) {
	return readConnectionDescriptor(path, "PostgREST", lookupEnv)
}

// readConnectionDescriptor is the one reader of a descriptor file; title names
// the source in the error text ("PostgreSQL", "CouchDB").
func readConnectionDescriptor(path, title string, lookupEnv func(string) (string, bool)) (ConnectionDescriptor, error) {
	f, err := os.Open(path)
	if err != nil {
		return ConnectionDescriptor{}, fmt.Errorf("open %s connection descriptor: %w", title, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPostgresDescriptorBytes+1))
	if err != nil {
		return ConnectionDescriptor{}, fmt.Errorf("read %s connection descriptor: %w", title, err)
	}
	if len(data) > maxPostgresDescriptorBytes {
		return ConnectionDescriptor{}, fmt.Errorf("invalid %s connection descriptor size", title)
	}
	descriptor, err := decodeConnectionDescriptor(data, title)
	if err != nil {
		return ConnectionDescriptor{}, err
	}
	if err = CheckDescriptorEnvName(descriptor.DSNEnv, lookupEnv); err != nil {
		return ConnectionDescriptor{}, err
	}
	return descriptor, nil
}

// DecodePostgresDescriptor strictly decodes a descriptor: one JSON object, no
// duplicate keys, no field other than dsnEnv, and a valid variable name. No
// error it returns holds a value from the input; an unknown field is named
// only when the name is a plain identifier.
func DecodePostgresDescriptor(data []byte) (PostgresDescriptor, error) {
	return decodeConnectionDescriptor(data, "PostgreSQL")
}

// DecodeCouchDBDescriptor is DecodePostgresDescriptor for a CouchDB descriptor:
// the same one-field shape, with errors that name CouchDB.
func DecodeCouchDBDescriptor(data []byte) (ConnectionDescriptor, error) {
	return decodeConnectionDescriptor(data, "CouchDB")
}

// DecodePostgRESTDescriptor is DecodePostgresDescriptor for a PostgREST
// descriptor: the same one-field shape, with errors that name PostgREST.
func DecodePostgRESTDescriptor(data []byte) (ConnectionDescriptor, error) {
	return decodeConnectionDescriptor(data, "PostgREST")
}

func decodeConnectionDescriptor(data []byte, title string) (ConnectionDescriptor, error) {
	var fields map[string]json.RawMessage
	if err := strictjson.DecodeObject(data, &fields); err != nil {
		return ConnectionDescriptor{}, fmt.Errorf("invalid %s connection descriptor", title)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "dsnEnv" {
			return ConnectionDescriptor{}, unknownDescriptorFieldError(key, title)
		}
	}
	var descriptor ConnectionDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		return ConnectionDescriptor{}, fmt.Errorf("invalid %s connection descriptor", title)
	}
	if descriptor.DSNEnv == "" {
		return ConnectionDescriptor{}, fmt.Errorf("%s connection descriptor requires dsnEnv: the name of the environment variable that holds the connection URL", title)
	}
	if !ValidEnvName(descriptor.DSNEnv) {
		return ConnectionDescriptor{}, fmt.Errorf("%s connection descriptor dsnEnv must be an environment variable name matching %s", title, envNamePattern)
	}
	return descriptor, nil
}

var plainFieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func unknownDescriptorFieldError(key, title string) error {
	const hint = "a descriptor holds only dsnEnv; put the connection URL in an environment variable and name it there"
	if plainFieldName.MatchString(key) {
		return fmt.Errorf("unknown %s connection field %q: %s", title, key, hint)
	}
	return fmt.Errorf("unknown %s connection field: %s", title, hint)
}
