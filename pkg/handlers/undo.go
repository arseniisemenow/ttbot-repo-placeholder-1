package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/validation"
)

// handleUndo implements /undo.
func (h *Handlers) handleUndo(ctx context.Context, m *messenger.Message, args string) error {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil {
		return nil
	}
	if !g.FullyConfigured() || m.MessageThreadID != g.MatchesTopicID {
		return nil
	}

	// Match-ID extraction: from args, then from the replied message text.
	var (
		mid uint64
		ok  bool
	)
	if mid, ok = validation.ExtractMatchID(args); !ok {
		if m.ReplyTo != nil {
			mid, ok = validation.ExtractMatchID(m.ReplyTo.Text)
		}
	}
	if !ok {
		return h.reply(ctx, m, "Could not find match ID. Use /undo #<id> or reply to a match message.")
	}

	match, err := h.Store.Matches().Get(ctx, g.GroupID, mid)
	if err != nil {
		return h.reply(ctx, m, fmt.Sprintf("Match #%d not found in this group.", mid))
	}

	// Authorization: participant or campus admin.
	isParticipant := match.Player1ID == m.From.ID || match.Player2ID == m.From.ID
	isAdmin := false
	if a, err := h.Store.Admins().Get(ctx, m.From.ID); err == nil && a.CampusID == g.CampusID {
		isAdmin = true
	}
	if !isParticipant && !isAdmin {
		return h.reply(ctx, m, "Error: Only match participants or group admins can undo matches.")
	}
	// Only APPROVED or UNDONE can be toggled.
	if match.Status != models.MatchStatusApproved && match.Status != models.MatchStatusUndone {
		return h.reply(ctx, m, "Only APPROVED matches can be undone.")
	}

	// Player cancelling own pending undo request.
	existing, _ := h.Store.UndoCommands().ListForMatch(ctx, g.GroupID, mid)
	for _, u := range existing {
		if u.TelegramID == m.From.ID && !isAdmin {
			_ = h.Store.UndoCommands().Delete(ctx, g.GroupID, mid, m.From.ID)
			return h.reply(ctx, m, fmt.Sprintf("Undo request for Match #%d cancelled.", mid))
		}
	}

	now := h.Config.Now()
	if err := h.Store.UndoCommands().Insert(ctx, models.UndoCommand{
		GroupID:     g.GroupID,
		MatchID:     mid,
		TelegramID:  m.From.ID,
		RequestedAt: now,
	}); err != nil {
		return err
	}

	// Toggle conditions.
	if isAdmin {
		return h.toggleMatchAndAnnounce(ctx, m, g, match)
	}
	// Player: need both participants in undo_commands.
	pending, _ := h.Store.UndoCommands().ListForMatch(ctx, g.GroupID, mid)
	have := map[int64]bool{}
	for _, u := range pending {
		have[u.TelegramID] = true
	}
	if have[match.Player1ID] && have[match.Player2ID] {
		return h.toggleMatchAndAnnounce(ctx, m, g, match)
	}

	_ = h.M.SendReaction(ctx, m.Chat.ID, m.MessageID, "👍")
	return h.reply(ctx, m, fmt.Sprintf("Undo requested for Match #%d. Waiting for other player.", mid))
}

func (h *Handlers) toggleMatchAndAnnounce(ctx context.Context, m *messenger.Message, g models.Group, match models.Match) error {
	var newStatus models.MatchStatus
	var verb string
	if match.Status == models.MatchStatusApproved {
		newStatus = models.MatchStatusUndone
		verb = "undone"
	} else {
		newStatus = models.MatchStatusApproved
		verb = "restored"
	}
	if err := h.Store.Matches().UpdateStatus(ctx, g.GroupID, match.MatchID, newStatus); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_ = h.Store.UndoCommands().DeleteForMatch(ctx, g.GroupID, match.MatchID)
	_ = h.M.SendReaction(ctx, m.Chat.ID, m.MessageID, "👍")
	if err := h.reply(ctx, m, fmt.Sprintf("Match #%d %s.", match.MatchID, verb)); err != nil {
		return err
	}
	_ = h.refreshStatsTopic(ctx, g)
	return nil
}

// helper for tests / external callers
func (h *Handlers) joinTokens(args ...string) string {
	return strings.Join(args, " ")
}
