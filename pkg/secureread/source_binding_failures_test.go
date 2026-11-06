package secureread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

func TestBoundSourceSnapshotIOFailuresRefuseAuthority(t *testing.T) {
	requireBindingPathPlatform(t)
	for _, name := range []string{"absolute-root", "target-stat-after-validation", "policy-stat-after-validation", "policy-snapshot-read", "policy-tree-read", "ordinary-policy-stat", "catalog-target-mismatch", "unrepresentable-native-path", "catalog-invalid-json", "catalog-null"} {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			abs, stat, lstat, read := bindingAbs, bindingStat, bindingLstat, bindingReadFile
			t.Cleanup(func() { bindingAbs, bindingStat, bindingLstat, bindingReadFile = abs, stat, lstat, read })
			switch name {
			case "absolute-root":
				bindingAbs = func(string) (string, error) { return "", os.ErrPermission }
			case "target-stat-after-validation":
				calls := 0
				bindingStat = func(path string) (os.FileInfo, error) {
					if path == f.target {
						calls++
						if calls == 2 {
							return nil, os.ErrPermission
						}
					}
					return stat(path)
				}
			case "policy-stat-after-validation":
				calls := 0
				bindingStat = func(path string) (os.FileInfo, error) {
					if path == f.policy {
						calls++
						if calls == 3 {
							return nil, os.ErrPermission
						}
					}
					return stat(path)
				}
			case "policy-snapshot-read":
				bindingReadFile = func(path string) ([]byte, error) {
					if path == f.policy {
						return nil, os.ErrPermission
					}
					return read(path)
				}
			case "policy-tree-read":
				f.configValue.RequiredPolicies = []string{"policies"}
				f.save(t)
				bindingReadFile = func(path string) ([]byte, error) {
					if path == f.policy {
						return nil, os.ErrPermission
					}
					return read(path)
				}
			case "ordinary-policy-stat":
				required := filepath.Join(f.root, "required.yaml")
				writeBindingFile(t, required, []byte(phase1TablePolicy))
				f.configValue.RequiredPolicies = []string{"required.yaml"}
				f.save(t)
				bindingLstat = func(path string) (os.FileInfo, error) {
					if path == filepath.Join(f.root, "policies") {
						return nil, os.ErrPermission
					}
					return lstat(path)
				}
			case "catalog-target-mismatch":
				f.configValue.Catalog = "two"
				f.save(t)
			case "catalog-invalid-json":
				writeBindingFile(t, filepath.Join(f.root, "environments", "local", "catalogs", "one", "one.db.json"), []byte("{broken"))
			case "catalog-null":
				writeBindingFile(t, filepath.Join(f.root, "environments", "local", "catalogs", "one", "one.db.json"), []byte("null"))
			case "unrepresentable-native-path":
				target := filepath.Join(f.root, "invalid\nurl.sqlite")
				if err := os.Rename(f.target, target); err != nil {
					t.Fatal(err)
				}
				f.configValue.SQLiteFile = target
				f.save(t)
			}
			if _, declared, err := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{Required: true}); !declared || !errors.Is(err, ErrSourceBinding) {
				t.Fatalf("failed %s snapshot retained authority: %v %v", name, declared, err)
			}
		})
	}
	if _, err := bindingTreeDigest(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk failure was ignored: %v", err)
	}
	// A malformed retained backend cannot produce a database or cleanup handle.
	if db, close, err := openBoundSourceFn(context.Background(), dbcopy.BackendRef{Scheme: "unknown"}); err == nil || db != nil || close != nil {
		t.Fatal("invalid native backend did not fail closed")
	}
}

func TestBoundSourceAbsentOrdinaryPoliciesCannotExpandOriginalSession(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	required := filepath.Join(f.root, "mandatory", "read.yaml")
	writeBindingFile(t, required, []byte(phase1TablePolicy))
	if err := os.RemoveAll(filepath.Join(f.root, "policies")); err != nil {
		t.Fatal(err)
	}
	f.configValue.RequiredPolicies = []string{"mandatory/read.yaml"}
	f.save(t)
	s := f.load(t)
	e := NewExecutor(s)
	if _, err := e.StreamFederatedTugQL(context.Background(), nil, nil, nil); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("direct federation stream escaped bound profile", err)
	}
	result, err := e.RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil)
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("initial required policy read: %v %+v", err, result)
	}
	revoke := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: new-mandatory-denial}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: deny, effect: deny, operations: [query]}]}]\n"
	path := filepath.Join(f.root, "policies", "revoke.yaml")
	writeBindingFile(t, path, []byte(revoke))
	if !errors.Is(s.CheckSourceBinding(context.Background()), ErrSourceBinding) {
		t.Fatal("new mandatory policy ignored")
	}
	if result, err = e.RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil); !errors.Is(err, ErrSourceBinding) || len(result.Rows) != 0 {
		t.Fatalf("original session survived mandatory change: %v %+v", err, result)
	}
	// Explicit reconstruction loads the newly mandatory denial; it cannot grant A.
	fresh := f.load(t)
	if result, err = NewExecutor(fresh).RunTugQL(context.Background(), fresh.binding.url, []byte(phase1AQuery), nil); !errors.Is(err, ErrAccessDenied) || len(result.Rows) != 0 {
		t.Fatalf("new session dropped mandatory denial: %v %+v", err, result)
	}
	if err = os.RemoveAll(filepath.Join(f.root, "policies")); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(s.CheckSourceBinding(context.Background()), ErrSourceBinding) {
		t.Fatal("restoring absence revived old session")
	}
}

