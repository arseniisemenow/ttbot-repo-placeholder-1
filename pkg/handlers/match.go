package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	s21account "github.com/arseniisemenow/s21-account-go"

	"github.com/arseniisemenow/ttbot-core/pkg/identity"
	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
	"github.com/arseniisemenow/ttbot-core/pkg/validation"
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
	tGroups := time.Now()
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	perfLog("handleMatch.groupsGet dur=%v", time.Since(tGroups))
	if err != nil {
		return nil // unknown group — silently ignore
	}
	if !g.FullyConfigured() {
		return h.reply(ctx, m, "Topics not configured. Admin: run /set_matches_topic and /set_stats_topic.")
	}
	if m.MessageThreadID != g.MatchesTopicID {
		return h.reply(ctx, m, "/match must be run in the matches topic of this group.")
	}

	// No-args form opens the interactive picker. Typed-args form continues
	// to the inline parser below for backward compat.
	if strings.TrimSpace(args) == "" {
		return h.startInteractiveMatch(ctx, m, g)
	}

	tokens := strings.Fields(args)
	if len(tokens) < 2 || len(tokens) > 3 {
		return h.reply(ctx, m, "Usage: /match [@player1] @player2 <s1>-<s2>, or /match alone for an interactive picker.")
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
			Display:    h.displayFor(ctx, m.Chat.ID, m.From.ID, m.From.Username),
		}
		p2, err = h.resolveMatchToken(ctx, m.Chat.ID, tokens[0])
	case 3:
		p1, err = h.resolveMatchToken(ctx, m.Chat.ID, tokens[0])
		if err == nil {
			p2, err = h.resolveMatchToken(ctx, m.Chat.ID, tokens[1])
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

	verb := "pending"
	if isAdmin {
		verb = "registered"
	}
	text := h.renderMatch(ctx, g.GroupID, models.Match{
		MatchID:      matchID,
		Player1ID:    p1.TelegramID,
		Player2ID:    p2.TelegramID,
		Player1Score: score.P1,
		Player2Score: score.P2,
	}, verb)
	if p1.Note != "" {
		text += "\n" + p1.Note
	}
	if p2.Note != "" {
		text += "\n" + p2.Note
	}

	// Two independent gates can keep a match out of the ratings: it has to be
	// APPROVED (PENDING matches are filtered out), and both players need a
	// nickname registered in the identity service. Surface whichever blockers
	// actually apply at this moment so the author isn't left guessing.
	var notes []string
	if !isAdmin {
		notes = append(notes, "Won't affect ratings until both players confirm (or a chat admin confirms).")
	}
	var missing []string
	if !h.hasNickname(ctx, p1.TelegramID) {
		missing = append(missing, p1.Display)
	}
	if !h.hasNickname(ctx, p2.TelegramID) {
		missing = append(missing, p2.Display)
	}
	if len(missing) > 0 {
		verb := "must provide an S21 nickname"
		if len(missing) > 1 {
			verb = "must each provide an S21 nickname"
		}
		notes = append(notes,
			fmt.Sprintf("%s %s via @school_21_identity_bot for this to count toward ratings.",
				strings.Join(missing, " and "), verb))
	}
	ratingsNote := ""
	if len(notes) > 0 {
		// Blank line + em-dash divider before the notes so the match summary
		// at the top reads cleanly and the auxiliary notes are visually
		// secondary.
		ratingsNote = "\n\n— " + strings.Join(notes, "\n— ")
	}

	if isAdmin {
		_, err := h.M.SendMessage(ctx, g.GroupID, g.MatchesTopicID, text+ratingsNote)
		if err != nil {
			return err
		}
		h.detachedRefreshStatsTopic(g)
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
		text+ratingsNote,
		"Confirm", cbConfirmPrefix+cb, "Cancel", cbCancelPrefix+cb)
	return err
}

