package handlers

import (
	"context"
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

// engineForKey returns the engine for a given key. Each call to refreshStatsTopic
// instantiates fresh engines so Glicko-2 picks up any rating-period changes.
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
		var u models.User
		if id.IsTelegram {
			u, err = h.Store.Users().GetByTelegramUsername(ctx, id.Value)
		} else {
			u, err = h.Store.Users().GetByS21Nickname(ctx, id.Value)
		}
		if err != nil {
			return h.reply(ctx, m, "Player not in rankings yet.")
		}
		target = u.TelegramID
	}
	user, err := h.Store.Users().Get(ctx, target)
	if err != nil || !user.IsVerified() {
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
	idStr := strconv.FormatInt(user.TelegramID, 10)
	eloR, eloHas := eloPR[idStr]
	glR, glHas := glPR[idStr]
	if !eloHas && !glHas {
		return h.reply(ctx, m, fmt.Sprintf("%s\nMatches: 0 | Wins: 0 | Losses: 0 | Win Rate: — | No rated matches yet.", user.DisplayName()))
	}
	// Wins/losses/games are engine-agnostic; pull them from whichever has data.
	chosen := eloR
	if !eloHas {
		chosen = glR
	}
	wr := "—"
	if chosen.Wins+chosen.Losses > 0 {
		wr = fmt.Sprintf("%.0f%%", 100*float64(chosen.Wins)/float64(chosen.Wins+chosen.Losses))
	}
	header := fmt.Sprintf("%s\nMatches: %d | Wins: %d | Losses: %d | Win Rate: %s",
		user.DisplayName(), chosen.GamesPlayed, chosen.Wins, chosen.Losses, wr)
	if eloHas {
		header += fmt.Sprintf("\nELO: %.0f", eloR.Rating)
	}
	if glHas {
		header += fmt.Sprintf("\nGlicko-2: %.0f (RD %.0f)", glR.Rating, glR.Deviation)
	}
	return h.reply(ctx, m, header)
}

// computeRatingsFor reads matches in the group, filters to APPROVED + both
// verified, then runs the given engine on the result.
func (h *Handlers) computeRatingsFor(ctx context.Context, g models.Group, engine rating.Engine) (rating.PlayerRatings, error) {
	matches, err := h.Store.Matches().ListByGroup(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	users := map[int64]models.User{}
	verified := map[int64]bool{}
	for _, mm := range matches {
		for _, id := range []int64{mm.Player1ID, mm.Player2ID} {
			if _, seen := users[id]; seen {
				continue
			}
			u, err := h.Store.Users().Get(ctx, id)
			if err != nil {
				continue
			}
			users[id] = u
			verified[id] = u.IsVerified()
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
		return label + "\n\nNo verified matches yet. Once two verified players play and approve a match, this list updates automatically.", nil
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
		u, _ := h.Store.Users().Get(ctx, uid)
		r := pr[idStr]
		if r.Deviation > 0 {
			sb.WriteString(fmt.Sprintf("%d. %s — %.0f (RD %.0f)\n", i+1, u.DisplayName(), r.Rating, r.Deviation))
		} else {
			sb.WriteString(fmt.Sprintf("%d. %s — %.0f\n", i+1, u.DisplayName(), r.Rating))
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// renderStatsAll produces the stats block for a single engine.
func (h *Handlers) renderStatsAll(ctx context.Context, g models.Group, engine rating.Engine, label string) (string, error) {
	pr, err := h.computeRatingsFor(ctx, g, engine)
	if err != nil {
		return "", err
	}
	if len(pr) == 0 {
		return label + "\n\nNo verified matches yet. Per-player stats will appear here once verified players approve their first match.", nil
	}
	order := rating.Sorted(pr)
	if len(order) > MaxRankings {
		order = order[:MaxRankings]
	}
	var sb strings.Builder
	sb.WriteString(label)
	sb.WriteString("\n\n")
	for _, idStr := range order {
		uid, _ := strconv.ParseInt(idStr, 10, 64)
		u, _ := h.Store.Users().Get(ctx, uid)
		sb.WriteString(formatStatsLine(u.DisplayName(), pr[idStr]))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func formatStatsLine(display string, r rating.Rating) string {
	wr := "—"
	if r.Wins+r.Losses > 0 {
		wr = fmt.Sprintf("%.0f%%", 100*float64(r.Wins)/float64(r.Wins+r.Losses))
	}
	if r.Deviation > 0 {
		return fmt.Sprintf("%s\nMatches: %d | Wins: %d | Losses: %d | Win Rate: %s | Rating: %.0f (RD %.0f)",
			display, r.GamesPlayed, r.Wins, r.Losses, wr, r.Rating, r.Deviation)
	}
	return fmt.Sprintf("%s\nMatches: %d | Wins: %d | Losses: %d | Win Rate: %s | Rating: %.0f",
		display, r.GamesPlayed, r.Wins, r.Losses, wr, r.Rating)
}

// refreshStatsTopic maintains four pinned messages in the stats topic: ELO
// rankings, Glicko-2 rankings, ELO stats, Glicko-2 stats.
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
	eloS, err := h.renderStatsAll(ctx, g, eloEng, "ELO Stats")
	if err != nil {
		return err
	}
	glS, err := h.renderStatsAll(ctx, g, glickoEng, "Glicko-2 Stats")
	if err != nil {
		return err
	}

	dirty := false
	if id, changed, err := h.upsertPinned(ctx, g.GroupID, g.StatsTopicID, g.RankingsELOMessageID, eloR); err != nil {
		return err
	} else if changed {
		g.RankingsELOMessageID = id
		dirty = true
	}
	if id, changed, err := h.upsertPinned(ctx, g.GroupID, g.StatsTopicID, g.RankingsGlickoMessageID, glR); err != nil {
		return err
	} else if changed {
		g.RankingsGlickoMessageID = id
		dirty = true
	}
	if id, changed, err := h.upsertPinned(ctx, g.GroupID, g.StatsTopicID, g.StatsELOMessageID, eloS); err != nil {
		return err
	} else if changed {
		g.StatsELOMessageID = id
		dirty = true
	}
	if id, changed, err := h.upsertPinned(ctx, g.GroupID, g.StatsTopicID, g.StatsGlickoMessageID, glS); err != nil {
		return err
	} else if changed {
		g.StatsGlickoMessageID = id
		dirty = true
	}

	if dirty {
		return h.Store.Groups().Upsert(ctx, g)
	}
	return nil
}

// upsertPinned posts a new pinned message in the given topic if storedID==0,
// or edits the existing message otherwise. Returns the (possibly new) message
// ID and a flag indicating whether the caller should persist it.
func (h *Handlers) upsertPinned(ctx context.Context, groupID, topicID, storedID int64, text string) (int64, bool, error) {
	if storedID == 0 {
		id, err := h.M.SendMessage(ctx, groupID, topicID, text)
		if err != nil {
			return 0, false, err
		}
		_ = h.M.PinMessage(ctx, groupID, id) // pin failures swallowed (permission may be missing)
		return id, true, nil
	}
	_ = h.M.EditMessage(ctx, groupID, storedID, text)
	return storedID, false, nil
}
