package handlers

import (
	"context"
	"strings"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
)

// handleStart prints the full help text. Same handler covers /start and /help.
//
// Sections are ordered Matches → DM → Admin so the most common-use commands
// are at the top. /admin no longer accepts inline args — credentials are
// taken via a force-reply prompt so they can be deleted right after reading.
func (h *Handlers) handleStart(ctx context.Context, m *messenger.Message) error {
	const help = `ttbot — table-tennis match tracker.

Matches topic:
  /match @opponent 3-1 — you vs opponent (your score first)
  /match @p1 @p2 3-1 — register a match between two named players
  /undo #N — undo or restore match #N (two-step confirm)
  /ping — react to your message; backfills your row in participants

Each player token can be either @telegram_username or a bare S21 nickname.

DM:
  /start, /help — this message
  /admin — claim or rotate admin role; two-step (I prompt for S21 creds in a reply and delete it)
  /refresh_identity — clear my local identity-service cache

Admin only — any topic of a registered group:
  /bot_register_group — link this group to ttbot
  /set_matches_topic — call inside the matches topic to register it
  /set_stats_topic — call inside the stats topic to register it
  /refresh_usernames — refresh the participants cache against Telegram`
	return h.reply(ctx, m, help)
}

// handleAdmin is step 1 of the two-step admin flow. /admin takes no
// arguments; sends a force-reply prompt and lets handleAdminSetReply
// (admin_set.go) finish.
//
// Inline-args form (`/admin login:password`) is rejected outright —
// Telegram keeps command text in chat history, so credentials there
// are exposed.
func (h *Handlers) handleAdmin(ctx context.Context, m *messenger.Message, args string) error {
	if strings.TrimSpace(args) != "" {
		return h.reply(ctx, m, "/admin takes no arguments. Run it again with nothing after the slash.")
	}
	prompt := "[ADMIN_OP=set]\n\n" +
		"Reply with your S21 credentials as `login:password` on a single line. " +
		"I'll authenticate against S21, encrypt the result, and **delete your reply immediately** so the creds don't linger in this chat."
	if _, err := h.M.SendMessageWithForceReply(ctx, m.Chat.ID, prompt, "login:password"); err != nil {
		return h.reply(ctx, m, "Couldn't send the prompt: "+err.Error())
	}
	return nil
}

// handleRefreshIdentity is admin-only (DM-only): flushes the in-process
// identity cache so the next /match or /rankings sees fresh data. Use after a
// nickname change in @school_21_identity_bot.
func (h *Handlers) handleRefreshIdentity(ctx context.Context, m *messenger.Message) error {
	if _, err := h.Store.Admins().Get(ctx, m.From.ID); err != nil {
		return h.reply(ctx, m, "Only admins can run /refresh_identity.")
	}
	if svc := h.Identity(); svc != nil {
		svc.Flush()
	}
	return h.reply(ctx, m, "Cache flushed.")
}
