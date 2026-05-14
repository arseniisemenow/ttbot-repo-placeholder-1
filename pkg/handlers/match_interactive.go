package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
)

// Interactive /match: typing `/match` with no args in the matches topic
// opens a two-step picker (opponent → score). The inline-args form
// (`/match @opp 3-1`, `/match @p1 @p2 3-1`) still works unchanged.
//
// State design — three layers, in order of priority:
//
//   1. **In-memory draft** (matchDraft): authoritative within one warm
//      container. Survives all taps for the duration of one /match flow
//      (~30 s). Each draft carries a per-draft mutex so fast clicks on
//      the same prompt serialize cleanly.
//   2. **Message-text header**: a mirror, parsed on cold start to rebuild
//      the in-memory draft. Includes URL-encoded owner_label / opp_label
//      so score-cell taps never need to re-resolve identity.
//   3. **Identity service**: only consulted when /match first opens (to
//      pre-resolve labels) and at Confirm. Score-cell taps make zero
//      identity calls.
//
// Header shapes:
//
//   [MATCH_OP=opp   owner=<tid> owner_label=<urlenc> page=<n>]
//   [MATCH_OP=score owner=<tid> owner_label=<urlenc> opp=<tid>
//                   opp_label=<urlenc> p1=<0..9|-> p2=<0..9|->]
//
// Callback payload shapes (all prefixed `m:i:`):
//
//   m:i:opp:<opp_tid>     opponent picked
//   m:i:nav:<page>        opponent-picker page navigation (1-indexed)
//   m:i:s:<side>:<value>  score cell tapped (side ∈ {1,2}, value 0..9)
//   m:i:confirm           confirm
//   m:i:cancel            cancel
//   m:i:back              from score picker back to opponent picker
//
// Owner check: every callback's q.From.ID is compared against the draft's
// ownerID. Mismatched taps get a polite toast and no state change.

const (
	miPrefix      = "m:i:"
	oppPerPage    = 15
	oppGridCols   = 3
	scoreMaxVal   = 9 // score range 0..9 → 10 cells per column
	unselectedTok = "-"
)

// matchPerfLogEnabled is set from the TTBOT_MATCH_PERF_LOG env var at
// process start. Off by default to keep production logs clean; flip to "1"
// (or any non-empty value) when investigating latency regressions.
var matchPerfLogEnabled = os.Getenv("TTBOT_MATCH_PERF_LOG") != ""

func perfLog(format string, args ...any) {
	if matchPerfLogEnabled {
		log.Printf("[match-perf] "+format, args...)
	}
}

// matchDraft is one in-flight /match interactive flow.
//
// The mutex serializes mutations from concurrent taps on the same prompt
// (fast clicks). Without it, two taps arriving milliseconds apart would
// both read the same stale state and the later edit would overwrite the
// earlier one — the bug the user reported.
type matchDraft struct {
	mu sync.Mutex

	ownerID    int64
	ownerLabel string

	isScore bool
	page    int // opponent-picker page (1-indexed), only meaningful when !isScore

	oppID    int64
	oppLabel string

	p1, p2 int // -1 = unselected, else 0..9
}

// ---------------- draft storage / restore ----------------

func draftKey(chatID, msgID int64) string {
	return fmt.Sprintf("%d:%d", chatID, msgID)
}

// loadOrRestoreDraft returns the in-memory draft for (chat, msg). If the
// process restarted between taps and the draft is missing from the map,
// it's rebuilt from the message-text header (slower path; one tap pays
// for the cold start). Returns nil only when the message text carries no
// recognizable MATCH_OP header — the caller treats that as "stale prompt,
// ack and ignore".
func (h *Handlers) loadOrRestoreDraft(chatID, msgID int64, msgText string) *matchDraft {
	key := draftKey(chatID, msgID)
	h.matchDraftsMu.RLock()
	if d, ok := h.matchDrafts[key]; ok {
		h.matchDraftsMu.RUnlock()
		return d
	}
	h.matchDraftsMu.RUnlock()

	h.matchDraftsMu.Lock()
	defer h.matchDraftsMu.Unlock()
	if d, ok := h.matchDrafts[key]; ok {
		return d // someone restored concurrently
	}
	d := parseDraftFromHeader(msgText)
	if d == nil {
		return nil
	}
	h.matchDrafts[key] = d
	return d
}

