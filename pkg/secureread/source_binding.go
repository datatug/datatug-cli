package secureread

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

const ExactTableReadProfile = "exact-table-readonly-v1"

var ErrSourceBinding = fmt.Errorf("%w: configured source binding is unavailable; validate a new session", access.ErrAccessDenied)

// BoundSessionOptions contains trusted startup options, never request fields.
// Required profile policies are always loaded; optional files add intersections.
type BoundSessionOptions struct {
	Required         bool
	As               string
	Roles, Groups    []string
	ExtraPolicyFiles []string
	ExtraPoliciesDir string
}

type boundSourceConfig struct {
	Profile          string   `json:"profile"`
	Project          string   `json:"project"`
	Environment      string   `json:"environment"`
	Catalog          string   `json:"catalog"`
	SQLiteFile       string   `json:"sqliteFile"`
	RequiredPolicies []string `json:"requiredPolicies"`
	Principal        struct {
		ID     string   `json:"id"`
		Roles  []string `json:"roles"`
		Groups []string `json:"groups"`
	} `json:"principal"`
}

type sourceBinding struct {
	config                         boundSourceConfig
	root, target, url, registry    string
	ref                            dbcopy.BackendRef
	file                           os.FileInfo
	files                          map[string][sha256.Size]byte
	directories                    map[string][sha256.Size]byte
	principal                      *access.Principal
	policies                       []accesspolicies.Loaded
	policyFingerprint, fingerprint string
	invalid                        atomic.Bool
	projectPoliciesAbsent          bool
}

// Private filesystem dependencies let failure tests interrupt a snapshot read
// deterministically. Production always uses these exact local filesystem calls.
var (
	bindingAbs      = filepath.Abs
	bindingStat     = os.Stat
	bindingLstat    = os.Lstat
	bindingReadFile = boundedBindingFile
)

