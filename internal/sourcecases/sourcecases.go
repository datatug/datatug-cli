// Package sourcecases generates the source strings the DT-0C property test
// feeds to every command path that takes a source: every scheme the CLI knows,
// in lower, upper and mixed case, bare and wrapped in another scheme, with a
// generated secret in each position a parser can read as userinfo or a user can
// put one: userinfo (with and without a user name), a token standing alone as
// the user name, the position a parser misreads as userinfo ("alice:42/secret@"),
// a token or a user name that holds a slash (so no colon or "@" comes before a
// slash), userinfo after a UNC start ("\\alice:secret@"), a user name with a slash
// after a UNC start, a token as the user name straight after a UNC start and after a
// UNC start and one more separator (a backslash or a slash), a second URL written
// after an explicit path start ("/" or "./") that holds the userinfo, the query
// string and the fragment.
//
// The secrets are generated, not typed, so a test that finds one in an output
// has found a real leak and not a coincidence with a fixed word. Generation is
// deterministic: the same call returns the same cases.
package sourcecases

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"unicode"
)

// Case is one source string a user could type.
type Case struct {
	// Name is unique across All.
	Name string
	// Source is the string given to the command.
	Source string
	// Secrets are the literals Source holds that must never appear in an output.
	Secrets []string
	// Style says what the secret looks like ("with spaces", "digits only", ...).
	Style string
	// Position says where the secret is ("userinfo", "query", ...).
	Position string
	// Wrapped is true when Source is a URL inside another scheme's URL.
	Wrapped bool
}

// Schemes are the schemes the CLI knows: everything Parse dispatches, and the
// postgresql alias.
var Schemes = []string{"sqlite", "ingitdb", "postgres", "postgresql", "http", "https", "openvaultdb"}

// wrapperSchemes name a file or directory, so a URL may be written after them.
var wrapperSchemes = []string{"ingitdb", "sqlite", "openvaultdb", "http", "https"}

// innerSchemes are the schemes of a URL written inside a wrapper.
var innerSchemes = []string{"https", "http", "postgres"}

var secretStyles = []string{"word", "digits only", "with spaces", "with slash", "with at sign", "digits then slash", "with question mark", "with hash", "percent-encoded"}

// queryParameters are secret-named parameters; one is picked per style.
var queryParameters = []string{"password", "token", "api_key", "sslpassword", "secret", "key", "pwd", "auth", "credential"}

// All returns every case.
func All() []Case {
	g := &generator{random: rand.New(rand.NewPCG(20261004, 7))}
	var cases []Case
	for _, scheme := range Schemes {
		for casing := 0; casing < 3; casing++ {
			cases = append(cases, g.positions(cased(scheme, casing), "db.example.com:5432/shop?sslmode=require", "", false)...)
		}
	}
	for _, outer := range wrapperSchemes {
		for _, inner := range innerSchemes {
			for casing := 0; casing < 3; casing++ {
				prefix := cased(inner, (casing+1)%3) + "://"
				cases = append(cases, g.positions(cased(outer, casing), "github.com/org/repo", prefix, true)...)
			}
		}
	}
	return cases
}

// CommandCases returns the cases a command-level test runs: the styles that
// stress the readers most (spaces, an "@", digits before a slash, digits only, a
// plain word, a token), so the many command runs stay quick.
func CommandCases() []Case {
	var cases []Case
	for _, c := range All() {
		switch c.Style {
		case "with spaces", "digits then slash", "with at sign", "word", "digits only", "token", "token with slash", "user name with slash":
			cases = append(cases, c)
		}
	}
	return cases
}

// Leaks returns the secrets of c found in texts: a whole secret, or any run of
// four or more letters and digits of it (so a password cut at a space or a slash
// and leaked in part still counts).
func Leaks(c Case, texts ...string) []string {
	var found []string
	for _, secret := range c.Secrets {
		candidates := []string{secret}
		for _, fragment := range strings.FieldsFunc(secret, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if len(fragment) >= 4 {
				candidates = append(candidates, fragment)
			}
		}
		for _, candidate := range candidates {
			for _, text := range texts {
				if strings.Contains(text, candidate) {
					found = append(found, candidate)
					break
				}
			}
		}
	}
	return found
}

