package faithful

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNoticeBytes is the size limit of Header.Notice: a licence notice is a few paragraphs, and the whole of it
// travels in every script.
const MaxNoticeBytes = 16 << 10

// ValidateNotice refuses a notice the script header must not carry: one over MaxNoticeBytes, one that is not valid
// UTF-8 and one with a NUL. It is the check of the request; NoticeLines is safe for any text regardless.
func ValidateNotice(s string) error {
	switch {
	case len(s) > MaxNoticeBytes:
		return fmt.Errorf("the notice is %d bytes; the limit is %d", len(s), MaxNoticeBytes)
	case !utf8.ValidString(s):
		return errors.New("the notice is not valid UTF-8")
	case strings.IndexByte(s, 0) >= 0:
		return errors.New("the notice holds a NUL character")
	}
	return nil
}

// isLineEnd reports the characters that end a line in some reader: LF, CR, VT, FF, the information separators
// (Python splits on them), NEL and the Unicode line and paragraph separators.
func isLineEnd(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// isInert reports the characters a comment line must not carry although they are not line ends: the other
// control characters (NUL included), the byte order mark, the bidirectional controls that make a line read other
// than it is written, and what is left of invalid UTF-8.
func isInert(r rune) bool {
	switch {
	case r == utf8.RuneError, unicode.IsControl(r), r == 0xFEFF:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}
	return false
}

// NoticeLines turns a multi-line notice into the comment lines of a script header: every line of the notice is
// one line that starts with prefix (the comment introducer of the dialect, with the space that MySQL needs after
// "--"), and nothing the notice holds can end that line, start another or end the comment. So:
//
//   - every line end of isLineEnd, "\r\n" being one, ends a line; leading and trailing blank lines are dropped, and
//     trailing spaces of a line are cut;
//   - a tab is four spaces and any other control character, the byte order mark, a bidirectional control and an
//     invalid byte is a space;
//   - "/" next to "*" (either order) is parted by a space, so neither "*/" nor "/*" appears: a block comment can
//     neither end nor start in the notice, on an engine where block comments nest or a client scans for them.
//
// neutralise, when not nil, is the dialect's own rule for one line of text (SQL Server breaks "$(", which sqlcmd
// reads even in a comment); it runs after the rules above and may not add a line end. A line the rules empty
// is written as the bare prefix without its trailing space, like the blank lines of the rest of the header.
func NoticeLines(prefix, notice string, neutralise func(string) string) []string {
	var lines []string
	var cur strings.Builder
	var prev rune
	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		prev = 0
	}
	rs := []rune(notice)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case isLineEnd(r):
			if r == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			flush()
			continue
		case r == '\t':
			cur.WriteString("    ")
			prev = ' '
			continue
		case isInert(r):
			r = ' '
		}
		if (prev == '/' && r == '*') || (prev == '*' && r == '/') {
			cur.WriteByte(' ')
		}
		cur.WriteRune(r)
		prev = r
	}
	flush()
	for i, l := range lines {
		l = strings.TrimRight(l, " ")
		if neutralise != nil && l != "" {
			l = strings.TrimRight(neutralise(l), " ")
		}
		lines[i] = l
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(prefix+l, " ")
	}
	return out
}