func (h *Handlers) storeDraft(chatID, msgID int64, d *matchDraft) {
	key := draftKey(chatID, msgID)
	h.matchDraftsMu.Lock()
	h.matchDrafts[key] = d
	h.matchDraftsMu.Unlock()
}

func (h *Handlers) dropDraft(chatID, msgID int64) {
	key := draftKey(chatID, msgID)
	h.matchDraftsMu.Lock()
	delete(h.matchDrafts, key)
	h.matchDraftsMu.Unlock()
}

// ---------------- header parsing / rendering ----------------

var (
	matchOppHeaderRe = regexp.MustCompile(
		`^\[MATCH_OP=opp owner=(\d+) owner_label=(\S*) page=(\d+)\]`)
	matchScoreHeaderRe = regexp.MustCompile(
		`^\[MATCH_OP=score owner=(\d+) owner_label=(\S*) opp=(\d+) opp_label=(\S*) p1=([0-9-]) p2=([0-9-])\]`)
)

func parseDraftFromHeader(text string) *matchDraft {
	if m := matchOppHeaderRe.FindStringSubmatch(text); m != nil {
		owner, _ := strconv.ParseInt(m[1], 10, 64)
		ownerLabel, _ := url.QueryUnescape(m[2])
		page, _ := strconv.Atoi(m[3])
		return &matchDraft{
			ownerID:    owner,
			ownerLabel: ownerLabel,
			page:       page,
			p1:         -1, p2: -1,
		}
	}
	if m := matchScoreHeaderRe.FindStringSubmatch(text); m != nil {
		owner, _ := strconv.ParseInt(m[1], 10, 64)
		ownerLabel, _ := url.QueryUnescape(m[2])
		oppID, _ := strconv.ParseInt(m[3], 10, 64)
		oppLabel, _ := url.QueryUnescape(m[4])
		return &matchDraft{
			ownerID:    owner,
			ownerLabel: ownerLabel,
			isScore:    true,
			oppID:      oppID,
			oppLabel:   oppLabel,
			p1:         parseScoreTok(m[5]),
			p2:         parseScoreTok(m[6]),
		}
	}
	return nil
}

func renderOppHeader(d *matchDraft) string {
	return fmt.Sprintf("[MATCH_OP=opp owner=%d owner_label=%s page=%d]",
		d.ownerID, url.QueryEscape(d.ownerLabel), d.page)
}

