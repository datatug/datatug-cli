package narrowingtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/strongo/aichat/ai/decision"
)

func TestChinookHasElevenTables(t *testing.T) {
	relations := Chinook()
	if len(relations) != 11 || relations[5].Name != "Invoice" || relations[5].Columns[8].Name != "Total" || relations[5].Columns[8].DbType != "NUMERIC(10,2)" {
		t.Fatalf("Chinook() = %+v", relations)
	}
}

func TestChinookLinks(t *testing.T) {
	links := ChinookLinks()
	if len(links) != 11 || links[0].From != "Album" || links[0].To != "Artist" || links[0].FromSchema != "main" {
		t.Fatalf("ChinookLinks() = %+v", links)
	}
}

func relevance(ids ...string) decision.ScoreRequest {
	q := decision.Question{ID: "q", Kind: decision.KindRelevance}
	for _, id := range ids {
		q.Candidates = append(q.Candidates, decision.Candidate{ID: id})
	}
	return decision.ScoreRequest{Questions: []decision.Question{q}}
}

func TestScorerAnswersAndRecords(t *testing.T) {
	s := &Scorer{Probabilities: map[string]float64{"a": 0.9}, Floor: 0.1, Calibrated: true, EngineName: "e"}
	res, err := s.Score(context.Background(), relevance("a", "b"))
	if err != nil || s.Name() != "e" || res.Engine != "e" {
		t.Fatalf("Score = %+v, %v", res, err)
	}
	answer := res.Answers["q"]
	if !answer.Calibrated || answer.Scores[0].ID != "a" || answer.Scores[1].Probability != 0.1 {
		t.Fatalf("answer = %+v", answer)
	}
	if got := s.Requests(); len(got) != 1 || got[0].Questions[0].ID != "q" {
		t.Fatalf("Requests() = %+v", got)
	}
	if (&Scorer{}).Name() != "fake-jev" {
		t.Fatal("default name")
	}
}

func TestScorerScriptsAnswersByQuestionText(t *testing.T) {
	s := &Scorer{Probabilities: map[string]float64{"a": 0.9}, ByText: map[string]map[string]float64{"later": {"b": 0.8}}, Floor: 0.1}
	req := relevance("a", "b")
	req.Text = "later"
	res, err := s.Score(context.Background(), req)
	if err != nil || res.Answers["q"].Scores[0].ID != "b" {
		t.Fatalf("scripted answer = %+v, %v", res, err)
	}
	req.Text = "other"
	if res, _ = s.Score(context.Background(), req); res.Answers["q"].Scores[0].ID != "a" {
		t.Fatalf("default answer = %+v", res)
	}
}

func TestScorerFailureModes(t *testing.T) {
	boom := errors.New("boom")
	if _, err := (&Scorer{Err: boom}).Score(context.Background(), relevance("a")); !errors.Is(err, boom) {
		t.Fatalf("Err = %v", err)
	}
	raw := map[string]decision.Answer{"x": {}}
	if res, _ := (&Scorer{Raw: raw}).Score(context.Background(), relevance("a")); len(res.Answers) != 1 {
		t.Fatalf("Raw = %+v", res)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Scorer{Block: true}).Score(ctx, relevance("a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Block = %v", err)
	}
}

func TestClockAdvancesOnlyWhenTold(t *testing.T) {
	clock := NewClock()
	start := clock.Now()
	s := &Scorer{Clock: clock, Delay: 50 * time.Millisecond}
	if _, err := s.Score(context.Background(), relevance("a")); err != nil {
		t.Fatal(err)
	}
	if got := clock.Now().Sub(start); got != 50*time.Millisecond {
		t.Fatalf("clock moved %v", got)
	}
}
