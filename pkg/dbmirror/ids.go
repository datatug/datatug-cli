package dbmirror

import (
	"fmt"
	"strings"
)

// A Firestore document id may hold any bytes but "/" (up to 1500 of them), and
// "." and ".." are not ids. A file name may not: "\" and ":" mean something on
// Windows, NUL, CON and the like are devices there, a trailing dot or space is
// dropped, a leading "." hides the file from inGitDB, and most filesystems stop
// at 255 bytes. encodeID gives every id a file name that is safe on Linux,
// macOS and Windows, and keeps an id that is already safe as it is: Firestore's
// own generated ids and the usual slugs never change, so their inGitDB record
// key is the Firestore id.
//
// An unsafe byte is written as "~" and two upper-case hex digits ("~" itself
// too), so the encoding is reversible and two ids never get one name
// (decodeID; TestEncodeIDProperties). inGitDB reads the file name as the record
// key: the key of an encoded id is its encoded name.

// maxNameBytes is the longest file or folder name the output writes.
const maxNameBytes = 255

// safeIDByte reports whether a byte is kept as it is in a file name.
func safeIDByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("-_.@+=,", c) >= 0
}

// windowsDevice reports whether name is a reserved device name on Windows
// (CON, PRN, AUX, NUL, COM0-9, LPT0-9, in any case, with or without an extension).
func windowsDevice(name string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && '0' <= base[3] && base[3] <= '9'
}

// encodeID is the file name part of a document id (see above).
func encodeID(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		edge := (i == 0 || i == len(id)-1) && c == '.' // no hidden file, no trailing dot
		if !safeIDByte(c) || edge || (i == 0 && windowsDevice(id)) {
			fmt.Fprintf(&b, "~%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// decodeID is the document id of a file name part written by encodeID; ok is
// false for a name encodeID never writes.
func decodeID(name string) (id string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] != '~' {
			b.WriteByte(name[i])
			continue
		}
		if i+2 >= len(name) || !isUpperHex(name[i+1]) || !isUpperHex(name[i+2]) {
			return "", false
		}
		b.WriteByte(hexValue(name[i+1])<<4 | hexValue(name[i+2]))
		i += 2
	}
	id = b.String()
	return id, id != "" && encodeID(id) == name
}

func isUpperHex(c byte) bool { return '0' <= c && c <= '9' || 'A' <= c && c <= 'F' }

func hexValue(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'A' + 10
}

// nameTooLong is why a file name cannot be written, or "".
func nameTooLong(name string) string {
	if len(name) > maxNameBytes {
		return fmt.Sprintf("its file name would be %d bytes long; a file name has at most %d", len(name), maxNameBytes)
	}
	return ""
}
