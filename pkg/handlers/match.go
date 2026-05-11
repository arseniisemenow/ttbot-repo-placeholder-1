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

// matchPlayer is a token-resolution result: who the player is (telegram_id)
// and how to render them. Note is an optional system-facing note (e.g.
// "ambiguous nickname, picked earliest of 2") that the handler appends to
// the public match message.
type matchPlayer struct {
	TelegramID int64
	Display    string
	Note       string
}

// handleMatch implements /match.
func (h *Handlers) handleMatch(ctx context.Context, m *messenger.Message, args string) error {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil {
		return nil // unknown group — silently ignore
	}
	if !g.FullyConfigured() {
		return h.reply(ctx, m, "Topics not configured. Admin: run /set_matches_topic and /set_stats_topic.")
	}
	if m.MessageThreadID != g.MatchesTopicID {
		return nil // wrong topic — silent ignore per docs
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

	var p1, p2 matchPlayer
	switch len(tokens) {
	case 2:
		// implicit author: caller vs token[0]. Caller's display name comes
		// from the identity service (fallback to @username or "Player <id>").
		p1 = matchPlayer{
			TelegramID: m.From.ID,
			Display:    h.displayFor(ctx, m.From.ID, m.From.Username),
		}
		p2, err = h.resolveMatchToken(ctx, tokens[0])
	case 3:
		p1, err = h.resolveMatchToken(ctx, tokens[0])
		if err == nil {
			p2, err = h.resolveMatchToken(ctx, tokens[1])
		}
	}
	if err != nil {
		return h.reply(ctx, m, err.Error())
	}
	if p1.TelegramID == p2.TelegramID {
		return h.reply(ctx, m, "A player cannot play themselves.")
	}

	// Admin-created → APPROVED immediately, no buttons.
	isAdmin, _ := h.M.IsChatAdmin(ctx, m.Chat.ID, m.From.ID)

	now := h.Config.Now()
	status := models.MatchStatusPending
	if isAdmin {
		status = models.MatchStatusApproved
	}

	matchID, err := h.Store.AllocateAndInsertMatch(ctx, g.GroupID, func(id uint64) models.Match {
		return models.Match{
			GroupID:      g.GroupID,
			MatchID:      id,
			Player1ID:    p1.TelegramID,
			Player2ID:    p2.TelegramID,
			Player1Score: score.P1,
			Player2Score: score.P2,
			RegisteredBy: m.From.ID,
			Status:       status,
			PlayedAt:     now,
			CreatedAt:    now,
		}
	})
	if err != nil {
		return err
	}

	header := fmt.Sprintf("Match #%d ", matchID)
	body := fmt.Sprintf("%s: %s vs %s. Score %d-%d.",
		map[bool]string{true: "registered", false: "pending"}[isAdmin],
		p1.Display, p2.Display,
		score.P1, score.P2)
	text := header + body
	if p1.Note != "" {
		text += "\n" + p1.Note
	}
	if p2.Note != "" {
		text += "\n" + p2.Note
	}

	if isAdmin {
		_, err := h.M.SendMessage(ctx, g.GroupID, g.MatchesTopicID, text)
		if err != nil {
			return err
		}
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
	_, err = h.M.SendKeyboard(ctx, g.GroupID, g.MatchesTopicID,
		text+"\nWon't affect ratings until both players are verified.",
		"Confirm", cbConfirmPrefix+cb, "Cancel", cbCancelPrefix+cb)
	return err
}

// resolveMatchToken turns an @username or bare S21 nickname into a
// matchPlayer (telegram_id + display name), or returns a user-facing error.
func (h *Handlers) resolveMatchToken(ctx context.Context, token string) (matchPlayer, error) {
	id, err := validation.ParseIdentifier(token)
	if err != nil {
		return matchPlayer{}, fmt.Errorf("invalid identifier: %s", token)
	}
	if id.IsTelegram {
		// Look up by global users table — populated when the user DM'd /start
		// or joined a registered group.
		u, err := h.Store.Users().GetByTelegramUsername(ctx, id.Value)
		if err != nil {
			return matchPlayer{}, fmt.Errorf("@%s is not known to me. Ask them to DM /start first.", id.Value)
		}
		return matchPlayer{
			TelegramID: u.TelegramID,
			Display:    h.displayFor(ctx, u.TelegramID, id.Value),
		}, nil
	}
	// Bare nickname → identity service.
	svc := h.Identity()
	if svc == nil {
		return matchPlayer{}, errors.New("Identity service not available yet. Admin must run /admin first.")
	}
	users, err := svc.GetUsersByNickname(ctx, id.Value)
	if err != nil {
		return matchPlayer{}, fmt.Errorf("Identity service error: %v", err)
	}
	if len(users) == 0 {
		return matchPlayer{}, fmt.Errorf(
			"Nickname %s not registered. Ask user to /provide_nickname %s in @school_21_identity_bot.",
			id.Value, id.Value)
	}
	chosen := users[0]
	mp := matchPlayer{
		TelegramID: chosen.TelegramID,
		Display:    chosen.Nickname,
	}
	if len(users) > 1 {
		mp.Note = fmt.Sprintf("Note: nickname %s is claimed by %d telegram accounts; picked the earliest.",
			id.Value, len(users))
	}
	return mp, nil
}

// displayFor returns the best human label for a telegram_id. Order: identity
// nickname (if found), then @username (if non-empty), then "Player <id>".
func (h *Handlers) displayFor(ctx context.Context, telegramID int64, fallbackUsername string) string {
	if svc := h.Identity(); svc != nil {
		if iu, err := svc.GetByTelegram(ctx, telegramID); err == nil && iu.Found {
			return iu.Nickname
		}
	}
	if fallbackUsername != "" {
		return "@" + fallbackUsername
	}
	// Try users-table username.
	if u, err := h.Store.Users().Get(ctx, telegramID); err == nil && u.TelegramUsername != "" {
		return "@" + u.TelegramUsername
	}
	return fmt.Sprintf("Player %d", telegramID)
}

// isVerified reports whether the given telegram_id should contribute to
// rankings and stats. With identity-service mode this means "the user has a
// nickname registered". Returns false when there is no identity service yet
// (no admin has registered creds) so rankings stay empty rather than blowing
// up.
func (h *Handlers) isVerified(ctx context.Context, telegramID int64) bool {
	svc := h.Identity()
	if svc == nil {
		return false
	}
	u, err := svc.GetByTelegram(ctx, telegramID)
	if err != nil {
		return false
	}
	return u.Found
}

// dispatchCallback handles inline-keyboard taps.
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
	if q.From.ID != match.Player1ID && q.From.ID != match.Player2ID && !h.isChatAdmin(ctx, gid, q.From.ID) {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if match.Status != models.MatchStatusPending {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	_ = h.Store.MatchConfirmations().Insert(ctx, models.MatchConfirmation{
		GroupID:     gid,
		MatchID:     mid,
		TelegramID:  q.From.ID,
		ConfirmedAt: h.Config.Now(),
	})

	g, _ := h.Store.Groups().Get(ctx, gid)
	// Telegram-chat admin participant: a single tap auto-approves.
	if h.isChatAdmin(ctx, gid, q.From.ID) {
		_ = h.Store.Matches().UpdateStatus(ctx, gid, mid, models.MatchStatusApproved)
		_ = h.M.EditKeyboard(ctx, q.Message.Chat.ID, q.Message.MessageID,
			renderApproved(match), nil)
		_ = h.refreshStatsTopic(ctx, g)
		return h.M.AnswerCallback(ctx, q.ID, "Approved by admin")
	}

	confs, _ := h.Store.MatchConfirmations().ListForMatch(ctx, gid, mid)
	confirmedSet := map[int64]bool{}
	for _, c := range confs {
		confirmedSet[c.TelegramID] = true
	}
	if confirmedSet[match.Player1ID] && confirmedSet[match.Player2ID] {
		_ = h.Store.Matches().UpdateStatus(ctx, gid, mid, models.MatchStatusApproved)
		_ = h.M.EditKeyboard(ctx, q.Message.Chat.ID, q.Message.MessageID,
			renderApproved(match), nil)
		_ = h.refreshStatsTopic(ctx, g)
		return h.M.AnswerCallback(ctx, q.ID, "Confirmed")
	}
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
	if q.From.ID != match.Player1ID && q.From.ID != match.Player2ID && !h.isChatAdmin(ctx, gid, q.From.ID) {
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

// isChatAdmin is a thin wrapper that swallows transport errors so callers can
// branch on a bool. Errors are treated as "not admin" — fail-closed.
func (h *Handlers) isChatAdmin(ctx context.Context, chatID, userID int64) bool {
	ok, err := h.M.IsChatAdmin(ctx, chatID, userID)
	if err != nil {
		return false
	}
	return ok
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
