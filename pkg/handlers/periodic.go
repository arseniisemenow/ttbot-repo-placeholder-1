package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	s21account "github.com/arseniisemenow/s21-account-go"

	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// PeriodicJob is the body of the ttbot-cron function. Four responsibilities:
//
//  1. Probe every s21_accounts row's stored creds against S21 once. The
//     shared package's ApplyAuthResult turns the auth outcome into a
//     Decision (persist markers, warn at 1d/3d/6d milestones, delete + DM
//     at the 7d deadline).
//  2. Delete expired pending matches and notify originating-group topic.
//  3. Delete expired undo commands.
//  4. Refresh participant @usernames across registered groups.
//
// Per-item failures are logged and the rest of the pass continues — one bad
// row should never abort the cron.
func (h *Handlers) PeriodicJob(ctx context.Context) error {
	if err := h.probeAllAccounts(ctx); err != nil {
		log.Printf("periodic: probe accounts: %v", err)
	}
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

// probeAllAccounts walks every s21_accounts row, authenticates each row's
// creds, and applies the shared package's Decision (warn / auto-logout).
func (h *Handlers) probeAllAccounts(ctx context.Context) error {
	rows, err := h.Store.S21Accounts().List(ctx)
	if err != nil {
		return err
	}
	adapter := s21ClientAdapter{inner: h.S21}
	for _, a := range rows {
		h.probeOne(ctx, adapter, a)
	}
	return nil
}

func (h *Handlers) probeOne(ctx context.Context, adapter s21ClientAdapter, a s21account.S21Account) {
	password, err := h.Cipher.Decrypt(a.S21CredsEncrypted)
	if err != nil {
		log.Printf("decrypt creds tid=%d: %v", a.TelegramID, err)
		_, _ = h.M.SendMessage(ctx, a.TelegramID, 0,
			"Internal: I can't decrypt your stored S21 credentials. Please /logout and /login again.")
		return
	}
	_, authErr := adapter.Authenticate(ctx, a.S21Login, password)
	d := s21account.ApplyAuthResult(a, authErr, h.Config.Now())
	if d.Logout {
		if err := h.Store.S21Accounts().Delete(ctx, a.TelegramID); err != nil {
			log.Printf("auto-logout delete tid=%d: %v", a.TelegramID, err)
			return
		}
		if _, err := h.M.SendMessage(ctx, a.TelegramID, 0, d.LogoutDM); err != nil {
			log.Printf("auto-logout DM tid=%d: %v", a.TelegramID, err)
		}
		log.Printf("auto-logout: cleared tid=%d login=%q", a.TelegramID, a.S21Login)
		return
	}
	if d.PersistUpdate {
		if err := h.Store.S21Accounts().Upsert(ctx, d.UpdatedAccount); err != nil {
			log.Printf("upsert account tid=%d: %v", a.TelegramID, err)
		}
	}
	if d.WarningDM != "" {
		if _, err := h.M.SendMessage(ctx, a.TelegramID, 0, d.WarningDM); err != nil {
			log.Printf("warning DM tid=%d: %v", a.TelegramID, err)
		}
	}
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
