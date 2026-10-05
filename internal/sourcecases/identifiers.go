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
