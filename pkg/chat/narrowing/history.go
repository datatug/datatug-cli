package narrowing

import "context"

// History is what the session already knows when a follow-up arrives. A
// question such as "and by genre?" is meaningless alone: it is scored together
// with the questions before it, and the tables the previous turn kept are
// carried over (see Narrow).
type History struct {
	// Questions are the user's earlier questions in this session, oldest first.
	// Only the last few are used.
	Questions []string
	// Kept are the tables the previous narrowed turn put in the model's context.
	// Empty when the previous turn did not narrow.
	Kept []string
}

type historyKey struct{}

// WithHistory returns ctx carrying the session's history for Narrow.
func WithHistory(ctx context.Context, h History) context.Context {
	return context.WithValue(ctx, historyKey{}, h)
}

func historyFrom(ctx context.Context) History {
	h, _ := ctx.Value(historyKey{}).(History)
	return h
}
