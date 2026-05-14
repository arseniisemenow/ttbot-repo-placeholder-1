package handlers

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"

	s21account "github.com/arseniisemenow/s21-account-go"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/validation"
)

// loginPromptRegex matches the bot's two-step /login force-reply prompt
// header. Same tag shape as identity-bot so the two flows stay symmetric.
var loginPromptRegex = regexp.MustCompile(`^\[LOGIN_OP=set\]`)

// handleLogin is step 1 of the two-step /login flow. Inline-args form is
// rejected (Telegram keeps command text in chat history). With no args we
// post a force-reply prompt and let handleLoginReply finish.
func (h *Handlers) handleLogin(ctx context.Context, m *messenger.Message, args string) error {
	if strings.TrimSpace(args) != "" {
		return h.reply(ctx, m, "/login takes no arguments. Run it again with nothing after the slash.")
	}
	prompt := "[LOGIN_OP=set]\n\n" +
		"Reply with your S21 credentials as `login:password` on a single line. " +
		"I'll authenticate against S21, encrypt the result, and **delete your reply immediately** so the creds don't linger in this chat. " +
		"Anyone can /login — multiple logged-in users let me keep working when one set of creds rotates."
	if _, err := h.M.SendMessageWithForceReply(ctx, m.Chat.ID, prompt, "login:password"); err != nil {
		return h.reply(ctx, m, "Couldn't send the prompt: "+err.Error())
	}
	return nil
}

// isLoginReply reports whether an inbound DM is the user's credentials
// reply to the /login force-reply prompt.
func isLoginReply(m *messenger.Message) bool {
	if m == nil || m.ReplyTo == nil || m.ReplyTo.From == nil || !m.ReplyTo.From.IsBot {
		return false
	}
	return loginPromptRegex.MatchString(m.ReplyTo.Text)
}

// handleLoginReply completes the /login flow. The user's reply contains
// `login:password`. We delete the message immediately, then ask the shared
// package to validate-and-store.
func (h *Handlers) handleLoginReply(ctx context.Context, m *messenger.Message) error {
	// Best-effort scrub of the user's message — runs even if validation
	// below fails so the creds don't linger in chat history.
	defer func() {
		if err := h.M.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			log.Printf("delete /login creds message chat=%d msg=%d: %v", m.Chat.ID, m.MessageID, err)
		}
	}()
	login, password, err := validation.ParseAdminCredentials(strings.TrimSpace(m.Text))
	if err != nil {
		return h.reply(ctx, m, "Couldn't read creds — expected `login:password` on a single line. Run /login again to start over.")
	}
	account, err := s21account.ValidateAndStore(ctx,
		h.Store.S21Accounts(), h.Cipher, s21ClientAdapter{inner: h.S21},
		m.From.ID, login, password, h.Config.Now())
	switch {
	case errors.Is(err, s21account.ErrInvalidCredentials):
		return h.reply(ctx, m, "S21 rejected those credentials. Run /login again to retry.")
	case err != nil:
		return h.reply(ctx, m, "S21 is unavailable right now. Try again later.")
	}
	greeting := "You're now logged in as " + account.S21Login
	if account.CampusName != "" {
		greeting += " (" + account.CampusName + ")"
	}
	greeting += ". I'll use your creds (alongside other logged-in users') for S21 API calls."
	return h.reply(ctx, m, greeting)
}
