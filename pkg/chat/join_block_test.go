package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/grid"
)

func testJoinCandidates() []JoinCandidate {
	return []JoinCandidate{
		{
			ID:          "invoice->customer",
			Source:      RelationInstance{ID: "invoice", Relation: "Invoice"},
			Target:      RelationInstance{ID: "customer", Relation: "Customer"},
			Cardinality: "many-to-one",
			Fields:      []JoinFieldPair{{SourceField: "CustomerId", TargetField: "CustomerId"}},
		},
		{
			ID:          "invoice->employee",
			Source:      RelationInstance{ID: "invoice", Relation: "Invoice"},
			Target:      RelationInstance{ID: "employee", Relation: "Employee"},
			Cardinality: "many-to-one",
			Fields:      []JoinFieldPair{{SourceField: "EmployeeId", TargetField: "EmployeeId"}},
		},
	}
}

func newTestJoinBlock() *JoinBlock {
	g := grid.New([]grid.Column{{Name: "InvoiceId"}}, []grid.Row{{Values: []any{1}}})
	return &JoinBlock{Grid: g, RecordSetID: "rs1", Candidates: testJoinCandidates()}
}

// joinTestKeys maps the letter keys this test needs to a plain-rune
// tea.KeyPressMsg, matching the {Code: 'x', Text: "x"} pattern the rest of
// pkg/chat's tests use (see e.g. export_test.go, http_command_test.go).
var joinTestKeys = map[string]tea.KeyPressMsg{
	"j":     {Code: 'j', Text: "j"},
	"l":     {Code: 'l', Text: "l"},
	"h":     {Code: 'h', Text: "h"},
	"k":     {Code: 'k', Text: "k"},
	"s":     {Code: 's', Text: "s"},
	"space": {Code: tea.KeySpace},
	"tab":   {Code: tea.KeyTab},
	"esc":   {Code: tea.KeyEscape},
	"enter": {Code: tea.KeyEnter},
}

func pressJoinKey(t *testing.T, b *JoinBlock, key string) tea.Cmd {
	t.Helper()
	msg, ok := joinTestKeys[key]
	if !ok {
		t.Fatalf("no test key mapping for %q", key)
	}
	updated, cmd := b.Update(msg)
	if got, ok := updated.(*JoinBlock); !ok || got != b {
		t.Fatalf("Update returned a different Block value: %#v", updated)
	}
	return cmd
}

func TestJoinBlockFocusable(t *testing.T) {
	b := newTestJoinBlock()
	if !b.Focusable() {
		t.Fatal("JoinBlock must be focusable")
	}
}

func TestJoinBlockViewShowsPressJHint(t *testing.T) {
	b := newTestJoinBlock()
	view := b.View(80, true)
	if !strings.Contains(view, "press j") {
		t.Fatalf("expected join hint in view, got:\n%s", view)
	}
	if !strings.Contains(view, "Customer") || !strings.Contains(view, "Employee") {
		t.Fatalf("expected both candidate targets rendered, got:\n%s", view)
	}
}

func TestJoinBlockJTogglesFocusAndDefocusesGrid(t *testing.T) {
	b := newTestJoinBlock()
	b.Grid.SetFocused(true)
	pressJoinKey(t, b, "j")
	if !b.joinFocused {
		t.Fatal("expected j to focus the join selector")
	}
	if b.Grid.Focused() {
		t.Fatal("expected the grid to lose focus while the join selector has it")
	}
	if !b.CapturesEsc() {
		t.Fatal("CapturesEsc must be true while join-focused")
	}
}

func TestJoinBlockNavigatesCandidatesWithArrows(t *testing.T) {
	b := newTestJoinBlock()
	pressJoinKey(t, b, "j")
	if b.candidateIndex != 0 {
		t.Fatalf("expected candidateIndex 0, got %d", b.candidateIndex)
	}
	pressJoinKey(t, b, "l")
	if b.candidateIndex != 1 {
		t.Fatalf("expected right/l to advance candidateIndex to 1, got %d", b.candidateIndex)
	}
	pressJoinKey(t, b, "l")
	if b.candidateIndex != 1 {
		t.Fatalf("expected candidateIndex to clamp at the last candidate, got %d", b.candidateIndex)
	}
	pressJoinKey(t, b, "h")
	if b.candidateIndex != 0 {
		t.Fatalf("expected left/h to move back to candidateIndex 0, got %d", b.candidateIndex)
	}
}

