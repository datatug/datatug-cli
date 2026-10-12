package dbcopy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/datatug/datatug-cli/pkg/openvaultdb"
)

const (
	// DescriptorEnvPrefix is the name prefix a project descriptor's dsnEnv may
	// always carry: a variable the operator named for DataTug.
	DescriptorEnvPrefix = "DATATUG_"

	// DescriptorEnvAllowList names the operator-set variable that lists, comma or
	// space separated, the other variables a project descriptor may name. It
	// follows the shape of the OpenVaultDB descriptor, whose token is bound to a
	// destination the operator sets (pkg/openvaultdb/source.go): the project
	// file proposes, the operator's own environment decides.
	DescriptorEnvAllowList = "DATATUG_DSN_ENV_ALLOW"
)

// maxPostgresDescriptorBytes caps a descriptor file; a real one is a few dozen bytes.
const maxPostgresDescriptorBytes = 65536

// PostgresDescriptor is the project-owned file that points a PostgreSQL source
// at its credentials. It names an environment variable and nothing else: the
// variable holds the whole connection URL (host, user and password together),
// so no project file can hold a password, and a project file cannot redirect a
// credential to a host of its own choosing.
type PostgresDescriptor struct {
	// DSNEnv is the name of the environment variable that holds a postgres://
	// or postgresql:// URL. It must match ^[A-Z][A-Z0-9_]*$.
	DSNEnv string `json:"dsnEnv"`
}

// SourceURL returns the source URL that resolves this descriptor: "env:NAME".
// It names the variable and never holds its value, so it is safe to pass
// through the chat store, logs and error messages.
func (d PostgresDescriptor) SourceURL() string {
	return envPrefix + d.DSNEnv
}

// CheckDescriptorEnvName reports whether a project descriptor may name the
// environment variable name. A project file is not trusted: a cloned project
// could otherwise select any database whose URL the operator keeps in the
// environment. So a descriptor may name only a variable whose name starts with
// DATATUG_, or one the operator lists in DATATUG_DSN_ENV_ALLOW. name must
// already match ValidEnvName, which keeps the error free to quote it.
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
		"PostgreSQL connection descriptor names %s, which is not allowed: a project file may name only environment variables that start with %s or that the operator lists in %s",
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
	f, err := os.Open(path)
	if err != nil {
		return PostgresDescriptor{}, fmt.Errorf("open PostgreSQL connection descriptor: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPostgresDescriptorBytes+1))
	if err != nil {
		return PostgresDescriptor{}, fmt.Errorf("read PostgreSQL connection descriptor: %w", err)
	}
	if len(data) > maxPostgresDescriptorBytes {
		return PostgresDescriptor{}, errors.New("invalid PostgreSQL connection descriptor size")
	}
	descriptor, err := DecodePostgresDescriptor(data)
	if err != nil {
		return PostgresDescriptor{}, err
	}
	if err = CheckDescriptorEnvName(descriptor.DSNEnv, lookupEnv); err != nil {
		return PostgresDescriptor{}, err
	}
	return descriptor, nil
}

// DecodePostgresDescriptor strictly decodes a descriptor: one JSON object, no
// duplicate keys, no field other than dsnEnv, and a valid variable name. No
// error it returns holds a value from the input; an unknown field is named
// only when the name is a plain identifier.
func DecodePostgresDescriptor(data []byte) (PostgresDescriptor, error) {
	var fields map[string]json.RawMessage
	if err := openvaultdb.DecodeJSONObjectStrict(data, &fields); err != nil {
		return PostgresDescriptor{}, errors.New("invalid PostgreSQL connection descriptor")
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "dsnEnv" {
			return PostgresDescriptor{}, unknownDescriptorFieldError(key)
		}
	}
	var descriptor PostgresDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		return PostgresDescriptor{}, errors.New("invalid PostgreSQL connection descriptor")
	}
	if descriptor.DSNEnv == "" {
		return PostgresDescriptor{}, errors.New(`PostgreSQL connection descriptor requires dsnEnv: the name of the environment variable that holds the connection URL`)
	}
	if !ValidEnvName(descriptor.DSNEnv) {
		return PostgresDescriptor{}, fmt.Errorf("PostgreSQL connection descriptor dsnEnv must be an environment variable name matching %s", envNamePattern)
	}
	return descriptor, nil
}

var plainFieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func unknownDescriptorFieldError(key string) error {
	const hint = "a descriptor holds only dsnEnv; put the connection URL in an environment variable and name it there"
	if plainFieldName.MatchString(key) {
		return fmt.Errorf("unknown PostgreSQL connection field %q: %s", key, hint)
	}
	return fmt.Errorf("unknown PostgreSQL connection field: %s", hint)
}
