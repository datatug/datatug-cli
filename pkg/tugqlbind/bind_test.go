package tugqlbind

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func text(body string) *datatug.QueryDef {
	return &datatug.QueryDef{ID: "q", Type: datatug.QueryTypeTugQL, Text: body}
}

func structured(body string) *datatug.QueryDef {
	return &datatug.QueryDef{ID: "q", Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: body}
}

// resolver resolves catalog c of env e to "sqlite:///<e>/<c>", and fails for "missing".
func resolver(projectID string) Resolver {
	return Resolver{
		ProjectID:   projectID,
		ProjectDir:  "/p",
		Environment: func(context.Context) (string, error) { return "local", nil },
		Local: func(_ context.Context, env, name string) (string, string, error) {
			if name == "missing" {
				return "", "", errors.New("no source missing")
			}
			catalog := name
			if name == "chinook-local" {
				catalog = "chinook" // another name of the same source
			}
			return "sqlite:///" + env + "/" + catalog, catalog, nil
		},
	}
}

const twoDatabases = "databases (\n  sales = 'datatug:///chinook?env=prod'\n  geo\n)\nfrom sales::Invoice as i\njoin geo::countries as c\n  on i.BillingCountry = c.name\nselect i.InvoiceId, c.code\n"

func TestSavedBindsEveryDatabaseOnce(t *testing.T) {
	ctx := context.Background()
	parsed, bindings, err := Saved(ctx, text(twoDatabases), resolver("shop"))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Equal(t, map[string]Binding{
		"sales": {URL: "sqlite:///prod/chinook", Ref: &dbcopy.DatatugRef{Project: "shop", Env: "prod", DB: "chinook"}, ProjectDir: "/p", Env: "prod", Catalog: "chinook"},
		"geo":   {URL: "sqlite:///local/geo", Ref: &dbcopy.DatatugRef{Project: "shop", Env: "local", DB: "geo"}, ProjectDir: "/p", Env: "local", Catalog: "geo"},
	}, bindings)
	assert.Equal(t, map[string]string{"sales": "sqlite:///prod/chinook", "geo": "sqlite:///local/geo"}, URLs(bindings))
	root, ok := Root(parsed, bindings)
	assert.True(t, ok)
	assert.Equal(t, "chinook", root.Catalog)
	_, ok = Root(parsed, nil)
	assert.False(t, ok)

	// A binding carries the catalog id the resolver names, not the name the query wrote.
	_, bindings, err = Saved(ctx, text("databases (\n  crm = 'datatug:///chinook-local'\n)\nfrom crm::Customer as c\nselect c.Email\n"), resolver("shop"))
	require.NoError(t, err)
	assert.Equal(t, "chinook", bindings["crm"].Catalog)
	assert.Equal(t, "chinook", bindings["crm"].Ref.DB)

	// An unregistered project has no reference; the project's own id is not foreign.
	_, bindings, err = Saved(ctx, text(twoDatabases), resolver(""))
	require.NoError(t, err)
	assert.Nil(t, bindings["geo"].Ref)
	_, bindings, err = Saved(ctx, text("databases (\n  geo = 'datatug://shop/geo'\n)\nfrom geo::countries as c\nselect c.code\n"), resolver("shop"))
	require.NoError(t, err)
	assert.Equal(t, "geo", bindings["geo"].Catalog)

	// A structured body binds the database names its sources carry, subqueries included.
	_, bindings, err = Saved(ctx, structured("from: {database: sales, name: Invoice}\nwhere: {exists: {query: {from: {database: geo, name: countries}}}}\n"), resolver("shop"))
	require.NoError(t, err)
	assert.Len(t, bindings, 2)
	// A query that names no database has no bindings, and asks no environment.
	r := resolver("shop")
	r.Environment = nil
	parsed, bindings, err = Saved(ctx, structured("from: {name: Invoice}\n"), r)
	require.NoError(t, err)
	assert.NotNil(t, parsed)
	assert.Nil(t, bindings)
	assert.Nil(t, URLs(nil))
}

func TestSavedRefusals(t *testing.T) {
	ctx := context.Background()
	_, _, err := Saved(ctx, text("not tugql"), resolver("shop"))
	assert.ErrorContains(t, err, "invalid TugQL text")
	assert.False(t, IsError(err))
	_, _, err = Saved(ctx, structured("from: ["), resolver("shop"))
	assert.Error(t, err)

	single := func(target string) error {
		_, _, err := Saved(ctx, text("databases (\n  geo = '"+target+"'\n)\nfrom geo::countries as c\nselect c.code\n"), resolver("shop"))
		return err
	}
	err = single("sqlite:///secret/host.db")
	assert.True(t, IsError(err))
	assert.ErrorContains(t, err, "connection string")
	assert.NotContains(t, err.Error(), "secret")
	err = single("datatug://bad name/x")
	assert.True(t, IsError(err), err)
	err = single("datatug://other/geo")
	assert.True(t, IsError(err))
	assert.ErrorContains(t, err, "names another project")
	assert.ErrorContains(t, single("datatug:///missing"), "no source missing")

	r := resolver("shop")
	r.Foreign = func(_ context.Context, target string) (Binding, error) { return Binding{URL: "foreign:" + target}, nil }
	_, bindings, err := Saved(ctx, text("databases (\n  geo = 'datatug://other/geo'\n)\nfrom geo::countries as c\nselect c.code\n"), r)
	require.NoError(t, err)
	assert.Equal(t, "foreign:datatug://other/geo", bindings["geo"].URL)

	r.Environment = func(context.Context) (string, error) { return "", errors.New("no environment") }
	_, _, err = Saved(ctx, text(twoDatabases), r)
	assert.ErrorContains(t, err, "no environment")

	_, _, err = Saved(ctx, structured("from: {database: sales, name: Invoice}\nwhere: {exists: {query: {from: {name: Customer}}}}\n"), resolver("shop"))
	assert.True(t, IsError(err))
	assert.Equal(t, `saved query "q": `+SavedUnqualifiedMessage("Customer"), err.Error())
	assert.Contains(t, SavedUnqualifiedMessage("Customer"), "structured source's database key")
	assert.Equal(t, "source c names no database: a query with a databases block reads every source as <alias>::c", UnqualifiedMessage("c"))
}

func TestLabelGuard(t *testing.T) {
	ctx := context.Background()
	built := map[string]int{}
	var checked []secureread.FederatedLeaf
	guard := LabelGuard(map[string]Binding{
		"sales": {ProjectDir: "/p", Catalog: "chinook"},
		"also":  {ProjectDir: "/p", Catalog: "geo"},
		"raw":   {URL: "sqlite:///x.db"},
	}, func(dir string) secureread.FederatedColumnGuard {
		built[dir]++
		return func(_ context.Context, leaf secureread.FederatedLeaf) error {
			checked = append(checked, leaf)
			return nil
		}
	})
	require.NoError(t, guard(ctx, secureread.FederatedLeaf{Database: "sales", Collection: "Invoice"}))
	require.NoError(t, guard(ctx, secureread.FederatedLeaf{Database: "also", Collection: "countries"}))
	require.NoError(t, guard(ctx, secureread.FederatedLeaf{Database: "raw"}), "a source of no project is named by no label")
	assert.ErrorContains(t, guard(ctx, secureread.FederatedLeaf{Database: "other"}), `database "other" is not configured`)
	assert.Equal(t, map[string]int{"/p": 1}, built, "one guard per project directory")
	require.Len(t, checked, 2)
	assert.Equal(t, "chinook", checked[0].Database, "a leaf is named by its source's catalog id")
	assert.Equal(t, "geo", checked[1].Database)
}
