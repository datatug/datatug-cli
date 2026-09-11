package accesspolicies

import (
	"errors"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dtql"
)

func TestQueryParameters(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		want    []string
		wantErr bool
	}{
		{
			name: "no where clause",
			doc:  "from:\n  name: Invoice\n",
		},
		{
			name: "a literal comparison has no parameters",
			doc:  "from:\n  name: Invoice\nwhere:\n  op: ==\n  left:\n    field: Country\n  right:\n    value: Canada\n",
		},
		{
			name: "parameters in a group, sorted and de-duplicated",
			doc: "from:\n  name: Invoice\nwhere:\n  and:\n" +
				"    - op: ==\n      left:\n        field: CustomerId\n      right:\n        param: Customer.ID\n" +
				"    - op: ==\n      left:\n        field: BillingCountry\n      right:\n        param: Country\n" +
				"    - op: ==\n      left:\n        field: SupportRepId\n      right:\n        param: Customer.ID\n",
			want: []string{"Country", "Customer.ID"},
		},
		{
			name:    "a parameter on the left-hand side",
			doc:     "from:\n  name: Invoice\nwhere:\n  op: ==\n  left:\n    param: Country\n  right:\n    field: BillingCountry\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := dtql.Deserialize([]byte(tt.doc))
			if err != nil {
				t.Fatalf("test DTQL does not parse: %v", err)
			}
			got, err := QueryParameters(query)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidQuery) {
					t.Fatalf("expected ErrInvalidQuery, got %v (%v)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("QueryParameters = %v, want %v", got, tt.want)
			}
		})
	}
}
