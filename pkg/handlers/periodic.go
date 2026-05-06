package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
)

// PeriodicJob is the body of the ttbot-cron function. It deletes expired
// pending matches and undo commands, sending the corresponding two-step
// notifications. Notification is only sent by the transaction that actually
// deleted the row (the memstore is single-locked so concurrent cron runs see
// each row at most once).
func (h *Handlers) PeriodicJob(ctx context.Context) error {
	if err := h.cleanExpiredMatches(ctx); err != nil {
		log.Printf("periodic: matches: %v", err)
	}
	if err := h.cleanExpiredUndo(ctx); err != nil {
		log.Printf("periodic: undo: %v", err)
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
		// Notify reporter and players.
		g, _ := h.Store.Groups().Get(ctx, m.GroupID)
		text := fmt.Sprintf("Match #%d expired (no confirmation within timeout).", m.MatchID)
		for _, uid := range uniqueIDs(m.RegisteredBy, m.Player1ID, m.Player2ID) {
			user, err := h.Store.Users().Get(ctx, uid)
			if err != nil {
				continue
			}
			_ = h.Notifier.SendUser(ctx, user, g, text)
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
		g, _ := h.Store.Groups().Get(ctx, u.GroupID)
		user, err := h.Store.Users().Get(ctx, u.TelegramID)
		if err != nil {
			continue
		}
		_ = h.Notifier.SendUser(ctx, user, g, fmt.Sprintf("Undo request for Match #%d expired.", u.MatchID))
	}
	return nil
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
