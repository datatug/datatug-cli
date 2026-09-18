package dtproject

import (
	"strings"
	"testing"
)

// TestSuggestProjectID pins the create screen's title -> id suggestion.
//
// The suggestion is the only id logic this package owns; whether a
// suggestion is acceptable is decided by datatug-core, which is why the
// "no usable suggestion" cases below are not spelled out as rules here but
// simply come back empty — the screen then leaves the id field for the user
// to fill rather than blocking on the title.
func TestSuggestProjectID(t *testing.T) {
	for _, tt := range []struct {
		name  string
		title string
		want  string
	}{
		{name: "the founder's example", title: "My First Project", want: "my-first-project"},
		{name: "upper case is folded, never kept", title: "UPPER Case", want: "upper-case"},
		{name: "surrounding whitespace is trimmed", title: "  Hello  World  ", want: "hello-world"},
		{name: "a run of separators collapses to one", title: "A -- B", want: "a-b"},
		{name: "underscores are separators too", title: "Sales_2026", want: "sales-2026"},
		{name: "digits may lead", title: "2026 Sales", want: "2026-sales"},
		{name: "a single character is enough", title: "A", want: "a"},
		{name: "accents are dropped, not transliterated", title: "Café", want: "caf"},
		{name: "an empty title suggests nothing", title: "", want: ""},
		{name: "a non-Latin title suggests nothing", title: "Проект", want: ""},
		{name: "a title of only separators suggests nothing", title: " --- ", want: ""},
		{
			name:  "the longest id core accepts is suggested whole",
			title: strings.Repeat("a", 64),
			want:  strings.Repeat("a", 64),
		},
		{
			name:  "one character longer is refused by core, so nothing is suggested",
			title: strings.Repeat("a", 65),
			want:  "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := suggestProjectID(tt.title); got != tt.want {
				t.Errorf("suggestProjectID(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

// TestSuggestProjectIDIsAlwaysAcceptable is the property that matters more
// than any single case above: whatever the screen prefills, the user can
// press Create on it unchanged.
func TestSuggestProjectIDIsAlwaysAcceptable(t *testing.T) {
	for _, title := range []string{
		"My First Project",
		"UPPER Case",
		"  Hello  World  ",
		"Sales_2026",
		"2026 Sales",
		"Café",
		"-leading and trailing-",
		"a/b/../c",
		strings.Repeat("a", 64),
	} {
		suggestion := suggestProjectID(title)
		if suggestion == "" {
			t.Errorf("suggestProjectID(%q) = %q, want a usable suggestion", title, suggestion)
			continue
		}
		if err := validateNewProject(suggestion, title); err != nil {
			t.Errorf("validateNewProject(suggestProjectID(%q)=%q, %q) = %v, want nil", title, suggestion, title, err)
		}
	}
}

// TestValidateNewProject checks that the screen really is asking
// datatug-core, rather than answering for it: every case here is a rule the
// screen does not implement.
func TestValidateNewProject(t *testing.T) {
	for _, tt := range []struct {
		name      string
		projectID string
		title     string
		wantErr   bool
	}{
		{name: "id and title", projectID: "my-first-project", title: "My First Project"},
		{name: "underscores and digits are allowed inside", projectID: "a_1-b", title: "T"},
		{name: "a missing id is refused", projectID: "", title: "My First Project", wantErr: true},
		{name: "a missing title is refused", projectID: "my-first-project", title: "", wantErr: true},
		{name: "upper case is refused, not folded", projectID: "My-Project", title: "T", wantErr: true},
		{name: "spaces are refused", projectID: "my project", title: "T", wantErr: true},
		{name: "a path separator is refused", projectID: "a/b", title: "T", wantErr: true},
		{name: "a leading separator is refused", projectID: "-abc", title: "T", wantErr: true},
		{name: "a trailing separator is refused", projectID: "abc-", title: "T", wantErr: true},
		{name: "over 64 characters is refused", projectID: strings.Repeat("a", 65), title: "T", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNewProject(tt.projectID, tt.title)
			if tt.wantErr && err == nil {
				t.Errorf("validateNewProject(%q, %q) = nil, want an error", tt.projectID, tt.title)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateNewProject(%q, %q) = %v, want nil", tt.projectID, tt.title, err)
			}
		})
	}
}
