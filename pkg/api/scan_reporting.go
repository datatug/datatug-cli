package api

import (
	"context"
	"io"
)

// scanWarningsKey is the key of the context value WithScanWarnings sets.
type scanWarningsKey struct{}

// WithScanWarnings returns a context whose scan names, on w, what it leaves out of
// the catalog it reads and why, one line for each: the `datatug scan` command gives
// it its error stream. Without it the scan says nothing of what it leaves out.
func WithScanWarnings(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, scanWarningsKey{}, w)
}

// scanWarningsFrom is where a scan under ctx names what it leaves out: the writer of
// WithScanWarnings, or io.Discard.
func scanWarningsFrom(ctx context.Context) io.Writer {
	if w, ok := ctx.Value(scanWarningsKey{}).(io.Writer); ok {
		return w
	}
	return io.Discard
}
