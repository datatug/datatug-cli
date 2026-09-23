package chat

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// SavedQuery is the small, presentation-only shape used by the command picker.
// The project store remains authoritative for query definitions and execution.
type SavedQuery struct {
	ID         string
	Title      string
	Type       string
	Tags       []string
	Parameters []SavedQueryParameter
}

type SavedQueryParameter struct {
	ID           string
	Title        string
	Type         string
	Required     bool
	Multi        bool
	DefaultValue string
	Entity       string
	Field        string
}

// SavedQueryLookupService resolves a parameter against the saved query's own
// source and returns policy-filtered rows. A nil result means no scalar FK.
type SavedQueryLookupService interface {
	LookupParameter(context.Context, string, string) (*SavedQueryLookup, error)
}

type SavedQueryLookup struct {
	Key    string
	Multi  bool
	Result secureread.Result
}

type SavedQueryService interface {
	List(context.Context) ([]SavedQuery, error)
	Run(context.Context, string) (QueryResult, error)
}

type SavedQueryParameterizedRunner interface {
	RunWithVariables(context.Context, string, map[string]string) (QueryResult, error)
}

type SavedQuerySaveRequest struct {
	Title    string
	Tags     []string
	Type     string
	Text     string
	Database string
}

type SavedQueryWriter interface {
	Save(context.Context, SavedQuerySaveRequest) (SavedQuery, error)
}

type savedQuerySaveMessage struct {
	query SavedQuery
	err   error
	draft *saveQueryDialog
}

type savedQueryMessage struct {
	sessionID string
	snapshot  ChatSession
	err       error
}

func (u *UI) SetSavedQueryService(service SavedQueryService) error {
	u.savedQueryService = service
	return u.reloadSavedQueries()
}

func (u *UI) reloadSavedQueries() error {
	if u.savedQueryService == nil {
		u.savedQueries = nil
		return nil
	}
	queries, err := u.savedQueryService.List(u.ctx)
	if err != nil {
		return err
	}
	u.savedQueries = queries
	return nil
}

func savedQuerySearch(input string) (string, bool) {
	for _, command := range []string{"/query", "/queries"} {
		if input == command || strings.HasPrefix(input, command+" ") {
			return strings.TrimSpace(strings.TrimPrefix(input, command)), true
		}
	}
	return "", false
}

func (u *UI) savedQueryMatches() []SavedQuery {
	if u.busy || u.gridFocused || u.messageFocused || u.workspaceFocused || !u.input.Focused() || u.commandMenuDismissed == u.input.Value() {
		return nil
	}
	search, ok := savedQuerySearch(u.input.Value())
	if !ok {
		return nil
	}
	search = strings.ToLower(search)
	matches := make([]SavedQuery, 0, len(u.savedQueries))
	for _, query := range u.savedQueries {
		text := strings.ToLower(query.Title + " " + query.ID + " " + query.Type + " " + strings.Join(query.Tags, " "))
		if strings.Contains(text, search) {
			matches = append(matches, query)
		}
	}
	return matches
}

func (u *UI) savedQueryMenuHeight() int {
	if _, ok := savedQuerySearch(u.input.Value()); ok && u.input.Focused() && !u.busy && !u.gridFocused && !u.messageFocused && !u.workspaceFocused && u.commandMenuDismissed != u.input.Value() {
		return 1 + min(6, max(1, len(u.savedQueryMatches())))
	}
	return 0
}

func (u *UI) savedQueryMenuView(width int) string {
	if u.savedQueryMenuHeight() == 0 {
		return ""
	}
	width = max(1, width)
	matches := u.savedQueryMatches()
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235"))
	base := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("235"))
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Background(lipgloss.Color("238"))
	lines := []string{muted.Width(width).Render(" Saved project queries · ↑↓ choose · Enter run · Esc close")}
	if len(matches) == 0 {
		return strings.Join(append(lines, muted.Width(width).Render(" No matching queries")), "\n")
	}
	visible := min(6, len(matches))
	selected := min(u.commandMenuIndex, len(matches)-1)
	start := max(0, selected-visible+1)
	for i := start; i < start+visible; i++ {
		query := matches[i]
		style, prefix := base, "  "
		if i == selected {
			style, prefix = active, "› "
		}
		label := fmt.Sprintf("%s%s  [%s]  %s", prefix, sanitizeTerminalText(nonempty(query.Title, query.ID)), sanitizeTerminalText(query.Type), sanitizeTerminalText(query.ID))
		lines = append(lines, style.Width(width).Render(ansi.Truncate(label, width, "…")))
	}
	return strings.Join(lines, "\n")
}

func (u *UI) selectSavedQuery(query SavedQuery) tea.Cmd {
	if len(query.Parameters) > 0 {
		u.openQueryParametersDialog(query)
		return nil
	}
	return u.runSavedQuery(query, nil)
}

func (u *UI) runSavedQuery(query SavedQuery, variables map[string]string) tea.Cmd {
	if u.savedQueryService == nil || u.sessions == nil || u.sessions.store == nil {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Saved project queries are unavailable."})
		return nil
	}
	label := nonempty(query.Title, query.ID)
	message := "Run query: " + label
	store, service, ctx, sessionID := u.sessions.store, u.savedQueryService, u.ctx, u.sessionID
	u.busy = true
	u.entries = append(u.entries, historyEntry{role: "You", text: message}, historyEntry{role: "DataTug", text: "Running…"})
	return func() tea.Msg {
		origin, err := store.AppendUser(ctx, sessionID, message)
		if err != nil {
			return savedQueryMessage{sessionID: sessionID, err: err}
		}
		var result QueryResult
		var runErr error
		if len(variables) > 0 {
			if runner, ok := service.(SavedQueryParameterizedRunner); ok {
				result, runErr = runner.RunWithVariables(ctx, query.ID, variables)
			} else {
				runErr = fmt.Errorf("this query runner does not accept parameters")
			}
		} else {
			result, runErr = service.Run(ctx, query.ID)
		}
		if runErr != nil {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, "", Turn{Text: "Query failed. Check its parameters and data source."})
		} else {
			if result.Title == "" {
				result.Title = label
			}
			_, err = store.AppendQuery(ctx, sessionID, origin.ID, result.Source, result)
		}
		if err != nil {
			return savedQueryMessage{sessionID: sessionID, err: err}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return savedQueryMessage{sessionID: sessionID, snapshot: snapshot, err: err}
	}
}
