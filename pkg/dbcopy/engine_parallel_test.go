package dbcopy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	"github.com/stretchr/testify/assert"
)

// TestCopy_ConcurrencyCap_WarnsOnExplicitRequest covers AC
// `concurrency-cap-warns-on-explicit-request`. With dalgo2sqlite as source
// (NoConcurrency) and dalgo2ingitdb as target (ConcurrencyAvailable), an
// explicit --parallel-streams=8 must be capped to 1 with exactly one
// warning line on stderr naming the constraining driver, the requested
// value, and the effective value.
func TestCopy_ConcurrencyCap_WarnsOnExplicitRequest(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	var stderr bytes.Buffer
	summary, err := Copy(context.Background(), src, tgt, CopyOpts{
		Stderr:          &stderr,
		ParallelStreams: 8,
	})
	assert.NoError(t, err)
	assert.Equal(t, 11, summary.Created)

	got := stderr.String()
	assert.Contains(t, got, "warning:")
	// Source is sqlite (NoConcurrency); target is ingitdb (ConcurrencyAvailable).
	assert.Contains(t, got, "sqlite")
	assert.Contains(t, got, "(source)")
	assert.Contains(t, got, "8")
	assert.Contains(t, got, "1")

	// Sanity: exactly one warning line.
	warnLines := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "warning:") {
			warnLines++
		}
	}
	assert.Equal(t, 1, warnLines, "expected exactly one warning line, stderr was:\n%s", got)
}

// TestCopy_ConcurrencyCap_SilentOnDefault covers AC
// `concurrency-cap-silent-on-default`. When the user takes the default
// (ParallelStreams: 0), the cap-to-1 happens silently — no warning.
func TestCopy_ConcurrencyCap_SilentOnDefault(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	var stderr bytes.Buffer
	summary, err := Copy(context.Background(), src, tgt, CopyOpts{
		Stderr:          &stderr,
		ParallelStreams: 0, // default
	})
	assert.NoError(t, err)
	assert.Equal(t, 11, summary.Created)

	assert.NotContains(t, stderr.String(), "warning:",
		"default ParallelStreams must cap silently; stderr was:\n%s", stderr.String())
}

// TestCopy_ParallelStreams_DefaultIsAtLeastOne asserts that a default
// (ParallelStreams=0) Copy completes correctly on Chinook with the same
// end result as a serial copy, regardless of runtime.NumCPU().
func TestCopy_ParallelStreams_DefaultIsAtLeastOne(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	summary, err := Copy(context.Background(), src, tgt, CopyOpts{
		ParallelStreams: 0,
	})
	assert.NoError(t, err)
	assert.Equal(t, 11, summary.Created)
	assert.Equal(t, int64(15607), summary.RowsCopied)
}

// dualConcurrentDB is a hand-rolled dal.DB stub that advertises
// ConcurrencyAvailable. Used to test that resolveParallelism does NOT
// cap when both sides allow concurrency. Only the ConcurrencyAware and
// Adapter methods of dal.DB are exercised by resolveParallelism, so the
// rest of dal.DB is satisfied via an embedded nil interface.
type dualConcurrentDB struct {
	dal.DB
}

func (dualConcurrentDB) SupportsConcurrentConnections() bool { return true }
func (dualConcurrentDB) Adapter() dal.Adapter                { return nil }

// TestResolveParallelism_NoCapWhenBothConcurrent verifies the cap rule
// itself: when both source and target advertise concurrency, the
// requested value is preserved verbatim and no warning is emitted.
//
// This is the non-capped path of resolveParallelism. We test the helper
// directly rather than wiring a full dummy DB through Copy, because no
// existing adapter pair has BOTH ConcurrencyAvailable.
func TestResolveParallelism_NoCapWhenBothConcurrent(t *testing.T) {
	t.Parallel()

	var src, tgt dualConcurrentDB

	var stderr bytes.Buffer
	got := resolveParallelism(8, src, tgt, &stderr)
	assert.Equal(t, 8, got)
	assert.Empty(t, stderr.String(), "no warning expected when both sides are concurrent")
}

// TestResolveParallelism_DefaultFloorIsOne pins the floor of the default
// path: even if runtime.NumCPU() returned 1 (so NumCPU-1 = 0), the
// effective requested value is clamped to 1.
func TestResolveParallelism_DefaultFloorIsOne(t *testing.T) {
	t.Parallel()

	var src, tgt dualConcurrentDB

	var stderr bytes.Buffer
	got := resolveParallelism(0, src, tgt, &stderr)
	assert.GreaterOrEqual(t, got, 1)
	assert.Empty(t, stderr.String())
}

// TestResolveParallelism_NegativeNormalizedToOne — defense in depth.
func TestResolveParallelism_NegativeNormalizedToOne(t *testing.T) {
	t.Parallel()

	var src, tgt dualConcurrentDB

	var stderr bytes.Buffer
	got := resolveParallelism(-5, src, tgt, &stderr)
	assert.Equal(t, 1, got)
	assert.Empty(t, stderr.String())
}
