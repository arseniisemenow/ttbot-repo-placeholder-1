package handlers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
)

// Interactive /match: typing "/match" with no args in the matches topic
// opens a two-step picker (opponent → score). The inline-args form
// (/match @opp 3-1, /match @p1 @p2 3-1) still works unchanged.
//
// State lives in the message text — one header line, parsed back on each
// callback. Stateless: nothing in DB beyond the regular match row created
// at Confirm time.
//
// Header shapes:
//   [MATCH_OP=opp owner=<tid> page=<n>]
//   [MATCH_OP=score owner=<tid> opp=<opp_tid> p1=<0..9|-> p2=<0..9|->]
//
// Callback payload shapes (all prefixed `m:i:`):
//   m:i:opp:<opp_tid>     opponent picked
//   m:i:nav:<page>        opponent-picker page navigation (page is 1-indexed)
//   m:i:s:<side>:<value>  score cell tapped (side in {1,2}, value 0..9)
//   m:i:confirm           confirm
//   m:i:cancel            cancel
//   m:i:back              from score picker back to opponent picker
//
// "Only the owner can drive" is enforced by comparing q.From.ID against the
// owner=<tid> field. Other taps get a silent AnswerCallback.

const (
	miPrefix      = "m:i:"
	oppPerPage    = 15
	oppGridCols   = 3
	scoreMaxVal   = 9 // score range 0..9 (10 cells per column)
	unselectedTok = "-"
)

var (
	matchOppHeaderRe   = regexp.MustCompile(`^\[MATCH_OP=opp owner=(\d+) page=(\d+)\]`)
	matchScoreHeaderRe = regexp.MustCompile(`^\[MATCH_OP=score owner=(\d+) opp=(\d+) p1=([0-9-]) p2=([0-9-])\]`)
)

// startInteractiveMatch posts the opponent picker. Called from handleMatch
// when /match arrives with no args.
func (h *Handlers) startInteractiveMatch(ctx context.Context, m *messenger.Message, g models.Group) error {
	text, rows, err := h.renderOpponentPicker(ctx, g.GroupID, m.From.ID, 1)
	if err != nil {
		return h.reply(ctx, m, "Couldn't build opponent list: "+err.Error())
	}
	if rows == nil {
		// No candidates at all — caller is alone in the group.
		return h.reply(ctx, m, "No other participants yet. Wait for someone else to /ping or send a command in this group, then try again.")
	}
	_, err = h.M.SendKeyboardGrid(ctx, g.GroupID, g.MatchesTopicID, text, rows)
	return err
}

// renderOpponentPicker builds the opp-picker text + grid for a given page.
// Returns (nil, nil) text/rows when there are zero candidates (caller alone).
func (h *Handlers) renderOpponentPicker(ctx context.Context, groupID, ownerID int64, page int) (string, [][]messenger.Button, error) {
	candidates, err := h.opponentCandidates(ctx, groupID, ownerID)
	if err != nil {
		return "", nil, err
	}
	if len(candidates) == 0 {
		return "", nil, nil
	}
	if page < 1 {
		page = 1
	}
	totalPages := (len(candidates) + oppPerPage - 1) / oppPerPage
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * oppPerPage
	end := start + oppPerPage
	if end > len(candidates) {
		end = len(candidates)
	}
	pageRows := candidates[start:end]

	header := fmt.Sprintf("[MATCH_OP=opp owner=%d page=%d]", ownerID, page)
	body := fmt.Sprintf("Page %d/%d — pick your opponent:", page, totalPages)
	text := header + "\n" + body

	// 3 columns × up to 5 rows of opponents.
	rows := [][]messenger.Button{}
	for i := 0; i < len(pageRows); i += oppGridCols {
		row := []messenger.Button{}
		for j := 0; j < oppGridCols && i+j < len(pageRows); j++ {
			c := pageRows[i+j]
			row = append(row, messenger.Button{
				Label:    c.label,
				Callback: fmt.Sprintf("%sopp:%d", miPrefix, c.telegramID),
			})
		}
		rows = append(rows, row)
	}
	// Nav row.
	nav := []messenger.Button{}
	if totalPages > 1 {
		prevLabel, prevCB := "·", miPrefix+"noop"
		if page > 1 {
			prevLabel = "⬅ Prev"
			prevCB = fmt.Sprintf("%snav:%d", miPrefix, page-1)
		}
		nav = append(nav, messenger.Button{Label: prevLabel, Callback: prevCB})
	}
	nav = append(nav, messenger.Button{Label: "✕ Cancel", Callback: miPrefix + "cancel"})
	if totalPages > 1 {
		nextLabel, nextCB := "·", miPrefix+"noop"
		if page < totalPages {
			nextLabel = "Next ➡"
			nextCB = fmt.Sprintf("%snav:%d", miPrefix, page+1)
		}
		nav = append(nav, messenger.Button{Label: nextLabel, Callback: nextCB})
	}
	rows = append(rows, nav)
	return text, rows, nil
}

