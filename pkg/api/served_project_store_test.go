package api

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// Every project store this package hands out comes from one helper (projectStoreForID),
// which refuses a project this process does not serve whenever a session is configured.
// The check of a name does not say whether a project is served, and a project that is not
// served has no project folder: the helper says so, for every entry that reaches a project
// store.

// projectStoreEntries are the entries of this package that take a project and reach its
// project store, each called with the project in its argument and plain values everywhere
// else: the entries that read the project from a query (see idEntries) and the entries that
// read it from the body of the request (see servedProjectEntries).
func projectStoreEntries() map[string]func(project string) error {
	entries := map[string]func(project string) error{}
	for _, e := range idEntries() {
		if e.position == "project" && !strings.HasPrefix(e.name, "AddRowsToRecordset") {
			entries[e.name] = e.call
		}
	}
	for name, call := range servedProjectEntries() {
		entries[name] = call
	}
	entries["ProjectStoreFor"] = func(project string) error {
		_, err := ProjectStoreFor(project)
		return err
	}
	return entries
}

func TestProjectStoreHelper_RefusesAnUnservedProjectBeforeAnyStoreIsAsked(t *testing.T) {
	asks := serveProjectsWithCounter(t, "p1")
	entries := projectStoreEntries()
	if len(entries) < 20 {
		t.Fatalf("only %d entries are tried, want every entry that reaches a project store", len(entries))
	}
	for name, call := range entries {
		*asks = storeAsks{}
		err := call("elsewhere")
		switch {
		case err == nil:
			t.Errorf("%s: a plain project that is not served was not refused", name)
		case !errors.Is(err, ErrUnknownStoreID) && !validation.IsBadRequestError(err):
			// The routes that answer through apicore give a status of 400 to a bad request only.
			t.Errorf("%s: a project that is not served gave %v, want %v or a bad request", name, err, ErrUnknownStoreID)
		case !strings.Contains(err.Error(), `"elsewhere"`):
			t.Errorf("%s: the answer does not name the project: %v", name, err)
		}
		if asks.total() != 0 {
			t.Errorf("%s: a project that is not served reached the store factory (%d stores, %d project stores), want nothing", name, asks.factories, asks.projects)
		}

		// The count means something only if the same entry reaches the project store when the
		// project is served.
		*asks = storeAsks{}
		if err := call("p1"); err != nil && errors.Is(err, ErrUnknownStoreID) {
			t.Errorf("%s: a served project was refused: %v", name, err)
		}
		if asks.projects == 0 {
			t.Errorf("%s: a served project never reached a project store, so the count proves nothing", name)
		}
	}
}

// serveProjectDirs makes this process serve the projects, each in the folder given, until the
// test ends.
func serveProjectDirs(t *testing.T, paths map[string]string) {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	ConfigureSecureSession(session, paths, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
}

// countProjectStores replaces the store factory with one that counts the stores and the
// project stores it hands out, and restores it when the returned function is called.
func countProjectStores(t *testing.T, asks *storeAsks) (restore func()) {
	t.Helper()
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		asks.factories++
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			asks.projects++
			return mockProjectStore{}
		}}, nil
	}
	return func() { storage.NewDatatugStore = previous }
}

// The refusal is the answer of a route that resolves the store of its project (see
// ResolveStoreID) and a bad request, so that it is a 400 whichever way the route answers.
func TestProjectStoreHelper_RefusalIsABadRequestThatSaysNoStoreIsConfigured(t *testing.T) {
	serveProjectsWithCounter(t, "p1")
	_, want := ResolveStoreID("", "elsewhere")
	for name, call := range map[string]func() error{
		"projectStoreForID": func() error { _, err := projectStoreForID("files", "elsewhere"); return err },
		"ProjectStoreFor":   func() error { _, err := ProjectStoreFor("elsewhere"); return err },
	} {
		err := call()
		if !errors.Is(err, ErrUnknownStoreID) || !validation.IsBadRequestError(err) || err.Error() != want.Error() {
			t.Errorf("%s: %v, want a bad request that is also %v, saying %q", name, err, ErrUnknownStoreID, want)
		}
	}
}

// With no session configured (a handler under test, or a command that is not serve) a plain
// project is handed a store: the helper refuses a project only when it can
// say which projects are served.
func TestProjectStoreHelper_HandsOutAStoreWhenNoSessionIsConfigured(t *testing.T) {
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	asks := &storeAsks{}
	restore := countProjectStores(t, asks)
	defer restore()
	if _, err := projectStoreForID("files", "any-plain-name"); err != nil {
		t.Fatalf("no session is configured, a plain name was refused: %v", err)
	}
	if _, err := ProjectStoreFor("any-plain-name"); err != nil {
		t.Fatalf("no session is configured, a plain name was refused by ProjectStoreFor: %v", err)
	}
	if asks.projects != 2 {
		t.Errorf("project stores asked = %d, want 2", asks.projects)
	}
	// A session that serves no project at all is a session: nothing is served.
	ConfigureSecureSession(secureread.Session{}, map[string]string{}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	if _, err := projectStoreForID("files", "any-plain-name"); !errors.Is(err, ErrUnknownStoreID) {
		t.Errorf("a session that serves no project handed out a store: %v", err)
	}
}

// storeCallSites returns, for the non-test source files of this package, the declarations
// that call a method or a function of the given names, as "file: declaration calls name".
func storeCallSites(t *testing.T, names ...string) []string {
	t.Helper()
	return storeCallSitesIn(t, ".", names...)
}

// storeCallSitesIn is storeCallSites for the non-test source files of the folder dir. It walks
// every declaration of a file: a function (named by it), and a variable, whose value may be a
// function literal, which is how the seams of a package are written (named by the variable).
func storeCallSitesIn(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	var sites []string
	fileSet := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Base(path)
		for _, declaration := range parsed.Decls {
			ast.Inspect(declaration, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				var called string
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					called = fn.Sel.Name
				case *ast.Ident:
					called = fn.Name
				}
				if wanted[called] {
					sites = append(sites, file+": "+declarationName(declaration)+" calls "+called)
				}
				return true
			})
		}
	}
	sort.Strings(sites)
	return sites
}