func TestBoundSourceOrdinaryPolicySetFailureAfterRequiredAdmission(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	required := filepath.Join(f.root, "mandatory.yaml")
	writeBindingFile(t, required, []byte(phase1TablePolicy))
	f.configValue.RequiredPolicies = []string{"mandatory.yaml"}
	f.save(t)
	if err := os.RemoveAll(filepath.Join(f.root, "policies")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.root, "policies"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{Required: true}); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("empty ordinary policy set was ignored", err)
	}
}

func TestBoundSourcePolicyAndRegistrySnapshotsMatchEvaluatedBytes(t *testing.T) {
	requireBindingPathPlatform(t)
	for _, kind := range []string{"file-policy", "directory-policy", "manifest", "registry"} {
		t.Run(kind, func(t *testing.T) {
			f := newBindingFixture(t)
			if kind == "directory-policy" {
				f.configValue.RequiredPolicies = []string{"policies"}
				f.save(t)
			}
			read := bindingReadFile
			t.Cleanup(func() { bindingReadFile = read })
			changed := false
			catalogReads := 0
			bindingReadFile = func(path string) ([]byte, error) {
				if (kind == "file-policy" || kind == "directory-policy") && path == f.policy && !changed {
					changed = true
					writeBindingFile(t, f.policy, []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: replacement-deny}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: deny, effect: deny, operations: [query]}]}]\n"))
				}
				data, err := read(path)
				if kind == "manifest" && path == f.config && !changed {
					changed = true
					f.configValue.Principal.ID = "changed-owner"
					f.save(t)
				}
				if kind == "registry" && filepath.Base(path) == "one.db.json" {
					catalogReads++
					if catalogReads == 2 && !changed {
						changed = true
						writeBindingFile(t, path, append(data, []byte("\n ")...))
						return read(path)
					}
				}
				return data, err
			}
			s, declared, err := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{Required: true})
			if !changed || !declared {
				t.Fatalf("fixture did not change %s during snapshot", kind)
			}
			if kind == "manifest" || kind == "registry" {
				if !errors.Is(err, ErrSourceBinding) {
					t.Fatalf("mismatched authority snapshot accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// The constructor may bind the replacement bytes, but cannot keep the old
			// allow policy while claiming it watches the replacement denial.
			result, err := NewExecutor(s).RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil)
			if !errors.Is(err, ErrAccessDenied) || len(result.Rows) != 0 {
				t.Fatalf("decoded policy differs from pinned bytes: %v %+v", err, result)
			}
		})
	}
}

func TestBoundSourcePolicyDirectoryPreservesPublicCodecAndEntryRules(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	if err := os.Rename(f.policy, filepath.Join(f.root, "policies", "tables.yml")); err != nil {
		t.Fatal(err)
	}
	f.configValue.RequiredPolicies = []string{"policies"}
	f.save(t)
	writeBindingFile(t, filepath.Join(f.root, "policies", "zz-all.JSON"), []byte(`{"apiVersion":"dalgo.io/access/v1","kind":"AccessPolicy","metadata":{"name":"extra-all"},"default":"deny","scopes":[{"path":"/**","rules":[{"id":"all","effect":"allow","operations":["query"]}]}]}`))
	writeBindingFile(t, filepath.Join(f.root, "policies", "note.txt"), []byte("not a policy"))
	writeBindingFile(t, filepath.Join(f.root, "policies", "nested", "ignored.yaml"), []byte("not parsed as a top-level policy"))
	s := f.load(t)
	if len(s.Policies) != 2 {
		t.Fatalf("public top-level entry rules changed: %d", len(s.Policies))
	}
	result, err := NewExecutor(s).RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil)
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("equivalent public codecs failed: %v %+v", err, result)
	}
	unsupported := filepath.Join(f.root, "mandatory.txt")
	writeBindingFile(t, unsupported, []byte(phase1TablePolicy))
	f.configValue.RequiredPolicies = []string{"mandatory.txt"}
	f.save(t)
	if _, _, err := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{Required: true}); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("unknown policy codec accepted", err)
	}
}