// opponentCandidate is one resolved row in the opponent picker.
type opponentCandidate struct {
	telegramID int64
	label      string
	matchCount int
}

// opponentCandidates returns every participant of `groupID` other than the
// caller, sorted by total matches in this group (desc), then telegram_id
// asc as a stable tiebreak. The caller is filtered out so self-play is
// impossible from the UI.
func (h *Handlers) opponentCandidates(ctx context.Context, groupID, callerID int64) ([]opponentCandidate, error) {
	ps, err := h.Store.Participants().ListByGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	counts, err := h.Store.Matches().CountsByPlayer(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]opponentCandidate, 0, len(ps))
	for _, p := range ps {
		if p.TelegramID == callerID {
			continue
		}
		label := p.TelegramUsername
		if label == "" {
			label = fmt.Sprintf("id %d", p.TelegramID)
		} else {
			label = "@" + label
		}
		out = append(out, opponentCandidate{
			telegramID: p.TelegramID,
			label:      label,
			matchCount: counts[p.TelegramID],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].matchCount != out[j].matchCount {
			return out[i].matchCount > out[j].matchCount
		}
		return out[i].telegramID < out[j].telegramID
	})
	return out, nil
}

// renderScorePicker builds the score-picker text + 2-column grid given the
// current selection state. p1/p2 are -1 for "not yet selected".
func (h *Handlers) renderScorePicker(ctx context.Context, groupID, ownerID, oppID int64, p1, p2 int) (string, [][]messenger.Button) {
	oppLabel := h.playerLabel(ctx, groupID, oppID)
	ownerLabel := h.playerLabel(ctx, groupID, ownerID)

	header := fmt.Sprintf("[MATCH_OP=score owner=%d opp=%d p1=%s p2=%s]",
		ownerID, oppID, scoreTok(p1), scoreTok(p2))
	body := fmt.Sprintf(
		"%s vs %s\n\nPick scores — left column is yours, right is %s. Tap to (re-)select.",
		ownerLabel, oppLabel, oppLabel)
	text := header + "\n" + body

	// Build score cells with a "•" marker on the current selection.
	rows := [][]messenger.Button{}
	for v := 0; v <= scoreMaxVal; v++ {
		left := messenger.Button{
			Label:    cellLabel(v, v == p1),
			Callback: fmt.Sprintf("%ss:1:%d", miPrefix, v),
		}
		right := messenger.Button{
			Label:    cellLabel(v, v == p2),
			Callback: fmt.Sprintf("%ss:2:%d", miPrefix, v),
		}
		rows = append(rows, []messenger.Button{left, right})
	}
	rows = append(rows, []messenger.Button{
		{Label: "✅ Confirm", Callback: miPrefix + "confirm"},
		{Label: "✕ Cancel", Callback: miPrefix + "cancel"},
	})
	rows = append(rows, []messenger.Button{
		{Label: "⬅ Back to opponent", Callback: miPrefix + "back"},
	})
	return text, rows
}

