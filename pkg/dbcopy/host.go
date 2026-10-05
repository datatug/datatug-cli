package dbcopy

import "regexp"

// MaxHostLength is the most characters a host name has: the limit of a name in the DNS.
const MaxHostLength = 253

// recordableHostName is the characters of a host name or an address (an IPv6 one included), with none
// of a separator, a space, a percent sign, a semicolon or an equals sign, starting with a
// letter, a digit or a colon.
var recordableHostName = regexp.MustCompile(`^[A-Za-z0-9:][A-Za-z0-9._:-]{0,252}$`)

// IsRecordableHost reports whether host is a host name or an address that a project can
// record as the host of a db server: letters, digits, ".", "_", ":" and "-", starting with a
// letter, a digit or ":", at most MaxHostLength characters. It is the one rule for a host
// that is a part of a file name (the ID of a db server) and of a connection string (the
// server of a SQL Server connection), so a host that passes it is neither: no separator of a
// path, no space, no "%", and no character that separates the keys of a connection string.
func IsRecordableHost(host string) bool {
	return recordableHostName.MatchString(host)
}
