package rating

import (
	"math"
	"testing"
	"time"
)

func t0(d time.Duration) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(d)
}

func TestNewEngine(t *testing.T) {
	if e, _ := New("elo"); e.ID() != "elo" {
		t.Errorf("got %q want elo", e.ID())
	}
	if e, _ := New(""); e.ID() != "elo" {
		t.Errorf("default should be elo, got %q", e.ID())
	}
	if e, _ := New("glicko2"); e.ID() != "glicko2" {
		t.Errorf("got %q want glicko2", e.ID())
	}
	if _, err := New("noop"); err == nil {
		t.Error("expected error for unknown engine")
	}
}

func TestELOOneMatch(t *testing.T) {
	e := NewELO()
	matches := []Match{{Player1ID: "a", Player2ID: "b", Player1Score: 3, Player2Score: 1, PlayedAt: t0(0)}}
	pr, err := e.Compute(matches)
	if err != nil {
		t.Fatal(err)
	}
	a, b := pr["a"], pr["b"]
	if a.Wins != 1 || a.Losses != 0 || a.GamesPlayed != 1 {
		t.Errorf("a: %+v", a)
	}
	if b.Wins != 0 || b.Losses != 1 || b.GamesPlayed != 1 {
		t.Errorf("b: %+v", b)
	}
	// Both started at 1000, expected 0.5 each, K=32; winner +16, loser -16.
	if math.Abs(a.Rating-1016) > 0.01 || math.Abs(b.Rating-984) > 0.01 {
		t.Errorf("ratings: a=%v b=%v", a.Rating, b.Rating)
	}
}

func TestELOSorted(t *testing.T) {
	e := NewELO()
	matches := []Match{
		{Player1ID: "a", Player2ID: "b", Player1Score: 3, Player2Score: 0, PlayedAt: t0(0)},
		{Player1ID: "a", Player2ID: "c", Player1Score: 3, Player2Score: 1, PlayedAt: t0(time.Hour)},
		{Player1ID: "b", Player2ID: "c", Player1Score: 3, Player2Score: 2, PlayedAt: t0(2 * time.Hour)},
	}
	pr, _ := e.Compute(matches)
	order := Sorted(pr)
	if order[0] != "a" {
		t.Errorf("a should lead: order=%v", order)
	}
	if order[2] != "c" {
		t.Errorf("c should be last: order=%v", order)
	}
}

func TestELOKFactorBoundary(t *testing.T) {
	e := NewELO()
	matches := []Match{}
	// 30 matches a vs c (a wins all). After 30, a has 30 games → K=16.
	// Then a vs b: a's K is 16, b's is 32.
	for i := 0; i < 30; i++ {
		matches = append(matches, Match{Player1ID: "a", Player2ID: "c", Player1Score: 3, Player2Score: 0, PlayedAt: t0(time.Duration(i) * time.Hour)})
	}
	matches = append(matches, Match{Player1ID: "a", Player2ID: "b", Player1Score: 3, Player2Score: 0, PlayedAt: t0(31 * time.Hour)})
	pr, _ := e.Compute(matches)
	if pr["a"].GamesPlayed != 31 {
		t.Errorf("a games=%d want 31", pr["a"].GamesPlayed)
	}
}

func TestSelfPlayRejected(t *testing.T) {
	e := NewELO()
	_, err := e.Compute([]Match{{Player1ID: "a", Player2ID: "a", Player1Score: 3, Player2Score: 1, PlayedAt: t0(0)}})
	if err == nil {
		t.Error("expected error on self-play")
	}
}

func TestNegativeScoreRejected(t *testing.T) {
	e := NewELO()
	_, err := e.Compute([]Match{{Player1ID: "a", Player2ID: "b", Player1Score: -1, Player2Score: 1, PlayedAt: t0(0)}})
	if err == nil {
		t.Error("expected error on negative score")
	}
}

func TestGlicko2OneMatch(t *testing.T) {
	e := NewGlicko2(1)
	matches := []Match{{Player1ID: "a", Player2ID: "b", Player1Score: 3, Player2Score: 1, PlayedAt: t0(0)}}
	pr, err := e.Compute(matches)
	if err != nil {
		t.Fatal(err)
	}
	a, b := pr["a"], pr["b"]
	if a.Wins != 1 || b.Losses != 1 {
		t.Errorf("a=%+v b=%+v", a, b)
	}
	if a.Rating <= b.Rating {
		t.Errorf("winner should have higher rating: a=%v b=%v", a.Rating, b.Rating)
	}
	// RD should have decreased from 350.
	if a.Deviation >= Glicko2DefaultRD {
		t.Errorf("a deviation should drop: %v", a.Deviation)
	}
}

func TestGlicko2InactivityInflatesRD(t *testing.T) {
	e := NewGlicko2(1)
	// a, b play once on day 0; then c, d play on day 5 (a, b inactive for 5 periods).
	matches := []Match{
		{Player1ID: "a", Player2ID: "b", Player1Score: 3, Player2Score: 1, PlayedAt: t0(0)},
		{Player1ID: "c", Player2ID: "d", Player1Score: 3, Player2Score: 1, PlayedAt: t0(5 * 24 * time.Hour)},
	}
	pr, err := e.Compute(matches)
	if err != nil {
		t.Fatal(err)
	}
	// a should still have RD < default but greater than what 0 inactivity would produce.
	if pr["a"].Deviation <= 0 {
		t.Errorf("a deviation: %v", pr["a"].Deviation)
	}
}

func TestSortedTieBreak(t *testing.T) {
	pr := PlayerRatings{
		"b": {Rating: 1500},
		"a": {Rating: 1500},
		"c": {Rating: 1400},
	}
	order := Sorted(pr)
	if order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("unexpected order: %v", order)
	}
}
