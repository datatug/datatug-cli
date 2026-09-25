package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/ai/session"
	"github.com/strongo/aichat/tui/grid"
	"github.com/strongo/aichat/tui/theme"
	"github.com/strongo/aichat/tui/transcript"
)

// JoinBlock is a transcript.Block that pairs a query-result grid with
// DataTug's inline FK-join candidate selector (the old ui.go's "press j"
// join mode). It ports historyEntry.joinGroups/selectedJoin, joinAreaView
// and the u.joinFocused branch of updateGrid (pkg/chat/ui.go) onto the
// tui/transcript.Block/EntityBlock contract so it can sit directly in a
// tui/chatshell transcript once the shell cutover lands — see checklist
// item #3 in the Lane C acceptance list.
//
// Applying a candidate (space) runs Apply, whose result — success or
// error — is delivered as a JoinAppliedMsg the way every other chatshell
// message is: through the transcript/Handler message flow, not through a
// direct callback, so JoinBlock stays testable without a live session.
type JoinBlock struct {
	Grid        *grid.Model
	RecordSetID string
	Candidates  []JoinCandidate
	Apply       func(recordSetID string, candidateID JoinCandidateID) tea.Cmd

	joinFocused    bool
	sourceIndex    int
	candidateIndex int
	showDetails    bool
}

// JoinAppliedMsg reports the outcome of applying a join candidate started by
// JoinBlock.Update on "space" — the ported joinMessage from ui.go.
type JoinAppliedMsg struct {
	RecordSetID string
	CandidateID JoinCandidateID
	Err         error
}

// joinGroup is one FK source relation with its candidate target relations —
// ported unchanged from ui.go's joinGroup/historyEntry.joinGroups.
type blockJoinGroup struct {
	source     RelationInstance
	candidates []JoinCandidate
}

func (b *JoinBlock) joinGroups() []blockJoinGroup {
	var groups []blockJoinGroup
	for _, candidate := range b.Candidates {
		if len(groups) == 0 || groups[len(groups)-1].source.ID != candidate.Source.ID {
			groups = append(groups, blockJoinGroup{source: candidate.Source})
		}
		groups[len(groups)-1].candidates = append(groups[len(groups)-1].candidates, candidate)
	}
	return groups
}

func (b *JoinBlock) selectedJoin() (JoinCandidate, bool) {
	groups := b.joinGroups()
	if b.sourceIndex < 0 || b.sourceIndex >= len(groups) {
		return JoinCandidate{}, false
	}
	group := groups[b.sourceIndex]
	if b.candidateIndex < 0 || b.candidateIndex >= len(group.candidates) {
		return JoinCandidate{}, false
	}
	return group.candidates[b.candidateIndex], true
}

// Focusable satisfies transcript.Block.
func (b *JoinBlock) Focusable() bool { return true }

// Current satisfies transcript.EntityBlock, delegating to the wrapped grid.
func (b *JoinBlock) Current() *session.EntityRef {
	if b.Grid == nil {
		return nil
	}
	return b.Grid.Current()
}

// JoinFocused reports whether "j" has switched this block into its inline
// JOIN candidate selector (ui.go's u.joinFocused) -- statusBar's
// ZoneTranscript branch (r1b item 5a) reads it, via ChatUI's own
// gridsByRecordSetID/activeJoinBlock lookup, to show ui.go's
// "JOIN candidates ↑↓ source ←→ relationship ..." hint set instead of the
// plain grid one while the selector has focus.
func (b *JoinBlock) JoinFocused() bool { return b.joinFocused }

// CapturesEsc satisfies transcript.EscCapturer: while the join selector has
// focus, Esc should return to the grid (handled in Update) rather than
// bubbling to chatshell's "return focus to composer" default — matching
// ui.go's updateGrid comment on g.CapturesEsc().
func (b *JoinBlock) CapturesEsc() bool {
	return b.joinFocused
}

// SelfFramed satisfies transcript.SelfFramed: JoinBlock's View already
// includes the embedded grid's own complete border (title/footer inline,
// right-edge scrollbar) plus the plain-text join selector beneath it, so
// transcript renders it directly instead of wrapping the whole thing in
// the shared theme.Card fill, which would otherwise sit AROUND the
// grid's own border as a second frame (strongo/aichat#chat-shared-look).
func (b *JoinBlock) SelfFramed() bool { return true }

// View satisfies transcript.Block: the grid, followed by the join area when
// there are candidates — ported from ui.go's joinAreaView, appended below
// gridState.view() by the old rebuildHistory.
func (b *JoinBlock) View(width int, focused bool) string {
	gridView := ""
	if b.Grid != nil {
		gridView = b.Grid.View(width, focused && !b.joinFocused)
	}
	joinView := b.joinAreaView(focused, width)
	if joinView == "" {
		return gridView
	}
	if gridView == "" {
		return joinView
	}
	return gridView + "\n" + joinView
}

