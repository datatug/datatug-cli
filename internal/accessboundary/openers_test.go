package accessboundary

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type openerInventory struct {
	References map[string]int    `json:"references"`
	Files      map[string]string `json:"files"`
}

// This ratchet detects native-provider references and source-opener methods.
// It is deliberately static: an inventoried call is not proof of governance.
// A new reference requires an explicit disposition in the reviewed inventory.
func TestNativeOpenerInventory(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	actual := map[string]int{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == "node_modules" || strings.HasPrefix(entry.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for id, count := range openerReferences(file) {
			actual[filepath.ToSlash(rel)+":"+id] = count
		}
		return nil
	}))
	require.NotEmpty(t, actual, "source inventory must not pass vacuously")
	data, err := os.ReadFile("native-openers.json")
	if err != nil {
		encoded, marshalErr := json.MarshalIndent(actual, "", "  ")
		require.NoError(t, marshalErr)
		t.Fatalf("missing inventory: %v\nACTUAL_INVENTORY=%s", err, encoded)
	}
	var expected openerInventory
	require.NoError(t, json.Unmarshal(data, &expected))
	require.Equal(t, expected.References, actual, "review the new/removed native reference and update its explicit disposition; never blindly regenerate")
	files := map[string]bool{}
	for id := range actual {
		path, _, found := strings.Cut(id, ":")
		require.True(t, found)
		require.NotEmpty(t, expected.Files[path], "missing disposition for %s", path)
		files[path] = true
	}
	for path := range expected.Files {
		require.True(t, files[path], "stale file disposition %s", path)
	}
}

func nativeProvider(path string) bool {
	return path == "database/sql" || strings.HasPrefix(path, "github.com/dal-go/dalgo2") ||
		path == "github.com/aws/aws-sdk-go-v2/service/dynamodb" ||
		strings.HasPrefix(path, "github.com/jackc/pgx") || strings.HasPrefix(path, "go.mongodb.org/") ||
		strings.HasPrefix(path, "github.com/redis/")
}

func openerReferences(file *ast.File) map[string]int {
	imports := map[string]string{}
	nativeImports := map[string]bool{}
	result := map[string]int{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := filepath.Base(path)
		if strings.HasPrefix(name, "v") {
			if _, err := strconv.Atoi(strings.TrimPrefix(name, "v")); err == nil {
				name = filepath.Base(filepath.Dir(path))
			}
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	for _, declaration := range file.Decls {
		owner := "package"
		if function, ok := declaration.(*ast.FuncDecl); ok {
			owner = function.Name.Name
		}
		ast.Inspect(declaration, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if identifier, ok := selector.X.(*ast.Ident); ok && nativeProvider(imports[identifier.Name]) &&
				(strings.HasPrefix(selector.Sel.Name, "Open") || strings.HasPrefix(selector.Sel.Name, "New") ||
					strings.HasPrefix(selector.Sel.Name, "Connect") || strings.HasPrefix(selector.Sel.Name, "Dial")) {
				result[owner+":"+imports[identifier.Name]+"."+selector.Sel.Name]++
				nativeImports[imports[identifier.Name]] = true
				return true
			}
			switch selector.Sel.Name {
			case "OpenForWrite", "OpenForCopy", "OpenForExport", "OpenForTest", "OpenProtected", "OpenProtectedForTest":
				result[owner+":*."+selector.Sel.Name]++
			case "Open":
				if identifier, ok := selector.X.(*ast.Ident); ok && imports[identifier.Name] == "" {
					result[owner+":*.Open"]++
				}
			}
			return true
		})
	}
	for path := range nativeImports {
		result["import:"+path] = 1
	}
	return result
}

func TestOpenerRatchetSeesAliasedNativeProvidersAndNewOpenerMethods(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "probe.go", `package probe
import native "database/sql"
import "github.com/jackc/pgx/v5"
var open = native.Open
func first(ref unknown) { ref.OpenProtected(nil); ref.OpenForWrite(nil); native.Open("sqlite", "owned") }
func second(ref unknown) { ref.Open(nil) }
func third() { pgx.Connect(nil, "owned") }
`, 0)
	require.NoError(t, err)
	require.Equal(t, map[string]int{
		"import:database/sql": 1, "package:database/sql.Open": 1,
		"first:*.OpenProtected": 1, "first:*.OpenForWrite": 1,
		"first:database/sql.Open": 1, "second:*.Open": 1,
		"import:github.com/jackc/pgx/v5": 1, "third:github.com/jackc/pgx/v5.Connect": 1,
	}, openerReferences(file))
}

func TestOpenerRatchetTracksOfficialDynamoSDKClient(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "dynamodb.go", `package probe
import sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
func explicitNative() { sdk.NewFromConfig(config) }
`, 0)
	require.NoError(t, err)
	require.Equal(t, map[string]int{
		"import:github.com/aws/aws-sdk-go-v2/service/dynamodb":                       1,
		"explicitNative:github.com/aws/aws-sdk-go-v2/service/dynamodb.NewFromConfig": 1,
	}, openerReferences(file))
	require.False(t, nativeProvider("github.com/aws/unrelated/client"))
}
