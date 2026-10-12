package dbviewer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

// stubHTTP replaces the HTTP client with fn.
func stubHTTP(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	old := httpDo
	httpDo = fn
	t.Cleanup(func() { httpDo = old })
}

// stubClock makes every reading of the clock a second later than the previous.
func stubClock(t *testing.T, step time.Duration) {
	t.Helper()
	old := nowFunc
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time {
		now = now.Add(step)
		return now
	}
	t.Cleanup(func() { nowFunc = old })
}

func bodyResponse(body io.Reader, length int64) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", ContentLength: length, Body: io.NopCloser(body)}
}

// pump runs a download screen the way the program would, feeding each command's
// message back, until it navigates; it returns that navigation message.
func pump(t *testing.T, d download) (download, tea.Msg) {
	t.Helper()
	msg := d.Init()()
	for {
		next, cmd := d.Update(msg)
		d = next.(download)
		if cmd == nil {
			return d, msg
		}
		msg = cmd()
		switch msg.(type) {
		case nil, nav.ReplaceMsg, nav.ErrorMsg, nav.PopMsg:
			return d, msg
		}
	}
}

func TestDownload_Succeeds(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 100_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	stubClock(t, time.Second)
	old := progressInterval
	progressInterval = 0
	defer func() { progressInterval = old }()

	dest := filepath.Join(t.TempDir(), "sub", "file.sqlite")
	d, last := pump(t, newDownload(srv.URL, dest))
	assert.IsType(t, nav.ReplaceMsg{}, last)
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.Equal(t, int64(len(data)), d.got)
	assert.NoFileExists(t, dest+".part")
	assert.Contains(t, uitest.Plain(d.View()), "Downloaded: 97.7 KiB of unknown (?%)")
}

func TestDownload_ExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := newDownload("http://x", "~/a/b.db")
	assert.Equal(t, filepath.Join(home, "a", "b.db"), d.job.dest)
	d.job.cancel()
}

func failure(t *testing.T, d download) string {
	t.Helper()
	_, last := pump(t, d)
	msg, ok := last.(nav.ErrorMsg)
	require.True(t, ok, "got %T", last)
	return msg.Err.Error()
}

func TestDownload_Failures(t *testing.T) {
	t.Run("mkdir", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		assert.Contains(t, failure(t, newDownload("http://x", filepath.Join(file, "sub", "db"))), "failed to create dir")
	})
	t.Run("bad url", func(t *testing.T) {
		assert.Contains(t, failure(t, newDownload("http://[::1", filepath.Join(t.TempDir(), "db"))), "download db")
	})
	t.Run("connection", func(t *testing.T) {
		stubHTTP(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("no route") })
		assert.Contains(t, failure(t, newDownload("http://x", filepath.Join(t.TempDir(), "db"))), "no route")
	})
	t.Run("http status", func(t *testing.T) {
		stubHTTP(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		assert.Contains(t, failure(t, newDownload("http://x", filepath.Join(t.TempDir(), "db"))), "HTTP error: 404 Not Found")
	})
	t.Run("temporary file", func(t *testing.T) {
		stubHTTP(t, func(*http.Request) (*http.Response, error) { return bodyResponse(strings.NewReader("x"), 1), nil })
		dest := filepath.Join(t.TempDir(), "db")
		require.NoError(t, os.Mkdir(dest+".part", 0o755))
		assert.Contains(t, failure(t, newDownload("http://x", dest)), "error creating file")
	})
	t.Run("read", func(t *testing.T) {
		stubHTTP(t, func(*http.Request) (*http.Response, error) {
			body := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errors.New("connection reset")))
			return bodyResponse(body, -1), nil
		})
		dest := filepath.Join(t.TempDir(), "db")
		assert.Contains(t, failure(t, newDownload("http://x", dest)), "connection reset")
		assert.NoFileExists(t, dest+".part")
	})
	t.Run("rename", func(t *testing.T) {
		stubHTTP(t, func(*http.Request) (*http.Response, error) { return bodyResponse(strings.NewReader("abc"), 3), nil })
		dest := filepath.Join(t.TempDir(), "db")
		require.NoError(t, os.MkdirAll(filepath.Join(dest, "keep"), 0o755))
		assert.Contains(t, failure(t, newDownload("http://x", dest)), "db")
		assert.NoFileExists(t, dest+".part")
	})
}

