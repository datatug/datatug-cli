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

// multiGroupJoinCandidates builds two source groups (Invoice, Order) so
// up/down can actually move between groups, an aliased source (Invoice
// aliased "i"), a target relation reached by two different FK paths
// (Customer, via BillingCustomerId and ShippingCustomerId -- the "counts>1"
// disambiguation label), and a one-to-many candidate (the "→*" label).
func multiGroupJoinCandidates() []JoinCandidate {
	return []JoinCandidate{
		{
			ID:          "invoice->customer-billing",
			Source:      RelationInstance{ID: "invoice", Relation: "Invoice", Alias: "i"},
			Target:      RelationInstance{ID: "customer", Relation: "Customer"},
			Cardinality: "many-to-one",
			Fields:      []JoinFieldPair{{SourceField: "BillingCustomerId", TargetField: "CustomerId"}},
		},
		{
			ID:          "invoice->customer-shipping",
			Source:      RelationInstance{ID: "invoice", Relation: "Invoice", Alias: "i"},
			Target:      RelationInstance{ID: "customer", Relation: "Customer"},
			Cardinality: "many-to-one",
			Fields:      []JoinFieldPair{{SourceField: "ShippingCustomerId", TargetField: "CustomerId"}},
		},
		{
			ID:          "order->lineitem",
			Source:      RelationInstance{ID: "order", Relation: "Order"},
			Target:      RelationInstance{ID: "lineitem", Relation: "LineItem"},
			Cardinality: "one-to-many",
			Fields:      []JoinFieldPair{{SourceField: "OrderId", TargetField: "OrderId"}},
		},
	}
}

// TestJoinBlockJoinAreaViewRendersAliasDisambiguationAndCardinality covers
// joinAreaView's alias-prefix branch, the same-target-relation
// disambiguation label (two candidates both named "Customer"), and the
// one-to-many "→*" label -- testJoinCandidates' single group with distinct
// target names never exercises any of them.
func TestJoinBlockJoinAreaViewRendersAliasDisambiguationAndCardinality(t *testing.T) {
	g := grid.New([]grid.Column{{Name: "InvoiceId"}}, []grid.Row{{Values: []any{1}}})
	b := &JoinBlock{Grid: g, RecordSetID: "rs1", Candidates: multiGroupJoinCandidates()}
	view := b.View(120, true)
	if !strings.Contains(view, "i (Invoice)") {
		t.Fatalf("expected the alias-prefixed source label, got:\n%s", view)
	}
	if !strings.Contains(view, "Customer (BillingCustomerId)") || !strings.Contains(view, "Customer (ShippingCustomerId)") {
		t.Fatalf("expected both same-target candidates disambiguated by field, got:\n%s", view)
	}
	if !strings.Contains(view, "→*") {
		t.Fatalf("expected the one-to-many cardinality label, got:\n%s", view)
	}
}

// TestJoinBlockUpDownAndLeftRightNavigateGroupsAndCandidates covers
// Update's up/down/left/right branches that actually move -- with only one
// source group and two candidates, testJoinCandidates' own tests only
// exercise a plain left/right pair, not the multi-group up/down movement or
// either direction's boundary no-op.
func TestJoinBlockUpDownAndLeftRightNavigateGroupsAndCandidates(t *testing.T) {
	g := grid.New([]grid.Column{{Name: "InvoiceId"}}, []grid.Row{{Values: []any{1}}})
	b := &JoinBlock{Grid: g, RecordSetID: "rs1", Candidates: multiGroupJoinCandidates()}
	updated, _ := b.Update(joinTestKeys["j"])
	b = updated.(*JoinBlock)

	// Right moves to the second candidate within the first group (Customer
	// via ShippingCustomerId).
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	b = updated.(*JoinBlock)
	if b.candidateIndex != 1 {
		t.Fatalf("candidateIndex after right = %d, want 1", b.candidateIndex)
	}
	// Right again at the last candidate in the group is a no-op.
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	b = updated.(*JoinBlock)
	if b.candidateIndex != 1 {
		t.Fatalf("candidateIndex after right at the boundary = %d, want unchanged 1", b.candidateIndex)
	}

	// Down moves to the second source group (Order) and resets candidateIndex.
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	b = updated.(*JoinBlock)
	if b.sourceIndex != 1 || b.candidateIndex != 0 {
		t.Fatalf("after down: sourceIndex=%d candidateIndex=%d, want 1/0", b.sourceIndex, b.candidateIndex)
	}
	// Down again at the last group is a no-op.
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	b = updated.(*JoinBlock)
	if b.sourceIndex != 1 {
		t.Fatalf("sourceIndex after down at the boundary = %d, want unchanged 1", b.sourceIndex)
	}

	// Left is a no-op at candidateIndex 0.
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	b = updated.(*JoinBlock)
	if b.candidateIndex != 0 {
		t.Fatalf("candidateIndex after left at the boundary = %d, want unchanged 0", b.candidateIndex)
	}

	// Up returns to the first source group.
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	b = updated.(*JoinBlock)
	if b.sourceIndex != 0 {
		t.Fatalf("sourceIndex after up = %d, want 0", b.sourceIndex)
	}
	// Up again at the first group is a no-op.
	updated, _ = b.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	b = updated.(*JoinBlock)
	if b.sourceIndex != 0 {
		t.Fatalf("sourceIndex after up at the boundary = %d, want unchanged 0", b.sourceIndex)
	}
}