// LoadBoundSession loads the committed local source profile, or reports no
// declared profile for unchanged legacy startup. Required=true never falls back.
// ctx must be nonnil, as required by the Go context contract. The concrete pinned
// filestore loader filters nil catalogs before returning its catalog list.
func LoadBoundSession(ctx context.Context, projectDir string, options BoundSessionOptions) (Session, bool, error) {
	if projectDir == "" {
		if options.Required {
			return Session{}, true, ErrSourceBinding
		}
		return Session{}, false, nil
	}
	if _, err := os.Lstat(filepath.Join(projectDir, ".datatug", "security.json")); errors.Is(err, fs.ErrNotExist) && !options.Required {
		return Session{}, false, nil
	}
	root, err := strictCanonicalFile(projectDir, true)
	if err != nil {
		return Session{}, true, ErrSourceBinding
	}
	configPath := filepath.Join(root, ".datatug", "security.json")
	data, err := bindingReadFile(configPath)
	if err != nil {
		return Session{}, true, ErrSourceBinding
	}
	var config boundSourceConfig
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || apicontract.DecodeStrict(data, &config) != nil || config.Profile != ExactTableReadProfile || !dbcopy.IsPlainSourceID(config.Project) || !dbcopy.IsPlainSourceID(config.Environment) || !dbcopy.IsPlainSourceID(config.Catalog) || config.Principal.ID == "" || len(config.RequiredPolicies) == 0 {
		return Session{}, true, ErrSourceBinding
	}
	if options.As != "" && options.As != config.Principal.ID || len(options.Roles) > 0 && !sameBindingNames(options.Roles, config.Principal.Roles) || len(options.Groups) > 0 && !sameBindingNames(options.Groups, config.Principal.Groups) {
		return Session{}, true, ErrSourceBinding
	}
	target, err := strictSQLitePath(config.SQLiteFile, root, true)
	if err != nil {
		return Session{}, true, ErrSourceBinding
	}
	ref, err := dbcopy.Parse(dbcopy.LocalSourceURL("sqlite", target))
	if err != nil || ref.Scheme != "sqlite" {
		return Session{}, true, ErrSourceBinding
	}
	binding := &sourceBinding{config: config, root: root, target: target, ref: ref, url: dbcopy.LocalSourceURL("sqlite", target), files: make(map[string][sha256.Size]byte), directories: make(map[string][sha256.Size]byte)}
	binding.files[configPath] = sha256.Sum256(data)
	binding.file, err = bindingStat(target)
	if err != nil {
		return Session{}, true, ErrSourceBinding
	}
	// The concrete registry decoder reads only this local tree. Bracket it
	// with the same tree snapshot so ordinary one-way edits cannot pair decoded
	// source P with a later watched registry Q. No hostile-filesystem ABA claim.
	binding.registry, err = bindingTreeDigest(filepath.Join(root, "environments", config.Environment))
	if err != nil {
		return Session{}, true, ErrSourceBinding
	}
	store := filestore.NewProjectStore(config.Project, root)
	catalogs, err := store.LoadEnvDbCatalogs(ctx, config.Environment)
	if err != nil {
		return Session{}, true, ErrSourceBinding
	}
	matches := 0
	for _, catalog := range catalogs {
		if catalog.ID != config.Catalog {
			if catalog.DbModel == config.Catalog {
				return Session{}, true, ErrSourceBinding
			}
			continue
		}
		matches++
		if catalog.Driver != "sqlite" && catalog.Driver != "sqlite3" {
			return Session{}, true, ErrSourceBinding
		}
		actual, err := strictSQLitePath(catalog.Path, root, false)
		if err != nil || actual != target {
			return Session{}, true, ErrSourceBinding
		}
	}
	if matches != 1 {
		return Session{}, true, ErrSourceBinding
	}
	registryAfter, registryErr := bindingTreeDigest(filepath.Join(root, "environments", config.Environment))
	if registryErr != nil || registryAfter != binding.registry {
		return Session{}, true, ErrSourceBinding
	}
	for _, relative := range config.RequiredPolicies {
		if !filepath.IsLocal(relative) || relative == "." {
			return Session{}, true, ErrSourceBinding
		}
		if err := binding.addPolicySet(filepath.Join(root, relative), true); err != nil {
			return Session{}, true, ErrSourceBinding
		}
	}
	// Preserve the ordinary project policy set as a separate intersection.
	projectPolicies := filepath.Join(root, "policies")
	if _, err := bindingLstat(projectPolicies); err == nil {
		if err := binding.addPolicySet(projectPolicies, true); err != nil {
			return Session{}, true, ErrSourceBinding
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		binding.projectPoliciesAbsent = true
	} else {
		return Session{}, true, ErrSourceBinding
	}
	if options.ExtraPoliciesDir != "" {
		if err := binding.addPolicySet(options.ExtraPoliciesDir, false); err != nil {
			return Session{}, true, ErrSourceBinding
		}
	}
	for _, extra := range options.ExtraPolicyFiles {
		if err := binding.addPolicySet(extra, false); err != nil {
			return Session{}, true, ErrSourceBinding
		}
	}
	binding.principal = &access.Principal{ID: config.Principal.ID, Roles: append([]string(nil), config.Principal.Roles...), Groups: append([]string(nil), config.Principal.Groups...)}
	binding.policyFingerprint = accesspolicies.Fingerprint(binding.policies, false, binding.principal)
	var epoch [16]byte
	// crypto/rand.Read fills the buffer or terminates the process (Go 1.27).
	_, _ = rand.Read(epoch[:])
	seed, _ := json.Marshal([]string{string(data), root, target, binding.registry, binding.policyFingerprint, hex.EncodeToString(epoch[:])})
	digest := sha256.Sum256(seed)
	binding.fingerprint = hex.EncodeToString(digest[:])
	session := Session{Principal: cloneBindingPrincipal(binding.principal), Policies: append([]accesspolicies.Loaded(nil), binding.policies...), DatabaseProfile: DatabasePolicyGoverned, binding: binding}
	if err := binding.check(ctx); err != nil {
		return Session{}, true, err
	}
	return session, true, nil
}

func sameBindingNames(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}

func cloneBindingPrincipal(p *access.Principal) *access.Principal {
	return &access.Principal{ID: p.ID, Roles: append([]string(nil), p.Roles...), Groups: append([]string(nil), p.Groups...)}
}

func strictCanonicalFile(path string, directory bool) (string, error) {
	absolute, err := bindingAbs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != filepath.Clean(absolute) {
		return "", ErrSourceBinding
	}
	info, err := bindingStat(canonical)
	if err != nil || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return "", ErrSourceBinding
	}
	return canonical, nil
}

