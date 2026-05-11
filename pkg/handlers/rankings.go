package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/rating"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/validation"
)

// MaxRankings is the cap on how many players appear in /rankings or stats.
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

// handleRankings replies in the matches topic with both engines' top-100.
func (h *Handlers) handleRankings(ctx context.Context, m *messenger.Message) error {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil || !g.FullyConfigured() || m.MessageThreadID != g.MatchesTopicID {
		return nil
	}
	eloEng, glickoEng := h.buildEngines(ctx)
	eloText, err := h.renderRankings(ctx, g, eloEng, "ELO Rankings")
	if err != nil {
		return err
	}
	glText, err := h.renderRankings(ctx, g, glickoEng, "Glicko-2 Rankings")
	if err != nil {
		return err
	}
	return h.reply(ctx, m, eloText+"\n\n"+glText)
}

// handleStats replies with the caller's (or named user's) stats under both engines.
func (h *Handlers) handleStats(ctx context.Context, m *messenger.Message, args string) error {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil || !g.FullyConfigured() || m.MessageThreadID != g.MatchesTopicID {
		return nil
	}
	target := m.From.ID
	args = strings.TrimSpace(args)
	if args != "" {
		id, err := validation.ParseIdentifier(args)
		if err != nil {
			return h.reply(ctx, m, "Usage: /stats [@user|s21_nickname]")
		}
		if id.IsTelegram {
			p, err := h.Store.Participants().GetByUsername(ctx, g.GroupID, id.Value)
			if err != nil {
				return h.reply(ctx, m, "Player not in rankings yet.")
			}
			target = p.TelegramID
		} else {
			svc := h.Identity()
			if svc == nil {
				return h.reply(ctx, m, "Player not in rankings yet.")
			}
			ius, err := svc.GetUsersByNickname(ctx, id.Value)
			if err != nil || len(ius) == 0 {
				return h.reply(ctx, m, "Player not in rankings yet.")
			}
			target = ius[0].TelegramID
		}
	}
	if !h.isVerified(ctx, target) {
		return h.reply(ctx, m, "Player not in rankings yet.")
	}

	eloEng, glickoEng := h.buildEngines(ctx)
	eloPR, err := h.computeRatingsFor(ctx, g, eloEng)
	if err != nil {
		return err
	}
	glPR, err := h.computeRatingsFor(ctx, g, glickoEng)
	if err != nil {
		return err
	}
	idStr := strconv.FormatInt(target, 10)
	eloR, eloHas := eloPR[idStr]
	glR, glHas := glPR[idStr]
	display := h.displayFor(ctx, g.GroupID, target, "")
	if !eloHas && !glHas {
		return h.reply(ctx, m, fmt.Sprintf("%s\nMatches: 0 | Wins: 0 | Losses: 0 | Win Rate: — | No rated matches yet.", display))
	}
	chosen := eloR
	if !eloHas {
		chosen = glR
	}
	wr := "—"
	if chosen.Wins+chosen.Losses > 0 {
		wr = fmt.Sprintf("%.0f%%", 100*float64(chosen.Wins)/float64(chosen.Wins+chosen.Losses))
	}
	header := fmt.Sprintf("%s\nMatches: %d | Wins: %d | Losses: %d | Win Rate: %s",
		display, chosen.GamesPlayed, chosen.Wins, chosen.Losses, wr)
	if eloHas {
		header += fmt.Sprintf("\nELO: %.0f", eloR.Rating)
	}
	if glHas {
		header += fmt.Sprintf("\nGlicko-2: %.0f (RD %.0f)", glR.Rating, glR.Deviation)
	}
	return h.reply(ctx, m, header)
}

// computeRatingsFor reads matches in the group, filters to APPROVED + both
// players verified (per identity service), then runs the given engine on the
// result.
func (h *Handlers) computeRatingsFor(ctx context.Context, g models.Group, engine rating.Engine) (rating.PlayerRatings, error) {
	matches, err := h.Store.Matches().ListByGroup(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	verified := map[int64]bool{}
	for _, mm := range matches {
		for _, id := range []int64{mm.Player1ID, mm.Player2ID} {
			if _, seen := verified[id]; seen {
				continue
			}
			verified[id] = h.isVerified(ctx, id)
		}
	}
	var input []rating.Match
	for _, mm := range matches {
		if mm.Status != models.MatchStatusApproved {
			continue
		}
		if !verified[mm.Player1ID] || !verified[mm.Player2ID] {
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
		return label + "\n\nNo verified matches yet. Once two registered players play and approve a match, this list updates automatically.", nil
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
		display := h.displayFor(ctx, g.GroupID, uid, "")
		if r.Deviation > 0 {
			sb.WriteString(fmt.Sprintf("%d. %s — %.0f (RD %.0f)\n", i+1, display, r.Rating, r.Deviation))
		} else {
			sb.WriteString(fmt.Sprintf("%d. %s — %.0f\n", i+1, display, r.Rating))
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
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

// renderCombinedStats renders ONE message containing every verified player's
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
		return "Stats\n\nNo verified matches yet. Per-player stats will appear here once verified players approve their first match.", nil
	}
	order := rating.Sorted(eloPR)
	if len(order) > MaxRankings {
		order = order[:MaxRankings]
	}
	var sb strings.Builder
	sb.WriteString("Stats\n")
	for _, idStr := range order {
		uid, _ := strconv.ParseInt(idStr, 10, 64)
		display := h.displayFor(ctx, g.GroupID, uid, "")
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
