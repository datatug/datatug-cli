package dbcopy

import (
	"net/url"
	"strings"
)

// sessionDefault is one parameter of the PostgreSQL session that DataTug sets when the URL does
// not.
type sessionDefault struct {
	key   string
	value string
	// readsOnly marks the default that makes the session read-only, which a connection that is
	// written through (the target of `datatug db copy`) does not get.
	readsOnly bool
}

// postgresSessionDefaults are what a connection gets when the URL sets none of them, in the order
// they are appended: a read-only session, a statement timeout of 30 seconds, the UTC time zone,
// a name that tells the server who is connected, and a connect timeout of 10 seconds. They are
// startup parameters of the connection (the driver reads connect_timeout itself and sends the
// others to the server), so they hold for every statement of every pooled connection.
var postgresSessionDefaults = []sessionDefault{
	{key: "default_transaction_read_only", value: "on", readsOnly: true},
	{key: "statement_timeout", value: "30000"},
	{key: "timezone", value: "UTC"},
	{key: "application_name", value: "datatug"},
	{key: "connect_timeout", value: "10"},
}

// readOnlyParameter names the parameter that decides whether a session may write.
const readOnlyParameter = "default_transaction_read_only"

// errPostgresServiceFile is what a read of a PostgreSQL source whose URL names a service or a
// service file is refused with: a service file sets parameters of the session that the URL does
// not show, so a read takes its settings from the URL alone. It names the two settings, not the
// URL. (A service file that the environment names, PGSERVICE or PGSERVICEFILE, is the
// operator's own, as the rest of the environment is, and is not refused.)
var errPostgresServiceFile error = &refusedError{"the PostgreSQL URL names a service or a service file (service, servicefile): a read of a PostgreSQL source takes its settings from the URL alone, so write them in the URL"}

// asciiLower lower-cases the ASCII letters of s and nothing else. The server compares the names of
// its parameters as ASCII, so a letter that Unicode folds to an ASCII one (U+0130, the dotted
// capital I, lower-cases to "i") must not turn a key into the name of a parameter.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// postgresConnectionString is the one function that assembles the options of a PostgreSQL
// connection and the only place that reads the parameters of its URL. It returns rawURL with every
// session default (see postgresSessionDefaults) that the URL does not set appended to its query,
// before any fragment; what the person typed is not rewritten, the user name and the password
// included. Parameter names are compared without regard to the case of their ASCII letters, as the
// server compares them (see asciiLower).
//
// A read (forWrite false) is refused with errPostgresReadOnlyOff when the URL turns the read-only
// session off: whatever it writes for off, and an "options" parameter that mentions the parameter
// at all; and with errPostgresServiceFile when it names a service or a service file. Only the
// target of `datatug db copy` is opened with forWrite true: it gets no read-only default, and a
// URL's own settings are kept as typed.
//
// The URL is read as net/url, which the driver uses too, reads it. A URL that cannot be read
// returns errUnreadablePostgresURL. No error quotes the URL.
func postgresConnectionString(rawURL string, forWrite bool) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errUnreadablePostgresURL
	}
	query := parsed.Query()
	set := make(map[string]bool, len(query))
	for key := range query {
		set[asciiLower(key)] = true
	}
	if !forWrite {
		if set["service"] || set["servicefile"] {
			return "", errPostgresServiceFile
		}
		if turnsReadOnlyOff(query) {
			return "", errPostgresReadOnlyOff
		}
	}
	var added []string
	for _, def := range postgresSessionDefaults {
		if set[def.key] || (forWrite && def.readsOnly) {
			continue
		}
		added = append(added, def.key+"="+def.value)
	}
	if len(added) == 0 {
		return rawURL, nil
	}
	base, fragment, hasFragment := strings.Cut(rawURL, "#")
	separator := "&"
	switch {
	case !strings.Contains(base, "?"):
		separator = "?"
	case strings.HasSuffix(base, "?"), strings.HasSuffix(base, "&"):
		separator = ""
	}
	connection := base + separator + strings.Join(added, "&")
	if hasFragment {
		connection += "#" + fragment
	}
	return connection, nil
}

// turnsReadOnlyOff reports whether query, the parameters of a PostgreSQL URL, leaves a
// session that may write: the read-only parameter is set to anything but a spelling of on (on,
// true, yes, 1, t, y) under any of its values, or an "options" parameter, which can set it
// through the server's -c syntax, mentions it.
func turnsReadOnlyOff(query url.Values) bool {
	for key, values := range query {
		switch asciiLower(key) {
		case readOnlyParameter:
			for _, value := range values {
				if !spellsOn(value) {
					return true
				}
			}
		case "options":
			for _, value := range values {
				if strings.Contains(asciiLower(optionsText(value)), readOnlyParameter) {
					return true
				}
			}
		}
	}
	return false
}

// optionsText is value as the server reads the text of an options parameter before it looks for a name in it: a
// backslash is dropped, and a dash stands for an underscore.
func optionsText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, ""), "-", "_")
}

// spellsOn reports whether value is a spelling of true that PostgreSQL reads as a boolean.
func spellsOn(value string) bool {
	switch asciiLower(value) {
	case "on", "true", "yes", "1", "t", "y":
		return true
	}
	return false
}
