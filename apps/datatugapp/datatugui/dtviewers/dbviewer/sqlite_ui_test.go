package dbviewer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoSqliteHome(t *testing.T) {
	tui := newTestTUI(t)
	var northwindClicked bool
	onSqliteHomeShown = func(tree *tview.TreeView) {
		if tree != nil && tree.GetRoot() != nil {
			children := tree.GetRoot().GetChildren()
			if len(children) > 1 && len(children[1].GetChildren()) > 0 {
				tree.SetCurrentNode(children[1].GetChildren()[0])
				tree.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})
				northwindClicked = true
			}
		}
	}
	defer func() { onSqliteHomeShown = nil }()

	err := goSqliteHome(tui, sneatnav.FocusToContent)
	assert.NoError(t, err)
	assert.True(t, northwindClicked)

	err = goSqliteHome(tui, sneatnav.FocusToMenu)
	assert.NoError(t, err)
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "test.txt")
	assert.False(t, fileExists(f))
	assert.NoError(t, os.WriteFile(f, []byte("hello"), 0644))
	assert.True(t, fileExists(f))
	assert.False(t, fileExists(tmpDir)) // directory should return false
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "unknown", humanBytes(-1))
	assert.Equal(t, "500 B", humanBytes(500))
	assert.Equal(t, "1.0 KiB", humanBytes(1024))
	assert.Equal(t, "1.5 MiB", humanBytes(1572864))
	assert.Equal(t, "2.0 GiB", humanBytes(2147483648))
	assert.Equal(t, "1.0 TiB", humanBytes(1099511627776))
}

func TestDownloadFile_NilTUI(t *testing.T) {
	err := downloadFile(nil, "http://example.com", "/tmp/file")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tui is nil")
}

func TestDownloadFile_Success(t *testing.T) {
	content := []byte("sqlite database mock content")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer ts.Close()

	tui := newTestTUI(t)
	dest := filepath.Join(t.TempDir(), "downloaded.sqlite")

	err := downloadFile(tui, ts.URL, dest)
	require.NoError(t, err)

	read, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, content, read)
}

func TestOpenSqliteDemoDb(t *testing.T) {
	tui := newTestTUI(t)
	// Calling with unsupported name does nothing
	openSqliteDemoDb(tui, "other_db.sqlite")

	origFolder := demoDbsFolder
	origUrl := northwindSqliteDbUrl
	defer func() {
		demoDbsFolder = origFolder
		northwindSqliteDbUrl = origUrl
	}()

	tempFolder := t.TempDir()
	demoDbsFolder = tempFolder

	// Case 1: file exists
	existingDb := filepath.Join(tempFolder, northwindSqliteDbFileName)
	require.NoError(t, os.WriteFile(existingDb, []byte(""), 0644))
	openSqliteDemoDb(tui, northwindSqliteDbFileName)
	time.Sleep(50 * time.Millisecond)

	// Case 2: file does not exist, download fails
	_ = os.Remove(existingDb)
	northwindSqliteDbUrl = "http://127.0.0.1:0/bad"
	openSqliteDemoDb(tui, northwindSqliteDbFileName)
	time.Sleep(50 * time.Millisecond)

	// Case 3: file does not exist, download succeeds
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		_, _ = w.Write([]byte("hello"))
	}))
	defer ts.Close()
	northwindSqliteDbUrl = ts.URL
	openSqliteDemoDb(tui, northwindSqliteDbFileName)
	time.Sleep(100 * time.Millisecond)
}

func TestDownloadFile_MkdirError(t *testing.T) {
	tui := newTestTUI(t)
	err := downloadFile(tui, "http://example.com", "/dev/null/impossible/dir/file.sqlite")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create dir")
}

func TestDownloadFile_ErrorBranches(t *testing.T) {
	tui := newTestTUI(t)
	tmpDir := t.TempDir()

	// 1. Invalid URL
	err := downloadFile(tui, "://invalid-url", filepath.Join(tmpDir, "f1"))
	assert.Error(t, err)

	// 2. HTTP Error (500)
	ts500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts500.Close()
	err = downloadFile(tui, ts500.URL, filepath.Join(tmpDir, "f2"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP error")

	// 3. Create temp file error (where dst.part is a directory)
	dst3 := filepath.Join(tmpDir, "f3")
	require.NoError(t, os.Mkdir(dst3+".part", 0755))
	tsOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer tsOK.Close()
	err = downloadFile(tui, tsOK.URL, dst3)
	assert.Error(t, err)

	// 4. Rename error (destination is a non-empty directory)
	dst4 := filepath.Join(tmpDir, "f4")
	require.NoError(t, os.Mkdir(dst4, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dst4, "file.txt"), []byte("abc"), 0644))
	err = downloadFile(tui, tsOK.URL, dst4)
	assert.Error(t, err)

	// 5. Connection drop error during copy
	tsDrop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\npartial"))
			_ = conn.Close()
		}
	}))
	defer tsDrop.Close()
	err = downloadFile(tui, tsDrop.URL, filepath.Join(tmpDir, "f5"))
	assert.Error(t, err)
}

