package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// PeriodicJob is the body of the ttbot-cron function. Deletes expired pending
// matches and expired undo commands, posting one notification per deletion
// into the matches topic of the originating group. Notification is only sent
// by the transaction that actually deleted the row.
func (h *Handlers) PeriodicJob(ctx context.Context) error {
	if err := h.cleanExpiredMatches(ctx); err != nil {
		log.Printf("periodic: matches: %v", err)
	}
	if err := h.cleanExpiredUndo(ctx); err != nil {
		log.Printf("periodic: undo: %v", err)
	}
	if err := h.refreshAllUsernames(ctx); err != nil {
		log.Printf("periodic: refresh_usernames: %v", err)
	}
	return nil
}

func (h *Handlers) cleanExpiredMatches(ctx context.Context) error {
	expired, err := h.Store.Matches().ListPendingExpired(ctx, func(g models.Group) bool { return true })
	if err != nil {
		return err
	}
	for _, m := range expired {
		if err := h.Store.Matches().Delete(ctx, m.GroupID, m.MatchID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // someone else deleted it
			}
			return err
		}
		_ = h.Store.MatchConfirmations().DeleteForMatch(ctx, m.GroupID, m.MatchID)
		g, gerr := h.Store.Groups().Get(ctx, m.GroupID)
		if gerr != nil {
			continue
		}
		text := fmt.Sprintf("Match #%d expired (no confirmation within timeout).", m.MatchID)
		for _, uid := range uniqueIDs(m.RegisteredBy, m.Player1ID, m.Player2ID) {
			_ = h.Notifier.SendInGroup(ctx, g, h.participantUsername(ctx, m.GroupID, uid), text)
		}
	}
	return nil
}

func (h *Handlers) cleanExpiredUndo(ctx context.Context) error {
	cutoff := h.Config.Now().Add(-24 * time.Hour).UnixNano()
	expired, err := h.Store.UndoCommands().ListExpired(ctx, cutoff)
	if err != nil {
		return err
	}
	for _, u := range expired {
		if err := h.Store.UndoCommands().Delete(ctx, u.GroupID, u.MatchID, u.TelegramID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		g, gerr := h.Store.Groups().Get(ctx, u.GroupID)
		if gerr != nil {
			continue
		}
		_ = h.Notifier.SendInGroup(ctx, g, h.participantUsername(ctx, u.GroupID, u.TelegramID),
			fmt.Sprintf("Undo request for Match #%d expired.", u.MatchID))
	}
	return nil
}

// participantUsername returns the cached @username for a (group, telegram)
// pair, or "" when nothing is cached (the mention prefix is then omitted).
func (h *Handlers) participantUsername(ctx context.Context, groupID, telegramID int64) string {
	if p, err := h.Store.Participants().Get(ctx, groupID, telegramID); err == nil {
		return p.TelegramUsername
	}
	return ""
}

func uniqueIDs(ids ...int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
