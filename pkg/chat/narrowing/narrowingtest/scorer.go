package narrowingtest

import (
	"context"
	"sync"
	"time"

	"github.com/strongo/aichat/ai/decision"
)

// Scorer is a fake decision engine standing in for Jev: a decision.ScoredProvider
// whose answers, errors and delay are set by the test. It records every request
// it receives so a test can assert exactly what would have left the machine.
type Scorer struct {
	// EngineName is Name(); default "fake-jev".
	EngineName string
	// Probabilities are the relevance probabilities by candidate id; a candidate
	// not listed scores Floor.
	Probabilities map[string]float64
	Floor         float64
	// Calibrated is the answer's flag; Jev's are calibrated, an LLM emulator's not.
	Calibrated bool
	// Model and Usage are reported back.
	Model string
	Usage decision.Usage
	// Err, when set, is returned instead of an answer.
	Err error
	// Block makes Score wait for its context to end and return that error, as an
	// engine that does not answer in time does.
	Block bool
	// Delay is how long Score takes on the Clock before it answers.
	Delay time.Duration
	// Clock, when set, is advanced by Delay so tests measure latency without
	// sleeping.
	Clock *Clock
	// Raw, when set, replaces the answers entirely (for malformed results).
	Raw map[string]decision.Answer

	mu       sync.Mutex
	requests []decision.ScoreRequest
}

// Name implements decision.ScoredProvider.
func (s *Scorer) Name() string {
	if s.EngineName == "" {
		return "fake-jev"
	}
	return s.EngineName
}

// Score implements decision.ScoredProvider.
func (s *Scorer) Score(ctx context.Context, req decision.ScoreRequest) (decision.ScoreResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.Block {
		<-ctx.Done()
		return decision.ScoreResult{}, ctx.Err()
	}
	if s.Clock != nil {
		s.Clock.Advance(s.Delay)
	}
	if s.Err != nil {
		return decision.ScoreResult{}, s.Err
	}
	res := decision.ScoreResult{Engine: s.Name(), Model: s.Model, Usage: s.Usage, Answers: map[string]decision.Answer{}}
	if s.Raw != nil {
		res.Answers = s.Raw
		return res, nil
	}
	for _, q := range req.Questions {
		scores := make([]decision.Score, 0, len(q.Candidates))
		for _, c := range q.Candidates {
			p, ok := s.Probabilities[c.ID]
			if !ok {
				p = s.Floor
			}
			scores = append(scores, decision.Score{ID: c.ID, Probability: p})
		}
		answer := decision.NewAnswer(q.ID, q.Kind, scores)
		answer.Calibrated = s.Calibrated
		res.Answers[q.ID] = answer
	}
	return res, nil
}

// Requests returns a copy of every request received so far.
func (s *Scorer) Requests() []decision.ScoreRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]decision.ScoreRequest(nil), s.requests...)
}

// Clock is a fake clock: it moves only when told to, so a test sees exact
// latencies.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock starts a fake clock at a fixed instant.
func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
}

// Now returns the fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake time forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