func TestJoinBlockEnterTogglesDetails(t *testing.T) {
	b := newTestJoinBlock()
	pressJoinKey(t, b, "j")
	updated, _ := b.Update(joinTestKeys["enter"])
	b = updated.(*JoinBlock)
	if !b.showDetails {
		t.Fatal("expected enter to toggle showDetails on")
	}
	view := b.View(80, true)
	if !strings.Contains(view, "Foreign key") || !strings.Contains(view, "CustomerId") {
		t.Fatalf("expected FK detail lines in view, got:\n%s", view)
	}
}

func TestJoinBlockSpaceAppliesSelectedCandidate(t *testing.T) {
	b := newTestJoinBlock()
	var gotRecordSetID string
	var gotCandidateID JoinCandidateID
	applied := false
	b.Apply = func(recordSetID string, candidateID JoinCandidateID) tea.Cmd {
		gotRecordSetID, gotCandidateID = recordSetID, candidateID
		applied = true
		return func() tea.Msg { return JoinAppliedMsg{RecordSetID: recordSetID, CandidateID: candidateID} }
	}
	pressJoinKey(t, b, "j")
	cmd := pressJoinKey(t, b, "space")
	if !applied {
		t.Fatal("expected space to call Apply")
	}
	if gotRecordSetID != "rs1" || gotCandidateID != "invoice->customer" {
		t.Fatalf("unexpected Apply args: %s %s", gotRecordSetID, gotCandidateID)
	}
	if cmd == nil {
		t.Fatal("expected Update to return Apply's tea.Cmd")
	}
	msg := cmd()
	applied2, ok := msg.(JoinAppliedMsg)
	if !ok || applied2.CandidateID != "invoice->customer" {
		t.Fatalf("unexpected message from Apply's cmd: %#v", msg)
	}
}

func TestJoinBlockTabAndEscReturnFocusToGrid(t *testing.T) {
	b := newTestJoinBlock()
	pressJoinKey(t, b, "j")
	updated, _ := b.Update(joinTestKeys["tab"])
	b = updated.(*JoinBlock)
	if b.joinFocused {
		t.Fatal("expected tab to leave join focus")
	}
	if !b.Grid.Focused() {
		t.Fatal("expected tab to restore grid focus")
	}

	b2 := newTestJoinBlock()
	pressJoinKey(t, b2, "j")
	updated2, _ := b2.Update(joinTestKeys["esc"])
	b2 = updated2.(*JoinBlock)
	if b2.joinFocused || !b2.Grid.Focused() {
		t.Fatal("expected esc to leave join focus and restore grid focus")
	}
}

func TestJoinBlockDelegatesKeysToGridOutsideJoinMode(t *testing.T) {
	b := newTestJoinBlock()
	// "s" sorts the grid (tui/grid native binding) and is not a join key, so
	// it must reach the wrapped grid untouched.
	_, cmd := b.Update(joinTestKeys["s"])
	_ = cmd
	if b.joinFocused {
		t.Fatal("plain grid keys must not enter join mode")
	}
}

func TestJoinBlockNoCandidatesNeverEntersJoinMode(t *testing.T) {
	g := grid.New([]grid.Column{{Name: "InvoiceId"}}, []grid.Row{{Values: []any{1}}})
	b := &JoinBlock{Grid: g, RecordSetID: "rs1"}
	pressJoinKey(t, b, "j")
	if b.joinFocused {
		t.Fatal("expected j to be a no-op with no candidates")
	}
	if view := b.View(80, true); strings.Contains(view, "press j") {
		t.Fatalf("expected no join hint with zero candidates, got:\n%s", view)
	}
}
