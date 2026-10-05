package sourcecases

import (
	"strconv"
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
	for _, position := range []string{"userinfo", "empty user name", "token as user name", "user name with a slash", "misread as userinfo", "empty user name misread as userinfo", "query", "fragment"} {
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

// A user name or a token that holds a slash puts the colon or the "@" after a
// slash. It is a shape of every scheme, bare and wrapped: it was once generated
// only inside a wrapped URL, which hid that the path schemes showed it.
func TestAll_ASlashInTheUserNameOrTokenIsGeneratedForEverySchemeBareAndWrapped(t *testing.T) {
	t.Parallel()
	counts := map[string]map[bool]int{}
	for _, c := range All() {
		if c.Style != "token with slash" && c.Style != "user name with slash" {
			continue
		}
		scheme := strings.ToLower(c.Source[:strings.Index(c.Source, "://")])
		key := c.Style + " " + scheme
		if counts[key] == nil {
			counts[key] = map[bool]int{}
		}
		counts[key][c.Wrapped]++
		for _, secret := range c.Secrets {
			if !strings.Contains(secret, "/") && c.Style == "token with slash" {
				t.Fatalf("case %q: the secret %q holds no slash", c.Name, secret)
			}
		}
		before, _, _ := strings.Cut(c.Source, "@")
		if userinfo := before[strings.LastIndex(before, "://")+3:]; !strings.Contains(userinfo, "/") {
			t.Fatalf("case %q: no slash stands in the userinfo %q", c.Name, userinfo)
		}
	}
	for _, scheme := range Schemes {
		for _, style := range []string{"token with slash", "user name with slash"} {
			if counts[style+" "+scheme][false] == 0 {
				t.Errorf("no bare %s case for %s", style, scheme)
			}
		}
	}
	for _, scheme := range wrapperSchemes {
		for _, style := range []string{"token with slash", "user name with slash"} {
			if counts[style+" "+scheme][true] == 0 {
				t.Errorf("no wrapped %s case for %s", style, scheme)
			}
		}
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
		case "with spaces", "digits then slash", "with at sign", "word", "digits only", "token", "token with slash", "user name with slash":
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

func TestWithoutGeneratedIdentifiers(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in, want string }{
		{"uuid", "id 6f1c2a9e-3b4d-4c5e-8f60-1a2b3c4d5e6f end", "id   end"},
		{"timestamp with a fraction", "at 2026-10-05T10:11:12.123456789Z end", "at   end"},
		{"timestamp with an offset", "at 2026-10-05T10:11:12+02:00 end", "at   end"},
		{"timestamp without a fraction", "at 2026-10-05T10:11:12Z end", "at   end"},
		{"digest", "scope 8df98b088eef33da4a2c7e1f0b9d6a3c5e7f9b1d3a5c7e9f1b3d5a7c9e1f3b5d end", "scope   end"},
		{"file name", "/h/.datatug/chat/8df98b088eef33da4a2c7e1f.sqlite", "/h/.datatug/chat/ .sqlite"},
		{"digits of a password stay", "pw 12345678 and abc12 and 2026-10-05", "pw 12345678 and abc12 and 2026-10-05"},
		{"a short hex run stays", "token f69f5 and 0123456789abcdef", "token f69f5 and 0123456789abcdef"},
	}
	for _, tc := range tests {
		if got := WithoutGeneratedIdentifiers(tc.in); got != tc.want {
			t.Errorf("%s: WithoutGeneratedIdentifiers(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	// A secret that sits inside an identifier by chance is not a leak; one that stands beside it is.
	c := Case{Secrets: []string{"12345678"}}
	if got := Leaks(c, WithoutGeneratedIdentifiers("t 2026-10-05T10:11:12.123456789Z")); len(got) != 0 {
		t.Errorf("a run of digits in a timestamp: %v", got)
	}
	if got := Leaks(c, WithoutGeneratedIdentifiers("t 2026-10-05T10:11:12Z password 12345678")); len(got) == 0 {
		t.Errorf("a secret beside a timestamp is still found: %v", got)
	}
}

// "\\alice:secret@host" starts with a UNC prefix, which reads as a path, and is
// credentials all the same. It is a shape of every scheme, bare and wrapped.
func TestAll_UserinfoAfterAUNCStartIsGeneratedForEverySchemeBareAndWrapped(t *testing.T) {
	t.Parallel()
	bare, wrapped := map[string]int{}, map[string]int{}
	for _, c := range All() {
		if c.Position != "userinfo after a UNC start" {
			continue
		}
		scheme := strings.ToLower(c.Source[:strings.Index(c.Source, "://")])
		if c.Wrapped {
			wrapped[scheme]++
		} else {
			bare[scheme]++
		}
		if !strings.Contains(c.Source, `\\alice:`+c.Secrets[0]+"@") {
			t.Errorf("case %q: %q is not userinfo after a UNC start", c.Name, c.Source)
		}
	}
	for _, scheme := range Schemes {
		if bare[scheme] == 0 {
			t.Errorf("no bare case of userinfo after a UNC start for %s", scheme)
		}
	}
	for _, scheme := range wrapperSchemes {
		if wrapped[scheme] == 0 {
			t.Errorf("no wrapped case of userinfo after a UNC start for %s", scheme)
		}
	}
}

// A text that starts like a path ("/", "./") and then holds a second URL is not a
// path: whoever typed it wrote credentials. A slash in the user name after a UNC
// start puts the colon after a slash. All three are shapes of every scheme.
func TestAll_ASecondURLAfterAPathStartIsGeneratedForEveryScheme(t *testing.T) {
	t.Parallel()
	counts := map[string]map[string]int{}
	// innerSeen holds every (scheme, casing) of a second URL: the casing a user types
	// ("https://") is a shape of its own, not only the upper and the mixed one.
	innerSeen := map[string]bool{}
	for _, c := range All() {
		switch c.Position {
		case "second URL after an absolute start", "second URL after a dot start":
			scheme := strings.ToLower(c.Source[:strings.Index(c.Source, "://")])
			rest := c.Source[strings.Index(c.Source, "://")+3:]
			start := "/"
			if c.Position == "second URL after a dot start" {
				start = "./"
			}
			if !strings.HasPrefix(rest, start) {
				t.Fatalf("case %q: %q does not start with %q after the scheme", c.Name, c.Source, start)
			}
			inner := rest[len(start):]
			cut := strings.Index(inner, "://")
			if cut < 0 || !strings.Contains(inner[cut:], c.Secrets[0]+"@") {
				t.Fatalf("case %q: %q holds no second URL whose userinfo is the secret", c.Name, c.Source)
			}
			for casing := 0; casing < 3; casing++ {
				if inner[:cut] == cased(strings.ToLower(inner[:cut]), casing) {
					innerSeen[strings.ToLower(inner[:cut])+"/"+strconv.Itoa(casing)] = true
				}
			}
			if counts[c.Position] == nil {
				counts[c.Position] = map[string]int{}
			}
			counts[c.Position][scheme]++
		case "token as the user name after a UNC start":
			// No colon and no separator: the token is the whole of the first segment, the
			// place a server name goes.
			scheme := strings.ToLower(c.Source[:strings.Index(c.Source, "://")])
			rest := c.Source[strings.Index(c.Source, "://")+3:]
			userinfo, _, _ := strings.Cut(rest, "@")
			if !strings.HasPrefix(userinfo, `\\`) || strings.ContainsAny(userinfo[2:], `\/:`) || userinfo[2:] != c.Secrets[0] {
				t.Fatalf("case %q: %q is not a token as the user name straight after a UNC start", c.Name, c.Source)
			}
			if counts[c.Position] == nil {
				counts[c.Position] = map[string]int{}
			}
			counts[c.Position][scheme]++
		case "user name with a slash after a UNC start":
			scheme := strings.ToLower(c.Source[:strings.Index(c.Source, "://")])
			rest := c.Source[strings.Index(c.Source, "://")+3:]
			userinfo, _, _ := strings.Cut(rest, "@")
			if !strings.HasPrefix(userinfo, `\\`) || !strings.Contains(userinfo[2:], "/") || strings.Index(userinfo, "/") > strings.Index(userinfo, ":") {
				t.Fatalf("case %q: %q is not a slash in the user name that comes before the colon after a UNC start", c.Name, c.Source)
			}
			if counts[c.Position] == nil {
				counts[c.Position] = map[string]int{}
			}
			counts[c.Position][scheme]++
		}
	}
	for _, position := range []string{"second URL after an absolute start", "second URL after a dot start", "user name with a slash after a UNC start", "token as the user name after a UNC start"} {
		for _, scheme := range Schemes {
			if counts[position][scheme] == 0 {
				t.Errorf("no case of %q for %s", position, scheme)
			}
		}
	}
	for _, inner := range innerSchemes {
		for casing, name := range []string{"lower case", "upper case", "mixed case"} {
			if !innerSeen[inner+"/"+strconv.Itoa(casing)] {
				t.Errorf("no second URL uses the scheme %s in %s", inner, name)
			}
		}
	}
}

// The identifiers a path-bound check must refuse cover each way out of a folder:
// every one has a name, they are distinct, and the list holds a NUL, a
// percent-encoded separator, an absolute path, both separators and a traversal.
func TestUnsafeIdentifiers_CoversEveryWayOutOfAFolder(t *testing.T) {
	t.Parallel()
	names, ids := map[string]bool{}, map[string]bool{}
	for _, unsafe := range UnsafeIdentifiers() {
		if unsafe.Name == "" || names[unsafe.Name] || ids[unsafe.ID] {
			t.Fatalf("every identifier needs a distinct name and text: %+v", unsafe)
		}
		names[unsafe.Name], ids[unsafe.ID] = true, true
	}
	for _, want := range []string{"..", "/etc/passwd", "a/b", `a\b`, "a\x00b", "a%2Fb", "../../etc", ""} {
		if !ids[want] {
			t.Errorf("no unsafe identifier %q", want)
		}
	}
}
