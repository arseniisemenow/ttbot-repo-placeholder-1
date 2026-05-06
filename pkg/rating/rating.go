// Package rating implements the ELO and Glicko-2 rating engines, with a shared
// Engine interface. Ratings are never persisted by the bot; this package
// recomputes them from match history on every call.
package rating

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Match is one recorded game between two players.
type Match struct {
	Player1ID    string
	Player2ID    string
	Player1Score int
	Player2Score int
	PlayedAt     time.Time
}

// Rating is the per-player result of Compute.
type Rating struct {
	Rating      float64    // ELO rating, or Glicko-2 mu
	Deviation   float64    // Glicko-2 RD; 0 for ELO
	Volatility  float64    // Glicko-2 volatility; 0 for ELO
	GamesPlayed int
	Wins        int
	Losses      int
	Meta        Meta
}

// Meta holds extra stats reported alongside rating.
type Meta struct {
	PointsFor     int
	PointsAgainst int
	WinStreak     int
	BestRating    float64
	LastPlayed    time.Time
}

// PlayerRatings maps a player ID to their rating.
type PlayerRatings map[string]Rating

// Engine is the rating-engine interface.
type Engine interface {
	Compute(matches []Match) (PlayerRatings, error)
	ID() string
}

// Defaults.
const (
	ELODefault              = 1000.0
	ELOKHigh                = 32.0
	ELOKLow                 = 16.0
	ELOKThreshold           = 30
	Glicko2DefaultRating    = 1500.0
	Glicko2DefaultRD        = 350.0
	Glicko2DefaultVol       = 0.06
	Glicko2Tau              = 0.5
	Glicko2Scale            = 173.7178
	Glicko2DefaultPeriodDay = 1
)

// Sorted returns players sorted by rating descending. Ties broken by player ID
// ascending for stability.
func Sorted(pr PlayerRatings) []string {
	ids := make([]string, 0, len(pr))
	for id := range pr {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ri, rj := pr[ids[i]].Rating, pr[ids[j]].Rating
		if ri != rj {
			return ri > rj
		}
		return ids[i] < ids[j]
	})
	return ids
}

// New returns the engine for the given ID.
//
// "elo" → ELO (default).
// "glicko2" → Glicko-2 with default rating period of 1 day.
//
// For Glicko-2 with a non-default period, use NewGlicko2(periodDays) directly.
func New(id string) (Engine, error) {
	switch id {
	case "", "elo":
		return NewELO(), nil
	case "glicko2":
		return NewGlicko2(Glicko2DefaultPeriodDay), nil
	default:
		return nil, fmt.Errorf("unknown engine: %q", id)
	}
}

// validateAndSort returns matches sorted by PlayedAt ascending after validating
// scores and player identifiers.
func validateAndSort(matches []Match) ([]Match, error) {
	out := make([]Match, len(matches))
	copy(out, matches)
	for i, m := range out {
		if m.Player1ID == "" || m.Player2ID == "" {
			return nil, fmt.Errorf("match %d: empty player id", i)
		}
		if m.Player1ID == m.Player2ID {
			return nil, fmt.Errorf("match %d: self-play", i)
		}
		if m.Player1Score < 0 || m.Player2Score < 0 {
			return nil, fmt.Errorf("match %d: negative score", i)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PlayedAt.Before(out[j].PlayedAt)
	})
	return out, nil
}

// ErrNoMatches is returned by some helpers when no matches are passed.
var ErrNoMatches = errors.New("no matches")
