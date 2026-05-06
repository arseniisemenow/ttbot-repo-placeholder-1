package rating

import "math"

type eloEngine struct{}

// NewELO returns an ELO engine.
func NewELO() Engine { return eloEngine{} }

func (eloEngine) ID() string { return "elo" }

// Compute applies ELO updates per match in chronological order.
func (eloEngine) Compute(matches []Match) (PlayerRatings, error) {
	sorted, err := validateAndSort(matches)
	if err != nil {
		return nil, err
	}

	pr := make(PlayerRatings)
	get := func(id string) Rating {
		r, ok := pr[id]
		if !ok {
			r = Rating{Rating: ELODefault}
		}
		return r
	}

	for _, m := range sorted {
		ra, rb := get(m.Player1ID), get(m.Player2ID)
		ka := eloK(ra.GamesPlayed)
		kb := eloK(rb.GamesPlayed)

		ea := 1.0 / (1.0 + math.Pow(10, (rb.Rating-ra.Rating)/400.0))
		eb := 1.0 - ea

		var sa, sb float64
		switch {
		case m.Player1Score > m.Player2Score:
			sa, sb = 1, 0
		case m.Player2Score > m.Player1Score:
			sa, sb = 0, 1
		default:
			sa, sb = 0.5, 0.5
		}

		ra.Rating += ka * (sa - ea)
		rb.Rating += kb * (sb - eb)

		applyMeta(&ra, m, m.Player1Score, m.Player2Score, sa)
		applyMeta(&rb, m, m.Player2Score, m.Player1Score, sb)

		pr[m.Player1ID] = ra
		pr[m.Player2ID] = rb
	}
	return pr, nil
}

func eloK(games int) float64 {
	if games < ELOKThreshold {
		return ELOKHigh
	}
	return ELOKLow
}

func applyMeta(r *Rating, m Match, scoreFor, scoreAgainst int, s float64) {
	r.GamesPlayed++
	r.Meta.PointsFor += scoreFor
	r.Meta.PointsAgainst += scoreAgainst
	r.Meta.LastPlayed = m.PlayedAt
	switch {
	case s == 1:
		r.Wins++
		if r.Meta.WinStreak >= 0 {
			r.Meta.WinStreak++
		} else {
			r.Meta.WinStreak = 1
		}
	case s == 0:
		r.Losses++
		if r.Meta.WinStreak <= 0 {
			r.Meta.WinStreak--
		} else {
			r.Meta.WinStreak = -1
		}
	}
	if r.Rating > r.Meta.BestRating {
		r.Meta.BestRating = r.Rating
	}
}
