package chat

import (
	"context"
	"fmt"
	"time"
)

// operation IDs fence late completions, including backends that return after
// cancellation. Cancel never takes the session mutex or rebuilds the workspace.
type workspaceOperation struct {
	id        uint64
	label     string
	started   time.Time
	cancel    context.CancelFunc
	shellBusy bool
	origin    workspaceContextStamp
}

func (u *ChatUI) beginOperation(label string) (uint64, context.Context) {
	u.cancelOperation(false)
	u.operationSequence++
	ctx, cancel := context.WithTimeout(u.ctx, 30*time.Second)
	stop := context.AfterFunc(u.workspaceContext, cancel)
	u.operation = &workspaceOperation{id: u.operationSequence, label: label, started: time.Now(), cancel: func() { stop(); cancel() }, origin: u.contextStamp()}
	if u.backends == nil {
		u.backends = map[uint64]*workspaceOperation{}
	}
	u.backends[u.operation.id] = u.operation
	return u.operation.id, ctx
}
func (u *ChatUI) cancelOperation(feedback bool) {
	if u.operation == nil {
		return
	}
	label := u.operation.label
	u.operation.cancel()
	if u.operation.shellBusy {
		u.shell.SetBusy(false)
	}
	if u.workspace != nil && u.workspace.relatedOperationID == u.operation.id {
		u.workspace.relatedLoading = false
	}
	u.operation = nil
	u.workspaceMutationPending = false
	if feedback {
		u.workspaceFeedback = label + " canceled; current workspace retained."
	}
}
func (u *ChatUI) finishOperation(id uint64) bool {
	delete(u.backends, id)
	if id == 0 {
		return true
	} // messages from existing non-tracked integrations
	if u.operation == nil || u.operation.id != id {
		return false
	}
	u.operation.cancel()
	if u.operation.shellBusy {
		u.shell.SetBusy(false)
	}
	u.operation = nil
	return true
}
func (u *ChatUI) operationLabel() string {
	if u.operation == nil {
		for _, backend := range u.backends {
			return fmt.Sprintf("Stopping · %ds · cancel requested", max(0, int(time.Since(backend.started).Seconds())))
		}
		return ""
	}
	seconds := max(0, int(time.Since(u.operation.started).Seconds()))
	return fmt.Sprintf("%s · %ds · Cancel [Ctrl+X]", sanitizeTerminalText(u.operation.label), seconds)
}

func (u *ChatUI) backendBusy() bool { return len(u.backends) > 0 }
func (u *ChatUI) sessionFence() bool {
	if !u.backendBusy() && !u.shell.Busy() {
		return false
	}
	u.workspaceFeedback = "A server operation is still in flight; Cancel or wait for the source to stop."
	return true
}

// Validate actual input-owner state, including mouse-only changes. A key
// counter alone cannot capture caret movement or a clicked result/column.
type workspaceContextStamp struct {
	focus, center, record, row, draft, editorText   string
	column, offset, caret, editorLine, editorColumn int
	expanded                                        bool
}

func (u *ChatUI) contextStamp() workspaceContextStamp {
	stamp := workspaceContextStamp{focus: u.shell.WorkspaceFocus(), center: fmt.Sprintf("%p", u.shell.CenterPanel()), draft: u.shell.ComposerText(), caret: u.shell.ComposerCursor(), expanded: u.shell.Expanded()}
	if g, r, ok := u.activeGrid(); ok && r != nil {
		stamp.record = r.ID
		stamp.row = g.sourceRowKey()
		stamp.column = g.SelectedColumn()
		stamp.offset = g.ColumnOffset()
	}
	if editor, ok := u.shell.CenterPanel().(*workspaceQueryEditor); ok {
		stamp.editorText = editor.text.Value()
		stamp.editorLine = editor.text.Line()
		stamp.editorColumn = editor.text.Column()
	}
	return stamp
}
func (u *ChatUI) operationContextCurrent(id uint64) bool {
	if id == 0 {
		return true
	}
	return u.operation != nil && u.operation.id == id && u.operation.origin == u.contextStamp()
}

// Browser/agent work can also own the session mutex without a local tracker.
// Event-loop refreshes must never wait for that external backend either.
func (u *ChatUI) snapshotIfReady() (ChatSession, bool) {
	if u.sessions == nil || !u.sessions.mu.TryLock() {
		return ChatSession{}, false
	}
	defer u.sessions.mu.Unlock()
	snapshot, err := u.sessions.store.Load(u.ctx, u.sessions.activeID)
	return snapshot, err == nil
}
