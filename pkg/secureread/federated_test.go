package secureread

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

const federatedSalesQuery = `from:
  database: orders
  name: Invoice
  alias: o
  scan: {orderBy: [{field: id, desc: true}], limit: 100}
  joins:
    - from: {database: countries, name: Country, alias: c}
      on: [{left: {field: country_id, source: o}, op: '==', right: {field: id, source: c}}]
groupBy: [{field: id, source: c}, {field: name, source: c}, {field: population, source: c}]
columns:
  - {field: name, source: c, as: country}
  - {aggregate: {function: sum, args: [{field: amount, source: o}]}, as: totalSales}
  - {binary: {op: '/', left: {aggregate: {function: sum, args: [{field: amount, source: o}]}}, right: {field: population, source: c}}, as: salesPerCapita}
`

func TestRunFederatedDTQLTwoSQLiteDatabases(t *testing.T) {
	root := t.TempDir()
	makeDB := func(name, schema string) *sql.DB {
		t.Helper()
		path := filepath.Join(root, name+".sqlite")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
		return db
	}
	orders := makeDB("orders", `CREATE TABLE Invoice (id INTEGER PRIMARY KEY, country_id INTEGER NOT NULL, amount NUMERIC NOT NULL)`)
	for id := 1; id <= 102; id++ {
		amount := 20
		if id%2 == 0 {
			amount = 10
		}
		if id <= 2 {
			amount = 999
		}
		if _, err := orders.Exec(`INSERT INTO Invoice VALUES (?, ?, ?)`, id, id%2+1, amount); err != nil {
			t.Fatal(err)
		}
	}
	countries := makeDB("countries", `CREATE TABLE Country (id INTEGER PRIMARY KEY, name TEXT NOT NULL, population INTEGER NOT NULL)`)
	for _, row := range []struct {
		id         int
		name       string
		population int
	}{{1, "Alpha", 100}, {2, "Beta", 200}} {
		if _, err := countries.Exec(`INSERT INTO Country VALUES (?, ?, ?)`, row.id, row.name, row.population); err != nil {
			t.Fatal(err)
		}
	}
	urls := map[string]string{"orders": "sqlite://" + filepath.Join(root, "orders.sqlite"), "countries": "sqlite://" + filepath.Join(root, "countries.sqlite")}
	result, err := NewExecutor(Session{Unrestricted: true}).RunFederatedDTQL(context.Background(), []byte(federatedSalesQuery), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows: %+v", result.Rows)
	}
	got := map[string]Row{}
	for _, row := range result.Rows {
		got[row.Data["country"].(string)] = row
	}
	if got["Alpha"].Data["totalSales"] != float64(500) || got["Alpha"].Data["salesPerCapita"] != float64(5) || got["Beta"].Data["totalSales"] != float64(1000) || got["Beta"].Data["salesPerCapita"] != float64(5) {
		t.Fatalf("unexpected totals: %+v", got)
	}
	delete(urls, "countries")
	if _, err := NewExecutor(Session{Unrestricted: true}).RunFederatedDTQL(context.Background(), []byte(federatedSalesQuery), urls, nil); err == nil {
		t.Fatal("missing database was accepted")
	}
}
