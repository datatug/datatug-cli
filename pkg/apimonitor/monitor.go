// Package apimonitor observes local API lifecycle and request metadata in memory.
// Its ResponseWriter facade tracks operations that ResponseController would
// otherwise reach through Unwrap, while retaining each operation's downstream
// errors and behavior. It deliberately never retains request headers, bodies,
// query strings or fragments.
package apimonitor

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	MaxRequests       = 500
	BucketCount       = 60
	BucketDuration    = 5 * time.Second
	MaxServers        = 32
	MaxRouteLength    = 160
	MaxMethodLength   = 16
	registryFullError = "API monitor registry is full; this server was not admitted. Stop an older API server before starting another"
)

var ErrRegistryFull = errors.New(registryFullError)

type Request struct {
	ID           uint64
	At           time.Time
	Method, Path string
	Status       int // The final HTTP status observed on the ResponseWriter; zero means none was sent.
	Outcome      string
	Duration     time.Duration
	Active       bool
}
type Bucket struct {
	At    time.Time
	Count uint64
}
type Snapshot struct {
	Name, State, BaseURL, Error string
	Started                     time.Time
	Total, Failures             uint64
	Active                      int
	Duration                    time.Duration
	Requests                    []Request
	Buckets                     []Bucket
}
type Monitor struct {
	mu       sync.RWMutex
	value    Snapshot
	sequence uint64
	now      func() time.Time
	admitted bool
}

func New(name string) *Monitor {
	return &Monitor{value: Snapshot{Name: boundedText(name, 80), State: "stopped"}, now: time.Now, admitted: true}
}

func (m *Monitor) Live(baseURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.admitted {
		return
	}
	m.value.State = "live"
	m.value.BaseURL = safeBaseURL(baseURL)
	m.value.Started = m.now()
	m.value.Error = ""
}
func (m *Monitor) Stop(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.admitted {
		return
	}
	m.value.State = "stopped"
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		m.value.State = "error"
		m.value.Error = "Listener stopped unexpectedly. Restart the local API service."
	}
}
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.value
	s.Requests = append([]Request(nil), s.Requests...)
	now := m.now()
	s.Buckets = make([]Bucket, BucketCount)
	start := now.Truncate(BucketDuration).Add(-time.Duration(BucketCount-1) * BucketDuration)
	for i := range s.Buckets {
		s.Buckets[i].At = start.Add(time.Duration(i) * BucketDuration)
	}
	for _, b := range m.value.Buckets {
		i := int(b.At.Sub(start) / BucketDuration)
		if !b.At.Before(start) && i < len(s.Buckets) {
			s.Buckets[i].Count = b.Count
		}
	}
	return s
}

func boundedText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(s) > limit {
		s = s[:limit]
	}
	return s
}

// safeBaseURL keeps only loopback origins. In particular it drops user info,
// paths, query data, fragments and arbitrary hostnames supplied by callers.
func safeBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "local API"
	}
	host := strings.ToLower(u.Hostname())
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "local API"
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "local API"
		}
	}
	if port == "" {
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return u.Scheme + "://" + host
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}

var safeRouteSegments = map[string]struct{}{
	"api": {}, "v1": {}, "v2": {}, "health": {}, "healthz": {}, "ready": {}, "readyz": {},
	"openapi.json": {}, "swagger": {}, "docs": {}, "projects": {}, "spaces": {}, "viewers": {},
	"queries": {}, "datasets": {}, "tables": {}, "columns": {}, "records": {}, "chat": {},
	"auth": {}, "login": {}, "logout": {}, "callback": {}, "events": {}, "stream": {},
}

