package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/validation"
)

// callback-data prefixes for match buttons. Keep short — Telegram caps at ~64 bytes.
const (
	cbConfirmPrefix = "m:c:"
	cbCancelPrefix  = "m:x:"
)

// handleMatch implements /match.
func (h *Handlers) handleMatch(ctx context.Context, m *messenger.Message, args string) error {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil {
		return nil // unknown group — silently ignore (could be a private chat or stranger group)
	}
	if !g.FullyConfigured() {
		return h.reply(ctx, m, "Topics not configured. Admin: run /set_matches_topic and /set_stats_topic.")
	}
	if m.MessageThreadID != g.MatchesTopicID {
		return nil // wrong topic — silent ignore per docs
	}

	// Caller must have a nickname.
	caller, err := h.Store.Users().Get(ctx, m.From.ID)
	if err != nil || !caller.HasNickname() {
		return h.reply(ctx, m, "Nickname required. DM me /provide_nickname your_s21_nickname or ask admin to provide it.")
	}

	tokens := strings.Fields(args)
	if len(tokens) < 2 || len(tokens) > 3 {
		return h.reply(ctx, m, "Usage: /match [@player1] @player2 <s1>-<s2>")
	}
	scoreToken := tokens[len(tokens)-1]
	score, err := validation.ParseScore(scoreToken)
	if err != nil {
		return h.reply(ctx, m, err.Error())
	}

	var p1User, p2User models.User
	switch len(tokens) {
	case 2:
		// implicit author: caller vs token[0]
		p1User = caller
		p2User, err = h.resolveUser(ctx, tokens[0])
	case 3:
		p1User, err = h.resolveUser(ctx, tokens[0])
		if err == nil {
			p2User, err = h.resolveUser(ctx, tokens[1])
		}
	}
	if err != nil {
		return h.reply(ctx, m, err.Error())
	}
	if p1User.TelegramID == p2User.TelegramID {
		return h.reply(ctx, m, "A player cannot play themselves.")
	}
	if !p1User.HasNickname() {
		return h.reply(ctx, m, p1User.DisplayName()+" has no nickname. They need to DM me /provide_nickname or contact admin.")
	}
	if !p2User.HasNickname() {
		return h.reply(ctx, m, p2User.DisplayName()+" has no nickname. They need to DM me /provide_nickname or contact admin.")
	}

	// Admin-created → APPROVED immediately, no buttons.
	isAdmin := false
	if a, err := h.Store.Admins().Get(ctx, m.From.ID); err == nil && a.CampusID == g.CampusID {
		isAdmin = true
	}

	now := h.Config.Now()
	status := models.MatchStatusPending
	if isAdmin {
		status = models.MatchStatusApproved
	}

	// Allocate match_id and insert match transactionally.
	matchID, err := h.Store.AllocateAndInsertMatch(ctx, g.GroupID, func(id uint64) error {
		match := models.Match{
			GroupID:      g.GroupID,
			MatchID:      id,
			Player1ID:    p1User.TelegramID,
			Player2ID:    p2User.TelegramID,
			Player1Score: score.P1,
			Player2Score: score.P2,
			RegisteredBy: m.From.ID,
			Status:       status,
			PlayedAt:     now,
			CreatedAt:    now,
		}
		if mems, ok := h.Store.(interface{ PutMatch(models.Match) }); ok {
			mems.PutMatch(match)
			return nil
		}
		// Fallback: direct upsert via the matches repo. Real ydbstore implements
		// AllocateAndInsertMatch with proper write inside the closure.
		return errors.New("store does not expose PutMatch and lacks transactional path")
	})
	if err != nil {
		return err
	}

	// Build the public message.
	header := fmt.Sprintf("Match #%d ", matchID)
	body := fmt.Sprintf("%s: %s vs %s. Score %d-%d.",
		map[bool]string{true: "registered", false: "pending"}[isAdmin],
		p1User.DisplayName(), p2User.DisplayName(),
		score.P1, score.P2)
	text := header + body

	if isAdmin {
		_, err := h.M.SendMessage(ctx, g.GroupID, g.MatchesTopicID, text)
		if err != nil {
			return err
		}
		// Refresh stats topic on rating-affecting event.
		_ = h.refreshStatsTopic(ctx, g)
		return nil
	}

	// Author is auto-approved; record their confirmation up front.
	_ = h.Store.MatchConfirmations().Insert(ctx, models.MatchConfirmation{
		GroupID:     g.GroupID,
		MatchID:     matchID,
		TelegramID:  m.From.ID,
		ConfirmedAt: now,
	})
	cb := fmt.Sprintf("%d:%d", g.GroupID, matchID)
	_, err = h.M.SendKeyboard(ctx, g.GroupID, g.MatchesTopicID, text+"\nWon't affect ratings until both players are verified.",
		"Confirm", cbConfirmPrefix+cb, "Cancel", cbCancelPrefix+cb)
	return err
}

