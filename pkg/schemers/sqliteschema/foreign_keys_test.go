package sqliteschema

import (
	"context"
	"database/sql"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

func TestGetForeignKeys(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	s := NewSchemaProvider(func() (*sql.DB, error) {
		return db, nil
	})

	testCases := []struct {
		table    string
		expected []schemer.ForeignKey
	}{
		{
			table: "User",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "User", Columns: []string{"MainCountryID"}},
					To:   schemer.FKAnchor{Name: "Country", Columns: []string{"ID"}},
				},
				{
					From: schemer.FKAnchor{Name: "User", Columns: []string{"DefaultCurrency"}},
					To:   schemer.FKAnchor{Name: "Currency", Columns: []string{"ID"}},
				},
			},
		},
		{table: "Country", expected: nil},
		{table: "Currency", expected: nil},
		{table: "Shop", expected: nil},
		{
			table: "Order",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "Order", Columns: []string{"Currency"}},
					To:   schemer.FKAnchor{Name: "Currency", Columns: []string{"ID"}},
				},
				{
					From: schemer.FKAnchor{Name: "Order", Columns: []string{"CountryID"}},
					To:   schemer.FKAnchor{Name: "Country", Columns: []string{"ID"}},
				},
			},
		},
		{
			table: "OrderDetails",
			expected: []schemer.ForeignKey{
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"ShopID", "OrderID"}},
					To:   schemer.FKAnchor{Name: "Order", Columns: []string{"ShopID", "OrderID"}},
				},
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"ProductID"}},
					To:   schemer.FKAnchor{Name: "Product", Columns: []string{"ProductID"}},
				},
				{
					From: schemer.FKAnchor{Name: "OrderDetails", Columns: []string{"Currency"}},
					To:   schemer.FKAnchor{Name: "Currency", Columns: []string{"ID"}},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.table, func(t *testing.T) {
			fks, err := s.GetForeignKeys(context.Background(), "", tc.table)
			if err != nil {
				t.Fatalf("failed to get foreign keys for %s: %v", tc.table, err)
			}
			assertForeignKeys(t, tc.table, tc.expected, fks, false)
		})
	}
}