// safePath accepts explicit ServeMux route patterns when available. For other
// handlers it retains only known route words and replaces every unknown segment.
func safePath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	path := r.Pattern
	if path == "" {
		path = r.URL.Path
	} else if i := strings.IndexByte(path, ' '); i >= 0 {
		path = path[i+1:]
	}
	if path == "" || path[0] != '/' || len(path) > MaxRouteLength {
		return "/…"
	}
	fromPattern := r.Pattern != ""
	parts := strings.Split(path, "/")
	for i := range parts {
		part := parts[i]
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if fromPattern {
				// Only route metadata is trusted to name a variable; raw path segments
				// are user-controlled even when they look like ServeMux syntax.
				name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
				if name != "" && safeIdentifier(name) {
					parts[i] = ":" + name
				} else {
					parts[i] = ":id"
				}
			} else {
				parts[i] = ":id"
			}
			continue
		}
		if _, ok := safeRouteSegments[strings.ToLower(part)]; !ok {
			parts[i] = ":id"
		}
	}
	return boundedText(strings.Join(parts, "/"), MaxRouteLength)
}

func safeIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 24 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

var standardMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {}, http.MethodPut: {},
	http.MethodPatch: {}, http.MethodDelete: {}, http.MethodConnect: {},
	http.MethodOptions: {}, http.MethodTrace: {},
}

type requestState struct {
	mu             sync.Mutex
	monitor        *Monitor
	id             uint64
	started        time.Time
	status         int
	outcome        string
	hijacked       bool
	active         bool
	finalized      bool
	duration       time.Duration
	failureCounted bool
}

func (m *Monitor) begin(r *http.Request) *requestState {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sequence++
	now := m.now()
	method := "OTHER"
	if r != nil {
		if _, ok := standardMethods[r.Method]; ok {
			method = r.Method
		}
	}
	item := Request{ID: m.sequence, At: now, Method: method, Path: safePath(r), Active: true, Outcome: "active"}
	m.value.Total++
	m.value.Active++
	if len(m.value.Requests) == MaxRequests {
		copy(m.value.Requests, m.value.Requests[1:])
		m.value.Requests = m.value.Requests[:MaxRequests-1]
	}
	m.value.Requests = append(m.value.Requests, item)
	at := now.Truncate(BucketDuration)
	buckets := m.value.Buckets
	if len(buckets) > 0 && buckets[len(buckets)-1].At.Equal(at) {
		buckets[len(buckets)-1].Count++
	} else {
		buckets = append(buckets, Bucket{At: at, Count: 1})
		if len(buckets) > BucketCount {
			buckets = buckets[len(buckets)-BucketCount:]
		}
	}
	m.value.Buckets = buckets
	return &requestState{monitor: m, id: item.ID, started: now, outcome: "active", active: true}
}

func (s *requestState) updateLocked() {
	m := s.monitor
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.value.Requests) - 1; i >= 0; i-- {
		if m.value.Requests[i].ID == s.id {
			q := &m.value.Requests[i]
			q.Status = s.status
			q.Outcome = s.outcome
			q.Active = s.active
			if s.finalized {
				q.Duration = s.duration
			}
			return
		}
	}
}

func (s *requestState) markHijacked(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hijacked = true
	if status != 0 {
		s.status = status
	}
	s.outcome = "hijacked"
	s.updateLocked()
}

func (s *requestState) panic(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status != 0 {
		s.status = status
	}
	s.outcome = "panic"
	if !s.failureCounted {
		s.monitor.mu.Lock()
		s.monitor.value.Failures++
		s.monitor.mu.Unlock()
		s.failureCounted = true
	}
	if !s.hijacked && !s.finalized {
		s.finalizeLocked()
	}
	s.updateLocked()
}

func (s *requestState) complete(status int, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status != 0 {
		s.status = status
	}
	s.outcome = outcome
	if !s.finalized {
		s.finalizeLocked()
	}
	s.updateLocked()
}

func (s *requestState) handlerReturned(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status != 0 {
		s.status = status
	}
	if s.hijacked {
		if s.outcome != "panic" {
			s.outcome = "hijacked"
		}
		s.updateLocked()
		return
	}
	if s.status == 0 {
		s.status = http.StatusOK
	}
	s.outcome = "completed"
	if !s.finalized {
		s.finalizeLocked()
	}
	s.updateLocked()
}