// generatedIdentifier finds the identifiers a program makes for itself: a UUID, an
// RFC 3339 timestamp (with or without a fraction), and a hexadecimal digest of 24
// or more digits (a SHA-256 scope hash, a hash-named file).
var generatedIdentifier = regexp.MustCompile(
	`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}` +
		`|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})` +
		`|[0-9a-fA-F]{24,}`)

// WithoutGeneratedIdentifiers returns text with every UUID, RFC 3339 timestamp
// and long hexadecimal digest replaced by a space. Read a file a program wrote
// through it before calling Leaks: those identifiers are random or time-based,
// so a run of four or more characters of a generated secret (all eight digits of
// a digits-only password, five hexadecimal letters of a short one) appears in one
// now and then by chance, and none is made from a source string.
func WithoutGeneratedIdentifiers(text string) string {
	return generatedIdentifier.ReplaceAllString(text, " ")
}

// cased returns scheme in lower case (0), upper case (1) or mixed case (2).
func cased(scheme string, casing int) string {
	switch casing {
	case 1:
		return strings.ToUpper(scheme)
	case 2:
		return mixedCase(scheme)
	}
	return scheme
}

// mixedCase upper-cases every other letter, starting with the first.
func mixedCase(s string) string {
	runes := []rune(s)
	for i := 0; i < len(runes); i += 2 {
		runes[i] = unicode.ToUpper(runes[i])
	}
	return string(runes)
}

type generator struct {
	random *rand.Rand
	// rotation counts the second URLs written, so that they cycle through
	// innerSchemes and every scheme of the CLI meets every inner scheme.
	rotation int
}

func (g *generator) letters(n int, alphabet string) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[g.random.IntN(len(alphabet))]
	}
	return string(out)
}

// word is n letters and digits that starts with a letter.
func (g *generator) word(n int) string {
	return g.letters(1, "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ") + g.letters(n-1, "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789")
}

func (g *generator) secret(style string) string {
	switch style {
	case "digits only":
		return g.letters(8, "0123456789")
	case "with spaces":
		return g.word(5) + " " + g.word(5) + " " + g.word(5)
	case "with slash":
		return g.word(5) + "/" + g.word(5)
	case "with at sign":
		return g.word(5) + "@" + g.word(5)
	case "digits then slash":
		return "42/" + g.word(8)
	case "with question mark":
		return g.word(5) + "?" + g.word(5)
	case "with hash":
		return g.word(5) + "#" + g.word(5)
	case "percent-encoded":
		return g.word(5) + "%2F" + g.word(5)
	case "token":
		return "ghp_" + g.word(12)
	case "token with slash":
		return g.word(3) + "/" + g.word(8)
	}
	return g.word(8)
}

