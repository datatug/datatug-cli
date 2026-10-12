package apimonitor

import (
	"fmt"
	"strings"
	"time"
)

// Render renders current, real observations. offset is distance from the newest
// request; zero follows incoming traffic. No sample traffic is generated.
func Render(s Snapshot, width, height, offset int) string {
	lines := []string{"API server  ·  " + strings.ToUpper(s.State), "Base URL: " + s.BaseURL}
	if s.BaseURL == "" {
		lines[1] = "Base URL: — (server is stopped)"
	}
	if s.Error != "" {
		lines = append(lines, s.Error)
	}
	mean := time.Duration(0)
	completed := int64(s.Total) - int64(s.Active)
	if completed > 0 {
		mean = s.Duration / time.Duration(completed)
	}
	lines = append(lines, fmt.Sprintf("Requests %d   Errors %d   Active %d   Mean %s", s.Total, s.Failures, s.Active, mean.Round(time.Millisecond)), "", "Request volume · last 5 minutes · each bar 5 seconds")
	count := min(len(s.Buckets), max(1, width-2))
	buckets := s.Buckets[len(s.Buckets)-count:]
	peak := uint64(0)
	for _, b := range buckets {
		peak = max(peak, b.Count)
	}
	bars := []rune(" ▁▂▃▄▅▆▇█")
	var chart strings.Builder
	for _, b := range buckets {
		i := 0
		if b.Count > 0 {
			i = 1 + int(b.Count*7/max(uint64(1), peak))
		}
		chart.WriteRune(bars[i])
	}
	lines = append(lines, chart.String(), fmt.Sprintf("Peak %d requests / 5s", peak), "", "Request log · ↑↓ scroll · End follow · Esc back", "Time      Method  Status  Outcome    Duration  Path")
	limit := max(1, height-len(lines))
	end := max(0, len(s.Requests)-offset)
	start := max(0, end-limit)
	if len(s.Requests) == 0 {
		lines = append(lines, "No requests yet. Open the web workspace with F5 to see API activity.")
	}
	for _, r := range s.Requests[start:end] {
		status := "—"
		if r.Status != 0 {
			status = fmt.Sprint(r.Status)
		}
		duration := r.Duration.Round(time.Millisecond).String()
		if r.Active {
			status = "open"
			duration = "active"
		}
		lines = append(lines, fmt.Sprintf("%s  %-6s  %-6s  %-9s  %-8s  %s", r.At.Format("15:04:05"), r.Method, status, r.Outcome, duration, r.Path))
	}
	if len(lines) > height {
		lines = lines[:max(1, height)]
	}
	return strings.Join(lines, "\n")
}
