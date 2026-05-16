package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/rating"
)

// MaxRankings is the cap on how many players appear in the auto-
// maintained stats-topic messages. The /rankings and /stats commands
// that used to surface this data on-demand in the matches topic were
// removed — the auto-maintained pinned messages in the stats topic
// cover the same use case without the duplication.
const MaxRankings = 100

// buildEngines returns fresh ELO + Glicko-2 engines, picking up any
// rating-period change from settings.
func (h *Handlers) buildEngines(ctx context.Context) (eloEng rating.Engine, glickoEng rating.Engine) {
	periodDays := h.Config.RatingPeriodDaysDefault
	if s, err := h.Store.Settings().Get(ctx, "rating_period_days"); err == nil {
		var d int
		_, _ = fmt.Sscanf(s.Value, "%d", &d)
		if d > 0 {
			periodDays = d
		}
	}
	return rating.NewELO(), rating.NewGlicko2(periodDays)
}

// computeRatingsFor reads matches in the group, filters to APPROVED matches
// where both players have an S21 nickname registered (per identity service),
// then runs the given engine on the result.
func (h *Handlers) computeRatingsFor(ctx context.Context, g models.Group, engine rating.Engine) (rating.PlayerRatings, error) {
	matches, err := h.Store.Matches().ListByGroup(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	nicknamed := map[int64]bool{}
	for _, mm := range matches {
		for _, id := range []int64{mm.Player1ID, mm.Player2ID} {
			if _, seen := nicknamed[id]; seen {
				continue
			}
			nicknamed[id] = h.hasNickname(ctx, id)
		}
	}
	var input []rating.Match
	for _, mm := range matches {
		if mm.Status != models.MatchStatusApproved {
			continue
		}
		if !nicknamed[mm.Player1ID] || !nicknamed[mm.Player2ID] {
			continue
		}
		input = append(input, rating.Match{
			Player1ID:    strconv.FormatInt(mm.Player1ID, 10),
			Player2ID:    strconv.FormatInt(mm.Player2ID, 10),
			Player1Score: int(mm.Player1Score),
			Player2Score: int(mm.Player2Score),
			PlayedAt:     mm.PlayedAt,
		})
	}
	return engine.Compute(input)
}

// renderRankings produces the rankings block for a single engine.
func (h *Handlers) renderRankings(ctx context.Context, g models.Group, engine rating.Engine, label string) (string, error) {
	pr, err := h.computeRatingsFor(ctx, g, engine)
	if err != nil {
		return "", err
	}
	if len(pr) == 0 {
		return label + "\n\nNo matches counted yet. Once two players with S21 nicknames play and approve a match, this list updates automatically.", nil
	}
	order := rating.Sorted(pr)
	if len(order) > MaxRankings {
		order = order[:MaxRankings]
	}
	var sb strings.Builder
	sb.WriteString(label)
	sb.WriteString("\n")
	for i, idStr := range order {
		uid, _ := strconv.ParseInt(idStr, 10, 64)
		r := pr[idStr]
		display := h.playerLabel(ctx, g.GroupID, uid)
		if r.Deviation > 0 {
			sb.WriteString(fmt.Sprintf("%d. %s — %.0f (RD %.0f)\n", i+1, display, r.Rating, r.Deviation))
		} else {
			sb.WriteString(fmt.Sprintf("%d. %s — %.0f\n", i+1, display, r.Rating))
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// detachedRefreshStatsTopic kicks off refreshStatsTopic in a goroutine
// with a fresh context, so user-facing handlers can return immediately
// without paying the 2–5 s stats-refresh cost on the critical path.
//
// Yandex Cloud Functions caveat: when the handler returns, the container
// may freeze CPU. The goroutine usually completes before the next
// invocation lands (warm containers stay up 10–15 min) but a kill
// mid-refresh silently drops that update. The user's stats topic catches
// up on the next match registration/confirm/undo in the same group —
// every mutation re-runs the full refresh, so missed refreshes self-heal
// on the next event. If that's not enough, layer a dirty-flag + cron
// safety net later (separate change).
//
// The Group struct is passed by value so the goroutine has a stable
// snapshot — concurrent mutations to the row in YDB don't race with the
// refresh-side reads.
func (h *Handlers) detachedRefreshStatsTopic(g models.Group) {
	h.detachedWG.Add(1)
	go func() {
		defer h.detachedWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := h.refreshStatsTopic(ctx, g); err != nil {
			log.Printf("detached refreshStatsTopic group=%d: %v", g.GroupID, err)
		}
	}()
}

// WaitForDetachedRefreshes blocks until every goroutine spawned by
// detachedRefreshStatsTopic has finished. Production code never calls
// this — the goroutines are fire-and-forget and the function returns
// to Yandex immediately after the user-visible reply. Tests call it
// after each Dispatch so assertions on the stats topic and group row
// see the refresh side effects.
func (h *Handlers) WaitForDetachedRefreshes() {
	h.detachedWG.Wait()
}

// refreshStatsTopic maintains exactly THREE messages in the stats topic.
func (h *Handlers) refreshStatsTopic(ctx context.Context, g models.Group) error {
	if !g.FullyConfigured() {
		return nil
	}
	eloEng, glickoEng := h.buildEngines(ctx)
	eloR, err := h.renderRankings(ctx, g, eloEng, "ELO Rankings")
	if err != nil {
		return err
	}
	glR, err := h.renderRankings(ctx, g, glickoEng, "Glicko-2 Rankings")
	if err != nil {
		return err
	}
	statsText, err := h.renderCombinedStats(ctx, g, eloEng, glickoEng)
	if err != nil {
		return err
	}

	dirty := false

	for _, orphan := range []*int64{&g.RankingsMessageID, &g.StatsELOMessageID, &g.StatsGlickoMessageID} {
		if *orphan != 0 {
			_ = h.M.DeleteMessage(ctx, g.GroupID, *orphan)
			*orphan = 0
			dirty = true
		}
	}

	if id, changed, err := h.upsertMessage(ctx, g.GroupID, g.StatsTopicID, g.RankingsELOMessageID, eloR, false); err != nil {
		return err
	} else if changed {
		g.RankingsELOMessageID = id
		dirty = true
	}
	if id, changed, err := h.upsertMessage(ctx, g.GroupID, g.StatsTopicID, g.RankingsGlickoMessageID, glR, false); err != nil {
		return err
	} else if changed {
		g.RankingsGlickoMessageID = id
		dirty = true
	}
	if id, changed, err := h.upsertMessage(ctx, g.GroupID, g.StatsTopicID, g.StatsMessageID, statsText, true); err != nil {
		return err
	} else if changed {
		g.StatsMessageID = id
		dirty = true
	}

	if dirty {
		return h.Store.Groups().Upsert(ctx, g)
	}
	return nil
}

func (h *Handlers) upsertMessage(ctx context.Context, groupID, topicID, storedID int64, text string, pin bool) (int64, bool, error) {
	if storedID != 0 {
		err := h.M.EditMessage(ctx, groupID, storedID, text)
		if err == nil {
			return storedID, false, nil
		}
		if !errors.Is(err, messenger.ErrNotFound) {
			return storedID, false, nil
		}
	}
	id, err := h.M.SendMessage(ctx, groupID, topicID, text)
	if err != nil {
		return 0, false, err
	}
	if pin {
		_ = h.M.PinMessage(ctx, groupID, id)
	}
	return id, true, nil
}

// renderCombinedStats renders ONE message containing every nicknamed player's
// per-engine ratings side by side, sorted by ELO desc.
func (h *Handlers) renderCombinedStats(ctx context.Context, g models.Group, eloEng, glickoEng rating.Engine) (string, error) {
	eloPR, err := h.computeRatingsFor(ctx, g, eloEng)
	if err != nil {
		return "", err
	}
	glPR, err := h.computeRatingsFor(ctx, g, glickoEng)
	if err != nil {
		return "", err
	}
	if len(eloPR) == 0 && len(glPR) == 0 {
		return "Stats\n\nNo matches counted yet. Per-player stats will appear here once players with S21 nicknames approve their first match.", nil
	}
	order := rating.Sorted(eloPR)
	if len(order) > MaxRankings {
		order = order[:MaxRankings]
	}
	var sb strings.Builder
	sb.WriteString("Stats\n")
	for _, idStr := range order {
		uid, _ := strconv.ParseInt(idStr, 10, 64)
		display := h.playerLabel(ctx, g.GroupID, uid)
		eloR := eloPR[idStr]
		glR, hasGl := glPR[idStr]
		wr := "—"
		if eloR.Wins+eloR.Losses > 0 {
			wr = fmt.Sprintf("%.0f%%", 100*float64(eloR.Wins)/float64(eloR.Wins+eloR.Losses))
		}
		sb.WriteString("\n")
		sb.WriteString(display)
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("Matches: %d | Wins: %d | Losses: %d | Win Rate: %s\n",
			eloR.GamesPlayed, eloR.Wins, eloR.Losses, wr))
		sb.WriteString(fmt.Sprintf("ELO: %.0f", eloR.Rating))
		if hasGl {
			sb.WriteString(fmt.Sprintf("\nGlicko-2: %.0f (RD %.0f)", glR.Rating, glR.Deviation))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
