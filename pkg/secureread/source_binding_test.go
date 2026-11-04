package secureread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

type bindingFixture struct {
	root, target, other, config, policy string
	store                               datatug.ProjectStore
	configValue                         boundSourceConfig
}

func writeBindingFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func newBindingFixture(t *testing.T) bindingFixture {
	t.Helper()
	f := bindingFixture{root: t.TempDir()}
	var err error
	f.root, err = filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.target = filepath.Join(f.root, "one.sqlite")
	f.other = filepath.Join(f.root, "two.sqlite")
	f.config = filepath.Join(f.root, ".datatug", "security.json")
	f.policy = filepath.Join(f.root, "policies", "tables.yaml")
	for i, path := range []string{f.target, f.other} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE A(id INTEGER,label TEXT); CREATE TABLE B(id INTEGER,label TEXT); CREATE TABLE C(id INTEGER,label TEXT); INSERT INTO A VALUES(1,?); INSERT INTO B VALUES(1,'secret-b'); INSERT INTO C VALUES(1,'allowed-c')`, []string{"allowed-a", "other-source-a"}[i])
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	f.store = filestore.NewProjectStore("bound", f.root)
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
	env.ID = "local"
	if err = f.store.SaveEnvironment(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	for _, entry := range []struct{ id, path string }{{"one", f.target}, {"two", f.other}} {
		c := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: entry.path, DbModel: entry.id}}
		c.ID = entry.id
		if err = f.store.SaveEnvDbCatalog(context.Background(), "local", server, c.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	writeBindingFile(t, f.policy, []byte(phase1TablePolicy))
	f.configValue = boundSourceConfig{Profile: ExactTableReadProfile, Project: "bound", Environment: "local", Catalog: "one", SQLiteFile: f.target, RequiredPolicies: []string{"policies/tables.yaml"}}
	f.configValue.Principal.ID = "operator"
	f.save(t)
	return f
}
func (f bindingFixture) save(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.configValue)
	if err != nil {
		t.Fatal(err)
	}
	writeBindingFile(t, f.config, data)
}
func (f bindingFixture) load(t *testing.T) Session {
	t.Helper()
	s, declared, err := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatalf("load declared=%v: %v", declared, err)
	}
	return s
}

func TestBoundSourceExactRegisteredConnectionAndPolicies(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	s := f.load(t)
	e := NewExecutor(s)
	source, declared, err := s.BoundSource(context.Background(), "bound", f.root, "local", "one")
	if err != nil || !declared {
		t.Fatal(err)
	}
	old := openBoundSourceFn
	opens, dispatch := 0, 0
	t.Cleanup(func() { openBoundSourceFn = old })
	openBoundSourceFn = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, func(), error) {
		opens++
		db, close, err := old(ctx, ref)
		if err != nil {
			return nil, close, err
		}
		return phase1DispatchDB{DB: db, calls: &dispatch}, close, nil
	}
	for _, tc := range []struct {
		doc     []byte
		allowed bool
	}{{[]byte(phase1AQuery), true}, {phase1JoinQuery("C"), true}, {[]byte("from: {name: B}\n"), false}, {phase1JoinQuery("B"), false}} {
		before := dispatch
		result, err := e.RunTugQL(context.Background(), source, tc.doc, nil)
		if tc.allowed {
			if err != nil || len(result.Rows) != 1 {
				t.Fatalf("permitted: %v %+v", err, result)
			}
		} else if !errors.Is(err, ErrAccessDenied) || len(result.Rows) != 0 || dispatch != before {
			t.Fatalf("denied dispatch=%d: %v", dispatch-before, err)
		}
	}
	before := opens
	for _, catalog := range []string{"two", "alias", "../one"} {
		if _, _, err := s.BoundSource(context.Background(), "bound", f.root, "local", catalog); !errors.Is(err, ErrSourceBinding) {
			t.Fatalf("catalog %s: %v", catalog, err)
		}
	}
	if result, err := e.RunTugQL(context.Background(), dbcopy.LocalSourceURL("sqlite", f.other), []byte(phase1AQuery), nil); !errors.Is(err, ErrSourceBinding) || len(result.Rows) != 0 || opens != before {
		t.Fatalf("second registered source opened: %v %+v opens=%d", err, result, opens-before)
	}
	denied := access.WithPolicy(context.Background(), access.MustPolicy("mandatory", access.Root(access.Deny(access.Query))))
	before = dispatch
	if result, err := e.RunTugQL(denied, source, []byte(phase1AQuery), nil); !errors.Is(err, ErrAccessDenied) || len(result.Rows) != 0 || dispatch != before {
		t.Fatalf("mandatory context bypass: %v", err)
	}
	if _, _, err = s.BoundSource(context.Background(), "other-project", f.root, "local", "one"); err == nil {
		t.Fatal("project substitution")
	}
	if _, _, err = s.BoundSource(context.Background(), "bound", f.root, "other", "one"); err == nil {
		t.Fatal("environment substitution")
	}
	if err = s.CheckSourceBinding(context.Background()); err != nil {
		t.Fatal("refused client input poisoned valid session", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(s.CheckSourceBinding(cancelled), context.Canceled) || s.CheckSourceBinding(context.Background()) != nil {
		t.Fatal("request cancellation changed binding")
	}
}

func TestBoundSourceRequiredDocumentsNeverFallback(t *testing.T) {
	requireBindingPathPlatform(t)
	for _, name := range []string{"missing", "empty", "audit-only", "malformed", "null", "missing-config", "config-null", "unknown-field", "missing-source", "alias-collision", "relative-target", "target-options", "symlink-target", "principal-override", "roles-override", "extra-missing", "extra-empty"} {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			o := BoundSessionOptions{Required: true}
			switch name {
			case "missing":
				_ = os.Remove(f.policy)
			case "empty":
				writeBindingFile(t, f.policy, nil)
			case "audit-only":
				writeBindingFile(t, f.policy, []byte("apiVersion: dalgo.io/access/v1\nkind: AuditPolicy\nmetadata: {name: audit}\ndefault: ignoreAudit\n"))
			case "malformed":
				writeBindingFile(t, f.policy, []byte("not: [valid"))
			case "null":
				writeBindingFile(t, f.policy, []byte("null"))
			case "missing-config":
				_ = os.Remove(f.config)
			case "config-null":
				writeBindingFile(t, f.config, []byte("null"))
			case "unknown-field":
				data, _ := os.ReadFile(f.config)
				data = append(data[:len(data)-1], []byte(",\"unknown\":true}")...)
				writeBindingFile(t, f.config, data)
			case "missing-source":
				f.configValue.Catalog = "missing"
				f.save(t)
			case "alias-collision":
				c := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: f.other, DbModel: "one"}}
				c.ID = "two"
				server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
				if err := f.store.SaveEnvDbCatalog(context.Background(), "local", server, "two", c); err != nil {
					t.Fatal(err)
				}
			case "relative-target":
				f.configValue.SQLiteFile = "one.sqlite"
				f.save(t)
			case "target-options":
				f.configValue.SQLiteFile = f.target + "?mode=ro"
				f.save(t)
			case "symlink-target":
				link := filepath.Join(f.root, "link.sqlite")
				if err := os.Symlink(f.target, link); err != nil {
					t.Fatal(err)
				}
				f.configValue.SQLiteFile = link
				f.save(t)
			case "principal-override":
				o.As = "someone-else"
			case "roles-override":
				o.Roles = []string{"admin"}
			case "extra-missing":
				o.ExtraPolicyFiles = []string{filepath.Join(f.root, "missing.yaml")}
			case "extra-empty":
				empty := filepath.Join(f.root, "extra")
				if err := os.Mkdir(empty, 0700); err != nil {
					t.Fatal(err)
				}
				o.ExtraPoliciesDir = empty
			}
			global := t.TempDir()
			writeBindingFile(t, filepath.Join(global, "allow.yaml"), []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: global}\ndefault: deny\nscopes: [{path: '/**', rules: [{effect: allow, operations: [query]}]}]\n"))
			t.Setenv("DATATUG_POLICIES_DIR", global)
			if _, declared, err := LoadBoundSession(context.Background(), f.root, o); !declared || !errors.Is(err, ErrSourceBinding) {
				t.Fatalf("required set fell back: declared=%v err=%v", declared, err)
			}
		})
	}
}

func TestBoundSourceChangedConfigurationInvalidatesOriginalSession(t *testing.T) {
	requireBindingPathPlatform(t)
	for _, name := range []string{"config", "policy", "policy-added", "catalog", "target-replaced", "target-symlink", "config-removed"} {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			s := f.load(t)
			original, _ := os.ReadFile(f.config)
			switch name {
			case "config":
				f.configValue.Principal.ID = "new-owner"
				f.save(t)
			case "policy":
				writeBindingFile(t, f.policy, []byte(phase1TablePolicy+"\n# changed\n"))
			case "policy-added":
				writeBindingFile(t, filepath.Join(f.root, "policies", "new.yaml"), []byte(phase1TablePolicy))
			case "catalog":
				writeBindingFile(t, filepath.Join(f.root, "environments", "local", "new-file"), []byte("changed"))
			case "target-replaced":
				if err := os.Rename(f.other, f.target); err != nil {
					t.Fatal(err)
				}
			case "target-symlink":
				if err := os.Remove(f.target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.other, f.target); err != nil {
					t.Fatal(err)
				}
			case "config-removed":
				if err := os.Remove(f.config); err != nil {
					t.Fatal(err)
				}
			}
			if !errors.Is(s.CheckSourceBinding(context.Background()), ErrSourceBinding) {
				t.Fatal("changed owner accepted")
			}
			writeBindingFile(t, f.config, original)
			if !errors.Is(s.CheckSourceBinding(context.Background()), ErrSourceBinding) {
				t.Fatal("purged owner resurrected")
			}
		})
	}
}

func TestBoundSourceSessionModesAndFinalOutputGate(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	s := f.load(t)
	fresh := f.load(t)
	if s.SourceBindingFingerprint() == fresh.SourceBindingFingerprint() {
		t.Fatal("new sessions share cache identity")
	}
	if err := s.ValidateBoundProjects(map[string]string{"bound": f.root}); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateBoundProjects(map[string]string{"other": f.root}); err == nil {
		t.Fatal("different served project")
	}
	mutated := s
	mutated.AllowOpaqueSQL = true
	if _, err := NormalizeSession(mutated); err == nil {
		t.Fatal("raw capability accepted")
	}
	mutated = s
	mutated.Principal = &access.Principal{ID: "admin"}
	if mutated.CheckSourceBinding(context.Background()) == nil {
		t.Fatal("principal replaced")
	}
	old := openBoundSourceFn
	t.Cleanup(func() { openBoundSourceFn = old })
	opens := 0
	openBoundSourceFn = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, func(), error) {
		opens++
		db, close, err := old(ctx, ref)
		if err == nil {
			writeBindingFile(t, f.policy, []byte(phase1TablePolicy+"\n# revoked\n"))
		}
		return db, close, err
	}
	result, err := NewExecutor(s).RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil)
	if !errors.Is(err, ErrSourceBinding) || len(result.Rows) != 0 || opens != 1 {
		t.Fatalf("changed at opener released data: %v %+v opens=%d", err, result, opens)
	}
	legacy, declared, err := LoadBoundSession(context.Background(), t.TempDir(), BoundSessionOptions{})
	if err != nil || declared || legacy.SourceBound() {
		t.Fatalf("legacy absent profile changed: %v %v", declared, err)
	}
	if _, declared, err = LoadBoundSession(context.Background(), "", BoundSessionOptions{}); err != nil || declared {
		t.Fatal("legacy empty project changed")
	}
}

func TestPolicyFreeNativeReadKeepsMandatoryContextAndProviderPolicies(t *testing.T) {
	f := newBindingFixture(t)
	old := openSourceFn
	t.Cleanup(func() { openSourceFn = old })
	dispatch := 0
	var provider func(context.Context) ([]access.Policy, error)
	required := false
	openSourceFn = func(ctx context.Context, url string, insecure bool, policies int) (dal.DB, func(), error) {
		db, close, err := old(ctx, url, insecure, policies)
		if err != nil {
			return nil, close, err
		}
		options := []access.DBOption{}
		if provider != nil {
			options = append(options, access.WithDatabasePolicyProvider(provider))
		}
		if required {
			options = append(options, access.RequireContextPolicy())
		}
		secured, err := access.SecureDB(phase1DispatchDB{DB: db, calls: &dispatch}, options...)
		return secured, close, err
	}
	e := NewExecutor(Session{Unrestricted: true})
	source := dbcopy.LocalSourceURL("sqlite", f.target)
	allow := access.MustPolicy("mandatory-owner", access.Root(access.Allow(access.Query)))
	denied := access.WithPolicy(context.Background(), access.MustPolicy("mandatory-context", access.Root(access.Deny(access.Query))))
	result, err := e.RunTugQL(denied, source, []byte(phase1AQuery), nil)
	if !errors.Is(err, ErrAccessDenied) || dispatch != 0 || len(result.Rows) != 0 {
		t.Fatalf("policy-free context bypass: %v dispatch=%d", err, dispatch)
	}
	provider = func(context.Context) ([]access.Policy, error) { return nil, errors.New("private-provider-outage") }
	result, err = e.RunTugQL(context.Background(), source, []byte(phase1AQuery), nil)
	if !errors.Is(err, ErrAccessDenied) || dispatch != 0 || len(result.Rows) != 0 {
		t.Fatalf("provider failure bypass: %v", err)
	}
	provider = func(context.Context) ([]access.Policy, error) { return []access.Policy{allow}, nil }
	required = true
	result, err = e.RunTugQL(context.Background(), source, []byte(phase1AQuery), nil)
	if !errors.Is(err, ErrAccessDenied) || dispatch != 0 || len(result.Rows) != 0 {
		t.Fatalf("baseline satisfied required context: %v", err)
	}
	result, err = e.RunTugQL(access.WithPolicy(context.Background(), allow), source, []byte(phase1AQuery), nil)
	if err != nil || dispatch != 1 || len(result.Rows) != 1 {
		t.Fatalf("valid mandatory layers: %v dispatch=%d", err, dispatch)
	}
	provider = nil
	required = false // A raw DB is policy-free, as ordinary legacy startup was.
	openSourceFn = old
	result, err = e.RunTugQL(context.Background(), source, []byte(phase1AQuery), nil)
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("ordinary policy-free read changed: %v", err)
	}
}

type bindingCloseDB struct {
	dal.DB
	onClose func()
}

func (db bindingCloseDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	r, err := db.DB.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	return bindingCloseReader{RecordsReader: r, onClose: db.onClose}, nil
}

type bindingCloseReader struct {
	dal.RecordsReader
	onClose func()
}

func (r bindingCloseReader) Close() error { err := r.RecordsReader.Close(); r.onClose(); return err }
func TestBoundSourceFinalGateAndAdditionalPolicyIntersection(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	// The source binding deliberately refuses symlink spellings. macOS's
	// default temporary root may be /var (a symlink to /private/var).
	extraDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(extraDir, "extra.yaml")
	allow := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: extra}\ndefault: deny\nscopes: [{path: '/**', rules: [{id: extra, effect: allow, operations: [query]}]}]\n"
	writeBindingFile(t, extra, []byte(allow))
	s, declared, err := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{ExtraPolicyFiles: []string{extra}, As: "operator"})
	if err != nil || !declared {
		t.Fatal(err)
	}
	e := NewExecutor(s)
	if err = e.Authorize(context.Background(), dal.From(dal.NewRootCollectionRef("B", "")).NewQuery().SelectIntoRecord(nil), nil); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("extra allow widened required policy", err)
	}
	old := openBoundSourceFn
	t.Cleanup(func() { openBoundSourceFn = old })
	rows := 0
	openBoundSourceFn = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, func(), error) {
		db, close, err := old(ctx, ref)
		return bindingCloseDB{DB: db, onClose: func() { rows++; writeBindingFile(t, f.policy, []byte(phase1TablePolicy+"\n# changed after read\n")) }}, close, err
	}
	result, err := e.RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil)
	if !errors.Is(err, ErrSourceBinding) || len(result.Rows) != 0 || rows != 1 {
		t.Fatalf("retained result released after policy changed: %v rows=%d %+v", err, rows, result)
	}
	// Refusal occurs before opening a raw/opaque query in this profile.
	if _, err = NewExecutor(f.load(t)).RunStructured(context.Background(), s.binding.url, dal.NewTextQuery("DELETE FROM A", nil), nil); !errors.Is(err, ErrOpaqueSQLNotGranted) {
		t.Fatalf("opaque structured entry: %v", err)
	}
}

func TestBoundSourceAdditionalStartupAndChangeFailures(t *testing.T) {
	requireBindingPathPlatform(t)
	for _, name := range []string{"missing-root", "file-as-root", "non-sqlite", "missing-file", "missing-registry", "outside-policy", "traversal-policy", "missing-project", "duplicate-field", "required-empty-list", "required-empty-dir", "policy-symlink", "config-symlink", "registry-symlink", "policy-permission", "registry-permission"} {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			root := f.root
			switch name {
			case "missing-root":
				root = filepath.Join(f.root, "missing")
			case "file-as-root":
				root = f.target
			case "non-sqlite":
				f.configValue.Catalog = "two"
				f.configValue.SQLiteFile = f.other
				server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
				c := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "postgres", Path: f.other}}
				c.ID = "two"
				if err := f.store.SaveEnvDbCatalog(context.Background(), "local", server, c.ID, c); err != nil {
					t.Fatal(err)
				}
				f.save(t)
			case "missing-file":
				if err := os.Remove(f.target); err != nil {
					t.Fatal(err)
				}
			case "missing-registry":
				if err := os.RemoveAll(filepath.Join(f.root, "environments")); err != nil {
					t.Fatal(err)
				}
			case "outside-policy":
				f.configValue.RequiredPolicies = []string{filepath.Join(t.TempDir(), "external.yaml")}
				f.save(t)
			case "traversal-policy":
				f.configValue.RequiredPolicies = []string{"../external.yaml"}
				f.save(t)
			case "missing-project":
				f.configValue.Project = ""
				f.save(t)
			case "duplicate-field":
				data, _ := os.ReadFile(f.config)
				data = append(data[:len(data)-1], []byte(",\"catalog\":\"two\"}")...)
				writeBindingFile(t, f.config, data)
			case "required-empty-list":
				f.configValue.RequiredPolicies = nil
				f.save(t)
			case "required-empty-dir":
				if err := os.Mkdir(filepath.Join(f.root, "required"), 0700); err != nil {
					t.Fatal(err)
				}
				f.configValue.RequiredPolicies = []string{"required"}
				f.save(t)
			case "policy-symlink":
				copy := filepath.Join(t.TempDir(), "copy.yaml")
				data, _ := os.ReadFile(f.policy)
				writeBindingFile(t, copy, data)
				if err := os.Remove(f.policy); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(copy, f.policy); err != nil {
					t.Fatal(err)
				}
			case "config-symlink":
				copy := filepath.Join(t.TempDir(), "copy.json")
				data, _ := os.ReadFile(f.config)
				writeBindingFile(t, copy, data)
				if err := os.Remove(f.config); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(copy, f.config); err != nil {
					t.Fatal(err)
				}
			case "registry-symlink":
				if err := os.Symlink(f.policy, filepath.Join(f.root, "environments", "local", "linked-policy")); err != nil {
					t.Fatal(err)
				}
			case "policy-permission":
				if err := os.Chmod(f.policy, 0); err != nil {
					t.Fatal(err)
				}
			case "registry-permission":
				path := filepath.Join(f.root, "environments", "local")
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Chmod(path, 0700) }()
			}
			if _, declared, err := LoadBoundSession(context.Background(), root, BoundSessionOptions{Required: true}); !declared || !errors.Is(err, ErrSourceBinding) {
				t.Fatalf("invalid startup accepted: %v %v", declared, err)
			}
		})
	}
	f := newBindingFixture(t)
	s := f.load(t)
	if err := s.ValidateBoundServer(map[string]string{"bound": f.root}, false, false, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BoundBackend(context.Background(), s.binding.url+"?mode=rw"); err == nil {
		t.Fatal("URI mutation")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.BoundBackend(cancelled, s.binding.url); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := s.BoundSource(context.Background(), "bound", t.TempDir(), "local", "one"); err == nil {
		t.Fatal("project root substitution")
	}
	unbound := Session{}
	if unbound.SourceBindingFingerprint() != "" {
		t.Fatal("legacy fingerprint")
	}
	if _, _, _, bound := unbound.BoundScope(); bound {
		t.Fatal("legacy scope")
	}
	if err := unbound.ValidateBoundProjects(nil); err != nil {
		t.Fatal(err)
	}
	if err := unbound.ValidateBoundServer(nil, true, true, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := unbound.BoundBackend(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := unbound.BoundSource(context.Background(), "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	// Unsupported alternative executors cannot retain a bound target accidentally.
	e := NewExecutor(s)
	if _, err := e.RunRecursiveTugQL(context.Background(), s.binding.url, nil, nil); !errors.Is(err, ErrSourceBinding) {
		t.Fatal(err)
	}
	if _, err := e.RunFederatedTugQL(context.Background(), nil, nil, nil); !errors.Is(err, ErrSourceBinding) {
		t.Fatal(err)
	}
	if _, err := e.RunExactAggregateTugQL(context.Background(), s.binding.url, nil, nil, nil, nil); !errors.Is(err, ErrSourceBinding) {
		t.Fatal(err)
	}
	if err := os.Remove(f.config); err != nil {
		t.Fatal(err)
	}
	if err := e.Authorize(context.Background(), dal.From(dal.NewRootCollectionRef("A", "")).NewQuery().SelectIntoRecord(nil), nil); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("metadata ignored removed authority", err)
	}
}

func TestBoundSourceOpenerFailureAndUnsupportedSnapshots(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	s := f.load(t)
	e := NewExecutor(s)
	old := openBoundSourceFn
	t.Cleanup(func() { openBoundSourceFn = old })
	openBoundSourceFn = func(context.Context, dbcopy.BackendRef) (dal.DB, func(), error) {
		return nil, nil, errors.New("owned opener unavailable")
	}
	if result, err := e.RunTugQL(context.Background(), s.binding.url, []byte(phase1AQuery), nil); err == nil || len(result.Rows) != 0 {
		t.Fatal("opener failure released rows")
	}
	if _, err := e.RunSnapshot(context.Background(), "A", apicontract.Recordset{}); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("snapshot changed authority", err)
	}
	invalid, _ := NormalizeSession(Session{binding: s.binding, AllowOpaqueSQL: true})
	if invalid.CheckSourceBinding(context.Background()) == nil {
		t.Fatal("invalid configuration retained authority")
	}
	if _, declared, err := LoadBoundSession(context.Background(), "", BoundSessionOptions{Required: true}); !declared || err == nil {
		t.Fatal("missing project fallback")
	}
	// A registered relative path is safe only after resolving to the exact fixed target.
	c := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: "one.sqlite", DbModel: "one"}}
	c.ID = "one"
	server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	if err := f.store.SaveEnvDbCatalog(context.Background(), "local", server, c.ID, c); err != nil {
		t.Fatal(err)
	}
	f.load(t)
	if _, err := boundedBindingFile(f.root); err == nil {
		t.Fatal("directory read accepted")
	}
	writeBindingFile(t, filepath.Join(f.root, "oversize"), make([]byte, (2<<20)+1))
	if _, err := boundedBindingFile(filepath.Join(f.root, "oversize")); err == nil {
		t.Fatal("oversize config accepted")
	}
	if err := (&sourceBinding{root: f.root}).addPolicySet(filepath.Join(t.TempDir(), "absent"), true); err == nil {
		t.Fatal("missing policy accepted")
	}
	external := filepath.Join(t.TempDir(), "external.yaml")
	writeBindingFile(t, external, []byte(phase1TablePolicy))
	if err := (&sourceBinding{root: f.root}).addPolicySet(external, true); err == nil {
		t.Fatal("outside required policy accepted")
	}
}

func requireBindingPathPlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
}

func TestBoundSourceScopeAndServerAdmissionRemainFixed(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	s := f.load(t)
	project, environment, catalog, bound := s.BoundScope()
	if !bound || project != "bound" || environment != "local" || catalog != "one" {
		t.Fatalf("scope: %q %q %q %v", project, environment, catalog, bound)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if source, bound, err := s.BoundSource(cancelled, project, f.root, environment, catalog); err == nil || !bound || source != "" {
		t.Fatalf("cancelled resolver released target: %q %v %v", source, bound, err)
	}
	for _, paths := range []map[string]string{nil, {"bound": f.root, "extra": f.root}} {
		if err := f.load(t).ValidateBoundProjects(paths); !errors.Is(err, ErrSourceBinding) {
			t.Fatalf("ambiguous project registry accepted: %v", err)
		}
	}
	for _, mode := range []string{"writes", "opaque", "live"} {
		s = f.load(t)
		if err := s.ValidateBoundServer(map[string]string{"bound": f.root}, mode == "writes", mode == "opaque", mode == "live"); !errors.Is(err, ErrSourceBinding) {
			t.Fatalf("bound %s capability accepted: %v", mode, err)
		}
		if err := s.CheckSourceBinding(context.Background()); err == nil {
			t.Fatalf("invalid %s session revived", mode)
		}
	}
	legacy, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.ValidateBoundServer(map[string]string{"remote": "https://example.invalid/project", "legacy": t.TempDir()}, false, false, false); err != nil {
		t.Fatalf("undeclared legacy registry changed: %v", err)
	}
	if err := legacy.ValidateBoundServer(map[string]string{"declared": f.root}, false, false, false); !errors.Is(err, ErrSourceBinding) {
		t.Fatalf("unbound server admitted declared profile: %v", err)
	}
	malformed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeBindingFile(t, filepath.Join(malformed, ".datatug", "security.json"), []byte(`{`))
	if err := legacy.ValidateBoundServer(map[string]string{"unreadable": malformed}, false, false, false); !errors.Is(err, ErrSourceBinding) {
		t.Fatalf("declaration inspection failure became legacy: %v", err)
	}
}