func (s *requestState) finishHijacked() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finalized {
		if s.outcome != "panic" {
			s.outcome = "hijacked"
		}
		s.finalizeLocked()
	}
	s.updateLocked()
}

func (s *requestState) finalizeLocked() {
	s.finalized = true
	s.active = false
	s.duration = s.monitor.now().Sub(s.started)
	s.monitor.mu.Lock()
	if s.monitor.value.Active > 0 {
		s.monitor.value.Active--
	}
	s.monitor.value.Duration += s.duration
	if !s.failureCounted && s.status >= 400 {
		s.monitor.value.Failures++
		s.failureCounted = true
	}
	s.monitor.mu.Unlock()
}

// Wrap belongs outside authorization and routing so rejected requests appear too.
func (m *Monitor) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := m.begin(r)
		record := &response{ResponseWriter: w, state: state}
		defer func() {
			recovered := recover()
			if recovered != nil {
				state.panic(record.status)
				panic(recovered)
			}
			state.handlerReturned(record.status)
		}()
		next.ServeHTTP(record.wrap(), r)
	})
}

// Default is the process-local observer for the current local API service.
var Default = &Registry{}

// Registry aggregates independent server instances. A full registry evicts one
// stopped instance; if every slot is live, NewServer returns a non-admitted
// monitor in the explicit "error" state and preserves every existing server.
type Registry struct {
	mu             sync.Mutex
	servers        []*Monitor
	admissionError string
}

func (r *Registry) NewServer(name string) *Monitor {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := New(name)
	m.value.State = "starting"
	if len(r.servers) >= MaxServers {
		remove := -1
		for i, s := range r.servers {
			state := s.Snapshot().State
			if state == "stopped" || state == "error" {
				remove = i
				break
			}
		}
		if remove < 0 {
			m.admitted = false
			m.value.State = "error"
			m.value.Error = ErrRegistryFull.Error()
			r.admissionError = ErrRegistryFull.Error()
			return m
		}
		r.servers = append(r.servers[:remove], r.servers[remove+1:]...)
	}
	r.servers = append(r.servers, m)
	r.admissionError = ""
	return m
}

func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	servers := append([]*Monitor(nil), r.servers...)
	admissionError := r.admissionError
	r.mu.Unlock()
	result := New("Local API").Snapshot()
	result.State = "stopped"
	result.Error = admissionError
	hasStarting := false
	serverError := ""
	urls := []string{}
	buckets := map[time.Time]uint64{}
	for _, m := range servers {
		s := m.Snapshot()
		result.Total += s.Total
		result.Failures += s.Failures
		result.Active += s.Active
		result.Duration += s.Duration
		result.Requests = append(result.Requests, s.Requests...)
		if s.State == "live" {
			result.State = "live"
			urls = append(urls, s.BaseURL)
			if result.Started.IsZero() || s.Started.Before(result.Started) {
				result.Started = s.Started
			}
		} else if s.State == "starting" {
			hasStarting = true
		} else if s.State == "error" && result.State != "live" {
			result.State = "error"
			serverError = s.Error
		}
		for _, b := range s.Buckets {
			buckets[b.At] += b.Count
		}
	}
	if result.State == "stopped" && hasStarting {
		result.State = "starting"
	}
	if result.State == "error" && serverError != "" {
		result.Error = serverError
	}
	result.BaseURL = strings.Join(urls, ", ")
	sort.SliceStable(result.Requests, func(i, j int) bool { return result.Requests[i].At.Before(result.Requests[j].At) })
	if len(result.Requests) > MaxRequests {
		result.Requests = result.Requests[len(result.Requests)-MaxRequests:]
	}
	for i := range result.Buckets {
		result.Buckets[i].Count = buckets[result.Buckets[i].At]
	}
	return result
}

// NewServerChecked provides an error-returning admission path for new callers.
func (r *Registry) NewServerChecked(name string) (*Monitor, error) {
	m := r.NewServer(name)
	if !m.admitted {
		return m, ErrRegistryFull
	}
	return m, nil
}