func renderScoreHeader(d *matchDraft) string {
	return fmt.Sprintf("[MATCH_OP=score owner=%d owner_label=%s opp=%d opp_label=%s p1=%s p2=%s]",
		d.ownerID, url.QueryEscape(d.ownerLabel),
		d.oppID, url.QueryEscape(d.oppLabel),
		scoreTok(d.p1), scoreTok(d.p2))
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

// ---------------- opponent-step rendering ----------------

// opponentCandidate is one resolved row in the opponent picker.
type opponentCandidate struct {
	telegramID int64
	label      string
	matchCount int
}

// opponentCandidates returns every participant of `groupID` other than the
// caller, sorted by total matches in this group (desc), telegram_id asc as
// a stable tiebreak.
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

// renderOpponentPicker builds the opp-picker text + grid. The returned
// (text, rows) reflect the draft's current page.
func (h *Handlers) renderOpponentPicker(ctx context.Context, groupID int64, d *matchDraft) (string, [][]messenger.Button, error) {
	candidates, err := h.opponentCandidates(ctx, groupID, d.ownerID)
	if err != nil {
		return "", nil, err
	}
	if len(candidates) == 0 {
		return "", nil, nil
	}
	totalPages := (len(candidates) + oppPerPage - 1) / oppPerPage
	if d.page < 1 {
		d.page = 1
	}
	if d.page > totalPages {
		d.page = totalPages
	}
	start := (d.page - 1) * oppPerPage
	end := start + oppPerPage
	if end > len(candidates) {
		end = len(candidates)
	}
	pageRows := candidates[start:end]

	body := fmt.Sprintf("Page %d/%d — pick your opponent:", d.page, totalPages)
	text := renderOppHeader(d) + "\n" + body

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
	nav := []messenger.Button{}
	if totalPages > 1 {
		prevLabel, prevCB := "·", miPrefix+"noop"
		if d.page > 1 {
			prevLabel = "⬅ Prev"
			prevCB = fmt.Sprintf("%snav:%d", miPrefix, d.page-1)
		}
		nav = append(nav, messenger.Button{Label: prevLabel, Callback: prevCB})
	}
	nav = append(nav, messenger.Button{Label: "✕ Cancel", Callback: miPrefix + "cancel"})
	if totalPages > 1 {
		nextLabel, nextCB := "·", miPrefix+"noop"
		if d.page < totalPages {
			nextLabel = "Next ➡"
			nextCB = fmt.Sprintf("%snav:%d", miPrefix, d.page+1)
		}
		nav = append(nav, messenger.Button{Label: nextLabel, Callback: nextCB})
	}
	rows = append(rows, nav)
	return text, rows, nil
}

// ---------------- score-step rendering ----------------

// renderScorePicker builds the score-picker text + 2-column grid from the
// draft alone (no identity calls — labels are pre-resolved at opp-pick
// time and cached in the draft).
func renderScorePicker(d *matchDraft) (string, [][]messenger.Button) {
	body := fmt.Sprintf(
		"%s vs %s\n\nPick scores — left column is you, right is %s. Tap to (re-)select. Confirm when both sides are set.",
		safeLabel(d.ownerLabel), safeLabel(d.oppLabel), safeLabel(d.oppLabel))
	text := renderScoreHeader(d) + "\n" + body

	rows := [][]messenger.Button{}
	for v := 0; v <= scoreMaxVal; v++ {
		left := messenger.Button{
			Label:    cellLabel(v, v == d.p1),
			Callback: fmt.Sprintf("%ss:1:%d", miPrefix, v),
		}
		right := messenger.Button{
			Label:    cellLabel(v, v == d.p2),
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

func safeLabel(s string) string {
	if s == "" {
		return "player"
	}
	return s
}

// ---------------- /match entry ----------------

// startInteractiveMatch posts the opponent picker. Called from handleMatch
// when /match arrives with no args. Pre-resolves the caller's label so the
// later score-picker step never has to do it again.
func (h *Handlers) startInteractiveMatch(ctx context.Context, m *messenger.Message, g models.Group) error {
	t0 := time.Now()

	tLabel := time.Now()
	ownerLabel := h.playerLabel(ctx, g.GroupID, m.From.ID)
	perfLog("start: playerLabel(owner) dur=%v", time.Since(tLabel))

	d := &matchDraft{
		ownerID:    m.From.ID,
		ownerLabel: ownerLabel,
		page:       1,
		p1:         -1, p2: -1,
	}

	tRender := time.Now()
	text, rows, err := h.renderOpponentPicker(ctx, g.GroupID, d)
	perfLog("start: renderOpponentPicker dur=%v", time.Since(tRender))
	if err != nil {
		return h.reply(ctx, m, "Couldn't build opponent list: "+err.Error())
	}
	if rows == nil {
		return h.reply(ctx, m, "No other participants yet. Wait for someone else to /ping or send a command in this group, then try again.")
	}

	tSend := time.Now()
	msgID, err := h.M.SendKeyboardGrid(ctx, g.GroupID, g.MatchesTopicID, text, rows)
	perfLog("start: SendKeyboardGrid dur=%v total=%v", time.Since(tSend), time.Since(t0))
	if err != nil {
		return err
	}
	h.storeDraft(g.GroupID, msgID, d)
	return nil
}

// ---------------- callback entrypoint ----------------

// handleMatchInteractiveCallback is the entry for every `m:i:*` callback.
// It owns parallelizing Edit + Ack and surfacing failures gracefully (the
// keyboard message is rewritten to a clear error string).
func (h *Handlers) handleMatchInteractiveCallback(ctx context.Context, q *messenger.CallbackQuery, payload string) error {
	t0 := time.Now()
	defer func() { perfLog("callback total dur=%v payload=%s", time.Since(t0), payload) }()

	if q.Message == nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	chatID := q.Message.Chat.ID
	msgID := q.Message.MessageID

	d := h.loadOrRestoreDraft(chatID, msgID, q.Message.Text)
	if d == nil {
		// Stale prompt (we restarted, lost in-memory state, and the message
		// text doesn't carry a header we can parse). Best we can do: ack.
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	if q.From == nil || q.From.ID != d.ownerID {
		return h.M.AnswerCallback(ctx, q.ID, "Only the person who ran /match can use these buttons.")
	}

	// Serialize taps on the same draft. Fast clicks queue here.
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := h.dispatchTap(ctx, q, d, payload); err != nil {
		// Graceful: rewrite the message to a readable error and clear the
		// keyboard. The draft is dropped so any further taps get the
		// "stale prompt" silent ack.
		h.failTapGracefully(ctx, q, err)
		return nil
	}
	return nil
}

func (h *Handlers) failTapGracefully(ctx context.Context, q *messenger.CallbackQuery, cause error) {
	chatID := q.Message.Chat.ID
	msgID := q.Message.MessageID
	h.dropDraft(chatID, msgID)
	msg := fmt.Sprintf("/match — failed.\nError: %s\n\nRun /match again to retry.", truncate(cause.Error(), 300))
	// Best effort: replace text, clear keyboard.
	if err := h.M.EditKeyboardGrid(ctx, chatID, msgID, msg, nil); err != nil {
		log.Printf("failTapGracefully: edit: %v", err)
	}
	toast := "Error: " + truncate(cause.Error(), 150)
	if err := h.M.AnswerCallback(ctx, q.ID, toast); err != nil {
		log.Printf("failTapGracefully: ack: %v", err)
	}
}

// dispatchTap mutates the draft and applies the resulting view. Returns
// any error suitable for graceful display.
func (h *Handlers) dispatchTap(ctx context.Context, q *messenger.CallbackQuery, d *matchDraft, payload string) error {
	chatID := q.Message.Chat.ID
	msgID := q.Message.MessageID

	switch {
	case payload == "cancel":
		h.dropDraft(chatID, msgID)
		if err := h.M.EditMessage(ctx, chatID, msgID, "/match cancelled."); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		return h.M.AnswerCallback(ctx, q.ID, "Cancelled")

	case payload == "noop":
		return h.M.AnswerCallback(ctx, q.ID, "")

	case strings.HasPrefix(payload, "nav:"):
		page, err := strconv.Atoi(strings.TrimPrefix(payload, "nav:"))
		if err != nil {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		d.page = page
		d.isScore = false
		tRender := time.Now()
		text, rows, err := h.renderOpponentPicker(ctx, chatID, d)
		perfLog("nav: renderOpponentPicker dur=%v", time.Since(tRender))
		if err != nil {
			return fmt.Errorf("render opp page: %w", err)
		}
		if rows == nil {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		return h.editAndAck(ctx, q, text, rows, "")

	case strings.HasPrefix(payload, "opp:"):
		tid, err := strconv.ParseInt(strings.TrimPrefix(payload, "opp:"), 10, 64)
		if err != nil || tid == d.ownerID {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		// Pre-resolve the opponent label here so subsequent score-cell
		// taps don't need any identity service round-trips.
		tLabel := time.Now()
		d.oppID = tid
		d.oppLabel = h.playerLabel(ctx, chatID, tid)
		d.isScore = true
		d.p1, d.p2 = -1, -1
		perfLog("opp: playerLabel(opp) dur=%v", time.Since(tLabel))

		text, rows := renderScorePicker(d)
		return h.editAndAck(ctx, q, text, rows, "")

	case payload == "back":
		if !d.isScore {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		d.isScore = false
		d.page = 1
		text, rows, err := h.renderOpponentPicker(ctx, chatID, d)
		if err != nil {
			return fmt.Errorf("render opp on back: %w", err)
		}
		if rows == nil {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		return h.editAndAck(ctx, q, text, rows, "")

	case strings.HasPrefix(payload, "s:"):
		if !d.isScore {
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
			d.p1 = val
		case 2:
			d.p2 = val
		default:
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		text, rows := renderScorePicker(d)
		return h.editAndAck(ctx, q, text, rows, "")

	case payload == "confirm":
		if !d.isScore {
			return h.M.AnswerCallback(ctx, q.ID, "")
		}
		if d.p1 < 0 || d.p2 < 0 {
			return h.M.AnswerCallback(ctx, q.ID, "Pick a score for both players first.")
		}
		if d.p1 == d.p2 {
			return h.M.AnswerCallback(ctx, q.ID, "Score must have a winner.")
		}
		// Confirm is the only path that actually needs the group row (for
		// matches_topic_id and the stats refresh).
		g, err := h.Store.Groups().Get(ctx, chatID)
		if err != nil {
			return fmt.Errorf("group lookup: %w", err)
		}
		tReg := time.Now()
		summary, err := h.registerInteractiveMatch(ctx, g, d.ownerID, d.oppID, uint32(d.p1), uint32(d.p2))
		perfLog("confirm: registerInteractiveMatch dur=%v", time.Since(tReg))
		if err != nil {
			return fmt.Errorf("registration: %w", err)
		}
		h.dropDraft(chatID, msgID)
		// Edit the keyboard message to the summary; final ack confirms tap.
		if err := h.M.EditKeyboardGrid(ctx, chatID, msgID, summary, nil); err != nil {
			log.Printf("confirm: edit: %v", err)
		}
		return h.M.AnswerCallback(ctx, q.ID, "Match registered")
	}
	return h.M.AnswerCallback(ctx, q.ID, "")
}

// editAndAck fires EditKeyboardGrid and AnswerCallback concurrently so the
// tap's apparent latency is max(edit, ack) rather than edit + ack.
func (h *Handlers) editAndAck(ctx context.Context, q *messenger.CallbackQuery, text string, rows [][]messenger.Button, toast string) error {
	t0 := time.Now()
	chatID := q.Message.Chat.ID
	msgID := q.Message.MessageID
	var wg sync.WaitGroup
	wg.Add(2)
	var editErr, ackErr error
	var tEdit, tAck time.Duration
	go func() {
		defer wg.Done()
		t := time.Now()
		editErr = h.M.EditKeyboardGrid(ctx, chatID, msgID, text, rows)
		tEdit = time.Since(t)
	}()
	go func() {
		defer wg.Done()
		t := time.Now()
		ackErr = h.M.AnswerCallback(ctx, q.ID, toast)
		tAck = time.Since(t)
	}()
	wg.Wait()
	perfLog("editAndAck total=%v edit=%v ack=%v", time.Since(t0), tEdit, tAck)
	if editErr != nil {
		return editErr
	}
	if ackErr != nil {
		// Ack failure is annoying but the user already sees the edit, so
		// just log it — don't fail the tap.
		log.Printf("editAndAck: ack: %v", ackErr)
	}
	return nil
}

// registerInteractiveMatch is the Confirm-tap registration path. Mirrors
// handleMatch's allocate-insert-render flow, minus the typed-args parsing.
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
		// Admin path: refresh stats; the edited keyboard message IS the
		// match-registered notice.
		_ = h.refreshStatsTopic(ctx, g)
		return text, nil
	}
	// Non-admin: stamp the author's auto-confirm and post a *new*
	// Confirm/Cancel message into the matches topic. The keyboard-prompt
	// message becomes the audit trail "you started /match".
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
