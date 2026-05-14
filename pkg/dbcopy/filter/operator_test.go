package filter

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestParseOperator(t *testing.T) {
	cases := []struct {
		token   string
		want    dal.Operator
		wantErr bool
	}{
		// Six DALgo-supported operators (MVP vocabulary).
		{"=", dal.Equal, false},
		{"<", dal.LessThen, false},
		{"<=", dal.LessOrEqual, false},
		{">", dal.GreaterThen, false},
		{">=", dal.GreaterOrEqual, false},
		{"in", dal.In, false},

		// Deferred operators — must be rejected with a vocabulary error
		// pointing at the dalgo-extended-operators follow-up.
		{"!=", "", true},
		{"not_in", "", true},
		{"is_null", "", true},
		{"is_not_null", "", true},

		// Plainly unknown.
		{"like", "", true},
		{"between", "", true},
		{"==", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			got, err := ParseOperator(tc.token)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseOperator(%q) err=%v, wantErr=%v", tc.token, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("ParseOperator(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestParseOperator_UnknownLists_All_Supported(t *testing.T) {
	_, err := ParseOperator("like")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, op := range []string{"=", "<", "<=", ">", ">=", "in"} {
		if !strings.Contains(msg, op) {
			t.Errorf("error %q must list supported operator %q", msg, op)
		}
	}
}
