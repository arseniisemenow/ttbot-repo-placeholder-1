package handlers

import (
	"context"
	"fmt"
	"log"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
)

// refreshGroupUsernames walks every cached participant in one group and
// re-asks Telegram for their current @username via getChatMember. Rows whose
// username has drifted (rename, set, cleared) are upserted with the fresh
// value. Returns the count of rows actually updated.
//
// Each per-user call is one Telegram API hit; participant counts are small
// enough that the linear walk is fine. Per-user errors are logged and the
// loop continues so one bad row doesn't abort the whole refresh.
func (h *Handlers) refreshGroupUsernames(ctx context.Context, g models.Group) (int, error) {
	ps, err := h.Store.Participants().ListByGroup(ctx, g.GroupID)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, p := range ps {
		newUsername, err := h.M.ResolveChatMemberUsername(ctx, g.GroupID, p.TelegramID)
		if err != nil {
			log.Printf("refresh_usernames: chat=%d user=%d: %v", g.GroupID, p.TelegramID, err)
			continue
		}
		if newUsername == p.TelegramUsername {
			continue
		}
		p.TelegramUsername = newUsername
		p.ActivatedAt = h.Config.Now()
		if err := h.Store.Participants().Upsert(ctx, p); err != nil {
			log.Printf("refresh_usernames: upsert chat=%d user=%d: %v", g.GroupID, p.TelegramID, err)
			continue
		}
		changed++
	}
	return changed, nil
}

// refreshAllUsernames runs refreshGroupUsernames against every registered
// group. Called from the periodic cron job; per-group errors are logged but
// don't abort the rest of the run.
func (h *Handlers) refreshAllUsernames(ctx context.Context) error {
	groups, err := h.Store.Groups().List(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := h.refreshGroupUsernames(ctx, g); err != nil {
			log.Printf("refresh_usernames: group=%d: %v", g.GroupID, err)
		}
	}
	return nil
}

// handleRefreshUsernames is the /refresh_usernames command. Admin-only, runs
// the same refresh as the cron tick but bounded to the calling group, and
// replies with the count so the admin sees something happened.
func (h *Handlers) handleRefreshUsernames(ctx context.Context, m *messenger.Message) error {
	g, err := h.assertGroupAdmin(ctx, m)
	if err != nil {
		return nil // assertGroupAdmin already replied
	}
	changed, err := h.refreshGroupUsernames(ctx, g)
	if err != nil {
		return h.reply(ctx, m, fmt.Sprintf("Refresh failed: %v", err))
	}
	return h.reply(ctx, m, fmt.Sprintf("Refresh complete. %d participants updated.", changed))
}