func cellLabel(v int, selected bool) string {
	if selected {
		return fmt.Sprintf("• %d •", v)
	}
	return strconv.Itoa(v)
}

func scoreTok(v int) string {
	if v < 0 {
		return unselectedTok
	}
	return strconv.Itoa(v)
}

func parseScoreTok(s string) int {
	if s == unselectedTok {
		return -1
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return v
}

// handleMatchInteractiveCallback is the entry point for every `m:i:*`
// callback. dispatched from match.go's dispatchCallback.
func (h *Handlers) handleMatchInteractiveCallback(ctx context.Context, q *messenger.CallbackQuery, payload string) error {
	if q.Message == nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	gID := q.Message.Chat.ID

	// Parse owner from whichever header is in the message.
	owner, _, scoreMode, oppID, p1, p2 := parseInteractiveHeader(q.Message.Text)
	if owner == 0 {
		// Stale message (was a /match prompt but we can't read it). Just ack.
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if q.From == nil || q.From.ID != owner {
		return h.M.AnswerCallback(ctx, q.ID, "Only the person who ran /match can use these buttons.")
	}

	g, err := h.Store.Groups().Get(ctx, gID)
	if err != nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}

	switch {
	case payload == "cancel":
		_ = h.M.EditMessage(ctx, gID, q.Message.MessageID, "/match cancelled.")
		return h.M.AnswerCallback(ctx, q.ID, "Cancelled")

	case payload == "noop":
		return h.M.AnswerCallback(ctx, q.ID, "")

	case strings.HasPrefix(payload, "nav:"):
		page, err := strconv.Atoi(strings.TrimPrefix(payload, "nav:"))
		if err != nil {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		text, rows, err := h.renderOpponentPicker(ctx, gID, owner, page)
		if err != nil || rows == nil {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		_ = h.M.EditKeyboardGrid(ctx, gID, q.Message.MessageID, text, rows)
		return h.M.AnswerCallback(ctx, q.ID, "")

	case strings.HasPrefix(payload, "opp:"):
		tid, err := strconv.ParseInt(strings.TrimPrefix(payload, "opp:"), 10, 64)
		if err != nil || tid == owner {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		text, rows := h.renderScorePicker(ctx, gID, owner, tid, -1, -1)
		_ = h.M.EditKeyboardGrid(ctx, gID, q.Message.MessageID, text, rows)
		return h.M.AnswerCallback(ctx, q.ID, "")

	case payload == "back":
		if !scoreMode {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		text, rows, err := h.renderOpponentPicker(ctx, gID, owner, 1)
		if err != nil || rows == nil {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		_ = h.M.EditKeyboardGrid(ctx, gID, q.Message.MessageID, text, rows)
		return h.M.AnswerCallback(ctx, q.ID, "")

	case strings.HasPrefix(payload, "s:"):
		if !scoreMode {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		parts := strings.SplitN(strings.TrimPrefix(payload, "s:"), ":", 2)
		if len(parts) != 2 {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		side, err1 := strconv.Atoi(parts[0])
		val, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || val < 0 || val > scoreMaxVal {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		switch side {
		case 1:
			p1 = val
		case 2:
			p2 = val
		default:
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		text, rows := h.renderScorePicker(ctx, gID, owner, oppID, p1, p2)
		_ = h.M.EditKeyboardGrid(ctx, gID, q.Message.MessageID, text, rows)
		return h.M.AnswerCallback(ctx, q.ID, "")

	case payload == "confirm":
		if !scoreMode {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		if p1 < 0 || p2 < 0 {
			return h.M.AnswerCallback(ctx, q.ID, "Pick a score for both players first.")
		}
		if p1 == p2 {
			return h.M.AnswerCallback(ctx, q.ID, "Score must have a winner.")
		}
		// Register the match using the same code path as the inline form.
		summary, err := h.registerInteractiveMatch(ctx, g, owner, oppID, uint32(p1), uint32(p2))
		if err != nil {
			return h.M.AnswerCallback(ctx, q.ID, "Registration failed: "+truncate(err.Error(), 180))
		}
		_ = h.M.EditMessage(ctx, gID, q.Message.MessageID, summary)
		return h.M.AnswerCallback(ctx, q.ID, "Match registered")
	}
	return h.M.AnswerCallback(ctx, q.ID, "")
}

// parseInteractiveHeader inspects the first line of the message text and
// returns (owner, page, scoreMode, oppID, p1, p2). scoreMode is true when
// the message is in score-picker state. On unknown shapes returns owner=0.
func parseInteractiveHeader(text string) (owner int64, page int, scoreMode bool, oppID int64, p1, p2 int) {
	p1, p2 = -1, -1
	if m := matchOppHeaderRe.FindStringSubmatch(text); m != nil {
		owner, _ = strconv.ParseInt(m[1], 10, 64)
		page, _ = strconv.Atoi(m[2])
		return
	}
	if m := matchScoreHeaderRe.FindStringSubmatch(text); m != nil {
		scoreMode = true
		owner, _ = strconv.ParseInt(m[1], 10, 64)
		oppID, _ = strconv.ParseInt(m[2], 10, 64)
		p1 = parseScoreTok(m[3])
		p2 = parseScoreTok(m[4])
		return
	}
	return 0, 0, false, 0, -1, -1
}

// registerInteractiveMatch is the Confirm-tap path. Mirrors handleMatch's
// allocate-insert-render flow, minus the typed-args parsing.
func (h *Handlers) registerInteractiveMatch(ctx context.Context, g models.Group, ownerID, oppID int64, p1Score, p2Score uint32) (string, error) {
	if ownerID == oppID {
		return "", errors.New("self-play not allowed")
	}
	isAdmin, _ := h.M.IsChatAdmin(ctx, g.GroupID, ownerID)
	now := h.Config.Now()
	status := models.MatchStatusPending
	if isAdmin {
		status = models.MatchStatusApproved
	}
	matchID, err := h.Store.AllocateAndInsertMatch(ctx, g.GroupID, func(id uint64) models.Match {
		return models.Match{
			GroupID:      g.GroupID,
			MatchID:      id,
			Player1ID:    ownerID,
			Player2ID:    oppID,
			Player1Score: p1Score,
			Player2Score: p2Score,
			RegisteredBy: ownerID,
			Status:       status,
			PlayedAt:     now,
			CreatedAt:    now,
		}
	})
	if err != nil {
		return "", err
	}
	verb := "pending"
	if isAdmin {
		verb = "registered"
	}
	text := h.renderMatch(ctx, g.GroupID, models.Match{
		MatchID:      matchID,
		Player1ID:    ownerID,
		Player2ID:    oppID,
		Player1Score: p1Score,
		Player2Score: p2Score,
	}, verb)
	if isAdmin {
		// Admin path: keep the keyboard message as the summary; refresh stats.
		_ = h.refreshStatsTopic(ctx, g)
		return text, nil
	}
	// Non-admin: record the author's auto-confirm and post a *new* message
	// with Confirm/Cancel buttons (the original keyboard message becomes the
	// audit-trail "you started /match" stub).
	_ = h.Store.MatchConfirmations().Insert(ctx, models.MatchConfirmation{
		GroupID:     g.GroupID,
		MatchID:     matchID,
		TelegramID:  ownerID,
		ConfirmedAt: now,
	})
	cb := fmt.Sprintf("%d:%d", g.GroupID, matchID)
	if _, err := h.M.SendKeyboard(ctx, g.GroupID, g.MatchesTopicID, text,
		"Confirm", cbConfirmPrefix+cb, "Cancel", cbCancelPrefix+cb); err != nil {
		return text, err
	}
	return text, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
