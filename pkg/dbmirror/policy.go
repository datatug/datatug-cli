package dbmirror

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// PolicyFormat is the format a mirror policy file must declare.
const PolicyFormat = "datatug-mirror-policy/v1"

// Policy says which defined collections of a mirror may be copied out of the
// source, which may not, and which fields are masked. It is a file of the
// DataTug project (YAML):
//
//	format: datatug-mirror-policy/v1
//	definitions: ../firestore         # the inGitDB mirror, relative to this file
//	output: ../data/prod-backup       # where records go, relative to this file
//	copy:
//	  - spaces
//	  - spaces/ext
//	exclude:
//	  - ext/secrets
//	mask:
//	  ext/customers: [email, displayName, members.*.phone]
//
// A collection is named by the collection ids of its path joined by "/".
// A collection in neither copy nor exclude is undeclared: it is never copied
// and the report lists it, so a collection added to the definitions later is
// not copied until someone decides about it. The policy holds no credential
// and no connection: unknown keys are refused.
type Policy struct {
	Format      string              `yaml:"format"`
	Definitions string              `yaml:"definitions,omitempty"`
	Output      string              `yaml:"output,omitempty"`
	Copy        []string            `yaml:"copy,omitempty"`
	Exclude     []string            `yaml:"exclude,omitempty"`
	Mask        map[string][]string `yaml:"mask,omitempty"`

	// Path is the file the policy was read from ("" when built in code).
	Path string `yaml:"-"`
}

// ReadPolicy reads and checks the structure of a policy file. Whether its
// collections are defined is checked by Check, once the definitions are read.
func ReadPolicy(path string) (*Policy, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	policy, err := ParsePolicy(content)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	policy.Path = path
	return policy, nil
}

// ParsePolicy decodes policy YAML, refusing unknown keys and a second document.
func ParsePolicy(content []byte) (*Policy, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	policy := &Policy{}
	if err := decoder.Decode(policy); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the policy file is empty")
		}
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("the policy file must hold one YAML document")
	}
	if policy.Format != PolicyFormat {
		return nil, fmt.Errorf("format must be %q, got %q", PolicyFormat, policy.Format)
	}
	return policy, nil
}

// resolve returns a path of the policy relative to the policy file's folder.
func (p *Policy) resolve(path string) string {
	if path == "" || filepath.IsAbs(path) || p.Path == "" {
		return path
	}
	return filepath.Join(filepath.Dir(p.Path), path)
}

// DefinitionsDir is the definitions folder the policy names ("" if none).
func (p *Policy) DefinitionsDir() string { return p.resolve(p.Definitions) }

// OutputDir is the output folder the policy names ("" if none).
func (p *Policy) OutputDir() string { return p.resolve(p.Output) }

// Check verifies the policy against the definitions: every collection it
// names is defined, none is both copied and excluded or named twice, every
// masked collection is copied, and every mask path is well formed.
func (p *Policy) Check(defs *Definitions) error {
	seen := map[string]string{}
	for _, list := range []struct {
		name  string
		items []string
	}{{"copy", p.Copy}, {"exclude", p.Exclude}} {
		for _, id := range list.items {
			if defs.Spec(id) == nil {
				return fmt.Errorf("policy %s: collection %q is not defined in the mirror", list.name, id)
			}
			if prior, ok := seen[id]; ok {
				return fmt.Errorf("policy: collection %q is listed in both %s and %s", id, prior, list.name)
			}
			seen[id] = list.name
		}
	}
	for _, id := range sortedKeys(p.Mask) {
		if seen[id] != "copy" {
			return fmt.Errorf("policy mask: collection %q must also be listed in copy", id)
		}
		for _, path := range p.Mask[id] {
			if path == "" || slices.Contains(strings.Split(path, "."), "") {
				return fmt.Errorf("policy mask: %q of collection %q is not a field path (names joined by \".\", \"*\" for every key or item)", path, id)
			}
		}
	}
	return nil
}

// action is what the policy says about a defined collection.
func (p *Policy) action(id string) string {
	switch {
	case slices.Contains(p.Exclude, id):
		return ActionExclude
	case !slices.Contains(p.Copy, id):
		return ActionUndeclared
	case len(p.Mask[id]) > 0:
		return ActionMask
	}
	return ActionCopy
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
