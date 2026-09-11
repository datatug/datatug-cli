package querywrite

import (
	"strings"
	"testing"
)

func TestLocationMessage(t *testing.T) {
	tests := []struct {
		folderPath, id, reason string
		want                   string
	}{
		{"locked/sub", "", `folder path segment "sub": lstat /private/tmp/x/001/queries/locked/sub: permission denied`, `folder path segment "sub" cannot be accessed`},
		{"linked", "", `folder path segment "linked": resolves through a symlink`, `folder path segment "linked": resolves through a symlink`},
		{"a/b", "", `folder path segment "b": a path component exists and is not a directory`, `folder path segment "b": a path component exists and is not a directory`},
		{"", "CON", "id: is a Windows-reserved device name", `query id "CON": is a Windows-reserved device name`},
		{"", "q", "id: open /srv/p/queries/q.query.json: input/output error", `query id "q" cannot be accessed`},
		{"", "q", "queries root: lstat /srv/p/queries: permission denied", "the project's queries folder cannot be accessed"},
		{"", "q", "queries root: resolves through a symlink", "the project's queries folder: resolves through a symlink"},
		{"x", "q", "something /srv/else: failed", `folder path "x" cannot be accessed`},
		{"", "", "anything at /srv", "the query location cannot be accessed"},
		{"", "", `folder path segment "unterminated: x`, "the query location cannot be accessed"},
	}
	for _, tt := range tests {
		got := LocationMessage(tt.folderPath, tt.id, tt.reason)
		if got != tt.want {
			t.Errorf("LocationMessage(%q, %q, %q) = %q, want %q", tt.folderPath, tt.id, tt.reason, got, tt.want)
		}
		for _, leak := range []string{"/srv", "/private", "lstat", "permission denied", "input/output"} {
			if strings.Contains(got, leak) {
				t.Errorf("LocationMessage(%q) = %q leaks %q", tt.reason, got, leak)
			}
		}
	}
}