// resolveMatchToken turns an @username or bare S21 nickname into a
// matchPlayer (telegram_id + display name), or returns a user-facing error.
//
// `groupID` scopes the @username lookup to a single chat (Telegram has no
// global username-to-id API; we cache (group_id, username) → telegram_id from
// chat_member events).
func (h *Handlers) resolveMatchToken(ctx context.Context, groupID int64, token string) (matchPlayer, error) {
	id, err := validation.ParseIdentifier(token)
	if err != nil {
		return matchPlayer{}, fmt.Errorf("invalid identifier: %s", token)
	}
	if id.IsTelegram {
		p, err := h.Store.Participants().GetByUsername(ctx, groupID, id.Value)
		if err != nil {
			return matchPlayer{}, fmt.Errorf("@%s hasn't joined this group yet — ask them to send /ping in the matches topic so I learn their username.", id.Value)
		}
		return matchPlayer{
			TelegramID: p.TelegramID,
			Display:    h.displayFor(ctx, groupID, p.TelegramID, id.Value),
		}, nil
	}
	// Bare nickname → identity service.
	var users []identity.User
	err = h.withIdentity(ctx, func(svc *identity.Service) error {
		got, err := svc.GetUsersByNickname(ctx, id.Value)
		if err != nil {
			return err
		}
		users = got
		return nil
	})
	switch {
	case errors.Is(err, s21account.ErrNoHealthy):
		return matchPlayer{}, errors.New("No logged-in S21 accounts available. Anyone in this group can run /login in @ttbot (DM).")
	case err != nil:
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
// nickname (if found), then @username (from the per-group participants
// cache, or the supplied fallback), then "Player <id>".
func (h *Handlers) displayFor(ctx context.Context, groupID, telegramID int64, fallbackUsername string) string {
	if nick, ok := h.lookupS21Nickname(ctx, telegramID); ok {
		return nick
	}
	if fallbackUsername != "" {
		return "@" + fallbackUsername
	}
	if p, err := h.Store.Participants().Get(ctx, groupID, telegramID); err == nil && p.TelegramUsername != "" {
		return "@" + p.TelegramUsername
	}
	return fmt.Sprintf("Player %d", telegramID)
}

// hasNickname reports whether the given telegram_id has a nickname registered
// in the identity service. Only players with a nickname contribute to rankings
// and stats. Returns false when there is no identity service yet (no admin has
// registered creds) so rankings stay empty rather than blowing up.
func (h *Handlers) hasNickname(ctx context.Context, telegramID int64) bool {
	_, ok := h.lookupS21Nickname(ctx, telegramID)
	return ok
}

// lookupS21Nickname returns the user's S21 nickname (the school-side
// identifier — distinct from their Telegram @username). The cache fronts
// the identity-service call: hits avoid network entirely, misses fetch and
// populate. A "no S21 nickname registered" result is itself cached so
// repeated lookups for the same telegram_id don't all hit identity.
//
// The bool is true iff the lookup yielded a real registered nickname.
// Errors (identity unavailable, no healthy login) collapse to false — the
// caller's fallbacks (Telegram username, "Player N") cover those cases.
func (h *Handlers) lookupS21Nickname(ctx context.Context, telegramID int64) (string, bool) {
	// L1: in-process LRU. ~ns. Per-container, lost on cold start.
	if u, ok := h.S21Nicks.Get(telegramID); ok {
		perfLog("lookupS21Nickname tid=%d hit=local", telegramID)
		return u.Nickname, u.Found && u.Nickname != ""
	}
	// L2: durable YDB-backed cache. ~tens of ms. Shared across all warm
	// containers and survives recycles. Negative ("found=false") entries
	// are honored so a non-registered telegram_id doesn't keep hitting
	// identity-service on every cold start. On hit, promote to L1.
	if h.Store != nil {
		tDb := time.Now()
		if row, err := h.Store.S21NickCache().Get(ctx, telegramID); err == nil {
			if h.Config.Now().Sub(row.CachedAt) <= s21NickDurableTTL {
				u := identity.User{
					TelegramID:    row.TelegramID,
					Nickname:      row.Nickname,
					CampusID:      row.CampusID,
					CampusName:    row.CampusName,
					CoalitionName: row.CoalitionName,
					Found:         row.Found,
				}
				h.S21Nicks.Put(telegramID, u)
				perfLog("lookupS21Nickname tid=%d hit=durable dur=%v age=%v", telegramID, time.Since(tDb), h.Config.Now().Sub(row.CachedAt).Round(time.Second))
				return u.Nickname, u.Found && u.Nickname != ""
			}
			// Row exists but is past TTL — fall through to identity. Don't
			// delete here; the next successful Upsert overwrites it.
			perfLog("lookupS21Nickname tid=%d durable=stale dur=%v age=%v", telegramID, time.Since(tDb), h.Config.Now().Sub(row.CachedAt).Round(time.Second))
		} else if !errors.Is(err, store.ErrNotFound) {
			// Don't fail the lookup on a cache I/O hiccup — log and fall
			// through to identity. The cache is best-effort.
			log.Printf("s21_nick_cache: durable read failed tid=%d: %v", telegramID, err)
		}
	}
	// L3: identity service round-trip (which itself caches X-S21-Token in
	// its own durable layer). Network call, hundreds of ms typical.
	tIdent := time.Now()
	var got identity.User
	var fetched bool
	h.tryIdentity(ctx, func(svc *identity.Service) error {
		u, err := svc.GetByTelegram(ctx, telegramID)
		if err != nil {
			return err
		}
		got = u
		fetched = true
		return nil
	})
	perfLog("lookupS21Nickname tid=%d hit=remote fetched=%t dur=%v", telegramID, fetched, time.Since(tIdent))
	if !fetched {
		return "", false
	}
	// Write-through to both cache layers so the next request hits L1 (and
	// other containers / cold starts hit L2).
	h.S21Nicks.Put(telegramID, got)
	if h.Store != nil {
		if err := h.Store.S21NickCache().Upsert(ctx, store.S21NickCacheEntry{
			TelegramID:    telegramID,
			Found:         got.Found,
			Nickname:      got.Nickname,
			CampusID:      got.CampusID,
			CampusName:    got.CampusName,
			CoalitionName: got.CoalitionName,
			CachedAt:      h.Config.Now().UTC(),
		}); err != nil {
			log.Printf("s21_nick_cache: durable write failed tid=%d: %v", telegramID, err)
		}
	}
	return got.Nickname, got.Found && got.Nickname != ""
}

// dispatchCallback handles inline-keyboard taps.
func (h *Handlers) dispatchCallback(ctx context.Context, q *messenger.CallbackQuery) error {
	if q == nil || q.From == nil {
		return nil
	}
	data := q.Data
	switch {
	case strings.HasPrefix(data, miPrefix):
		return h.handleMatchInteractiveCallback(ctx, q, strings.TrimPrefix(data, miPrefix))
	case strings.HasPrefix(data, cbConfirmPrefix):
		return h.handleConfirmTap(ctx, q, strings.TrimPrefix(data, cbConfirmPrefix))
	case strings.HasPrefix(data, cbCancelPrefix):
		return h.handleCancelTap(ctx, q, strings.TrimPrefix(data, cbCancelPrefix))
	case strings.HasPrefix(data, cbAddParticipantPrefix):
		return h.handleAddParticipantTap(ctx, q, strings.TrimPrefix(data, cbAddParticipantPrefix))
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
			h.renderMatch(ctx, gid, match, "confirmed"), nil)
		h.detachedRefreshStatsTopic(g)
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
			h.renderMatch(ctx, gid, match, "confirmed"), nil)
		h.detachedRefreshStatsTopic(g)
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

// renderMatch is the canonical formatter for any match-status announcement.
// Every flow that posts a "Match #N <verb>" line (/match, confirmation tap,
// undo, restore) routes through this so the wording and layout stay in one
// place. `verb` is the lifecycle word: "registered", "pending", "confirmed",
// "undone", "restored". The winner is always placed on the left of the
// scoreboard line.
func (h *Handlers) renderMatch(ctx context.Context, groupID int64, m models.Match, verb string) string {
	p1 := h.playerLabel(ctx, groupID, m.Player1ID)
	p2 := h.playerLabel(ctx, groupID, m.Player2ID)
	leftLabel, leftScore := p1, m.Player1Score
	rightLabel, rightScore := p2, m.Player2Score
	if m.Player2Score > m.Player1Score {
		leftLabel, leftScore = p2, m.Player2Score
		rightLabel, rightScore = p1, m.Player1Score
	}
	return fmt.Sprintf("Match #%d %s.\n%s %d — %d %s",
		m.MatchID, verb, leftLabel, leftScore, rightScore, rightLabel)
}

// playerLabel returns "<s21 nickname> (@<telegram username>)" when both are
// available, falling back to whichever is known, or "Player <id>" when
// neither identity nor the participants cache yields anything.
func (h *Handlers) playerLabel(ctx context.Context, groupID, telegramID int64) string {
	nickname, _ := h.lookupS21Nickname(ctx, telegramID)
	username := ""
	if p, err := h.Store.Participants().Get(ctx, groupID, telegramID); err == nil {
		username = p.TelegramUsername
	}
	switch {
	case nickname != "" && username != "":
		return fmt.Sprintf("%s (@%s)", nickname, username)
	case nickname != "":
		return nickname
	case username != "":
		return "@" + username
	default:
		return fmt.Sprintf("Player %d", telegramID)
	}
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
