package sourcecases

import "strings"

// UnsafeIdentifier is a text a client can send where an environment, a catalog
// or another ID belongs that must never reach a file path: it would leave the
// folder the ID names a child of, name an absolute path, or be read differently
// by a layer that decodes it once more.
type UnsafeIdentifier struct {
	// Name says what the text is.
	Name string
	// ID is the text, as it stands after the request's own decoding.
	ID string
}

// UnsafeIdentifiers returns the texts that a path-bound ID check must refuse:
// the parent and the current directory, a traversal, an absolute path, a path
// separator of either kind, a drive path, a NUL, a separator that is still
// percent-encoded after one decoding, a hidden name, a home directory, an empty
// text and one over the length limit.
func UnsafeIdentifiers() []UnsafeIdentifier {
	return []UnsafeIdentifier{
		{"parent directory", ".."},
		{"current directory", "."},
		{"traversal", "../../etc"},
		{"traversal after a name", "local/../../x"},
		{"absolute path", "/etc/passwd"},
		{"slash", "a/b"},
		{"backslash", `a\b`},
		{"drive path", `C:\data`},
		{"NUL", "a\x00b"},
		{"percent-encoded slash", "a%2Fb"},
		{"percent-encoded traversal", "%2e%2e%2f"},
		{"hidden name", ".hidden"},
		{"home directory", "~"},
		{"empty", ""},
		{"too long", strings.Repeat("a", 129)},
	}
}

// UnsafePathIdentifiers returns the texts that a check of an ID made of folders (a
// recordset definition, a folder path) must refuse: every unsafe identifier but the
// text that is a valid path of two plain names, and the paths whose own folders are
// unsafe, empty or both.
func UnsafePathIdentifiers() []UnsafeIdentifier {
	var out []UnsafeIdentifier
	for _, unsafe := range UnsafeIdentifiers() {
		if unsafe.ID == "a/b" {
			continue
		}
		out = append(out, unsafe)
	}
	return append(out,
		UnsafeIdentifier{"empty folder", "a//b"},
		UnsafeIdentifier{"trailing slash", "a/"},
		UnsafeIdentifier{"current directory folder", "a/./b"},
		UnsafeIdentifier{"parent directory folder", "a/../b"},
		UnsafeIdentifier{"hidden folder", "a/.hidden"},
		UnsafeIdentifier{"backslash after a folder", `a/b\c`},
		UnsafeIdentifier{"too many folders", strings.Repeat("a/", 300) + "a"},
	)
}

// UnsafeHosts returns the texts that a check of the host of a db server must refuse:
// the unsafe identifiers (but the name of 129 letters, which is a host name), the
// shapes that are not a host (a space, user information, a path, a zone) and a host
// over the 253 characters a name can have.
func UnsafeHosts() []UnsafeIdentifier {
	var out []UnsafeIdentifier
	for _, unsafe := range UnsafeIdentifiers() {
		if unsafe.Name == "too long" {
			continue
		}
		out = append(out, unsafe)
	}
	return append(out,
		UnsafeIdentifier{"space", "a b"},
		UnsafeIdentifier{"user information", "user@host"},
		UnsafeIdentifier{"path after a host", "host/x"},
		UnsafeIdentifier{"zone", "fe80::1%eth0"},
		UnsafeIdentifier{"too long for a host", strings.Repeat("a", 254)},
	)
}