func strictSQLitePath(path, root string, absoluteRequired bool) (string, error) {
	if path == "" || strings.ContainsAny(path, "?$~#%\x00") || strings.Contains(path, ":") || absoluteRequired && !filepath.IsAbs(path) {
		return "", ErrSourceBinding
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return strictCanonicalFile(path, false)
}

func boundedBindingFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return nil, ErrSourceBinding
	}
	return data, nil
}

func (b *sourceBinding) addPolicySet(path string, withinProject bool) error {
	canonical, err := strictCanonicalFile(path, true)
	if err != nil {
		canonical, err = strictCanonicalFile(path, false)
	}
	if err != nil {
		return err
	}
	if withinProject {
		relative, err := filepath.Rel(b.root, canonical)
		if err != nil || !filepath.IsLocal(relative) {
			return ErrSourceBinding
		}
	}
	info, err := bindingStat(canonical)
	if err != nil {
		return err
	}
	type ownedPolicy struct {
		path  string
		data  []byte
		codec access.Codec
	}
	var owned []ownedPolicy
	if info.IsDir() {
		digest, err := bindingTreeSnapshot(canonical, func(path string, data []byte) {
			if filepath.Dir(path) == canonical {
				if codec := bindingPolicyCodec(path); codec != nil {
					owned = append(owned, ownedPolicy{path: path, data: data, codec: codec})
				}
			}
		})
		if err != nil {
			return err
		}
		b.directories[canonical] = sha256.Sum256([]byte(digest))
	} else {
		codec := bindingPolicyCodec(canonical)
		if codec == nil {
			return ErrSourceBinding
		}
		data, err := bindingReadFile(canonical)
		if err != nil {
			return err
		}
		owned = append(owned, ownedPolicy{path: canonical, data: data, codec: codec})
	}
	if len(owned) == 0 {
		return ErrSourceBinding
	}
	for _, document := range owned {
		// Decode and pin exactly the same owned bytes. There is no later file read
		// whose contents can differ from the policy object actually evaluated.
		policy, err := accesspolicies.DecodeLoaded(document.data, document.codec, document.path)
		if err != nil {
			return ErrSourceBinding
		}
		if _, seen := b.files[document.path]; seen {
			continue
		}
		b.files[document.path] = sha256.Sum256(document.data)
		b.policies = append(b.policies, policy)
	}
	return nil
}

func bindingPolicyCodec(path string) access.Codec {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return access.YAMLCodec{}
	case ".json":
		return access.JSONCodec{}
	}
	return nil
}

// Watches only trusted configuration trees. This is not a query/resource walker.
func bindingTreeDigest(root string) (string, error) { return bindingTreeSnapshot(root, nil) }

func bindingTreeSnapshot(root string, onFile func(string, []byte)) (string, error) {
	root = filepath.Clean(root)
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrSourceBinding
		}
		// WalkDir generates only root and descendants; their relative identity
		// is the traversal prefix removed, without a fallible volume conversion.
		relative := strings.TrimPrefix(path, prefix)
		if path == root {
			relative = "."
		}
		if entry.IsDir() {
			_, _ = fmt.Fprintf(hash, "D%d:%s\n", len(relative), relative)
			return nil
		}
		data, err := bindingReadFile(path)
		if err != nil {
			return err
		}
		if onFile != nil {
			onFile(path, data)
		}
		digest := sha256.Sum256(data)
		_, _ = fmt.Fprintf(hash, "F%d:%s:%x\n", len(relative), relative, digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (b *sourceBinding) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.invalid.Load() {
		return ErrSourceBinding
	}
	fail := func() error { b.invalid.Store(true); return ErrSourceBinding }
	current, err := strictCanonicalFile(b.target, false)
	if err != nil || current != b.target {
		return fail()
	}
	info, err := bindingStat(current)
	if err != nil || !os.SameFile(b.file, info) {
		return fail()
	}
	// Absence is part of the original mandatory-policy snapshot. Newly
	// introduced project policies require an explicit new validated session.
	if b.projectPoliciesAbsent {
		if _, err := bindingLstat(filepath.Join(b.root, "policies")); !errors.Is(err, fs.ErrNotExist) {
			return fail()
		}
	}
	for path, expected := range b.files {
		canonical, err := strictCanonicalFile(path, false)
		if err != nil || canonical != path {
			return fail()
		}
		data, err := bindingReadFile(path)
		if err != nil || sha256.Sum256(data) != expected {
			return fail()
		}
	}
	for path, expected := range b.directories {
		digest, err := bindingTreeDigest(path)
		if err != nil || sha256.Sum256([]byte(digest)) != expected {
			return fail()
		}
	}
	registry, err := bindingTreeDigest(filepath.Join(b.root, "environments", b.config.Environment))
	if err != nil || registry != b.registry {
		return fail()
	}
	return ctx.Err()
}

