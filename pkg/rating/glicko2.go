package rating

import (
	"math"
	"time"
)

// glicko2Engine implements Glickman's Glicko-2 rating system. It groups
// matches into rating periods of `periodDays` (UTC, midnight-aligned), then
// runs the Glicko-2 update once per player per period.
type glicko2Engine struct {
	periodDays int
}

// NewGlicko2 returns a Glicko-2 engine with the given rating-period length in
// days (>=1).
func NewGlicko2(periodDays int) Engine {
	if periodDays < 1 {
		periodDays = 1
	}
	return glicko2Engine{periodDays: periodDays}
}

func (glicko2Engine) ID() string { return "glicko2" }

// internal Glicko-2 player state (in scaled units).
type g2Player struct {
	mu, phi, sigma float64
	gamesPlayed    int
	wins, losses   int
	pointsFor      int
	pointsAgainst  int
	lastPlayed     time.Time
	bestRating     float64
	winStreak      int
}

func newG2Player() *g2Player {
	return &g2Player{
		mu:         (Glicko2DefaultRating - 1500) / Glicko2Scale,
		phi:        Glicko2DefaultRD / Glicko2Scale,
		sigma:      Glicko2DefaultVol,
		bestRating: Glicko2DefaultRating,
	}
}

func (g *g2Player) rating() float64    { return g.mu*Glicko2Scale + 1500 }
func (g *g2Player) deviation() float64 { return g.phi * Glicko2Scale }

func (e glicko2Engine) Compute(matches []Match) (PlayerRatings, error) {
	sorted, err := validateAndSort(matches)
	if err != nil {
		return nil, err
	}

	periods := groupByPeriod(sorted, e.periodDays)
	players := map[string]*g2Player{}

	get := func(id string) *g2Player {
		p, ok := players[id]
		if !ok {
			p = newG2Player()
			players[id] = p
		}
		return p
	}

	// Iterate periods in chronological order.
	for _, p := range periods {
		// Bucket per player → opponents played in this period.
		participants := map[string][]g2Opp{}
		// Track meta updates for stats.
		metaUpdates := []func(){}

		for _, m := range p.matches {
			a, b := get(m.Player1ID), get(m.Player2ID)
			var sa, sb float64
			switch {
			case m.Player1Score > m.Player2Score:
				sa, sb = 1, 0
			case m.Player2Score > m.Player1Score:
				sa, sb = 0, 1
			default:
				sa, sb = 0.5, 0.5
			}
			participants[m.Player1ID] = append(participants[m.Player1ID], g2Opp{b.mu, b.phi, sa})
			participants[m.Player2ID] = append(participants[m.Player2ID], g2Opp{a.mu, a.phi, sb})

			capturedM := m
			capturedSA, capturedSB := sa, sb
			metaUpdates = append(metaUpdates, func() {
				ap, bp := get(capturedM.Player1ID), get(capturedM.Player2ID)
				applyG2Meta(ap, capturedM, capturedM.Player1Score, capturedM.Player2Score, capturedSA)
				applyG2Meta(bp, capturedM, capturedM.Player2Score, capturedM.Player1Score, capturedSB)
			})
		}

		// Snapshot pre-update state for everyone present so opponents see the
		// pre-period rating during their own update.
		pre := map[string]g2Player{}
		for id, p := range players {
			pre[id] = *p
		}

		// Active players: update mu, phi, sigma per Glickman 2012.
		for id, opps := range participants {
			pp := pre[id] // pre-update state of this player
			newMu, newPhi, newSigma := glicko2Update(pp.mu, pp.phi, pp.sigma, opps)
			players[id].mu = newMu
			players[id].phi = newPhi
			players[id].sigma = newSigma
		}

		// Inactive players (no matches this period): RD grows by step 6 only.
		for id, p := range players {
			if _, active := participants[id]; active {
				continue
			}
			p.phi = math.Sqrt(p.phi*p.phi + p.sigma*p.sigma)
		}

		// Apply meta after rating updates.
		for _, fn := range metaUpdates {
			fn()
		}

		// BestRating tracking.
		for _, p := range players {
			if r := p.rating(); r > p.bestRating {
				p.bestRating = r
			}
		}
	}

	// Translate to PlayerRatings.
	out := make(PlayerRatings, len(players))
	for id, p := range players {
		out[id] = Rating{
			Rating:      p.rating(),
			Deviation:   p.deviation(),
			Volatility:  p.sigma,
			GamesPlayed: p.gamesPlayed,
			Wins:        p.wins,
			Losses:      p.losses,
			Meta: Meta{
				PointsFor:     p.pointsFor,
				PointsAgainst: p.pointsAgainst,
				WinStreak:     p.winStreak,
				BestRating:    p.bestRating,
				LastPlayed:    p.lastPlayed,
			},
		}
	}
	return out, nil
}

