package sourcecases

import (
	"strings"
	"testing"
)

func TestAll_CoversEverySchemeCasingAndPosition(t *testing.T) {
	t.Parallel()
	cases := All()
	if len(cases) < 1000 {
		t.Fatalf("only %d cases", len(cases))
	}
	seen := map[string]map[string]bool{}
	names := map[string]bool{}
	for _, c := range cases {
		if names[c.Name] {
			t.Fatalf("duplicate case name %q", c.Name)
		}
		names[c.Name] = true
		scheme := strings.ToLower(c.Source[:strings.Index(c.Source, "://")])
		if seen[scheme] == nil {
			seen[scheme] = map[string]bool{}
		}
		switch head := c.Source[:strings.Index(c.Source, "://")]; {
		case head == strings.ToLower(head):
			seen[scheme]["lower"] = true
		case head == strings.ToUpper(head):
			seen[scheme]["upper"] = true
		default:
			seen[scheme]["mixed"] = true
		}
		if c.Style == "" || c.Position == "" {
			t.Fatalf("case %q has no style or position", c.Name)
		}
		for _, secret := range c.Secrets {
			if len(secret) < 4 {
				t.Fatalf("case %q has a secret shorter than four characters: %q", c.Name, secret)
			}
			if !strings.Contains(c.Source, secret) {
				t.Fatalf("case %q: the source %q does not hold its secret %q", c.Name, c.Source, secret)
			}
		}
	}
	for _, scheme := range Schemes {
		for _, casing := range []string{"lower", "upper", "mixed"} {
			if !seen[scheme][casing] {
				t.Errorf("no %s-case %s case", casing, scheme)
			}
		}
	}
	wantPositions := map[string]bool{}
	wantStyles := map[string]bool{}
	wrapped := 0
	for _, c := range cases {
		wantPositions[c.Position] = true
		wantStyles[c.Style] = true
		if c.Wrapped {
			wrapped++
		}
	}
	for _, position := range []string{"userinfo", "empty user name", "token as user name", "misread as userinfo", "empty user name misread as userinfo", "query", "fragment"} {
		if !wantPositions[position] {
			t.Errorf("no case with the secret in position %q", position)
		}
	}
	for _, style := range []string{"word", "digits only", "with spaces", "with slash", "with at sign", "digits then slash", "with question mark", "with hash", "percent-encoded"} {
		if !wantStyles[style] {
			t.Errorf("no case with a %s secret", style)
		}
	}
	if wrapped == 0 || wrapped == len(cases) {
		t.Fatalf("%d of %d cases are wrapped", wrapped, len(cases))
	}
}

func TestAll_IsDeterministic(t *testing.T) {
	t.Parallel()
	first, second := All(), All()
	if len(first) != len(second) {
		t.Fatal("the number of cases changes between calls")
	}
	for i := range first {
		if first[i].Source != second[i].Source {
			t.Fatalf("case %d differs between calls", i)
		}
	}
}

func TestCommandCases_IsASmallerSetWithTheNastiestStyles(t *testing.T) {
	t.Parallel()
	all, some := All(), CommandCases()
	if len(some) == 0 || len(some) >= len(all) {
		t.Fatalf("%d of %d", len(some), len(all))
	}
	for _, c := range some {
		switch c.Style {
		case "with spaces", "digits then slash", "with at sign", "word", "digits only", "token", "token with slash":
		default:
			t.Fatalf("unexpected style %q in the command set", c.Style)
		}
	}
}

func TestLeaks(t *testing.T) {
	t.Parallel()
	c := Case{Secrets: []string{"42/abcdefgh", "pa ss wordxyz"}}
	if got := Leaks(c, "nothing here", "supported schemes"); len(got) != 0 {
		t.Fatalf("false positive: %v", got)
	}
	if got := Leaks(c, "x 42/abcdefgh y"); len(got) != 2 || got[0] != "42/abcdefgh" || got[1] != "abcdefgh" {
		t.Fatalf("whole secret (and the run of it that is long enough): %v", got)
	}
	if got := Leaks(c, "only abcdefgh leaked"); len(got) != 1 || got[0] != "abcdefgh" {
		t.Fatalf("fragment: %v", got)
	}
	if got := Leaks(c, "short 42 and pa ss"); len(got) != 0 {
		t.Fatalf("fragments under four characters are not secrets: %v", got)
	}
	if got := Leaks(c, "one wordxyz", "two abcdefgh"); len(got) != 2 {
		t.Fatalf("every text is searched: %v", got)
	}
}

func TestMixedCase(t *testing.T) {
	t.Parallel()
	if got := mixedCase("postgres"); got != "PoStGrEs" {
		t.Fatalf("mixedCase = %q", got)
	}
}
