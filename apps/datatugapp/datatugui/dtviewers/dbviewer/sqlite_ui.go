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

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/fsutils"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func goSqliteHome(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	const screenTitle = "SQLite Viewer"

	breadcrumbs := GetDbViewersBreadcrumbs(tui)
	breadcrumbs.Push(sneatv.NewBreadcrumb("SQLite", nil))

	menu := getDbViewerMenu(tui, focusTo, "")
	menuPanel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(menu, menu.Box))

	tree := tview.NewTreeView()
	tree.SetTitle(screenTitle)
	root := tview.NewTreeNode(screenTitle)
	root.SetSelectable(false)

	tree.SetRoot(root)
	tree.SetTopLevel(1)

	openNode := tview.NewTreeNode("Open SQLite db file")
	root.AddChild(openNode)
	tree.SetCurrentNode(openNode)

	demoNode := tview.NewTreeNode("Demo")
	demoNode.SetSelectable(false)
	root.AddChild(demoNode)

	northwindNode := tview.NewTreeNode(" " + demoDbsFolder + northwindSqliteDbFileName + " ")
	northwindNode.SetSelectedFunc(func() {
		go openSqliteDemoDb(tui, northwindSqliteDbFileName)
	})
	demoNode.AddChild(northwindNode)

	menu.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			tui.App.SetFocus(tree)
			return nil
		case tcell.KeyUp:
			if menu.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, menu)
				return nil
			}
			return event
		default:
			return event
		}
	})

	tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			tui.App.SetFocus(tui.Menu)
			return nil
		case tcell.KeyUp:
			if tree.GetCurrentNode() == openNode {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, tree)
				return nil
			}
			return event
		default:
			return event
		}
	})

	content := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(tree, tree.Box))

	tui.SetPanels(menuPanel, content, sneatnav.WithFocusTo(focusTo))
	dtlog.ScreenOpened("viewers/sqlite", screenTitle)
	return nil
}

const demoDbsFolder = "~/datatug/demo-dbs/"
const northwindSqliteDbFileName = "northwind.sqlite"
const northwindSqliteDbUrl = "https://raw.githubusercontent.com/jpwhite3/northwind-SQLite3/refs/heads/main/dist/northwind.db"

