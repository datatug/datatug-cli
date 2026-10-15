package faithful

import "testing"

func TestPGExprToSQLite(t *testing.T) {
	cases := map[string]string{
		`(gender = ANY (ARRAY['M'::text, 'F'::text]))`:                                  `(gender IN ('M', 'F'))`,
		`((au_id)::text ~ '^[0-9][0-9]-[0-9]$'::text)`:                                  `((au_id) GLOB '[0-9][0-9]-[0-9]')`,
		`((discount >= (0)::double precision) AND (discount <= (1)::double precision))`: `((discount >= (0)) AND (discount <= (1)))`,
	}
	for in, want := range cases {
		if got := pgExprToSQLite(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}
