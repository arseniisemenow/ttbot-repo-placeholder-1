package handlers

import (
	"context"
	"errors"
	"regexp"
	"strings"

	s21account "github.com/arseniisemenow/s21-account-go"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
)

// logoutPromptRegex matches the bot's /logout confirmation prompt header.
var logoutPromptRegex = regexp.MustCompile(`^\[LOGIN_OP=logout\]`)

// handleLogout starts the two-step /logout flow. Caller must be logged in.
func (h *Handlers) handleLogout(ctx context.Context, m *messenger.Message) error {
	if _, err := h.Store.S21Accounts().Get(ctx, m.From.ID); errors.Is(err, s21account.ErrNotFound) {
		return h.reply(ctx, m, "You're not logged in — nothing to log out from.")
	}
	prompt := "[LOGIN_OP=logout]\n\n" +
		"You are about to log out (your stored S21 creds for ttbot will be deleted).\n\n" +
		"After this:\n" +
		"- Other logged-in users continue to back ttbot's S21 calls; only your row is removed.\n" +
		"- Group registration, matches, rankings — all keep working as long as at least one healthy login remains.\n\n" +
		"Reply with `confirm` to proceed. Any other reply cancels."
	if _, err := h.M.SendMessageWithForceReply(ctx, m.Chat.ID, prompt, "confirm"); err != nil {
		return h.reply(ctx, m, "Couldn't send confirmation prompt: "+err.Error())
	}
	return nil
}

// isLogoutReply detects the confirm reply.
func isLogoutReply(m *messenger.Message) bool {
	if m == nil || m.ReplyTo == nil || m.ReplyTo.From == nil || !m.ReplyTo.From.IsBot {
		return false
	}
	return logoutPromptRegex.MatchString(m.ReplyTo.Text)
}

// handleLogoutReply parses the confirm reply. Anything other than "confirm"
// cancels.
func (h *Handlers) handleLogoutReply(ctx context.Context, m *messenger.Message) error {
	if strings.TrimSpace(strings.ToLower(m.Text)) != "confirm" {
		return h.reply(ctx, m, "Cancelled — you are still logged in.")
	}
	if err := h.Store.S21Accounts().Delete(ctx, m.From.ID); err != nil {
		return h.reply(ctx, m, "Couldn't delete your account row: "+err.Error())
	}
	return h.reply(ctx, m, "Logged out. Your stored S21 credentials have been removed. /login again whenever you want.")
}
