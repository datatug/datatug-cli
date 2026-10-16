package crossverify

import (
	"database/sql"
	"math"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

func fam(f string) faithful.Choice { return faithful.Choice{Family: f, Type: strings.ToUpper(f)} }

func TestCanonSource(t *testing.T) {
	cases := []struct {
		family string
		v      any
		want   string
	}{
		{"int", int64(-5), "i-5"}, {"bool", int64(1), "i1"},
		{"float", 1.5, "f3ff8000000000000"}, {"float", int64(2), "f4000000000000000"}, {"float", math.Copysign(0, -1), "f8000000000000000"},
		{"decimal", int64(5), "d5/1"}, {"decimal", 0.99, "d99/100"}, {"decimal", "1.50", "d3/2"}, {"decimal", " 2 ", "d2/1"},
		{"text", "abc", "t3:abc"}, {"text", int64(12), "t2:12"}, {"text", 1.5, "t3:1.5"}, {"text", []byte("é"), "t2:é"},
		{"bytes", []byte{1}, "b1:\x01"}, {"bytes", "ab", "b2:ab"}, {"bytes", int64(7), "b1:7"},
		{"date", "2024-01-01", "D2024-01-01"}, {"datetime", "2024-01-01 10:00:00.500", "T2024-01-01 10:00:00.5"}, {"time", "10:00:00", "T10:00:00"}, {"time", "10:00:00.000", "T10:00:00"},
		{"int", nil, "N"},
	}
	for _, c := range cases {
		got, err := canonSource(fam(c.family), c.v)
		if err != nil || got != c.want {
			t.Errorf("canonSource(%s, %#v) = %q, %v; want %q", c.family, c.v, got, err, c.want)
		}
	}
	for _, c := range []struct {
		family string
		v      any
	}{{"int", "x"}, {"float", "x"}, {"decimal", true}, {"decimal", "abc"}, {"text", true}, {"bytes", true}, {"date", int64(1)}, {"datetime", 1.5}, {"weird", int64(1)}} {
		if got, err := canonSource(fam(c.family), c.v); err == nil {
			t.Errorf("canonSource(%s, %#v) = %q, want an error", c.family, c.v, got)
		}
	}
}

func TestCanonTarget(t *testing.T) {
	cases := []struct {
		family string
		v      any
		want   string
	}{
		{"int", int64(-5), "i-5"}, {"bool", int64(1), "i1"}, {"float", 1.5, "f3ff8000000000000"}, {"decimal", []byte("1.50"), "d3/2"},
		{"text", []byte("abc"), "t3:abc"}, {"bytes", []byte{1}, "b1:\x01"}, {"date", []byte("2024-01-01"), "D2024-01-01"},
		{"datetime", []byte("2024-01-01 10:00:00.500"), "T2024-01-01 10:00:00.5"}, {"time", []byte("10:00:00"), "T10:00:00"}, {"text", nil, "N"},
	}
	for _, c := range cases {
		got, err := canonTarget(fam(c.family), c.v)
		if err != nil || got != c.want {
			t.Errorf("canonTarget(%s, %#v) = %q, %v; want %q", c.family, c.v, got, err, c.want)
		}
	}
	// What the driver should not return is an error, not a quiet mismatch.
	for _, c := range []struct {
		family string
		v      any
	}{{"int", []byte("1")}, {"float", int64(1)}, {"decimal", int64(1)}, {"decimal", []byte("abc")}, {"text", "s"}, {"bytes", "s"}, {"date", "s"}, {"datetime", "s"}, {"weird", int64(1)}} {
		if got, err := canonTarget(fam(c.family), c.v); err == nil {
			t.Errorf("canonTarget(%s, %#v) = %q, want an error", c.family, c.v, got)
		}
	}
}

func TestRowDigestSeparatesFields(t *testing.T) {
	a, b := rowDigest([]string{"ab", "c"}), rowDigest([]string{"a", "bc"})
	if a == b || a != rowDigest([]string{"ab", "c"}) {
		t.Error("digests")
	}
}

var _ = sql.ErrNoRows