func applyG2Meta(p *g2Player, m Match, scoreFor, scoreAgainst int, s float64) {
	p.gamesPlayed++
	p.pointsFor += scoreFor
	p.pointsAgainst += scoreAgainst
	p.lastPlayed = m.PlayedAt
	switch {
	case s == 1:
		p.wins++
		if p.winStreak >= 0 {
			p.winStreak++
		} else {
			p.winStreak = 1
		}
	case s == 0:
		p.losses++
		if p.winStreak <= 0 {
			p.winStreak--
		} else {
			p.winStreak = -1
		}
	}
}

// g2Opp is one opponent encountered in a rating period.
type g2Opp struct {
	oppMu, oppPhi float64
	score         float64
}

// glicko2Update runs steps 3–8 of Glickman 2012 for one player against a list
// of opponents played in a single rating period.
func glicko2Update(mu, phi, sigma float64, opps []g2Opp) (float64, float64, float64) {
	// Step 3: variance.
	var v float64
	for _, o := range opps {
		g := gFn(o.oppPhi)
		E := eFn(mu, o.oppMu, o.oppPhi)
		v += g * g * E * (1 - E)
	}
	if v == 0 {
		// No data. Just inflate phi per inactivity rule (step 6).
		return mu, math.Sqrt(phi*phi + sigma*sigma), sigma
	}
	v = 1.0 / v

	// Step 4: delta.
	var delta float64
	for _, o := range opps {
		g := gFn(o.oppPhi)
		E := eFn(mu, o.oppMu, o.oppPhi)
		delta += g * (o.score - E)
	}
	delta *= v

	// Step 5: new sigma via iterative algorithm.
	a := math.Log(sigma * sigma)
	tau := Glicko2Tau
	f := func(x float64) float64 {
		ex := math.Exp(x)
		num := ex * (delta*delta - phi*phi - v - ex)
		den := 2 * (phi*phi + v + ex) * (phi*phi + v + ex)
		return num/den - (x-a)/(tau*tau)
	}
	const eps = 1e-6
	A := a
	var B float64
	if delta*delta > phi*phi+v {
		B = math.Log(delta*delta - phi*phi - v)
	} else {
		k := 1.0
		for f(a-k*tau) < 0 {
			k++
		}
		B = a - k*tau
	}
	fA, fB := f(A), f(B)
	for math.Abs(B-A) > eps {
		C := A + (A-B)*fA/(fB-fA)
		fC := f(C)
		if fC*fB <= 0 {
			A, fA = B, fB
		} else {
			fA /= 2
		}
		B, fB = C, fC
	}
	newSigma := math.Exp(A / 2)

	// Step 6: phi*.
	phiStar := math.Sqrt(phi*phi + newSigma*newSigma)

	// Step 7: new phi, new mu.
	newPhi := 1.0 / math.Sqrt(1.0/(phiStar*phiStar)+1.0/v)
	muSum := 0.0
	for _, o := range opps {
		muSum += gFn(o.oppPhi) * (o.score - eFn(mu, o.oppMu, o.oppPhi))
	}
	newMu := mu + newPhi*newPhi*muSum
	return newMu, newPhi, newSigma
}

func gFn(phi float64) float64 {
	return 1.0 / math.Sqrt(1.0+3.0*phi*phi/(math.Pi*math.Pi))
}

func eFn(mu, muJ, phiJ float64) float64 {
	return 1.0 / (1.0 + math.Exp(-gFn(phiJ)*(mu-muJ)))
}

// period buckets matches into [start, start+periodDays) windows aligned to UTC
// midnight.
type period struct {
	start   time.Time
	matches []Match
}

func groupByPeriod(matches []Match, periodDays int) []period {
	if len(matches) == 0 {
		return nil
	}
	dur := time.Duration(periodDays) * 24 * time.Hour
	first := matches[0].PlayedAt.UTC()
	startOfFirst := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.UTC)

	var out []period
	cur := period{start: startOfFirst}
	for _, m := range matches {
		mt := m.PlayedAt.UTC()
		// Advance period boundary while the current match is past it.
		for !mt.Before(cur.start.Add(dur)) {
			out = append(out, cur)
			cur = period{start: cur.start.Add(dur)}
		}
		cur.matches = append(cur.matches, m)
	}
	out = append(out, cur)
	return out
}