// declarationName names a top-level declaration: a function by its name, a variable or a
// constant by the first name it declares.
func declarationName(declaration ast.Decl) string {
	switch d := declaration.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			if value, ok := spec.(*ast.ValueSpec); ok && len(value.Names) > 0 {
				return d.Tok.String() + " " + value.Names[0].Name
			}
		}
		return d.Tok.String()
	}
	return "a declaration"
}

// A new entry that reaches a project store by a call of its own, and not by the helper, is
// the entry that opens a project that nothing serves. The only function of this package that
// asks a store for a project store is the helper; and the only one that asks for a store by
// its ID besides is the one that gets the store of the whole process.
func TestProjectStoreHelper_IsTheOnlyCallerOfGetProjectStore(t *testing.T) {
	want := []string{"store_resolution.go: projectStoreForID calls GetProjectStore"}
	if got := storeCallSites(t, "GetProjectStore"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the calls of GetProjectStore are\n  %s\nwant only\n  %s\nan entry gets its project store from projectStoreForID", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	// A call of storeFor, which hands out the store of the process, outside the helper is a
	// way to a project store that the call above does not see.
	if got := storeCallSites(t, "storeFor"); strings.Join(got, "\n") != "store_resolution.go: projectStoreForID calls storeFor" {
		t.Errorf("the calls of storeFor are\n  %s\nwant only the helper's", strings.Join(got, "\n  "))
	}
}

// The handlers of the routes (pkg/server/endpoints) and the server (pkg/server) get a project
// store only through pkg/api, so that the rule about which projects there is a store for is
// kept in one place: none of them asks a store for a project store, or for a store.
func TestProjectStores_AreNotAskedForOutsidePkgAPI(t *testing.T) {
	for _, dir := range []string{"../server/endpoints", "../server"} {
		for _, name := range []string{"GetProjectStore", "NewDatatugStore"} {
			if got := storeCallSitesIn(t, dir, name); len(got) != 0 {
				t.Errorf("%s asks for a store by %s: %s", dir, name, strings.Join(got, ", "))
			}
		}
	}
}

// The file store opens a project folder by constructors of its own (filestore.NewProjectStore,
// NewSingleProjectStore and NewStore): a way to a project store that does not go through the
// helper. This package calls one in two places, the scan, which is given a folder and not the ID
// of a project that is served, and the server packages call none (pkg/server keeps its one
// factory of stores in a variable, and the walk does not count a variable as a call).
func TestFileStoreConstructors_AreCalledByTheScanOnly(t *testing.T) {
	constructors := []string{"NewProjectStore", "NewSingleProjectStore", "NewStore"}
	want := []string{
		"scan_driver.go: recordedCatalogDriver calls NewProjectStore",
		"scan_names.go: recordedDbModel calls NewProjectStore",
	}
	if got := storeCallSites(t, constructors...); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the calls of the file store's constructors are\n  %s\nwant only the scan's\n  %s\nan entry gets its project store from projectStoreForID", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, dir := range []string{"../server/endpoints", "../server"} {
		if got := storeCallSitesIn(t, dir, constructors...); len(got) != 0 {
			t.Errorf("%s opens a project folder by a constructor of the file store: %s", dir, strings.Join(got, ", "))
		}
	}
}

// The walk above sees a call by a name: it must see one, or it proves nothing.
func TestStoreCallSites_SeesACallByItsName(t *testing.T) {
	sites := storeCallSites(t, "projectStoreForID")
	if len(sites) < 10 {
		t.Fatalf("the walk found %d calls of projectStoreForID, want the entries of the package: %v", len(sites), sites)
	}
}

// A call in a function literal that a package-level variable holds, the way the seams of a
// package are written, and a call by a function of another package are seen, and a test file
// is not walked.
func TestStoreCallSites_SeesACallInAPackageLevelFunctionLiteral(t *testing.T) {
	dir := t.TempDir()
	write := func(name, source string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("seam.go", "package p\n\nvar seam = func() { store.GetProjectStore(\"x\") }\n\nfunc plain() { storage.NewDatatugStore(\"x\") }\n\nconst other = 1\n\ntype T struct{}\n")
	write("seam_test.go", "package p\n\nfunc inATest() { GetProjectStore() }\n")

	if got, want := storeCallSitesIn(t, dir, "GetProjectStore", "NewDatatugStore"), "seam.go: plain calls NewDatatugStore,seam.go: var seam calls GetProjectStore"; strings.Join(got, ",") != want {
		t.Errorf("the walk found %v, want %s", got, want)
	}
}

func TestDeclarationName(t *testing.T) {
	for _, tc := range []struct {
		source, want string
	}{
		{"package p\nfunc f() {}", "f"},
		{"package p\nvar a, b = 1, 2", "var a"},
		{"package p\nconst c = 1", "const c"},
		{"package p\nimport \"os\"", "import"},
		{"package p\ntype T struct{}", "type"},
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), "x.go", tc.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := declarationName(parsed.Decls[0]); got != tc.want {
			t.Errorf("declarationName(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
	if got := declarationName(&ast.BadDecl{}); got != "a declaration" {
		t.Errorf("declarationName of a bad declaration = %q", got)
	}
}
