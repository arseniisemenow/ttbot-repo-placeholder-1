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

// handleRankings replies with the current top-100.
func (h *Handlers) handleRankings(ctx context.Context, m *messenger.Message) error {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil || !g.FullyConfigured() || m.MessageThreadID != g.MatchesTopicID {
		return nil
	}
	text, err := h.renderRankings(ctx, g)
	if err != nil {
		return err
	}
	return h.reply(ctx, m, text)
}

// handleStats replies with stats for caller (/stats) or named user.
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
	prs, err := h.computeRatingsFor(ctx, g)
	if err != nil {
		return err
	}
	r, ok := prs[strconv.FormatInt(user.TelegramID, 10)]
	if !ok {
		return h.reply(ctx, m, fmt.Sprintf("%s\nMatches: 0 | Wins: 0 | Losses: 0 | Win Rate: — | Rating: —", user.DisplayName()))
	}
	return h.reply(ctx, m, formatStatsLine(user.DisplayName(), r))
}

// computeRatingsFor reads all matches in the group, filters by APPROVED + both
// verified, then runs the active engine.
func (h *Handlers) computeRatingsFor(ctx context.Context, g models.Group) (rating.PlayerRatings, error) {
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
	engine, err := h.activeRatingEngine(ctx)
	if err != nil {
		return nil, err
	}
	return engine.Compute(input)
}

func (h *Handlers) renderRankings(ctx context.Context, g models.Group) (string, error) {
	pr, err := h.computeRatingsFor(ctx, g)
	if err != nil {
		return "", err
	}
	if len(pr) == 0 {
		return "Rankings\n\nNo verified matches yet. Once two verified players play and approve a match, this list updates automatically.", nil
	}
	order := rating.Sorted(pr)
	if len(order) > MaxRankings {
		order = order[:MaxRankings]
	}
	var sb strings.Builder
	sb.WriteString("Current Rankings:\n")
	for i, idStr := range order {
		uid, _ := strconv.ParseInt(idStr, 10, 64)
		u, _ := h.Store.Users().Get(ctx, uid)
		sb.WriteString(fmt.Sprintf("%d. %s — %.0f\n", i+1, u.DisplayName(), pr[idStr].Rating))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func (h *Handlers) renderStatsAll(ctx context.Context, g models.Group) (string, error) {
	pr, err := h.computeRatingsFor(ctx, g)
	if err != nil {
		return "", err
	}
	if len(pr) == 0 {
		return "Stats\n\nNo verified matches yet. Per-player stats will appear here once verified players approve their first match.", nil
	}
	order := rating.Sorted(pr)
	if len(order) > MaxRankings {
		order = order[:MaxRankings]
	}
	var sb strings.Builder
	sb.WriteString("Stats:\n")
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

// refreshStatsTopic is a no-op when the group has no stats topic configured.
// Otherwise it edits the existing rankings and stats messages, or posts and
// pins them on first call.
func (h *Handlers) refreshStatsTopic(ctx context.Context, g models.Group) error {
	if !g.FullyConfigured() {
		return nil
	}
	rText, err := h.renderRankings(ctx, g)
	if err != nil {
		return err
	}
	sText, err := h.renderStatsAll(ctx, g)
	if err != nil {
		return err
	}

	// Rankings message.
	if g.RankingsMessageID == 0 {
		id, err := h.M.SendMessage(ctx, g.GroupID, g.StatsTopicID, rText)
		if err != nil {
			return err
		}
		_ = h.M.PinMessage(ctx, g.GroupID, id)
		g.RankingsMessageID = id
	} else {
		_ = h.M.EditMessage(ctx, g.GroupID, g.RankingsMessageID, rText)
	}

	// Stats message.
	if g.StatsMessageID == 0 {
		id, err := h.M.SendMessage(ctx, g.GroupID, g.StatsTopicID, sText)
		if err != nil {
			return err
		}
		_ = h.M.PinMessage(ctx, g.GroupID, id)
		g.StatsMessageID = id
	} else {
		_ = h.M.EditMessage(ctx, g.GroupID, g.StatsMessageID, sText)
	}

	return h.Store.Groups().Upsert(ctx, g)
}