func fileExists(path string) bool {
	path = fsutils.ExpandHome(path)
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func openSqliteDemoDb(tui *sneatnav.TUI, name string) {
	switch name {
	case northwindSqliteDbFileName:
		filePath := filepath.Join(demoDbsFolder, name)
		if !fileExists(filePath) {
			if err := downloadFile(tui, northwindSqliteDbUrl, filePath); err != nil {
				if !errors.Is(err, context.Canceled) {
					tui.App.QueueUpdateDraw(func() {
						sneatnav.ShowErrorModal(tui, err)
					})
				}
				return
			}
		}
		tui.App.QueueUpdateDraw(func() {
			dbContext := dtviewers.GetSQLiteDbContext(filePath)
			_ = GoSqlDbHome(tui, dbContext)
		})
	}
}

func downloadFile(tui *sneatnav.TUI, from, to string) error {
	if tui == nil {
		return errors.New("tui is nil")
	}

	dst := fsutils.ExpandHome(to)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("failed to create dir: %w", err)
	}

	// Create progress view + Cancel button in Content panel
	progress := tview.NewTextView()
	progress.SetDynamicColors(true)
	progress.SetTitle("Downloading")
	// No border around the progress text
	progress.SetBorder(false)

	cancelBtn := tview.NewButton("Cancel")
	cancelBtn.SetBorder(true)
	cancelBtn.SetLabelColor(tview.Styles.PrimaryTextColor)

	// Put the Cancel button into a horizontal row so it doesn't take full width
	btnRow := tview.NewFlex().SetDirection(tview.FlexColumn)
	// Fixed width for button to avoid stretching; add a spacer to fill remaining width
	btnRow.AddItem(cancelBtn, 12, 0, false)
	btnRow.AddItem(tview.NewBox(), 0, 1, false)

	// Container with progress on top and button row at bottom
	container := tview.NewFlex().SetDirection(tview.FlexRow)
	container.AddItem(progress, 0, 1, true)
	container.AddItem(btnRow, 3, 0, false)

	// Show initial content panel with progress + button
	boxed := sneatv.WithDefaultBorders(container, container.Box)
	progressPanel := sneatnav.NewPanel(tui, boxed)

	// Use a channel to wait for the download to complete
	doneChan := make(chan error, 1)

	tui.App.QueueUpdateDraw(func() {
		tui.SetPanels(tui.Menu, progressPanel, sneatnav.WithFocusTo(sneatnav.FocusToContent))
		// Focus the Cancel button when the download view opens
		// Set focus explicitly to the Cancel button so user can press Enter immediately
		tui.SetFocus(cancelBtn)
	})

	// Run download in background and update progress via QueueUpdateDraw
	go func() {
		defer close(doneChan)
		// Context to support cancel
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var resp *http.Response
		var f *os.File
		var tmp string
		var canceled bool

		tui.App.QueueUpdate(func() {
			cancelBtn.SetSelectedFunc(func() {
				if !canceled {
					canceled = true
					cancel()
				}
			})
			// Allow ESC when the Cancel button itself is focused
			cancelBtn.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				if event.Key() == tcell.KeyEsc {
					if !canceled {
						canceled = true
						cancel()
					}
					return nil
				}
				return event
			})

			// Allow ESC key to cancel while this container has focus
			container.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				if event.Key() == tcell.KeyEsc {
					if !canceled {
						canceled = true
						cancel()
					}
					return nil // consume
				}
				return event
			})
		})

		start := time.Now()

		// Pre-initialize progress state and render initial status before any HTTP is issued
		// so the user immediately sees a status even if networking is slow.
		total := int64(-1)       // unknown until headers are received
		var downloaded int64 = 0 // nothing downloaded yet
		lastTick := time.Now()
		var lastBytes int64 = 0

		// Pre-calculate label alignment width so ':' columns line up
		labels := []string{"Source", "Dest", "Downloaded", "Avg speed", "Now speed", "Elapsed"}
		labelWidth := 0
		for _, l := range labels {
			if len(l) > labelWidth {
				labelWidth = len(l)
			}
		}
		kv := func(label, value string) string {
			return fmt.Sprintf("%*s: %s", labelWidth, label, value)
		}

		// UI update function (also used later during/after download)
		update := func(final bool) {
			elapsed := time.Since(start).Seconds()
			if elapsed <= 0 {
				elapsed = 1e-9
			}
			speed := float64(downloaded) / 1024.0 / 1024.0 / elapsed
			var percent string
			if total > 0 {
				p := float64(downloaded) * 100 / float64(total)
				percent = fmt.Sprintf("%.1f%%", p)
			} else {
				percent = "?%"
			}
			now := time.Now()
			interval := now.Sub(lastTick).Seconds()
			if interval <= 0 {
				interval = 1e-9
			}
			instSpeed := float64(downloaded-lastBytes) / 1024.0 / 1024.0 / interval
			lastTick = now
			lastBytes = downloaded

			var text string
			if final {
				// Hide instantaneous speed in the final summary
				text = strings.Join([]string{
					kv("Source", from),
					kv("Dest", dst),
					"",
					kv("Downloaded", fmt.Sprintf("%s of %s (%s)", humanBytes(downloaded), humanBytes(total), percent)),
					kv("Avg speed", fmt.Sprintf("%.2f MiB/s", speed)),
					kv("Elapsed", time.Since(start).Truncate(time.Second).String()),
					"",
				}, "\n")
			} else {
				text = strings.Join([]string{
					kv("Source", from),
					kv("Dest", dst),
					"",
					kv("Downloaded", fmt.Sprintf("%s of %s (%s)", humanBytes(downloaded), humanBytes(total), percent)),
					kv("Avg speed", fmt.Sprintf("%.2f MiB/s", speed)),
					kv("Now speed", fmt.Sprintf("%.2f MiB/s", instSpeed)),
					kv("Elapsed", time.Since(start).Truncate(time.Second).String()),
					"",
				}, "\n")
			}

			tui.App.QueueUpdateDraw(func() {
				progress.SetText(text)
			})
		}

		// Render the very first status snapshot immediately
		// QueueUpdateDraw is called inside
		update(false)

		// HTTP GET with context
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, from, nil)
		if err != nil {
			tui.App.QueueUpdateDraw(func() {
				_, _ = fmt.Fprintf(progress, "[red]Error: %v[-]\n", err)
			})
			doneChan <- err
			return
		}
		resp, err = http.DefaultClient.Do(req) //nolint:gosec
		if err != nil {
			if canceled || errors.Is(err, context.Canceled) {
				// Canceled before headers
				tui.App.QueueUpdateDraw(func() {
					// Hide the Cancel button row when canceled
					container.RemoveItem(btnRow)
					// Move focus to progress text since the button row is gone
					tui.SetFocus(progress)
					_, _ = fmt.Fprint(progress, "[yellow]Canceled.[-]\n")
				})
				doneChan <- ctx.Err()
				return
			}
			tui.App.QueueUpdateDraw(func() {
				_, _ = fmt.Fprintf(progress, "[red]Error: %v[-]\n", err)
			})
			doneChan <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			tui.App.QueueUpdateDraw(func() {
				_, _ = fmt.Fprintf(progress, "[red]HTTP error: %s[-]\n", resp.Status)
			})
			doneChan <- fmt.Errorf("HTTP error: %s", resp.Status)
			return
		}

		// Create temp file to download to
		tmp = dst + ".part"
		f, err = os.Create(tmp)
		if err != nil {
			tui.App.QueueUpdateDraw(func() {
				_, _ = fmt.Fprintf(progress, "[red]Error creating file: %v[-]\n", err)
			})
			doneChan <- err
			return
		}

		// Now that we have the response, update total if known
		total = resp.ContentLength // may be -1

		buf := make([]byte, 32*1024)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		// Reader loop with periodic updates
		done := make(chan struct{})
		var copyErr error

		go func() {
			defer close(done)
			for {
				n, respErr := resp.Body.Read(buf)
				if n > 0 {
					if _, writeErr := f.Write(buf[:n]); writeErr != nil {
						copyErr = writeErr
						return
					}
					downloaded += int64(n)
				}
				if respErr != nil {
					if respErr != io.EOF {
						copyErr = respErr
					}
					return
				}
			}
		}()

		// UI update loop continues to use the same update() defined above

		// Periodic updates until done
		for {
			select {
			case <-ticker.C:
				update(false)
			case <-ctx.Done():
				// Cancel requested
				canceled = true
				_ = resp.Body.Close()
				_ = f.Close()
				_ = os.Remove(tmp)
				tui.App.QueueUpdateDraw(func() {
					// Hide the Cancel button row when canceled
					container.RemoveItem(btnRow)
					// Move focus to progress text since the button row is gone
					tui.SetFocus(progress)
					_, _ = fmt.Fprintln(progress, "[yellow]Canceled.[-]")
				})
				doneChan <- ctx.Err()
				return
			case <-done:
				// finish copy
				_ = f.Close()
				if copyErr != nil {
					// If context canceled, treat as cancel, not error
					if canceled || errors.Is(copyErr, context.Canceled) {
						_ = os.Remove(tmp)
						tui.App.QueueUpdateDraw(func() {
							// Hide the Cancel button row when canceled
							container.RemoveItem(btnRow)
							// Move focus to progress text since the button row is gone
							tui.SetFocus(progress)
							_, _ = fmt.Fprintln(progress, "[yellow]Canceled.[-]")
						})
						doneChan <- ctx.Err()
						return
					}
					tui.App.QueueUpdateDraw(func() {
						_, _ = fmt.Fprintf(progress, "[red]Error during download: %v[-]\n", copyErr)
					})
					_ = os.Remove(tmp)
					doneChan <- copyErr
					return
				}
				if err = os.Rename(tmp, dst); err != nil {
					tui.App.QueueUpdateDraw(func() {
						_, _ = fmt.Fprintf(progress, "[red]Error saving file: %v[-]\n", err)
					})
					doneChan <- err
					return
				}
				update(true)
				tui.App.QueueUpdateDraw(func() {
					// Hide the Cancel button row after successful completion
					container.RemoveItem(btnRow)
					// Move focus to progress text since the button row is gone
					tui.SetFocus(progress)
					_, _ = fmt.Fprintf(progress, "\n[green]Completed successfully.[-]\n")
				})
				doneChan <- nil
				return
			}
		}
	}()

	return <-doneChan
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
