// Package accesspolicies discovers and decodes the DALgo access policies a
// DataTug user keeps on disk, runs a query through them as a secured
// application would, and explains what the policies did to that query.
package accesspolicies

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dal-go/dalgo/access"
)

// DirEnv names the environment variable that overrides the default policies
// directory.
const DirEnv = "DATATUG_POLICIES_DIR"

// DefaultDir is the per-user policies directory relative to the home directory.
const DefaultDir = ".datatug/policies"

// ErrNoPolicies is returned by Load when nothing was loaded and the caller did
// not ask to run unrestricted.
var ErrNoPolicies = errors.New("no access policies loaded; pass --no-policies to run unrestricted")

// ResolveDir picks the policies directory: an explicit value first, then
// $DATATUG_POLICIES_DIR, then ~/.datatug/policies. explicit reports whether
// the directory was configured by the caller (and so must exist).
func ResolveDir(flagValue string) (dir string, explicit bool, err error) {
	if flagValue != "" {
		return flagValue, true, nil
	}
	if fromEnv := os.Getenv(DirEnv); fromEnv != "" {
		return fromEnv, true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve policies directory: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(DefaultDir)), false, nil
}

// Loaded is one decoded access document and the file it came from.
type Loaded struct {
	Policy access.Policy
	Source string
}

// LoadOptions says where policies come from.
type LoadOptions struct {
	// Dir is the --policies-dir value; empty means environment or default.
	Dir string
	// Files are additional --policy documents, appended after the directory.
	Files []string
	// None skips discovery (--no-policies); it conflicts with Files.
	None bool
}

// Load discovers and decodes the caller's policies. A missing default
// directory is empty; a missing explicit one is an error. Nothing loaded is
// ErrNoPolicies unless None is set.
func Load(o LoadOptions) ([]Loaded, error) {
	if o.None {
		if len(o.Files) > 0 {
			return nil, errors.New("--no-policies and --policy are mutually exclusive")
		}
		return nil, nil
	}
	dir, explicit, err := ResolveDir(o.Dir)
	if err != nil {
		return nil, err
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		if explicit && errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("policies directory %s does not exist", dir)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	for _, path := range o.Files {
		item, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, item)
	}
	if len(loaded) == 0 {
		return nil, ErrNoPolicies
	}
	return loaded, nil
}

// LoadDir decodes every regular *.yaml, *.yml and *.json file in dir, in file
// name order. A directory that does not exist is reported with fs.ErrNotExist.
func LoadDir(dir string) ([]Loaded, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read policies directory %s: %w", dir, err)
	}
	var loaded []Loaded
	for _, entry := range entries {
		if entry.IsDir() || codecFor(entry.Name()) == nil {
			continue
		}
		item, err := LoadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, item)
	}
	return loaded, nil
}

// LoadFile decodes one access document; the codec follows the extension.
func LoadFile(path string) (Loaded, error) {
	codec := codecFor(path)
	if codec == nil {
		return Loaded{}, fmt.Errorf("policy %s: unsupported extension, want .yaml, .yml or .json", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("policy %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	policy, err := access.DecodePolicy(file, codec, access.WithSource(path))
	if err != nil {
		return Loaded{}, fmt.Errorf("policy %s: %w", path, err)
	}
	return Loaded{Policy: policy, Source: path}, nil
}

// Policies returns the decoded policies in load order.
func Policies(loaded []Loaded) []access.Policy {
	policies := make([]access.Policy, 0, len(loaded))
	for _, item := range loaded {
		policies = append(policies, item.Policy)
	}
	return policies
}

func codecFor(name string) access.Codec {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		return access.YAMLCodec{}
	case ".json":
		return access.JSONCodec{}
	}
	return nil
}