// positions returns the cases for one scheme (cased) in front of tail. When
// wrapped, tail follows prefix, a URL's scheme ("https://"), after the outer
// scheme; the caller has put the right casing on both. Every position is
// generated for every scheme, bare and wrapped.
func (g *generator) positions(scheme, tail, prefix string, wrapped bool) []Case {
	var cases []Case
	name := func(position, style string, extra ...string) string {
		parts := append([]string{scheme + "://" + prefix, position, style}, extra...)
		return strings.Join(parts, " | ")
	}
	add := func(position, style, source string, secrets ...string) {
		cases = append(cases, Case{Name: name(position, style), Source: source, Secrets: secrets, Style: style, Position: position, Wrapped: wrapped})
	}
	head := scheme + "://" + prefix
	hostPath, query, _ := strings.Cut(tail, "?")
	if query != "" {
		query += "&"
	}

	for _, style := range secretStyles {
		password := g.secret(style)
		add("userinfo", style, head+"alice:"+password+"@"+tail, password)
		password = g.secret(style)
		add("empty user name", style, head+":"+password+"@"+tail, password)
		if style != "digits then slash" {
			password = "42/" + g.secret(style)
			add("misread as userinfo", style, head+"alice:"+password+"@"+tail, password)
			password = "42/" + g.secret(style)
			add("empty user name misread as userinfo", style, head+":"+password+"@"+tail, password)
		}
		if style != "with hash" {
			value := g.secret(style)
			parameter := queryParameters[g.random.IntN(len(queryParameters))]
			add("query", style, head+hostPath+"?"+query+parameter+"="+value, value)
			value = g.secret(style)
			add("fragment", style, head+tail+"#"+value, value)
		}
	}
	token := g.secret("token")
	add("token as user name", "token", head+token+"@"+tail, token)
	token = g.secret("token with slash")
	add("token as user name", "token with slash", head+token+"@"+tail, token)
	// A user name with a slash in it puts the colon after a slash, so the text
	// does not start like "user:password@host" and a reader that looks for that
	// shape takes it for a path.
	user, password := g.secret("token with slash"), g.secret("word")
	add("user name with a slash", "user name with slash", head+user+":"+password+"@"+tail, user, password)
	// A UNC start ("\\") reads as a path, and "\\alice:password@host" starts with
	// one all the same: a reader that takes every UNC start for a path shows it.
	password = g.secret("word")
	add("userinfo after a UNC start", "word", head+`\\alice:`+password+"@"+tail, password)
	if !wrapped {
		cases = append(cases, g.afterPathStart(scheme, tail)...)
	}
	return cases
}

// afterPathStart returns the cases for a scheme whose text after "://" starts
// like a path and holds more than a path. "/" and "./" read as a path, so a
// reader that takes every text that starts like one for a path shows what
// follows whole, and a second URL written after the start (a URL that holds
// userinfo, or a token as the user name) is shown with its credentials. A user
// name with a slash in it, after a UNC start, is the same door: the slash comes
// before the colon, so the text does not look like "user:password@host". So is a
// token as the user name straight after a UNC start: it has no colon at all. So is
// a token after a UNC start and one more separator, backslash or slash: the first
// segment, where a server name goes, is empty.
func (g *generator) afterPathStart(scheme, tail string) []Case {
	// The scheme cycles fastest and its casing once per cycle, so over nine second
	// URLs every inner scheme is written in lower, upper and mixed case: the casing a
	// user types is a shape of its own.
	inner := innerSchemes[g.rotation%len(innerSchemes)]
	casing := (g.rotation / len(innerSchemes)) % 3
	g.rotation++
	innerHead := cased(inner, casing) + "://"
	var cases []Case
	add := func(position, style, source string, secrets ...string) {
		cases = append(cases, Case{
			Name:     strings.Join([]string{scheme + "://", position, style}, " | "),
			Source:   source,
			Secrets:  secrets,
			Style:    style,
			Position: position,
			// The second URL is a URL inside another scheme's text.
			Wrapped: strings.Contains(position, "second URL"),
		})
	}
	password := g.secret("word")
	add("second URL after an absolute start", "word", scheme+":///"+innerHead+"alice:"+password+"@"+tail, password)
	token := g.secret("token")
	add("second URL after a dot start", "token", scheme+"://./"+innerHead+token+"@"+tail, token)
	user, password := g.secret("token with slash"), g.secret("word")
	add("user name with a slash after a UNC start", "user name with slash", scheme+`://\\`+user+":"+password+"@"+tail, user, password)
	// A token as the user name straight after a UNC start has no colon and no
	// separator: it stands where a server name does, and no server name holds an "@".
	token = g.secret("token")
	add("token as the user name after a UNC start", "token", scheme+`://\\`+token+"@"+tail, token)
	// A UNC path has a server name. With one more separator straight after the two
	// backslashes the first segment is empty, so the token stands where a share does
	// and nothing stands where the server name goes.
	token = g.secret("token")
	add("token as the user name after a UNC start and a backslash", "token", scheme+`://\\\`+token+"@"+tail, token)
	token = g.secret("token")
	add("token as the user name after a UNC start and a slash", "token", scheme+`://\\/`+token+"@"+tail, token)
	return cases
}
