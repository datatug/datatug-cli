package dbviewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// Seams over the network and the clock.
var (
	httpDo           = http.DefaultClient.Do
	nowFunc          = time.Now
	progressInterval = 200 * time.Millisecond
)

const downloadBufferSize = 32 * 1024

type downloadState int

const (
	downloadConnecting downloadState = iota
	downloadRunning
	downloadCanceled
)

// downloadJob is the handle on the transfer that the commands share. They run
// one after the other, so the fields are never touched concurrently; only cancel
// is called from another command.
type downloadJob struct {
	ctx    context.Context
	cancel context.CancelFunc
	from   string
	dest   string
	body   io.ReadCloser
	file   *os.File
	tmp    string
}

// download is the screen that shows the progress of fetching a database file and
// opens the database when it is complete.
type download struct {
	job     *downloadJob
	state   downloadState
	total   int64 // -1 while unknown
	got     int64
	lastGot int64
	start   time.Time
	last    time.Time
	nowRate float64 // MiB/s over the last interval
}

var (
	_ nav.Screen      = download{}
	_ nav.Titled      = download{}
	_ nav.ShortHelper = download{}
)

func newDownload(from, dest string) download {
	ctx, cancel := context.WithCancel(context.Background())
	now := nowFunc()
	return download{
		job:   &downloadJob{ctx: ctx, cancel: cancel, from: from, dest: fsutil.ExpandHome(dest)},
		total: -1,
		start: now,
		last:  now,
	}
}

// Messages of the download commands.
type (
	downloadStarted struct {
		total int64
		err   error
	}
	downloadChunk struct {
		n    int
		at   time.Time
		done bool
		err  error
	}
	downloadFinished struct{ err error }
)

// startDownload opens the connection and the temporary file.
func startDownload(job *downloadJob) tea.Cmd {
	return func() tea.Msg {
		if err := os.MkdirAll(filepath.Dir(job.dest), 0o755); err != nil {
			return downloadStarted{err: fmt.Errorf("failed to create dir: %w", err)}
		}
		req, err := http.NewRequestWithContext(job.ctx, http.MethodGet, job.from, nil)
		if err != nil {
			return downloadStarted{err: err}
		}
		resp, err := httpDo(req) //nolint:gosec // the URL is a constant of this package
		if err != nil {
			return downloadStarted{err: err}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return downloadStarted{err: fmt.Errorf("HTTP error: %s", resp.Status)}
		}
		job.tmp = job.dest + ".part"
		if job.file, err = os.Create(job.tmp); err != nil {
			_ = resp.Body.Close()
			return downloadStarted{err: fmt.Errorf("error creating file: %w", err)}
		}
		job.body = resp.Body
		return downloadStarted{total: resp.ContentLength}
	}
}

// readChunk copies data until progressInterval has passed or the body ends.
func readChunk(job *downloadJob) tea.Cmd {
	return func() tea.Msg {
		buf := make([]byte, downloadBufferSize)
		begin := nowFunc()
		n := 0
		for {
			read, err := job.body.Read(buf)
			if read > 0 {
				if _, werr := job.file.Write(buf[:read]); werr != nil {
					return downloadChunk{n: n, at: nowFunc(), err: werr}
				}
				n += read
			}
			if err != nil {
				return downloadChunk{n: n, at: nowFunc(), done: errors.Is(err, io.EOF), err: nonEOF(err)}
			}
			if at := nowFunc(); at.Sub(begin) >= progressInterval {
				return downloadChunk{n: n, at: at}
			}
		}
	}
}

func nonEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// finishDownload closes the transfer: the temporary file becomes the database
// when err is nil and is removed otherwise.
func finishDownload(job *downloadJob, err error) tea.Cmd {
	return func() tea.Msg {
		_ = job.body.Close()
		closeErr := job.file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(job.tmp, job.dest)
		}
		if err != nil {
			_ = os.Remove(job.tmp)
			return downloadFinished{err: err}
		}
		return downloadFinished{}
	}
}

func cancelDownload(job *downloadJob) tea.Cmd {
	return func() tea.Msg {
		job.cancel()
		return nil
	}
}

// Init implements nav.Screen.
func (d download) Init() tea.Cmd { return startDownload(d.job) }

// Update implements nav.Screen.
func (d download) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case downloadStarted:
		if msg.err != nil {
			return d.stop(msg.err)
		}
		d.state, d.total = downloadRunning, msg.total
		return d, readChunk(d.job)
	case downloadChunk:
		d.got += int64(msg.n)
		if interval := msg.at.Sub(d.last).Seconds(); interval > 0 {
			d.nowRate = float64(d.got-d.lastGot) / (1 << 20) / interval
		}
		d.last, d.lastGot = msg.at, d.got
		if msg.done || msg.err != nil {
			return d, finishDownload(d.job, msg.err)
		}
		return d, readChunk(d.job)
	case downloadFinished:
		if msg.err != nil {
			return d.stop(msg.err)
		}
		d.job.cancel()
		return d, nav.Replace(DbHomePage(dtviewers.GetSQLiteDbContext(d.job.dest)))
	case tea.KeyPressMsg:
		return d.key(msg)
	}
	return d, nil
}

// stop ends the screen's transfer for good: a cancellation is shown, any other
// error is reported.
func (d download) stop(err error) (nav.Screen, tea.Cmd) {
	d.job.cancel()
	if errors.Is(err, context.Canceled) {
		d.state = downloadCanceled
		return d, nil
	}
	return d, datatugui.ReportError("download "+filepath.Base(d.job.dest), err)
}

func (d download) key(msg tea.KeyPressMsg) (nav.Screen, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		if d.state == downloadCanceled {
			return d, nav.Pop()
		}
		return d, cancelDownload(d.job)
	}
	return d, nil
}

// View implements nav.Screen.
func (d download) View() string {
	rows := [][2]string{{"Source", d.job.from}, {"Dest", d.job.dest}, {"", ""}}
	elapsed := max(d.last.Sub(d.start).Seconds(), 1e-9)
	avg := float64(d.got) / (1 << 20) / elapsed
	percent := "?%"
	if d.total > 0 {
		percent = fmt.Sprintf("%.1f%%", float64(d.got)*100/float64(d.total))
	}
	rows = append(rows,
		[2]string{"Downloaded", fmt.Sprintf("%s of %s (%s)", humanBytes(d.got), humanBytes(d.total), percent)},
		[2]string{"Avg speed", fmt.Sprintf("%.2f MiB/s", avg)},
		[2]string{"Now speed", fmt.Sprintf("%.2f MiB/s", d.nowRate)},
		[2]string{"Elapsed", d.last.Sub(d.start).Truncate(time.Second).String()},
	)
	var b strings.Builder
	for _, r := range rows {
		if r[0] == "" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "%10s: %s\n", r[0], r[1])
	}
	b.WriteString("\n")
	if d.state == downloadCanceled {
		b.WriteString("Canceled. Press Esc to go back.")
	} else {
		button := widgets.Button{Label: "Cancel", Focused: true}
		b.WriteString(button.View(button.Width()))
	}
	return b.String()
}

// Title implements nav.Titled.
func (download) Title() string { return "Downloading" }

// ShortHelp implements nav.ShortHelper.
func (download) ShortHelp() []key.Binding {
	return []key.Binding{key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("esc", "cancel"))}
}

func humanBytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
