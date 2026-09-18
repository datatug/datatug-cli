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

// TestNewProjectIDFollowsTitleUntilEdited walks the create form's
// Title/ID coupling keystroke by keystroke, which is the whole of the
// founder's ruling on who owns the id.
func TestNewProjectIDFollowsTitleUntilEdited(t *testing.T) {
	t.Run("the suggestion tracks the title while nobody has claimed it", func(t *testing.T) {
		var id newProjectID
		for _, step := range []struct {
			title string
			want  string
		}{
			{title: "M", want: "m"},
			{title: "My", want: "my"},
			{title: "My ", want: "my"},
			{title: "My F", want: "my-f"},
			{title: "My First Project", want: "my-first-project"},
		} {
			id.titleChanged(step.title)
			if id.value != step.want {
				t.Errorf("after title %q: id = %q, want %q", step.title, id.value, step.want)
			}
			if id.userOwns {
				t.Errorf("after title %q: the title still owns the id, nothing was typed into it", step.title)
			}
		}
	})

	t.Run("titleChanged reports whether the widget needs rewriting", func(t *testing.T) {
		var id newProjectID
		if !id.titleChanged("My Project") {
			t.Error("the first suggestion is a change, want true")
		}
		// A trailing space suggests the same id: rewriting the widget here
		// would move the user's cursor for nothing.
		if id.titleChanged("My Project ") {
			t.Error("an unchanged suggestion reported a change, want false")
		}
	})

	t.Run("a non-empty edit takes the id from the title for good", func(t *testing.T) {
		var id newProjectID
		id.titleChanged("My First Project")
		id.edited("mine")
		if !id.userOwns {
			t.Error("a typed id is the user's, want userOwns")
		}
		if id.titleChanged("A Completely Different Title") {
			t.Error("the title overwrote an id the user had typed")
		}
		if id.value != "mine" {
			t.Errorf("id = %q, want it left alone as %q", id.value, "mine")
		}
	})

	t.Run("clearing the field hands the id back to the title", func(t *testing.T) {
		var id newProjectID
		id.titleChanged("My First Project")
		id.edited("mine")
		// Wiping the field is the absence of a choice, not a choice.
		id.edited("")
		if id.userOwns {
			t.Error("an emptied id field still counted as the user's choice")
		}
		if id.value != "" {
			t.Errorf("id = %q, want %q until the title suggests again", id.value, "")
		}
		if !id.titleChanged("Second Attempt") {
			t.Fatal("the title did not resume suggesting after the id was cleared")
		}
		if id.value != "second-attempt" {
			t.Errorf("id = %q, want the title's suggestion %q back", id.value, "second-attempt")
		}
	})

	t.Run("the latch re-engages on the next non-empty edit", func(t *testing.T) {
		var id newProjectID
		id.titleChanged("First")
		id.edited("mine")
		id.edited("")
		id.edited("m")
		if !id.userOwns {
			t.Error("typing after a wipe did not take the id back, want userOwns")
		}
		if id.titleChanged("Another Title") {
			t.Error("the title overwrote the id the user typed after the wipe")
		}
		if id.value != "m" {
			t.Errorf("id = %q, want %q", id.value, "m")
		}
	})

	t.Run("a title that suggests nothing leaves the field for the user", func(t *testing.T) {
		var id newProjectID
		id.titleChanged("Проект")
		if id.value != "" {
			t.Errorf("id = %q, want %q: a non-Latin title must not block the form", id.value, "")
		}
		if id.userOwns {
			t.Error("an empty suggestion counted as a user edit")
		}
		// The user types their own, and it sticks.
		id.edited("my-project")
		if id.titleChanged("Проект 2") {
			t.Error("the title overwrote an id the user typed for an untranslatable title")
		}
		if id.value != "my-project" {
			t.Errorf("id = %q, want %q", id.value, "my-project")
		}
	})
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
