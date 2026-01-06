package sqliteschema

import (
	"context"
	"database/sql"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

func TestGetReferrers(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()

	s := NewSchemaProvider(func() (*sql.DB, error) {
		return db, nil
	})

	testCases := []struct {
		table    string
		expected []schemer.ForeignKey
	}{
		{
			table: "Country",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "User", Columns: []string{"MainCountryID"}},
					To:   schemer.FKAnchor{Name: "Country", Columns: []string{"ID"}},
				},
				{
					From: schemer.FKAnchor{Name: "Order", Columns: []string{"CountryID"}},
					To:   schemer.FKAnchor{Name: "Country", Columns: []string{"ID"}},
				},
			},
		},
		{
			table: "Currency",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "User", Columns: []string{"DefaultCurrency"}},
					To:   schemer.FKAnchor{Name: "Currency", Columns: []string{"ID"}},
				},
				{
					From: schemer.FKAnchor{Name: "Order", Columns: []string{"Currency"}},
					To:   schemer.FKAnchor{Name: "Currency", Columns: []string{"ID"}},
				},
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"Currency"}},
					To:   schemer.FKAnchor{Name: "Currency", Columns: []string{"ID"}},
				},
			},
		},
		{
			table: "[Order]",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"ShopID", "OrderID"}},
					To:   schemer.FKAnchor{Name: "Order", Columns: []string{"ShopID", "OrderID"}},
				},
			},
		},
		{
			table: "Order",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"ShopID", "OrderID"}},
					To:   schemer.FKAnchor{Name: "Order", Columns: []string{"ShopID", "OrderID"}},
				},
			},
		},
		{
			table: "Product",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"ProductID"}},
					To:   schemer.FKAnchor{Name: "Product", Columns: []string{"ProductID"}},
				},
			},
		},
		{table: "User", expected: nil},
		{table: "Shop", expected: nil},
		{table: "OrderDetails", expected: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.table, func(t *testing.T) {
			referrers, err := s.GetReferrers(context.Background(), "", tc.table)
			if err != nil {
				t.Fatalf("failed to get referrers for %s: %v", tc.table, err)
			}
			assertForeignKeys(t, tc.table, tc.expected, referrers, true)
		})
	}

	t.Run("error cases", func(t *testing.T) {
		sErr := NewSchemaProvider(func() (*sql.DB, error) {
			return nil, sql.ErrConnDone
		})
		_, err := sErr.GetReferrers(context.Background(), "", "User")
		if err == nil {
			t.Error("expected error when DB connection fails")
		}

		// To test db.Query error, we can use a closed DB
		closedDB, _ := sql.Open("sqlite3", ":memory:")
		_ = closedDB.Close()

		sClosed := NewSchemaProvider(func() (*sql.DB, error) {
			return closedDB, nil
		})
		_, err = sClosed.GetReferrers(context.Background(), "", "User")
		if err == nil {
			t.Error("expected error when querying closed DB")
		}

		// To test error in GetForeignKeys, we can use a custom provider that returns a DB that fails later
		// or just call GetForeignKeys with an invalid table name (if that produces an error)
		_, err = s.GetReferrers(context.Background(), "", "")
		if err == nil {
			t.Error("expected error when target table is empty")
		}
	})
}