// joinAreaView renders the "press j" join selector — ported verbatim from
// ui.go's package-level joinAreaView, adapted to JoinBlock's own fields.
func (b *JoinBlock) joinAreaView(focused bool, width int) string {
	groups := b.joinGroups()
	if len(groups) == 0 {
		return ""
	}
	lines := []string{statusStyle().Render("  You can ") + lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor()).Render("J") + statusStyle().Render("OIN  · press j")}
	for sourceIndex, group := range groups {
		source := sanitizeTerminalText(group.source.Relation)
		if group.source.Alias != "" && !strings.EqualFold(group.source.Alias, group.source.Relation) {
			source = sanitizeTerminalText(group.source.Alias) + " (" + source + ")"
		}
		prefix := "  " + source + ": "
		counts := map[string]int{}
		for _, candidate := range group.candidates {
			counts[strings.ToLower(candidate.Target.Relation)]++
		}
		labels := make([]string, len(group.candidates))
		for index, candidate := range group.candidates {
			label := sanitizeTerminalText(candidate.Target.Relation)
			if counts[strings.ToLower(candidate.Target.Relation)] > 1 {
				fields := make([]string, len(candidate.Fields))
				for i, pair := range candidate.Fields {
					fields[i] = sanitizeTerminalText(pair.SourceField)
				}
				label += " (" + strings.Join(fields, ", ") + ")"
			}
			if candidate.Cardinality == "one-to-many" {
				label += " →*"
			} else {
				label += " →1"
			}
			labels[index] = label
		}
		plain := prefix + strings.Join(labels, "  ·  ")
		if focused && b.joinFocused && sourceIndex == b.sourceIndex {
			selected := min(b.candidateIndex, len(labels)-1)
			selectedBG, selectedFG := theme.FocusSurfaceColors()
			selectedLabel := lipgloss.NewStyle().Bold(true).Foreground(selectedFG).Background(selectedBG).Render(labels[selected])
			parts := append([]string(nil), labels...)
			parts[selected] = selectedLabel
			highlighted := activeTitleStyle().Render(prefix) + strings.Join(parts, "  ·  ")
			if lipgloss.Width(highlighted) <= width {
				lines = append(lines, highlighted)
			} else {
				lines = append(lines, ansi.Truncate(activeTitleStyle().Render(prefix)+selectedLabel+statusStyle().Render(fmt.Sprintf("  (%d/%d)", selected+1, len(labels))), width, "…"))
			}
		} else {
			lines = append(lines, statusStyle().Render(ansi.Truncate(plain, width, "…")))
		}
	}
	if focused && b.joinFocused && b.showDetails {
		if candidate, ok := b.selectedJoin(); ok {
			lines = append(lines, statusStyle().Render("  Foreign key • "+candidate.Cardinality))
			for _, pair := range candidate.Fields {
				line := "  " + candidate.Source.Relation + "." + pair.SourceField + " → " + candidate.Target.Relation + "." + pair.TargetField
				lines = append(lines, statusStyle().Render(ansi.Truncate(sanitizeTerminalText(line), width, "…")))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// Update satisfies transcript.Block: "j" toggles the join selector on (when
// there are candidates); while it has focus, up/down/k/j pick the source
// group, left/right/h/l pick the candidate, enter toggles the FK-fields
// detail line, space applies the selected candidate, tab and esc return
// focus to the grid — ported from ui.go's updateGrid u.joinFocused branch.
// Any other key, or no join focus, is delegated to the wrapped grid.
func (b *JoinBlock) Update(msg tea.Msg) (transcript.Block, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if isKey && b.joinFocused {
		groups := b.joinGroups()
		if len(groups) == 0 {
			b.joinFocused = false
			return b, nil
		}
		switch keyMsg.String() {
		case "up", "k":
			if b.sourceIndex > 0 {
				b.sourceIndex--
				b.candidateIndex = 0
			}
		case "down", "j":
			if b.sourceIndex+1 < len(groups) {
				b.sourceIndex++
				b.candidateIndex = 0
			}
		case "left", "h":
			if b.candidateIndex > 0 {
				b.candidateIndex--
			}
		case "right", "l":
			if b.candidateIndex+1 < len(groups[b.sourceIndex].candidates) {
				b.candidateIndex++
			}
		case "enter":
			b.showDetails = !b.showDetails
		case "space":
			candidate, ok := b.selectedJoin()
			if !ok || b.Apply == nil {
				return b, nil
			}
			recordSetID, candidateID := b.RecordSetID, candidate.ID
			applyCmd := b.Apply(recordSetID, candidateID)
			return b, applyCmd
		case "tab", "esc":
			b.joinFocused = false
			if b.Grid != nil {
				b.Grid.SetFocused(true)
			}
		default:
			// consumed — join mode swallows unrecognised keys, as before.
		}
		return b, nil
	}
	if isKey && keyMsg.String() == "j" && len(b.Candidates) > 0 {
		b.joinFocused = true
		if b.Grid != nil {
			b.Grid.SetFocused(false)
		}
		return b, nil
	}
	if b.Grid == nil {
		return b, nil
	}
	gridBlock, cmd := b.Grid.Update(msg)
	if updated, ok := gridBlock.(*grid.Model); ok {
		b.Grid = updated
	}
	return b, cmd
}

var _ transcript.Block = (*JoinBlock)(nil)
var _ transcript.EntityBlock = (*JoinBlock)(nil)
var _ transcript.EscCapturer = (*JoinBlock)(nil)
