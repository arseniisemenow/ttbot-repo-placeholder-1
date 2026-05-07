package handlers

import (
	"context"
	"errors"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
)

// handleBotRegisterGroup links a supergroup to the inviting admin's campus.
func (h *Handlers) handleBotRegisterGroup(ctx context.Context, m *messenger.Message) error {
	if !isSupergroup(m.Chat) {
		return h.reply(ctx, m, "I only work in supergroups with forum topics enabled.")
	}
	admin, err := h.Store.Admins().Get(ctx, m.From.ID)
	if err != nil {
		return h.reply(ctx, m, "Only campus admins can configure the group.")
	}
	if existing, err := h.Store.Groups().GetByCampus(ctx, admin.CampusID); err == nil {
		if existing.GroupID != m.Chat.ID {
			return h.reply(ctx, m, admin.CampusName+" is already linked to another group.")
		}
		// Re-running on the same group is a no-op.
		return h.reply(ctx, m, "Group is already linked to "+admin.CampusName+".")
	}
	g := models.Group{
		GroupID:                  m.Chat.ID,
		CampusID:                 admin.CampusID,
		CampusName:               admin.CampusName,
		AdminTelegramID:          admin.TelegramID,
		ConfirmationTimeoutHours: 24,
		CreatedAt:                h.Config.Now(),
	}
	if err := h.Store.Groups().Upsert(ctx, g); err != nil {
		return err
	}
	return h.reply(ctx, m,
		"Group linked to "+admin.CampusName+". Now run /set_matches_topic in the matches topic and /set_stats_topic in the read-only stats topic.")
}

// handleSetMatchesTopic stores the current topic ID as the matches topic.
func (h *Handlers) handleSetMatchesTopic(ctx context.Context, m *messenger.Message) error {
	g, err := h.assertGroupAdmin(ctx, m)
	if err != nil {
		return nil // already replied
	}
	if m.MessageThreadID == 0 {
		return h.reply(ctx, m, "Run this command inside the topic you want to use as the matches topic.")
	}
	g.MatchesTopicID = m.MessageThreadID
	if err := h.Store.Groups().Upsert(ctx, g); err != nil {
		return err
	}
	return h.reply(ctx, m, "Matches topic set. /match and /undo will be accepted here.")
}

// handleSetStatsTopic stores the current topic ID as the stats topic and
// immediately posts empty placeholder rankings + stats messages so the topic
// is visibly bound from day one. Subsequent rating-affecting events edit
// these messages in place.
func (h *Handlers) handleSetStatsTopic(ctx context.Context, m *messenger.Message) error {
	g, err := h.assertGroupAdmin(ctx, m)
	if err != nil {
		return nil // already replied
	}
	if m.MessageThreadID == 0 {
		return h.reply(ctx, m, "Run this command inside the topic you want to use as the stats topic.")
	}
	g.StatsTopicID = m.MessageThreadID
	// If the topic is being changed, drop stale message-IDs so we re-post.
	g.RankingsMessageID = 0
	g.StatsMessageID = 0
	if err := h.Store.Groups().Upsert(ctx, g); err != nil {
		return err
	}

	// Post placeholders. refreshStatsTopic will fill them in if any rating
	// data exists; otherwise the placeholder text stays.
	_ = h.refreshStatsTopic(ctx, g)

	return h.reply(ctx, m,
		"Stats topic set. I posted rankings and stats messages here — they update automatically on every match.\n"+
			"Tip: restrict 'Send messages' permission in this topic so only admins/bots can post.")
}

// assertGroupAdmin returns the group row if the message comes from a registered
// group whose campus admin matches the caller. Otherwise it sends an error
// reply and returns store.ErrNotFound.
func (h *Handlers) assertGroupAdmin(ctx context.Context, m *messenger.Message) (models.Group, error) {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil {
		_ = h.reply(ctx, m, "This group isn't linked to a campus yet. Admin: run /bot_register_group.")
		return models.Group{}, store.ErrNotFound
	}
	admin, err := h.Store.Admins().Get(ctx, m.From.ID)
	if err != nil || admin.CampusID != g.CampusID {
		_ = h.reply(ctx, m, "Only campus admins can configure the group.")
		return models.Group{}, store.ErrNotFound
	}
	return g, nil
}

// dispatchMyChatMember is fired when the bot itself is added/removed from a
// chat. We use it to check that the chat is a supergroup with topics, and to
// leave hostile/unsuitable chats.
func (h *Handlers) dispatchMyChatMember(ctx context.Context, ev *messenger.ChatMemberUpdate) error {
	if ev.NewChatMember == nil {
		return nil
	}
	status := ev.NewChatMember.Status
	if status != "member" && status != "administrator" {
		return nil // leaving / kicked — nothing to do
	}
	if !isSupergroup(ev.Chat) {
		_, _ = h.M.SendMessage(ctx, ev.Chat.ID, 0, "I only work in supergroups with forum topics enabled. Please enable Topics in group settings, or move me to a supergroup.")
		return h.M.LeaveChat(ctx, ev.Chat.ID)
	}
	// If inviter is an admin, send the welcome message; else leave.
	if ev.From != nil {
		if _, err := h.Store.Admins().Get(ctx, ev.From.ID); err == nil {
			_, _ = h.M.SendMessage(ctx, ev.Chat.ID, 0, "Hi! Use /bot_register_group to link this group to your campus, then /set_matches_topic and /set_stats_topic.")
			return nil
		}
	}
	_, _ = h.M.SendMessage(ctx, ev.Chat.ID, 0, "Only campus admins can configure me. Ask your campus admin to run /admin first.")
	return h.M.LeaveChat(ctx, ev.Chat.ID)
}

// dispatchChatMember is fired when a user joins/leaves a registered group.
// On a fresh join, auto-activate the user as a player.
func (h *Handlers) dispatchChatMember(ctx context.Context, ev *messenger.ChatMemberUpdate) error {
	if ev.NewChatMember == nil || ev.NewChatMember.User == nil {
		return nil
	}
	newStatus := ev.NewChatMember.Status
	if newStatus != "member" && newStatus != "administrator" && newStatus != "creator" {
		return nil
	}
	// Only act inside registered groups.
	if _, err := h.Store.Groups().Get(ctx, ev.Chat.ID); err != nil {
		return nil
	}
	tgUser := ev.NewChatMember.User
	user, err := h.Store.Users().Get(ctx, tgUser.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	user.TelegramID = tgUser.ID
	if user.TelegramUsername == "" {
		user.TelegramUsername = tgUser.Username
	}
	if user.NicknameStatus == "" {
		user.NicknameStatus = models.NicknameStatusNone
	}
	if err := h.Store.Users().Upsert(ctx, user); err != nil {
		return err
	}
	return h.Store.Players().Upsert(ctx, models.Player{
		GroupID:     ev.Chat.ID,
		TelegramID:  tgUser.ID,
		ActivatedAt: h.Config.Now(),
	})
}

func isSupergroup(c messenger.Chat) bool {
	return c.Type == "supergroup" && c.IsForum
}
