// Package dbcopy implements the `datatug db copy` cross-engine database
// copy primitive. See spec/features/cli/db/copy/ for the contract.
package dbcopy

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// ProgressWriter emits per-table start/finish lines to a writer (intended
// to be os.Stderr in production). When enabled is false, all methods are
// no-ops. Safe for concurrent use across multiple worker goroutines.
//
// Format is pinned by REQ:progress-reporting in
// spec/features/cli/db/copy/README.md.
type ProgressWriter struct {
	w   io.Writer
	mu  sync.Mutex
	on  bool
}

// NewProgressWriter returns a ProgressWriter writing to w when enabled is
// true. If enabled is false (or w is nil), all methods are no-ops.
func NewProgressWriter(w io.Writer, enabled bool) *ProgressWriter {
	return &ProgressWriter{w: w, on: enabled && w != nil}
}

// StartTable announces that a copy of `table` is starting. If estRows is
// negative, the start line prints `est. ? rows` instead of a number.
func (p *ProgressWriter) StartTable(table string, estRows int64) {
	if p == nil || !p.on {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if estRows < 0 {
		_, _ = fmt.Fprintf(p.w, "copying %s (est. ? rows)…\n", table)
		return
	}
	_, _ = fmt.Fprintf(p.w, "copying %s (est. %d rows)…\n", table, estRows)
}

// FinishTable announces that a copy of `table` has completed: `rows`
// inserted in `d`. Duration is rounded to millisecond per AC.
func (p *ProgressWriter) FinishTable(table string, rows int64, d time.Duration) {
	if p == nil || !p.on {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.w, "copied %s: %d rows in %s\n", table, rows, d.Round(time.Millisecond).String())
}