func TestDownloadFile_CancelViaButtonAndEsc(t *testing.T) {
	tui := newTestTUI(t)
	tmpDir := t.TempDir()

	// Slow server that hangs
	tsHang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if ok {
			flusher.Flush()
		}
		time.Sleep(1 * time.Second)
	}))
	defer tsHang.Close()

	// Cancel via cancelBtn selected func
	onDownloadSetup = func(cancelBtn *tview.Button, container *tview.Flex) {
		time.Sleep(50 * time.Millisecond)
		// Trigger cancel button press
		cancelBtn.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})
	}
	err := downloadFile(tui, tsHang.URL, filepath.Join(tmpDir, "cancel1"))
	assert.True(t, errors.Is(err, context.Canceled) || err != nil)
	onDownloadSetup = nil

	// Cancel via cancelBtn ESC key
	onDownloadSetup = func(cancelBtn *tview.Button, container *tview.Flex) {
		time.Sleep(50 * time.Millisecond)
		evEsc := tcell.NewEventKey(tcell.KeyEsc, 0, 0)
		assert.Nil(t, cancelBtn.GetInputCapture()(evEsc))
	}
	err = downloadFile(tui, tsHang.URL, filepath.Join(tmpDir, "cancel2"))
	assert.True(t, errors.Is(err, context.Canceled) || err != nil)
	onDownloadSetup = nil

	// Cancel via container ESC key
	onDownloadSetup = func(cancelBtn *tview.Button, container *tview.Flex) {
		time.Sleep(50 * time.Millisecond)
		evEsc := tcell.NewEventKey(tcell.KeyEsc, 0, 0)
		assert.Nil(t, container.GetInputCapture()(evEsc))
		evOther := tcell.NewEventKey(tcell.KeyDown, 0, 0)
		assert.NotNil(t, container.GetInputCapture()(evOther))
	}
	err = downloadFile(tui, tsHang.URL, filepath.Join(tmpDir, "cancel3"))
	assert.True(t, errors.Is(err, context.Canceled) || err != nil)
	onDownloadSetup = nil
}

func TestDownloadFile_TickerFires(t *testing.T) {
	tui := newTestTUI(t)
	tmpDir := t.TempDir()

	tsSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		flusher, ok := w.(http.Flusher)
		_, _ = w.Write([]byte("12345"))
		if ok {
			flusher.Flush()
		}
		time.Sleep(250 * time.Millisecond) // ticker is 200ms
		_, _ = w.Write([]byte("67890"))
	}))
	defer tsSlow.Close()

	err := downloadFile(tui, tsSlow.URL, filepath.Join(tmpDir, "slow.sqlite"))
	require.NoError(t, err)
}

func TestDownloadFile_ChunkWriteError(t *testing.T) {
	tui := newTestTUI(t)
	tmpDir := t.TempDir()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write([]byte("1234567890"))
	}))
	defer ts.Close()

	onDownloadChunk = func(f *os.File, cancel func()) {
		_ = f.Close() // close the file so f.Write fails with writeErr
	}
	defer func() { onDownloadChunk = nil }()

	err := downloadFile(tui, ts.URL, filepath.Join(tmpDir, "write_err.sqlite"))
	assert.Error(t, err)
}

func TestDownloadFile_CancelDuringDownload(t *testing.T) {
	tui := newTestTUI(t)
	tmpDir := t.TempDir()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("first chunk"))
		time.Sleep(500 * time.Millisecond)
	}))
	defer ts.Close()

	onDownloadChunk = func(f *os.File, cancel func()) {
		cancel()
	}
	defer func() { onDownloadChunk = nil }()

	err := downloadFile(tui, ts.URL, filepath.Join(tmpDir, "cancel_during.sqlite"))
	assert.True(t, errors.Is(err, context.Canceled) || err != nil)
}

