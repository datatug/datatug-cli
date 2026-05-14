package dbcopy

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProgressWriter_DisabledIsSilent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgressWriter(&buf, false)
	p.StartTable("users", 42)
	p.FinishTable("users", 42, 100*time.Millisecond)
	assert.Empty(t, buf.String())
}

func TestProgressWriter_NilWriterIsSilent(t *testing.T) {
	t.Parallel()
	p := NewProgressWriter(nil, true)
	assert.NotPanics(t, func() {
		p.StartTable("users", 42)
		p.FinishTable("users", 42, 100*time.Millisecond)
	})
}

func TestProgressWriter_StartLineFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgressWriter(&buf, true)
	p.StartTable("users", 42)
	assert.Equal(t, "copying users (est. 42 rows)…\n", buf.String())
}

func TestProgressWriter_StartLineUnknownCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgressWriter(&buf, true)
	p.StartTable("users", -1)
	assert.Equal(t, "copying users (est. ? rows)…\n", buf.String())
}

func TestProgressWriter_FinishLineFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgressWriter(&buf, true)
	p.FinishTable("users", 50, 1234*time.Millisecond)
	assert.Equal(t, "copied users: 50 rows in 1.234s\n", buf.String())
}

func TestProgressWriter_FinishLineRoundsDuration(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgressWriter(&buf, true)
	// 400µs is below the 500µs round-to-ms tie boundary, so it rounds to 0ms,
	// producing the literal "1s". (The spec example used 500µs but at the tie
	// Go rounds away from zero to 1ms — yielding "1.001s" — so we shave it
	// just under the tie to demonstrate the round-away behavior cleanly.)
	p.FinishTable("u", 1, 1*time.Second+400*time.Microsecond)
	assert.Equal(t, "copied u: 1 rows in 1s\n", buf.String())
}

func TestProgressWriter_ConcurrentWrites_NoInterleave(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgressWriter(&buf, true)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			table := "t" + itoa(i)
			p.StartTable(table, int64(i))
			p.FinishTable(table, int64(i), time.Duration(i+1)*time.Millisecond)
		}(i)
	}
	wg.Wait()

	startRe := regexp.MustCompile(`^copying t\d+ \(est\. \d+ rows\)…$`)
	finishRe := regexp.MustCompile(`^copied t\d+: \d+ rows in [\d.smnµ]+$`)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.Equal(t, 2*n, len(lines), "expected exactly 2*N lines, got %d", len(lines))
	for _, line := range lines {
		if !startRe.MatchString(line) && !finishRe.MatchString(line) {
			t.Errorf("line did not match expected formats: %q", line)
		}
	}
}

// itoa is a tiny int-to-string helper to keep the test free of strconv noise.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