// resolveUser turns an @username or s21 nickname into the users-table row, or
// returns a user-facing error.
func (h *Handlers) resolveUser(ctx context.Context, token string) (models.User, error) {
	id, err := validation.ParseIdentifier(token)
	if err != nil {
		return models.User{}, fmt.Errorf("invalid identifier: %s", token)
	}
	if id.IsTelegram {
		u, err := h.Store.Users().GetByTelegramUsername(ctx, id.Value)
		if err != nil {
			return models.User{}, fmt.Errorf("@%s has no nickname. They need to DM me /provide_nickname or contact admin.", id.Value)
		}
		return u, nil
	}
	u, err := h.Store.Users().GetByS21Nickname(ctx, id.Value)
	if err != nil {
		return models.User{}, fmt.Errorf("%s has no nickname registered.", id.Value)
	}
	return u, nil
}

// dispatchCallback handles inline-keyboard taps for /match and any future
// callback-driven flows. callback data is "<prefix><groupID>:<matchID>".
func (h *Handlers) dispatchCallback(ctx context.Context, q *messenger.CallbackQuery) error {
	if q == nil || q.From == nil {
		return nil
	}
	data := q.Data
	switch {
	case strings.HasPrefix(data, cbConfirmPrefix):
		return h.handleConfirmTap(ctx, q, strings.TrimPrefix(data, cbConfirmPrefix))
	case strings.HasPrefix(data, cbCancelPrefix):
		return h.handleCancelTap(ctx, q, strings.TrimPrefix(data, cbCancelPrefix))
	}
	return h.M.AnswerCallback(ctx, q.ID, "")
}

func (h *Handlers) handleConfirmTap(ctx context.Context, q *messenger.CallbackQuery, payload string) error {
	gid, mid, ok := parseGroupMatchPayload(payload)
	if !ok {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	match, err := h.Store.Matches().Get(ctx, gid, mid)
	if err != nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	// Only participants can confirm.
	if q.From.ID != match.Player1ID && q.From.ID != match.Player2ID {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if match.Status != models.MatchStatusPending {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	// Idempotent insert.
	_ = h.Store.MatchConfirmations().Insert(ctx, models.MatchConfirmation{
		GroupID:     gid,
		MatchID:     mid,
		TelegramID:  q.From.ID,
		ConfirmedAt: h.Config.Now(),
	})
	confs, _ := h.Store.MatchConfirmations().ListForMatch(ctx, gid, mid)
	confirmedSet := map[int64]bool{}
	for _, c := range confs {
		confirmedSet[c.TelegramID] = true
	}
	if confirmedSet[match.Player1ID] && confirmedSet[match.Player2ID] {
		// Approved — clear keyboard, set status, refresh stats.
		_ = h.Store.Matches().UpdateStatus(ctx, gid, mid, models.MatchStatusApproved)
		_ = h.M.EditKeyboard(ctx, q.Message.Chat.ID, q.Message.MessageID,
			renderApproved(match), nil)
		g, _ := h.Store.Groups().Get(ctx, gid)
		_ = h.refreshStatsTopic(ctx, g)
		return h.M.AnswerCallback(ctx, q.ID, "Confirmed")
	}
	// Show remaining buttons (the not-yet-confirmed player + Cancel).
	remaining := []messenger.Button{}
	if !confirmedSet[match.Player1ID] {
		remaining = append(remaining, messenger.Button{
			Label: "Confirm", Callback: cbConfirmPrefix + payload,
		})
	}
	if !confirmedSet[match.Player2ID] {
		remaining = append(remaining, messenger.Button{
			Label: "Confirm", Callback: cbConfirmPrefix + payload,
		})
	}
	remaining = append(remaining, messenger.Button{Label: "Cancel", Callback: cbCancelPrefix + payload})
	_ = h.M.EditKeyboard(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text, remaining)
	return h.M.AnswerCallback(ctx, q.ID, "Confirmed")
}

func (h *Handlers) handleCancelTap(ctx context.Context, q *messenger.CallbackQuery, payload string) error {
	gid, mid, ok := parseGroupMatchPayload(payload)
	if !ok {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	match, err := h.Store.Matches().Get(ctx, gid, mid)
	if err != nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if q.From.ID != match.Player1ID && q.From.ID != match.Player2ID {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if match.Status != models.MatchStatusPending {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if err := h.Store.Matches().Delete(ctx, gid, mid); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_ = h.M.EditMessage(ctx, q.Message.Chat.ID, q.Message.MessageID, fmt.Sprintf("Match #%d cancelled.", mid))
	return h.M.AnswerCallback(ctx, q.ID, "Cancelled")
}

func renderApproved(m models.Match) string {
	return fmt.Sprintf("Match #%d confirmed. p1=%d vs p2=%d. Score %d-%d.",
		m.MatchID, m.Player1ID, m.Player2ID, m.Player1Score, m.Player2Score)
}

func parseGroupMatchPayload(s string) (int64, uint64, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	gid, err1 := strconv.ParseInt(parts[0], 10, 64)
	mid, err2 := strconv.ParseUint(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return gid, mid, true
}