func (s Session) SourceBound() bool { return s.binding != nil }
func (s Session) SourceBindingFingerprint() string {
	if s.binding == nil {
		return ""
	}
	return s.binding.fingerprint
}
func (s Session) BoundScope() (project, environment, catalog string, bound bool) {
	if s.binding == nil {
		return "", "", "", false
	}
	c := s.binding.config
	return c.Project, c.Environment, c.Catalog, true
}

// CheckSourceBinding revalidates the fixed configuration. ctx must be nonnil.
func (s Session) CheckSourceBinding(ctx context.Context) error {
	if s.configurationErr != nil {
		return s.configurationErr
	}
	if s.binding == nil {
		return nil
	}
	if s.DatabaseProfile != DatabasePolicyGoverned || s.Unrestricted || s.LocalProjectOwner || s.AllowAnonymousLocalIdentity || s.AllowOpaqueSQL || s.Principal == nil || s.Principal.Subject != nil || accesspolicies.Fingerprint(s.Policies, false, s.Principal) != s.binding.policyFingerprint {
		return ErrSourceBinding
	}
	return s.binding.check(ctx)
}
func (s Session) BoundSource(ctx context.Context, project, root, environment, catalog string) (string, bool, error) {
	if s.binding == nil {
		return "", false, nil
	}
	b := s.binding
	if err := s.CheckSourceBinding(ctx); err != nil {
		return "", true, err
	}
	canonical, err := strictCanonicalFile(root, true)
	if err != nil || canonical != b.root || project != b.config.Project || environment != b.config.Environment || catalog != "" && catalog != b.config.Catalog {
		return "", true, ErrSourceBinding
	}
	return b.url, true, nil
}
func (s Session) BoundBackend(ctx context.Context, url string) (dbcopy.BackendRef, bool, error) {
	if s.binding == nil {
		return dbcopy.BackendRef{}, false, nil
	}
	if err := s.CheckSourceBinding(ctx); err != nil {
		return dbcopy.BackendRef{}, true, err
	}
	if url != s.binding.url {
		return dbcopy.BackendRef{}, true, ErrSourceBinding
	}
	return s.binding.ref, true, nil
}
func (s Session) ValidateBoundProjects(paths map[string]string) error {
	if s.binding == nil {
		return nil
	}
	if len(paths) != 1 {
		return ErrSourceBinding
	}
	root, ok := paths[s.binding.config.Project]
	if !ok {
		return ErrSourceBinding
	}
	_, _, err := s.BoundSource(context.Background(), s.binding.config.Project, root, s.binding.config.Environment, s.binding.config.Catalog)
	return err
}

func (s Session) ValidateBoundServer(paths map[string]string, writes, opaque, live bool) error {
	if s.binding == nil {
		return validateUnboundSourceRegistry(paths)
	}
	if writes || opaque || live || s.ValidateBoundProjects(paths) != nil {
		s.binding.invalid.Store(true)
		return ErrSourceBinding
	}
	return s.CheckSourceBinding(context.Background())
}

// Use the exact served registry at each startup boundary; callers must not
// normalize paths differently from the store or omit discovered child projects.
func validateUnboundSourceRegistry(paths map[string]string) error {
	for id, root := range paths {
		if strings.Contains(root, "://") {
			continue
		}
		_, declared, err := LoadBoundSession(context.Background(), root, BoundSessionOptions{})
		if err != nil {
			return fmt.Errorf("%w: cannot inspect security configuration for project %q; select --project and correct the configuration", ErrSourceBinding, dbcopy.SourceIDDisplay(id))
		}
		if declared {
			return fmt.Errorf("%w: project %q declares security; select it with --project", ErrSourceBinding, dbcopy.SourceIDDisplay(id))
		}
	}
	return nil
}
