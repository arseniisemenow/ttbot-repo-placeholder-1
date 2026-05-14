package handlers

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
)

// callback-data prefix for "pick which group to backfill this participant
// into" buttons. Kept very short — Telegram caps callback_data at ~64 bytes
// and we still need to encode two int64s after the prefix.
const cbAddParticipantPrefix = "p:a:"

// forwardedPromptRegex extracts the target's telegram id (and optional
// @username) back out of the bot's prompt text when a button is tapped.
// The bot fully controls the prompt format, so a strict regex is safe.
var forwardedPromptRegex = regexp.MustCompile(`Forwarded user(?: @([A-Za-z0-9_]+))? \(id (\d+)\)`)

// handleForwardedAdd handles a forwarded message that arrived in a DM. We
// extract the original sender's telegram id (and @username, when not hidden
// by forward-privacy), then either upsert the participant immediately (when
// the DMing admin manages exactly one group) or ask which group to target.
func (h *Handlers) handleForwardedAdd(ctx context.Context, m *messenger.Message, target *messenger.User) error {
	if target == nil {
		return nil
	}
	if target.IsBot {
		return h.reply(ctx, m, "Bots can't be participants.")
	}
	if target.ID == m.From.ID {
		return h.reply(ctx, m, "That forward is from you. Forward someone else's message.")
	}

	candidates, err := h.adminGroupsFor(ctx, m.From.ID)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return h.reply(ctx, m, "You're not an admin of any registered group.")
	}

	if len(candidates) == 1 {
		g := candidates[0]
		if err := h.upsertBackfillParticipant(ctx, g.GroupID, target); err != nil {
			return err
		}
		return h.reply(ctx, m,
			fmt.Sprintf("Added %s to %s.", targetLabel(target.ID, target.Username), groupDisplayName(g)))
	}

	// Multiple groups — let the admin pick. Each button carries the group id
	// and the target's telegram id; the @username is recovered from the
	// prompt text via forwardedPromptRegex when the tap fires.
	prompt := fmt.Sprintf("Forwarded user %s\nAdd to which group?",
		targetLabel(target.ID, target.Username))
	buttons := make([]messenger.Button, 0, len(candidates))
	for _, g := range candidates {
		buttons = append(buttons, messenger.Button{
			Label:    groupDisplayName(g),
			Callback: fmt.Sprintf("%s%d:%d", cbAddParticipantPrefix, g.GroupID, target.ID),
		})
	}
	_, err = h.M.SendInlineKeyboard(ctx, m.Chat.ID, 0, prompt, buttons)
	return err
}

// handleAddParticipantTap fires when an admin taps one of the "Add to which
// group?" buttons. Payload format: "<group_id>:<target_telegram_id>".
func (h *Handlers) handleAddParticipantTap(ctx context.Context, q *messenger.CallbackQuery, payload string) error {
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) != 2 {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}
	gid, err1 := strconv.ParseInt(parts[0], 10, 64)
	tid, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}

	if !h.isChatAdmin(ctx, gid, q.From.ID) {
		return h.M.AnswerCallback(ctx, q.ID, "Not authorized.")
	}
	g, err := h.Store.Groups().Get(ctx, gid)
	if err != nil {
		return h.M.AnswerCallback(ctx, q.ID, "")
	}

	// Recover the @username from the prompt text (bot-controlled format).
	username := ""
	if q.Message != nil {
		if mm := forwardedPromptRegex.FindStringSubmatch(q.Message.Text); mm != nil {
			username = mm[1]
		}
	}

	if err := h.Store.Participants().Upsert(ctx, models.Participant{
		GroupID:          gid,
		TelegramID:       tid,
		TelegramUsername: username,
		ActivatedAt:      h.Config.Now(),
	}); err != nil {
		return err
	}

	_ = h.M.EditMessage(ctx, q.Message.Chat.ID, q.Message.MessageID,
		fmt.Sprintf("Added %s to %s.", targetLabel(tid, username), groupDisplayName(g)))
	return h.M.AnswerCallback(ctx, q.ID, "Added")
}

// adminGroupsFor lists every registered group where the given user is a
// Telegram chat admin. This matches the rest of the bot's authority model:
// chat admin = "may configure / backfill this group".
func (h *Handlers) adminGroupsFor(ctx context.Context, telegramID int64) ([]models.Group, error) {
	all, err := h.Store.Groups().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Group, 0, len(all))
	for _, g := range all {
		if h.isChatAdmin(ctx, g.GroupID, telegramID) {
			out = append(out, g)
		}
	}
	return out, nil
}

// upsertBackfillParticipant writes the participants row for a forwarded user.
func (h *Handlers) upsertBackfillParticipant(ctx context.Context, groupID int64, u *messenger.User) error {
	return h.Store.Participants().Upsert(ctx, models.Participant{
		GroupID:          groupID,
		TelegramID:       u.ID,
		TelegramUsername: u.Username,
		ActivatedAt:      h.Config.Now(),
	})
}

// targetLabel renders "@<username> (id <tid>)" when the username is known,
// or "(id <tid>)" otherwise. Used in both the prompt (parsed back by
// forwardedPromptRegex) and the confirmation messages.
func targetLabel(tid int64, username string) string {
	if username != "" {
		return fmt.Sprintf("@%s (id %d)", username, tid)
	}
	return fmt.Sprintf("(id %d)", tid)
}

// groupDisplayName picks the most human-friendly label we have for a
// registered group, falling back to the numeric id when nothing else is set.
func groupDisplayName(g models.Group) string {
	if g.CampusName != "" {
		return g.CampusName
	}
	return fmt.Sprintf("Group %d", g.GroupID)
}