func TestDownload_WriteFailure(t *testing.T) {
	stubHTTP(t, func(*http.Request) (*http.Response, error) { return bodyResponse(strings.NewReader("abc"), 3), nil })
	d := newDownload("http://x", filepath.Join(t.TempDir(), "db"))
	next, cmd := d.Update(d.Init()())
	d = next.(download)
	require.NoError(t, d.job.file.Close()) // the next write fails
	next, cmd = d.Update(cmd())
	_, cmd = next.Update(cmd())
	msgs := uitest.Msgs(cmd)
	require.Len(t, msgs, 1)
	assert.IsType(t, nav.ErrorMsg{}, msgs[0])
}

func TestDownload_CancelWhileRunning(t *testing.T) {
	stubHTTP(t, func(req *http.Request) (*http.Response, error) {
		return bodyResponse(blockingReader{ctx: req.Context()}, -1), nil
	})
	d := newDownload("http://x", filepath.Join(t.TempDir(), "db"))
	next, cmd := d.Update(d.Init()())
	d = next.(download)

	read := make(chan tea.Msg, 1)
	go func() { read <- cmd() }()
	_, cancel := d.Update(uitest.Key("esc"))
	require.NotNil(t, cancel)
	assert.Nil(t, cancel())

	next, cmd = d.Update(<-read)
	next, cmd = next.Update(cmd())
	d = next.(download)
	assert.Nil(t, cmd)
	assert.Contains(t, uitest.Plain(d.View()), "Canceled.")
	assert.NoFileExists(t, d.job.dest+".part")

	_, back := d.Update(uitest.Key("enter"))
	assert.Equal(t, nav.PopMsg{}, back())
}

func TestDownload_CancelWhileConnecting(t *testing.T) {
	stubHTTP(t, func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	d := newDownload("http://x", filepath.Join(t.TempDir(), "db"))
	started := make(chan tea.Msg, 1)
	init := d.Init()
	go func() { started <- init() }()
	_, cancel := d.Update(uitest.Key("enter"))
	cancel()
	next, cmd := d.Update(<-started)
	assert.Nil(t, cmd)
	assert.Contains(t, uitest.Plain(next.View()), "Canceled.")
}

func TestDownload_IgnoresOtherMessages(t *testing.T) {
	d := newDownload("http://x", filepath.Join(t.TempDir(), "db"))
	defer d.job.cancel()
	_, cmd := d.Update(uitest.Key("x"))
	assert.Nil(t, cmd)
	_, cmd = d.Update(struct{}{})
	assert.Nil(t, cmd)
}

func TestDownload_ViewsProgress(t *testing.T) {
	d := newDownload("http://x/file", "/dest/file")
	defer d.job.cancel()
	view := uitest.Plain(d.View())
	assert.Contains(t, view, "Source: http://x/file")
	assert.Contains(t, view, "Downloaded: 0 B of unknown (?%)")
	assert.Contains(t, view, "Cancel")
	assert.Equal(t, "Downloading", d.Title())
	assert.NotEmpty(t, d.ShortHelp())
}

func TestDownload_ViewsPercentWhenTheSizeIsKnown(t *testing.T) {
	d := newDownload("http://x", filepath.Join(t.TempDir(), "db"))
	defer d.job.cancel()
	next, _ := d.Update(downloadStarted{total: 200})
	next, _ = next.Update(downloadChunk{n: 50, at: d.last.Add(time.Second)})
	assert.Contains(t, uitest.Plain(next.View()), "Downloaded: 50 B of 200 B (25.0%)")
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		-1: "unknown", 0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB",
		1 << 20: "1.0 MiB", 5 << 30: "5.0 GiB",
	}
	for n, want := range cases {
		assert.Equal(t, want, humanBytes(n), n)
	}
}

// blockingReader blocks until its context ends.
type blockingReader struct{ ctx context.Context }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
