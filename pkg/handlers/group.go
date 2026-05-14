package handlers

import (
	"context"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// handleBotRegisterGroup links a supergroup. Authority is Telegram-chat
// admin (creator/admin in the group itself); we no longer require an entry
// in the admins table for this.
func (h *Handlers) handleBotRegisterGroup(ctx context.Context, m *messenger.Message) error {
	if !isSupergroup(m.Chat) {
		return h.reply(ctx, m, "I only work in supergroups with forum topics enabled.")
	}
	if !h.isChatAdmin(ctx, m.Chat.ID, m.From.ID) {
		return h.reply(ctx, m, "Only group admins can configure the group.")
	}
	// Stamp campus name/id from any stored admin row (just for display in the
	// group). Best-effort: empty strings are OK if no admin is registered yet.
	var campusID, campusName string
	if admins, _ := h.Store.Admins().List(ctx); len(admins) > 0 {
		campusID = admins[0].CampusID
		campusName = admins[0].CampusName
	}
	if existing, err := h.Store.Groups().Get(ctx, m.Chat.ID); err == nil {
		existing.CampusID = campusID
		existing.CampusName = campusName
		_ = h.Store.Groups().Upsert(ctx, existing)
		label := campusName
		if label == "" {
			label = "this group"
		}
		return h.reply(ctx, m, "Group is already linked ("+label+").")
	}
	g := models.Group{
		GroupID:                  m.Chat.ID,
		CampusID:                 campusID,
		CampusName:               campusName,
		AdminTelegramID:          m.From.ID,
		ConfirmationTimeoutHours: 24,
		CreatedAt:                h.Config.Now(),
	}
	if err := h.Store.Groups().Upsert(ctx, g); err != nil {
		return err
	}
	label := campusName
	if label == "" {
		label = "ttbot"
	}
	return h.reply(ctx, m,
		"Group linked to "+label+". Now run /set_matches_topic in the matches topic and /set_stats_topic in the read-only stats topic.")
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
// is visibly bound from day one.
func (h *Handlers) handleSetStatsTopic(ctx context.Context, m *messenger.Message) error {
	g, err := h.assertGroupAdmin(ctx, m)
	if err != nil {
		return nil // already replied
	}
	if m.MessageThreadID == 0 {
		return h.reply(ctx, m, "Run this command inside the topic you want to use as the stats topic.")
	}
	g.StatsTopicID = m.MessageThreadID
	g.RankingsELOMessageID = 0
	g.RankingsGlickoMessageID = 0
	g.StatsMessageID = 0
	if err := h.Store.Groups().Upsert(ctx, g); err != nil {
		return err
	}
	_ = h.refreshStatsTopic(ctx, g)
	return h.reply(ctx, m,
		"Stats topic set. I posted rankings and stats messages here — they update automatically on every match.\n"+
			"Tip: restrict 'Send messages' permission in this topic so only admins/bots can post.")
}

// assertGroupAdmin returns the group row if the message comes from a
// registered group whose chat-admin matches the caller. Otherwise it sends
// an error reply and returns store.ErrNotFound.
func (h *Handlers) assertGroupAdmin(ctx context.Context, m *messenger.Message) (models.Group, error) {
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil {
		_ = h.reply(ctx, m, "This group isn't linked yet. Admin: run /bot_register_group.")
		return models.Group{}, store.ErrNotFound
	}
	if !h.isChatAdmin(ctx, m.Chat.ID, m.From.ID) {
		_ = h.reply(ctx, m, "Only group admins can configure the group.")
		return models.Group{}, store.ErrNotFound
	}
	return g, nil
}

// dispatchMyChatMember handles bot-join/leave events.
func (h *Handlers) dispatchMyChatMember(ctx context.Context, ev *messenger.ChatMemberUpdate) error {
	if ev.NewChatMember == nil {
		return nil
	}
	status := ev.NewChatMember.Status
	if status != "member" && status != "administrator" {
		return nil
	}
	if !isSupergroup(ev.Chat) {
		_, _ = h.M.SendMessage(ctx, ev.Chat.ID, 0, "I only work in supergroups with forum topics enabled. Please enable Topics in group settings, or move me to a supergroup.")
		return h.M.LeaveChat(ctx, ev.Chat.ID)
	}
	// Welcome the inviter regardless of role; chat-admin authority is now
	// checked at command time.
	_, _ = h.M.SendMessage(ctx, ev.Chat.ID, 0,
		"Hi! A group admin can run /bot_register_group here, then /set_matches_topic and /set_stats_topic inside the relevant topics.")
	return nil
}

// dispatchChatMember is fired when a user joins/leaves a registered group.
// We upsert their participants row so that /match @username can resolve their
// telegram_id inside this specific group.
func (h *Handlers) dispatchChatMember(ctx context.Context, ev *messenger.ChatMemberUpdate) error {
	if ev.NewChatMember == nil || ev.NewChatMember.User == nil {
		return nil
	}
	newStatus := ev.NewChatMember.Status
	if newStatus != "member" && newStatus != "administrator" && newStatus != "creator" {
		return nil
	}
	if _, err := h.Store.Groups().Get(ctx, ev.Chat.ID); err != nil {
		return nil
	}
	tgUser := ev.NewChatMember.User
	return h.Store.Participants().Upsert(ctx, models.Participant{
		GroupID:          ev.Chat.ID,
		TelegramID:       tgUser.ID,
		TelegramUsername: tgUser.Username,
		ActivatedAt:      h.Config.Now(),
	})
}

func isSupergroup(c messenger.Chat) bool {
	return c.Type == "supergroup" && c.IsForum
}
