package commands

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
)

type athenaControlInputKind uint8

const (
	athenaControlInputZero athenaControlInputKind = iota
	athenaControlInputFinite
	athenaControlInputPendingFile
)

// athenaControlInputSource is a private descriptor, not an io.Reader. It
// deliberately exposes no Read method so callers cannot bypass qualification.
// pendingFile means an UNQUALIFIED native file: it is not proof of positive
// cancellation, admission, or platform support. A future reviewed platform
// constructor must qualify that file and own any auxiliary resources and
// preloaded regular scripts BEFORE starting any SDK or pump. This stage never
// reads, stats, deadline-inspects, fd-inspects, or closes pendingFile.
type athenaControlInputSource struct {
	kind        athenaControlInputKind
	data        []byte
	pendingFile *os.File
	maxBytes    int
}

// athenaAdmitControlInput takes a bounded finite snapshot or records an
// unqualified native file. Admission requires exclusive use of reader: the
// caller must not read or mutate it concurrently until this function returns.
// maxBytes is the intended combined stream ceiling; callers later reserve
// athenaControlMaxBytes for controls alone or twice that for a shared request
// plus controls. Existing pure parser caps are unchanged.
func athenaAdmitControlInput(ctx context.Context, reader io.Reader, maxBytes int) (source athenaControlInputSource, err error) {
	if ctx == nil {
		return athenaControlInputSource{}, errAthenaControl
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			source = athenaControlInputSource{}
			err = ctxErr
			return
		}
		if err != nil {
			source = athenaControlInputSource{}
			err = errAthenaControl
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return athenaControlInputSource{}, err
	}
	if reader == nil || maxBytes < 1 || maxBytes > 2*athenaControlMaxBytes {
		return athenaControlInputSource{}, errAthenaControl
	}
	switch r := reader.(type) {
	case *bytes.Reader:
		if r == nil {
			return athenaControlInputSource{}, errAthenaControl
		}
		remaining := r.Len()
		size := r.Size()
		if remaining < 0 || remaining > maxBytes || size < int64(remaining) {
			return athenaControlInputSource{}, errAthenaControl
		}
		data := make([]byte, remaining)
		if remaining > 0 {
			n, readErr := r.ReadAt(data, size-int64(remaining))
			if readErr != nil || n != remaining {
				return athenaControlInputSource{}, errAthenaControl
			}
		}
		return athenaControlInputSource{kind: athenaControlInputFinite, data: data, maxBytes: maxBytes}, nil
	case *strings.Reader:
		if r == nil {
			return athenaControlInputSource{}, errAthenaControl
		}
		remaining := r.Len()
		size := r.Size()
		if remaining < 0 || remaining > maxBytes || size < int64(remaining) {
			return athenaControlInputSource{}, errAthenaControl
		}
		data := make([]byte, remaining)
		if remaining > 0 {
			n, readErr := r.ReadAt(data, size-int64(remaining))
			if readErr != nil || n != remaining {
				return athenaControlInputSource{}, errAthenaControl
			}
		}
		return athenaControlInputSource{kind: athenaControlInputFinite, data: data, maxBytes: maxBytes}, nil
	case *bytes.Buffer:
		if r == nil {
			return athenaControlInputSource{}, errAthenaControl
		}
		remaining := r.Len()
		if remaining < 0 || remaining > maxBytes {
			return athenaControlInputSource{}, errAthenaControl
		}
		data := make([]byte, remaining)
		copy(data, r.Bytes())
		return athenaControlInputSource{kind: athenaControlInputFinite, data: data, maxBytes: maxBytes}, nil
	case *os.File:
		if r == nil {
			return athenaControlInputSource{}, errAthenaControl
		}
		return athenaControlInputSource{kind: athenaControlInputPendingFile, pendingFile: r, maxBytes: maxBytes}, nil
	default:
		return athenaControlInputSource{}, errAthenaControl
	}
}

// athenaOwnedControlReader is the private native control input contract
// returned by the reviewed platform constructor and consumed by its
// implementation. Cancel only signals a cancellation request: it is not a join
// and never proves that an active Read has finished or that owned resources
// have been released. Close remains the only acknowledgement of ownership loss.
type athenaOwnedControlReader interface {
	io.Reader
	Cancel() bool
	Close() error
}