// TestJoinBlockSelectedJoinOutOfRangeIndices covers selectedJoin's own
// bounds guards directly.
func TestJoinBlockSelectedJoinOutOfRangeIndices(t *testing.T) {
	b := newTestJoinBlock()
	b.sourceIndex = 99
	if _, ok := b.selectedJoin(); ok {
		t.Fatal("expected selectedJoin to fail for an out-of-range sourceIndex")
	}
	b.sourceIndex = 0
	b.candidateIndex = 99
	if _, ok := b.selectedJoin(); ok {
		t.Fatal("expected selectedJoin to fail for an out-of-range candidateIndex")
	}
}

// TestJoinBlockCurrentAndViewWithoutGrid cover Current() and View()'s own
// Grid==nil branches.
func TestJoinBlockCurrentAndViewWithoutGrid(t *testing.T) {
	b := &JoinBlock{RecordSetID: "rs1", Candidates: testJoinCandidates()}
	if ref := b.Current(); ref != nil {
		t.Fatalf("Current() with no Grid = %v, want nil", ref)
	}
	view := b.View(80, true)
	if !strings.Contains(view, "press j") {
		t.Fatalf("expected the join area alone (no grid) in the view:\n%s", view)
	}
}

// TestJoinBlockJoinAreaViewTruncatesOverwideHighlightedLine covers
// joinAreaView's "highlighted line still too wide" fallback branch: a
// narrow width forces it past the plain lipgloss.Width(highlighted)<=width
// case into the ansi.Truncate one.
func TestJoinBlockJoinAreaViewTruncatesOverwideHighlightedLine(t *testing.T) {
	g := grid.New([]grid.Column{{Name: "InvoiceId"}}, []grid.Row{{Values: []any{1}}})
	b := &JoinBlock{Grid: g, RecordSetID: "rs1", Candidates: multiGroupJoinCandidates()}
	updated, _ := b.Update(joinTestKeys["j"])
	b = updated.(*JoinBlock)
	view := b.View(60, true)
	if !strings.Contains(view, "(1/2)") {
		t.Fatalf("expected the truncated (index/total) fallback for a narrow width:\n%s", view)
	}
}

// TestJoinBlockUpdateWithJoinFocusedButNoCandidates covers Update's own
// len(groups)==0-while-joinFocused guard (unreachable through the normal
// "j" toggle, which only enters join mode when candidates exist -- this can
// only happen if a caller sets joinFocused directly, or Candidates changes
// out from under an already-focused block).
func TestJoinBlockUpdateWithJoinFocusedButNoCandidates(t *testing.T) {
	g := grid.New([]grid.Column{{Name: "InvoiceId"}}, []grid.Row{{Values: []any{1}}})
	b := &JoinBlock{Grid: g, RecordSetID: "rs1", joinFocused: true}
	updated, cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	b = updated.(*JoinBlock)
	if b.joinFocused || cmd != nil {
		t.Fatalf("expected join focus to clear with no candidates: focused=%v cmd=%v", b.joinFocused, cmd)
	}
}

// TestJoinBlockSpaceWithNilApplyIsNoop covers Update's "space" branch's own
// Apply==nil guard.
func TestJoinBlockSpaceWithNilApplyIsNoop(t *testing.T) {
	b := newTestJoinBlock() // Apply is left nil
	updated, _ := b.Update(joinTestKeys["j"])
	b = updated.(*JoinBlock)
	_, cmd := b.Update(joinTestKeys["space"])
	if cmd != nil {
		t.Fatalf("expected space with a nil Apply to be a no-op, got cmd=%v", cmd)
	}
}

// TestJoinBlockUpdateDelegatesToNilGridOutsideJoinMode covers Update's
// trailing Grid==nil guard, on the delegate-to-grid path (not the earlier
// Current()/View() nil-Grid guards).
func TestJoinBlockUpdateDelegatesToNilGridOutsideJoinMode(t *testing.T) {
	b := &JoinBlock{RecordSetID: "rs1", Candidates: testJoinCandidates()}
	updated, cmd := b.Update(joinTestKeys["s"])
	if got, ok := updated.(*JoinBlock); !ok || got != b || cmd != nil {
		t.Fatalf("expected a no-op with no Grid: updated=%#v cmd=%v", updated, cmd)
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
